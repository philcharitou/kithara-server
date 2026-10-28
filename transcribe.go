package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Transcription on the server, with whisper.cpp.
//
// A phone takes minutes per chapter; the machine a library lives on is usually far
// faster, and plugged in. The server runs whisper.cpp's command-line program
// (whisper-cli) the way it runs ffprobe: if it is installed (it is in the Docker
// image) transcription is offered, and if not, nothing else changes. Models are
// downloaded on first use into the data folder.
//
// Jobs are chapters, queued and worked through one at a time. For each, ffmpeg cuts the
// chapter out of the book's files (a chapter may run across two) as 16 kHz mono in
// pieces of up to twenty minutes, and whisper-cli transcribes each piece, using its
// voice-activity detector where it has one so that passages start where speech does.

type whisperModel struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	File    string `json:"-"`
	Bytes   int64  `json:"sizeBytes"` // roughly; for showing, not for checking
	English bool   `json:"englishOnly"`
}

var whisperModels = []whisperModel{
	{"tiny.en", "Tiny (English)", "ggml-tiny.en.bin", 78e6, true},
	{"base.en", "Base (English)", "ggml-base.en.bin", 148e6, true},
	{"small.en", "Small (English)", "ggml-small.en.bin", 488e6, true},
	{"medium.en", "Medium (English)", "ggml-medium.en.bin", 1533e6, true},
	{"large-v3-turbo-q5_0", "Large v3 turbo, compressed (any language)", "ggml-large-v3-turbo-q5_0.bin", 574e6, false},
	{"large-v3-turbo", "Large v3 turbo (any language)", "ggml-large-v3-turbo.bin", 1624e6, false},
}

const (
	defaultWhisperModel = "small.en"
	whisperModelURL     = "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/"
	vadModelFile        = "ggml-silero-v5.1.2.bin"
	vadModelURL         = "https://huggingface.co/ggml-org/whisper-vad/resolve/main/" + vadModelFile
	transcribePiece     = 20 * 60 * 1000 // ms of audio given to whisper at once
	maxTranscribeQueue  = 20000
)

func modelByID(id string) (whisperModel, bool) {
	i := slices.IndexFunc(whisperModels, func(m whisperModel) bool { return m.ID == id })
	if i < 0 {
		return whisperModel{}, false
	}
	return whisperModels[i], true
}

type TranscribeSettings struct {
	Model    string `json:"model,omitempty"`
	Threads  int    `json:"threads,omitempty"`
	Language string `json:"language,omitempty"` // en, another code, or auto; multilingual models only
}

type transcribeJob struct {
	BookID  string `json:"bookId"`
	Chapter int    `json:"chapter"`
	By      string `json:"by"`
	AddedAt int64  `json:"addedAt"`
}

type modelDownload struct {
	Got   int64  `json:"got"`
	Total int64  `json:"total"`
	Error string `json:"error,omitempty"`
}

type transcriber struct {
	s    *server
	dir  string // the data folder
	wake chan struct{}
	once sync.Once

	mu        sync.Mutex
	cfg       TranscribeSettings
	queue     []transcribeJob // the first is the one running, if any
	running   bool
	progress  float64 // of the running job, 0 to 1
	cancel    context.CancelFunc
	lastError string
	downloads map[string]*modelDownload

	binMu      sync.Mutex
	bin        string // whisper-cli, or "" when there is none
	binVAD     bool   // it has a voice-activity detector
	binChecked time.Time
}

func newTranscriber(s *server) *transcriber {
	t := &transcriber{s: s, dir: s.store.dir, wake: make(chan struct{}, 1), downloads: map[string]*modelDownload{}}
	if err := readJSON(filepath.Join(t.dir, "transcription.json"), &t.cfg); err != nil {
		log.Printf("transcription.json: %v", err)
	}
	if err := readJSON(filepath.Join(t.dir, "transcribe-queue.json"), &t.queue); err != nil {
		log.Printf("transcribe-queue.json: %v", err)
	}
	if len(t.queue) > 0 {
		t.start()
	}
	return t
}

