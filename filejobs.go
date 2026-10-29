package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// File jobs: changes to a book's audio files, on an admin's request, one at a time.
//
//   - "tags" writes the book's details (as the server shows them, matched or edited
//     ones included), its chapters and its cover into the files themselves, so they
//     are right in any other player too.
//   - "merge" makes one m4b of a book that is several files (mp3 parts, CD tracks),
//     with its chapters, details and cover, and moves the old files into .originals/
//     beside it (hidden from scans), or deletes them if asked.
//
// Either way each new file is written beside the old one, checked (its length must
// match), and only then swapped in. The book keeps its id, and so everyone's place in
// it, bookmarks and its transcript.

type fileJob struct {
	BookID          string `json:"bookId"`
	Kind            string `json:"kind"` // tags or merge
	DeleteOriginals bool   `json:"deleteOriginals,omitempty"`
	By              string `json:"by"`
}

type FileJobResult struct {
	fileJob
	Title string `json:"title"`
	OK    bool   `json:"ok"`
	Note  string `json:"note,omitempty"` // what was done, or what went wrong
	At    int64  `json:"at"`
}

type fileJobs struct {
	s        *server
	mu       sync.Mutex
	queue    []fileJob
	running  *fileJob
	progress float64
	recent   []FileJobResult // newest first, a few
	wake     chan struct{}
}

func newFileJobs(s *server) *fileJobs {
	f := &fileJobs{s: s, wake: make(chan struct{}, 1)}
	go f.run()
	return f
}

// runningOn reports whether a file job is changing the book's files right now.
func (f *fileJobs) runningOn(bookID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running != nil && f.running.BookID == bookID
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func (f *fileJobs) add(j fileJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running != nil && f.running.BookID == j.BookID || slices.ContainsFunc(f.queue, func(q fileJob) bool { return q.BookID == j.BookID }) {
		return errors.New("that book already has a file job waiting")
	}
	f.queue = append(f.queue, j)
	select {
	case f.wake <- struct{}{}:
	default:
	}
	return nil
}

// next takes the first job whose book is not being transcribed (its files must stay put
// while whisper reads them).
func (f *fileJobs) next() fileJob {
	for {
		f.mu.Lock()
		for i, j := range f.queue {
			if !f.s.trans.busyWith(j.BookID) {
				f.queue = slices.Delete(f.queue, i, i+1)
				f.running, f.progress = &j, 0
				f.mu.Unlock()
				return j
			}
		}
		f.mu.Unlock()
		select {
		case <-f.wake:
		case <-time.After(30 * time.Second):
		}
	}
}

func (f *fileJobs) run() {
	for {
		j := f.next()
		b := f.s.lib.book(j.BookID)
		res := FileJobResult{fileJob: j, At: time.Now().UnixMilli()}
		var err error
		if b == nil {
			err = errors.New("the book is gone")
		} else {
			res.Title = b.Title
			old := audioPrint(b)
			report := func(p float64) { f.mu.Lock(); f.progress = p; f.mu.Unlock() }
			if j.Kind == "merge" {
				res.Note, err = f.s.mergeBook(b, j.DeleteOriginals, report)
			} else {
				res.Note, err = f.s.writeTags(b, report)
			}
			// The files changed: read the book again, and keep its transcript with it.
			if rerr := f.s.lib.rescan(); rerr != nil {
				log.Printf("scan after a file job: %v", rerr)
			}
			f.s.carryTranscript(j.BookID, old)
		}
		res.OK = err == nil
		if err != nil {
			res.Note = err.Error()
			log.Printf("%s of %q: %v", j.Kind, res.Title, err)
			f.s.notify(eventFilesFailed, fmt.Sprintf("Could not %s %s: %v", jobVerb(j.Kind), res.Title, err), map[string]any{"bookId": j.BookID, "kind": j.Kind, "error": err.Error()})
		} else {
			log.Printf("%s of %q: %s", j.Kind, res.Title, res.Note)
			f.s.notify(eventFilesDone, fmt.Sprintf("%s: %s", res.Title, res.Note), map[string]any{"bookId": j.BookID, "kind": j.Kind})
		}
		f.mu.Lock()
		f.running = nil
		f.recent = append([]FileJobResult{res}, f.recent[:min(len(f.recent), 9)]...)
		f.mu.Unlock()
	}
}

func jobVerb(kind string) string {
	if kind == "merge" {
		return "merge"
	}
	return "write the details into"
}

// carryTranscript moves a book's transcript over to its new files, when they replaced
// the ones it was made from (same audio, same timeline).
func (s *server) carryTranscript(id, oldPrint string) {
	b := s.lib.book(id)
	if b == nil {
		return
	}
	s.tmu.Lock()
	defer s.tmu.Unlock()
	var t transcriptFile
	if readJSON(s.transcriptPath(id), &t) != nil || t.Audio != oldPrint || t.Audio == audioPrint(b) {
		return
	}
	t.Audio = audioPrint(b)
	if err := writeJSONCompact(s.transcriptPath(id), t); err != nil {
		log.Printf("transcript of %q: %v", b.Title, err)
	}
	delete(s.tdone, id)
}

// ------------------------------------------------------------------ writing details

// fileTags are the tags a book's files get, for the container they are in. m4b and
// m4a keep to the standard atoms (players do not read custom ones); mp3, FLAC and Ogg
// take the rest as named fields, as this server and audiobookshelf read them.
func fileTags(b *Book, kind string, part int) map[string]string {
	t := map[string]string{
		"album": b.Title, "artist": b.Author, "album_artist": b.Author, "composer": b.Narrator,
		"genre": strings.Join(b.Genres, ", "), "date": b.PublishedYear, "description": b.Description,
		"comment": clip(b.Description, 1000), "grouping": b.Series, "title": b.Title,
	}
	if len(b.Files) > 1 {
		t["title"] = strings.TrimSuffix(b.Files[part].Name, filepath.Ext(b.Files[part].Name))
		t["track"] = fmt.Sprintf("%d/%d", part+1, len(b.Files))
	}
	if kind != "mp4" {
		idx := ""
		if b.SeriesIndex != nil {
			idx = strconv.FormatFloat(*b.SeriesIndex, 'f', -1, 64)
		}
		for k, v := range map[string]string{"subtitle": b.Subtitle, "series": b.Series, "series-part": idx,
			"publisher": b.Publisher, "language": b.Language, "isbn": b.ISBN, "asin": b.ASIN} {
			t[k] = v
		}
	}
	return t
}

// containerOf is how tags are written to a file, or "" when they cannot be.
func containerOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4b", ".m4a", ".mp4":
		return "mp4"
	case ".mp3":
		return "mp3"
	case ".flac":
		return "flac"
	case ".ogg", ".opus":
		return "ogg"
	}
	return ""
}