func (t *transcriber) start() { t.once.Do(func() { go t.run() }) }

func (t *transcriber) poke() {
	t.start()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *transcriber) settings() TranscribeSettings {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.cfg
	if _, ok := modelByID(c.Model); !ok {
		c.Model = defaultWhisperModel
	}
	if c.Threads <= 0 {
		c.Threads = min(max(runtime.NumCPU()-1, 1), 8)
	}
	m, _ := modelByID(c.Model)
	if c.Language == "" || m.English {
		c.Language = "en"
	}
	return c
}

func (t *transcriber) modelPath(file string) string { return filepath.Join(t.dir, "models", file) }

func (t *transcriber) installed(m whisperModel) bool {
	_, err := os.Stat(t.modelPath(m.File))
	return err == nil
}

// whisper finds whisper.cpp's program (KITHARA_WHISPER, or whisper-cli on the PATH)
// and whether it has voice-activity detection, looking again at most once a minute.
func (t *transcriber) whisper() (bin string, vad bool) {
	t.binMu.Lock()
	defer t.binMu.Unlock()
	if time.Since(t.binChecked) < time.Minute {
		return t.bin, t.binVAD
	}
	t.binChecked = time.Now()
	t.bin, t.binVAD = "", false
	candidates := []string{"whisper-cli", "whisper-cpp"}
	if env := os.Getenv("KITHARA_WHISPER"); env != "" {
		candidates = []string{env}
	}
	for _, c := range candidates {
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		help, _ := exec.CommandContext(ctx, p, "--help").CombinedOutput()
		cancel()
		// Make sure it is whisper.cpp (OpenAI's Python whisper takes other arguments).
		if strings.Contains(string(help), "--output-json") {
			t.bin, t.binVAD = p, strings.Contains(string(help), "--vad")
			break
		}
	}
	return t.bin, t.binVAD
}

func (t *transcriber) available() bool { bin, _ := t.whisper(); return bin != "" }

// ------------------------------------------------------------------ the queue

func (t *transcriber) saveQueueLocked() {
	if err := writeJSONCompact(filepath.Join(t.dir, "transcribe-queue.json"), t.queue); err != nil {
		log.Printf("transcribe queue: %v", err)
	}
}

// enqueue adds chapters of a book (all of them for nil), skipping ones already queued
// and, unless again, ones already transcribed. It returns how many it added.
func (t *transcriber) enqueue(b *Book, chapters []int, again bool, by string) int {
	chs := chapterRanges(b)
	if chapters == nil {
		for i := range chs {
			chapters = append(chapters, i)
		}
	}
	var tf transcriptFile
	if !again {
		t.s.tmu.Lock()
		tf, _ = t.s.loadTranscript(b)
		t.s.tmu.Unlock()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, i := range chapters {
		if i < 0 || i >= len(chs) || len(t.queue) >= maxTranscribeQueue {
			continue
		}
		if slices.ContainsFunc(t.queue, func(j transcribeJob) bool { return j.BookID == b.ID && j.Chapter == i }) {
			continue
		}
		if st := chapterStatus(tf, chs[i]); !again && st != nil && st.Status == statusDone {
			continue
		}
		t.queue = append(t.queue, transcribeJob{BookID: b.ID, Chapter: i, By: by, AddedAt: time.Now().UnixMilli()})
		n++
	}
	if n > 0 {
		t.saveQueueLocked()
		defer t.poke()
	}
	return n
}

// drop takes a book's jobs (every job, for "") off the queue, stopping the one running
// if it is one of them.
func (t *transcriber) drop(bookID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	kept := t.queue[:0]
	for i, j := range t.queue {
		if bookID == "" || j.BookID == bookID {
			if i == 0 && t.running && t.cancel != nil {
				t.cancel() // the runner takes it off when it stops
				kept = append(kept, j)
			}
			n++
			continue
		}
		kept = append(kept, j)
	}
	t.queue = kept
	t.saveQueueLocked()
	return n
}

// next waits for a job it can run: one queued, the program installed, and the model
// downloaded.
func (t *transcriber) next() transcribeJob {
	for {
		t.mu.Lock()
		ready := len(t.queue) > 0
		t.mu.Unlock()
		if ready && t.available() && t.installedCurrent() {
			t.mu.Lock()
			if len(t.queue) > 0 {
				j := t.queue[0]
				t.running, t.progress = true, 0
				t.mu.Unlock()
				return j
			}
			t.mu.Unlock()
		}
		select {
		case <-t.wake:
		case <-time.After(time.Minute):
		}
	}
}

func (t *transcriber) installedCurrent() bool {
	m, _ := modelByID(t.settings().Model)
	return t.installed(m)
}

func (t *transcriber) run() {
	for {
		j := t.next()
		ctx, cancel := context.WithCancel(context.Background())
		t.mu.Lock()
		t.cancel = cancel
		t.mu.Unlock()
		err := t.runJob(ctx, j)
		cancel()
		t.mu.Lock()
		t.running, t.cancel = false, nil
		if len(t.queue) > 0 && t.queue[0] == j {
			t.queue = t.queue[1:]
		}
		t.saveQueueLocked()
		if err != nil && ctx.Err() == nil {
			t.lastError = fmt.Sprintf("%s, chapter %d: %v", j.BookID, j.Chapter+1, err)
			log.Printf("transcribe %s", t.lastError)
		}
		t.mu.Unlock()
	}
}

// runJob transcribes one chapter and stores what it got: all of it, or as much as it
// managed before an error or a cancel (marked PARTIAL).
func (t *transcriber) runJob(ctx context.Context, j transcribeJob) error {
	b := t.s.lib.book(j.BookID)
	if b == nil {
		return nil // the book has gone
	}
	chs := chapterRanges(b)
	if j.Chapter >= len(chs) {
		return nil
	}
	c, cfg := chs[j.Chapter], t.settings()
	m, _ := modelByID(cfg.Model)
	start := time.Now()
	ps, err := t.transcribeRange(ctx, b, c.StartMs, c.EndMs, cfg, m, func(f float64) {
		t.mu.Lock()
		t.progress = f
		t.mu.Unlock()
	})
	if err != nil && len(ps) == 0 {
		return err
	}
	status := statusDone
	if err != nil {
		status = statusPartial
	}
	entry := transcribed{Status: status, Model: m.ID, Engine: "whisper.cpp", UpdatedAt: time.Now().UnixMilli()}
	if serr := t.s.saveChapter(b, c, entry, ps); serr != nil {
		return serr
	}
	if err == nil {
		log.Printf("transcribed %q chapter %d (%s of audio in %s)", b.Title, j.Chapter+1,
			time.Duration(c.EndMs-c.StartMs)*time.Millisecond, time.Since(start).Round(time.Second))
	}
	return err
}

// transcribeRange transcribes [startMs, endMs) of a book, piece by piece.
func (t *transcriber) transcribeRange(ctx context.Context, b *Book, startMs, endMs int64, cfg TranscribeSettings, m whisperModel, progress func(float64)) ([]Passage, error) {
	bin, vad := t.whisper()
	if bin == "" {
		return nil, errors.New("whisper.cpp is not installed")
	}
	tmp, err := os.MkdirTemp("", "kithara-transcribe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	pieces := int((endMs - startMs + transcribePiece - 1) / transcribePiece)
	var out []Passage
	for i := range pieces {
		from := startMs + int64(i)*transcribePiece
		to := min(from+transcribePiece, endMs)
		wav := filepath.Join(tmp, "piece.wav")
		if err := decodeRange(ctx, b, from, to, wav); err != nil {
			return out, err
		}
		args := []string{"-m", t.modelPath(m.File), "-f", wav, "-oj", "-of", filepath.Join(tmp, "piece"),
			"-l", cfg.Language, "-t", strconv.Itoa(cfg.Threads), "-pp"}
		if vadPath := t.modelPath(vadModelFile); vad {
			if _, err := os.Stat(vadPath); err == nil {
				args = append(args, "--vad", "-vm", vadPath)
			}
		}
		// Slow machines and big models can take several times the audio's length.
		pctx, cancel := context.WithTimeout(ctx, time.Duration(to-from)*time.Millisecond*10+10*time.Minute)
		cmd := exec.CommandContext(pctx, bin, args...)
		lines := &progressWriter{report: func(p float64) { progress((float64(i) + p) / float64(pieces)) }}
		cmd.Stdout, cmd.Stderr = lines, lines
		err := cmd.Run()
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("whisper-cli: %v: %s", err, lines.tail())
		}
		got, err := readWhisperJSON(filepath.Join(tmp, "piece.json"), from, to)
		if err != nil {
			return out, err
		}
		out = append(out, got...)
		progress(float64(i+1) / float64(pieces))
	}
	return out, nil
}

// decodeRange writes [from, to) of the book as 16 kHz mono WAV, cutting it out of
// whichever files it falls in with ffmpeg's concat list (inpoint and outpoint per file).
func decodeRange(ctx context.Context, b *Book, from, to int64, wav string) error {
	var list strings.Builder
	list.WriteString("ffconcat version 1.0\n")
	var at int64
	for _, f := range b.Files {
		fs, fe := at, at+f.DurationMs
		at = fe
		if fe <= from || fs >= to {
			continue
		}
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(f.path, "'", `'\''`))
		if from > fs {
			fmt.Fprintf(&list, "inpoint %.3f\n", float64(from-fs)/1000)
		}
		if to < fe {
			fmt.Fprintf(&list, "outpoint %.3f\n", float64(to-fs)/1000)
		}
	}
	listPath := wav + ".txt"
	if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(dctx, "ffmpeg", "-y", "-v", "error", "-protocol_whitelist", "file",
		"-f", "concat", "-safe", "0", "-i", listPath, "-vn", "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, clip(strings.TrimSpace(string(out)), 300))
	}
	return nil
}