// ffmetadata is ffmpeg's metadata file: tags, then chapters in milliseconds.
func ffmetadata(tags map[string]string, chapters []Chapter) string {
	esc := strings.NewReplacer(`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`, "\n", "\\\n")
	var sb strings.Builder
	sb.WriteString(";FFMETADATA1\n")
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if v := strings.TrimSpace(tags[k]); v != "" {
			fmt.Fprintf(&sb, "%s=%s\n", k, esc.Replace(v))
		}
	}
	for _, c := range chapters {
		fmt.Fprintf(&sb, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", c.StartMs, c.EndMs, esc.Replace(c.Title))
	}
	return sb.String()
}

// fileChapters are the book's chapters that fall in a file starting at from and lasting
// length, on the file's own timeline.
func fileChapters(b *Book, from, length int64) []Chapter {
	var out []Chapter
	for _, c := range b.Chapters {
		s, e := max(c.StartMs, from), min(c.EndMs, from+length)
		if e-s >= 1000 {
			out = append(out, Chapter{Title: c.Title, StartMs: s - from, EndMs: e - from})
		}
	}
	return out
}

func (s *server) writeTags(b *Book, progress func(float64)) (string, error) {
	var at int64
	written, skipped := 0, 0
	for i, f := range b.Files {
		start := at
		at += f.DurationMs
		kind := containerOf(f.path)
		if kind == "" {
			skipped++
			continue
		}
		meta := ffmetadata(fileTags(b, kind, i), fileChapters(b, start, f.DurationMs))
		cover := ""
		if kind == "mp3" || kind == "mp4" || kind == "flac" {
			cover = b.coverPath
		}
		out := filepath.Join(filepath.Dir(f.path), ".kithara-"+filepath.Base(f.path))
		args := []string{"-c:a", "copy"}
		switch kind {
		case "mp3":
			args = append(args, "-id3v2_version", "3")
		case "mp4":
			args = append(args, "-movflags", "+faststart")
		}
		if err := s.remux([]string{f.path}, false, meta, cover, args, out, f.DurationMs, nil); err != nil {
			return "", fmt.Errorf("%s: %w", f.Name, err)
		}
		if err := os.Rename(out, f.path); err != nil {
			os.Remove(out)
			return "", fmt.Errorf("%s: %w", f.Name, err)
		}
		written++
		progress(float64(i+1) / float64(len(b.Files)))
	}
	note := fmt.Sprintf("details written into %s", plural(written, "file"))
	if skipped > 0 {
		note += fmt.Sprintf(" (%d in a format that holds no tags left alone)", skipped)
	}
	return note, nil
}

// ------------------------------------------------------------------ merging

type audioStream struct {
	Codec    string `json:"codec_name"`
	Rate     string `json:"sample_rate"`
	Channels int    `json:"channels"`
	BitRate  string `json:"bit_rate"`
}

func probeStream(path string) (audioStream, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,sample_rate,channels,bit_rate", "-of", "json", path).Output()
	if err != nil {
		return audioStream{}, err
	}
	var r struct{ Streams []audioStream }
	if err := json.Unmarshal(out, &r); err != nil || len(r.Streams) == 0 {
		return audioStream{}, errors.New("no audio stream")
	}
	return r.Streams[0], nil
}

var unsafeName = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)

func (s *server) mergeBook(b *Book, deleteOriginals bool, progress func(float64)) (string, error) {
	if len(b.Files) < 2 {
		return "", errors.New("it is one file already")
	}
	if b.loose {
		return "", errors.New("it is not in a folder of its own")
	}
	// Copy the audio as it is when every part is AAC alike; otherwise encode AAC at
	// about the parts' own bitrate.
	same, kbps := true, 0
	var first audioStream
	for i, f := range b.Files {
		st, err := probeStream(f.path)
		if err != nil {
			return "", fmt.Errorf("%s: %w", f.Name, err)
		}
		if i == 0 {
			first = st
		}
		same = same && st.Codec == "aac" && st.Rate == first.Rate && st.Channels == first.Channels
		if br, err := strconv.Atoi(st.BitRate); err == nil {
			kbps = max(kbps, br/1000)
		}
	}
	codec := []string{"-c:a", "copy"}
	how := "joined without re-encoding"
	if !same {
		rate := min(max(kbps, 48), 128)
		if kbps == 0 {
			rate = 64
		}
		codec = []string{"-c:a", "aac", "-b:a", strconv.Itoa(rate) + "k"}
		how = fmt.Sprintf("encoded to AAC at %d kbps", rate)
	}
	paths := make([]string, len(b.Files))
	for i, f := range b.Files {
		paths[i] = f.path
	}
	// Without chapter marks the apps play the parts as chapters; the merged file keeps
	// them as chapters, named after the files.
	chs := b.Chapters
	if len(chs) == 0 {
		var at int64
		for _, f := range b.Files {
			chs = append(chs, Chapter{Title: strings.TrimSuffix(f.Name, filepath.Ext(f.Name)), StartMs: at, EndMs: at + f.DurationMs})
			at += f.DurationMs
		}
	}
	meta := ffmetadata(fileTags(b, "mp4", 0), chs)
	out := filepath.Join(b.dir, ".kithara-merge.m4b")
	args := append(codec, "-movflags", "+faststart")
	if err := s.remux(paths, true, meta, b.coverPath, args, out, b.DurationMs, progress); err != nil {
		return "", err
	}
	// The old files go aside (or away), then the new one takes their place.
	for _, f := range b.Files {
		var err error
		if deleteOriginals {
			err = os.Remove(f.path)
		} else {
			rel, _ := filepath.Rel(b.dir, f.path)
			dest := filepath.Join(b.dir, ".originals", rel)
			if err = os.MkdirAll(filepath.Dir(dest), 0o755); err == nil {
				err = os.Rename(f.path, dest)
			}
		}
		if err != nil {
			return "", fmt.Errorf("moving %s aside: %w (the merged file is %s)", f.Name, err, out)
		}
	}
	name := strings.TrimSpace(unsafeName.ReplaceAllString(b.Title, " "))
	if name == "" {
		name = "Book"
	}
	final := filepath.Join(b.dir, clip(name, 120)+".m4b")
	for n := 2; ; n++ {
		if _, err := os.Stat(final); errors.Is(err, os.ErrNotExist) {
			break
		}
		final = filepath.Join(b.dir, fmt.Sprintf("%s (%d).m4b", clip(name, 120), n))
	}
	if err := os.Rename(out, final); err != nil {
		return "", err
	}
	note := fmt.Sprintf("%s merged into %s, %s", plural(len(b.Files), "file"), filepath.Base(final), how)
	if deleteOriginals {
		return note + "; the old files were deleted", nil
	}
	return note + "; the old files are in .originals in the book's folder", nil
}