// noise is whisper's way of saying there was no speech: [BLANK_AUDIO], (music) and
// the like.
var noise = regexp.MustCompile(`^\s*[\[(][^\])]*[\])]\s*$`)

// readWhisperJSON reads whisper-cli's -oj output for a piece starting at from (ms on
// the book's timeline).
func readWhisperJSON(path string, from, to int64) ([]Passage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out struct {
		Transcription []struct {
			Offsets struct{ From, To int64 } `json:"offsets"`
			Text    string                   `json:"text"`
		} `json:"transcription"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("whisper-cli output: %v", err)
	}
	ps := make([]Passage, 0, len(out.Transcription))
	for _, seg := range out.Transcription {
		text := clip(strings.TrimSpace(seg.Text), maxPassageText)
		if text == "" || noise.MatchString(text) {
			continue
		}
		start := min(from+seg.Offsets.From, to)
		ps = append(ps, Passage{StartMs: start, EndMs: max(min(from+seg.Offsets.To, to), start), Text: text})
	}
	return ps, nil
}

// progressWriter picks whisper-cli's "progress = 42%" lines out of its output and keeps
// the last few lines for error messages.
type progressWriter struct {
	report func(float64)
	buf    []byte
	last   []string
}

var progressLine = regexp.MustCompile(`progress\s*=\s*(\d+)%`)

func (p *progressWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := strings.IndexAny(string(p.buf), "\r\n")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(p.buf[:i]))
		p.buf = p.buf[i+1:]
		if m := progressLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			p.report(float64(min(n, 100)) / 100)
		} else if line != "" {
			p.last = append(p.last, line)
			if len(p.last) > 5 {
				p.last = p.last[1:]
			}
		}
	}
	if len(p.buf) > 1<<16 {
		p.buf = p.buf[:0]
	}
	return len(b), nil
}

func (p *progressWriter) tail() string { return clip(strings.Join(p.last, " / "), 500) }

// ------------------------------------------------------------------ models

// fetchModel downloads a model (and the small voice-activity model with it) into the
// data folder, checking it against the SHA-256 Hugging Face publishes for it.
func (t *transcriber) fetchModel(m whisperModel) {
	t.mu.Lock()
	if _, busy := t.downloads[m.ID]; busy {
		t.mu.Unlock()
		return
	}
	d := &modelDownload{Total: m.Bytes}
	t.downloads[m.ID] = d
	t.mu.Unlock()
	go func() {
		err := download(vadModelURL, t.modelPath(vadModelFile), nil)
		if err == nil {
			err = download(whisperModelURL+m.File, t.modelPath(m.File), func(got, total int64) {
				t.mu.Lock()
				d.Got = got
				if total > 0 {
					d.Total = total
				}
				t.mu.Unlock()
			})
		}
		t.mu.Lock()
		if err != nil {
			d.Error = err.Error()
			log.Printf("downloading %s: %v", m.ID, err)
		} else {
			delete(t.downloads, m.ID)
		}
		t.mu.Unlock()
		t.poke()
	}()
}

// download fetches url to path unless it is there already.
func download(url, path string, progress func(got, total int64)) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Hugging Face answers with a redirect to its file store; the redirect carries the
	// file's SHA-256 as X-Linked-Etag.
	var want string
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if want == "" && req.Response != nil {
			want = req.Response.Header.Get("X-Linked-Etag")
		}
		return nil
	}}
	res, err := client.Get(url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, res.Status)
	}
	if want == "" {
		want = res.Header.Get("X-Linked-Etag")
	}
	want = strings.Trim(want, `"W/`)
	part := path + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	var got int64
	buf := bufio.NewReaderSize(res.Body, 1<<20)
	chunk := make([]byte, 1<<20)
	for {
		n, rerr := buf.Read(chunk)
		if n > 0 {
			if _, werr := f.Write(chunk[:n]); werr != nil {
				f.Close()
				os.Remove(part)
				return werr
			}
			h.Write(chunk[:n])
			got += int64(n)
			if progress != nil {
				progress(got, res.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(part)
			return rerr
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(part)
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	switch {
	case res.ContentLength > 0 && got != res.ContentLength:
		err = fmt.Errorf("got %d of %d bytes", got, res.ContentLength)
	case len(want) == 64 && !strings.EqualFold(want, sum):
		err = errors.New("the download does not match its checksum")
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, path)
}

// ------------------------------------------------------------------ routes

type TranscriptionStatus struct {
	Available bool                      `json:"available"`
	VAD       bool                      `json:"vad,omitempty"`
	Settings  TranscribeSettings        `json:"settings"`
	Ready     bool                      `json:"ready"` // the chosen model is downloaded
	Running   *RunningJob               `json:"running,omitempty"`
	Queue     []transcribeJob           `json:"queue"`
	Models    []ModelState              `json:"models,omitempty"`
	LastError string                    `json:"lastError,omitempty"`
	Downloads map[string]*modelDownload `json:"downloads,omitempty"`
}

type RunningJob struct {
	transcribeJob
	Progress float64 `json:"progress"`
}

type ModelState struct {
	whisperModel
	Installed bool `json:"installed"`
}

// transcriptionStatus: whether the server can transcribe, and what it is doing. Listeners
// see the queue for the books they may see; admins see it all, with models and errors.
func (s *server) transcriptionStatus(w http.ResponseWriter, r *http.Request, user, token string) {
	t := s.trans
	a := s.acct(user)
	bin, vad := t.whisper()
	if _, err := os.Stat(t.modelPath(vadModelFile)); err != nil {
		vad = false // it can, but has nothing to detect with yet
	}
	st := TranscriptionStatus{Available: bin != "", VAD: vad, Settings: t.settings(), Ready: t.installedCurrent(), Queue: []transcribeJob{}}
	t.mu.Lock()
	for i, j := range t.queue {
		if s.lib.bookFor(a, j.BookID) == nil {
			continue
		}
		if !a.admin() {
			j.By = ""
		}
		if i == 0 && t.running {
			st.Running = &RunningJob{j, t.progress}
			continue
		}
		if len(st.Queue) < 500 {
			st.Queue = append(st.Queue, j)
		}
	}
	if a.admin() {
		st.LastError = t.lastError
		if len(t.downloads) > 0 {
			st.Downloads = map[string]*modelDownload{}
			for k, d := range t.downloads {
				c := *d
				st.Downloads[k] = &c
			}
		}
	}
	t.mu.Unlock()
	if a.admin() {
		for _, m := range whisperModels {
			st.Models = append(st.Models, ModelState{m, t.installed(m)})
		}
	}
	writeJSONResponse(w, 200, st)
}

var languageCode = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)

func (s *server) putTranscribeSettings(w http.ResponseWriter, r *http.Request, user, token string) {
	var in TranscribeSettings
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad settings")
		return
	}
	if _, ok := modelByID(in.Model); !ok && in.Model != "" {
		writeError(w, http.StatusBadRequest, "no such model")
		return
	}
	if in.Threads < 0 || in.Threads > 64 || in.Language != "" && !languageCode.MatchString(in.Language) {
		writeError(w, http.StatusBadRequest, "threads are 1 to 64, and a language is a code such as en or de, or auto")
		return
	}
	t := s.trans
	t.mu.Lock()
	t.cfg = in
	err := writeJSON(filepath.Join(t.dir, "transcription.json"), in)
	t.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.poke()
	s.transcriptionStatus(w, r, user, token)
}

func (s *server) fetchModelHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	m, ok := modelByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such model")
		return
	}
	s.trans.mu.Lock()
	if d := s.trans.downloads[m.ID]; d != nil && d.Error != "" {
		delete(s.trans.downloads, m.ID) // a failed one: try again
	}
	s.trans.mu.Unlock()
	s.trans.fetchModel(m)
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) deleteModel(w http.ResponseWriter, r *http.Request, user, token string) {
	m, ok := modelByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such model")
		return
	}
	if err := os.Remove(s.trans.modelPath(m.File)); err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// transcribeBook queues a book's chapters: {"chapters": [0, 1]} (all of them without),
// {"again": true} to redo ones already transcribed.
func (s *server) transcribeBook(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	var in struct {
		Chapters []int `json:"chapters"`
		Again    bool  `json:"again"`
	}
	if r.ContentLength != 0 {
		if err := readBody(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "send {chapters, again}")
			return
		}
	}
	if !s.trans.available() {
		writeError(w, http.StatusConflict, "this server cannot transcribe: whisper.cpp is not installed")
		return
	}
	writeJSONResponse(w, 200, map[string]int{"queued": s.trans.enqueue(b, in.Chapters, in.Again, user)})
}

func (s *server) cancelBook(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	writeJSONResponse(w, 200, map[string]int{"removed": s.trans.drop(b.ID)})
}

// transcribeLibrary (admin) queues every chapter of every book not yet transcribed.
func (s *server) transcribeLibrary(w http.ResponseWriter, r *http.Request, user, token string) {
	if !s.trans.available() {
		writeError(w, http.StatusConflict, "this server cannot transcribe: whisper.cpp is not installed")
		return
	}
	n := 0
	for _, b := range s.lib.snapshot().Books {
		if full := s.lib.book(b.ID); full != nil {
			n += s.trans.enqueue(full, nil, false, user)
		}
	}
	writeJSONResponse(w, 200, map[string]int{"queued": n})
}

func (s *server) clearQueue(w http.ResponseWriter, r *http.Request, user, token string) {
	writeJSONResponse(w, 200, map[string]int{"removed": s.trans.drop("")})
}