// remux runs ffmpeg over inputs (joined, when concat) with a metadata file, an optional
// cover and the given output options, into out, and checks out is as long as wantMs.
func (s *server) remux(inputs []string, concat bool, meta, cover string, outArgs []string, out string, wantMs int64, progress func(float64)) error {
	tmp, err := os.MkdirTemp("", "kithara-files-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	metaPath := filepath.Join(tmp, "meta.txt")
	if err := os.WriteFile(metaPath, []byte(meta), 0o600); err != nil {
		return err
	}
	args := []string{"-y", "-v", "error", "-nostdin", "-progress", "pipe:1", "-nostats"}
	if concat {
		list := filepath.Join(tmp, "list.txt")
		if err := writeConcatList(list, inputs); err != nil {
			return err
		}
		args = append(args, "-protocol_whitelist", "file", "-f", "concat", "-safe", "0", "-i", list)
	} else {
		args = append(args, "-protocol_whitelist", "file", "-i", inputs[0])
	}
	args = append(args, "-f", "ffmetadata", "-i", metaPath)
	maps := []string{"-map", "0:a", "-map_metadata", "1", "-map_chapters", "1"}
	if cover != "" && goodPicture(cover) {
		args = append(args, "-i", cover)
		maps = append(maps, "-map", "2:v", "-c:v", "copy", "-disposition:v:0", "attached_pic")
	} else if !concat {
		maps = append(maps, "-map", "0:v?", "-c:v", "copy") // keep the picture the file has
	}
	args = append(append(append(args, maps...), outArgs...), out)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(wantMs)*time.Millisecond/2+30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "out_time_us="); ok && progress != nil && wantMs > 0 {
			if us, err := strconv.ParseInt(v, 10, 64); err == nil {
				progress(min(float64(us/1000)/float64(wantMs), 1))
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		os.Remove(out)
		return fmt.Errorf("ffmpeg: %v: %s", err, clip(strings.TrimSpace(stderr.String()), 300))
	}
	got, err := ffprobe(out)
	if err != nil || abs(got.DurationMs-wantMs) > 2000 {
		os.Remove(out)
		return fmt.Errorf("the new file is %d ms long, not %d; nothing was changed", got.DurationMs, wantMs)
	}
	return nil
}

// goodPicture reports whether ffmpeg can read the picture: a broken cover is left out
// rather than failing the whole job.
func goodPicture(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file", "-select_streams", "v:0",
		"-show_entries", "stream=width", "-of", "csv=p=0", path).Output()
	w, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return err == nil && w > 0
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// writeConcatList writes ffmpeg's concat list for whole files.
func writeConcatList(path string, files []string) error {
	var sb strings.Builder
	sb.WriteString("ffconcat version 1.0\n")
	for _, f := range files {
		p, err := filepath.Abs(f)
		if err != nil {
			return err
		}
		if strings.ContainsAny(p, "\r\n") {
			return fmt.Errorf("the file name %q has a line break in it", filepath.Base(p))
		}
		fmt.Fprintf(&sb, "file 'file:%s'\n", strings.ReplaceAll(p, "'", `'\''`))
	}
	return os.WriteFile(path, []byte(sb.String()), 0o600)
}

// ------------------------------------------------------------------ routes (admin)

func (s *server) queueFileJob(kind string) userHandler {
	return func(w http.ResponseWriter, r *http.Request, user, token string) {
		b := s.bookOr404(w, r, user)
		if b == nil {
			return
		}
		var in struct {
			DeleteOriginals bool `json:"deleteOriginals"`
		}
		if r.ContentLength != 0 {
			if err := readBody(r, &in); err != nil {
				writeError(w, http.StatusBadRequest, "send {deleteOriginals}")
				return
			}
		}
		if kind == "merge" && len(b.Files) < 2 {
			writeError(w, http.StatusBadRequest, "that book is one file already")
			return
		}
		if err := s.files.add(fileJob{BookID: b.ID, Kind: kind, DeleteOriginals: in.DeleteOriginals, By: user}); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *server) fileJobStatus(w http.ResponseWriter, r *http.Request, user, token string) {
	f := s.files
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]any{"queue": append([]fileJob{}, f.queue...), "recent": append([]FileJobResult{}, f.recent...)}
	if f.running != nil {
		out["running"] = map[string]any{"bookId": f.running.BookID, "kind": f.running.Kind, "progress": f.progress}
	}
	writeJSONResponse(w, 200, out)
}

func (s *server) cancelFileJob(w http.ResponseWriter, r *http.Request, user, token string) {
	f := s.files
	f.mu.Lock()
	n := len(f.queue)
	f.queue = slices.DeleteFunc(f.queue, func(j fileJob) bool { return j.BookID == r.PathValue("bookId") })
	removed := n - len(f.queue)
	f.mu.Unlock()
	writeJSONResponse(w, 200, map[string]int{"removed": removed})
}
