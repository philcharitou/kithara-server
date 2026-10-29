package main

import (
	"bufio"
	"bytes"
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
	"sync/atomic"
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
	// Auto transcribes books as they arrive, in AutoLibraries (all of them when empty).
	Auto          bool     `json:"auto,omitempty"`
	AutoLibraries []string `json:"autoLibraries,omitempty"`
}

// autoTranscribe queues new books, when the admin has asked for that.
func (s *server) autoTranscribe(ids []string) {
	cfg := s.trans.settings()
	if !cfg.Auto || !s.trans.available() {
		return
	}
	var reqs []queueRequest
	for _, id := range ids {
		b := s.lib.book(id)
		if b == nil || len(cfg.AutoLibraries) > 0 && !slices.ContainsFunc(cfg.AutoLibraries, func(n string) bool { return strings.EqualFold(n, b.Library) }) {
			continue
		}
		reqs = append(reqs, queueRequest{book: b})
	}
	if n := s.trans.enqueue(reqs, false, autoQueuer, 0); n > 0 {
		log.Printf("transcribing %d new book(s): %d chapters queued", len(reqs), n)
	}
}

// autoQueuer is who the queue says asked, for books queued as they arrived.
const autoQueuer = "(new books)"

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
	startedAt int64   // when it began (ms)
	cancel    context.CancelFunc
	lastError string
	downloads map[string]*modelDownload

	binMu      sync.Mutex
	bin        string // whisper-cli, or "" when there is none
	binVAD     bool   // it has a voice-activity detector
	binChecked time.Time
	binStamp   fileStamp // of bin when its --help was read
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
		// About the number of cores, not hardware threads: past that whisper.cpp gets
		// slower, not faster (15 threads took nearly three times as long as 4 on an 8-core,
		// 16-thread laptop).
		c.Threads = min(max(runtime.NumCPU()/2, 1), 8)
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
	bin, vad, known, stamp := t.bin, t.binVAD, t.bin, t.binStamp
	fresh := time.Since(t.binChecked) < time.Minute
	if !fresh {
		t.binChecked = time.Now() // others use the last answer meanwhile
	}
	t.binMu.Unlock()
	if fresh {
		return bin, vad
	}
	bin, vad = "", false
	candidates := []string{"whisper-cli", "whisper-cpp"}
	if env := os.Getenv("KITHARA_WHISPER"); env != "" {
		candidates = []string{env}
	}
	var found fileStamp
	for _, c := range candidates {
		p, err := exec.LookPath(c)
		if err != nil {
			continue
		}
		found = stampOf(p)
		if p == known && found == stamp {
			bin, vad = known, t.binVADLocked()
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		help, _ := exec.CommandContext(ctx, p, "--help").CombinedOutput()
		cancel()
		// Make sure it is whisper.cpp (OpenAI's Python whisper takes other arguments).
		if strings.Contains(string(help), "--output-json") {
			bin, vad = p, strings.Contains(string(help), "--vad")
			break
		}
	}
	t.binMu.Lock()
	t.bin, t.binVAD, t.binStamp = bin, vad, found
	t.binMu.Unlock()
	return bin, vad
}

func (t *transcriber) binVADLocked() bool {
	t.binMu.Lock()
	defer t.binMu.Unlock()
	return t.binVAD
}

func (t *transcriber) available() bool { bin, _ := t.whisper(); return bin != "" }

// ------------------------------------------------------------------ the queue

func (t *transcriber) saveQueueLocked() {
	if err := writeJSONCompact(filepath.Join(t.dir, "transcribe-queue.json"), t.queue); err != nil {
		log.Printf("transcribe queue: %v", err)
	}
}

// queueRequest is chapters of a book to transcribe (all of them for nil).
type queueRequest struct {
	book     *Book
	chapters []int
}

// enqueue adds the chapters asked for, skipping ones already queued and, unless again,
// ones already transcribed. A listener (quota > 0) may have at most quota chapters
// waiting. It returns how many it added.
func (t *transcriber) enqueue(reqs []queueRequest, again bool, by string, quota int) int {
	type key struct {
		book    string
		chapter int
	}
	// Work out what is wanted first, reading transcripts without holding t.mu.
	var want []transcribeJob
	seen := map[key]bool{}
	now := time.Now().UnixMilli()
	for _, rq := range reqs {
		chs := chapterRanges(rq.book)
		chapters := rq.chapters
		if chapters == nil {
			for i := range chs {
				chapters = append(chapters, i)
			}
		}
		var tf transcriptFile
		if !again {
			tf = t.s.transcribedOf(rq.book)
		}
		for _, i := range chapters {
			k := key{rq.book.ID, i}
			if i < 0 || i >= len(chs) || seen[k] {
				continue
			}
			seen[k] = true
			if st := chapterStatus(tf, chs[i]); !again && st != nil && st.Status == statusDone {
				continue
			}
			want = append(want, transcribeJob{BookID: rq.book.ID, Chapter: i, By: by, AddedAt: now})
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	queued, mine := map[key]bool{}, 0
	for _, j := range t.queue {
		queued[key{j.BookID, j.Chapter}] = true
		if j.By == by {
			mine++
		}
	}
	n := 0
	for _, j := range want {
		if len(t.queue) >= maxTranscribeQueue || quota > 0 && mine >= quota {
			break
		}
		if queued[key{j.BookID, j.Chapter}] {
			continue
		}
		t.queue = append(t.queue, j)
		n++
		mine++
	}
	if n > 0 {
		t.saveQueueLocked()
		defer t.poke()
	}
	return n
}

// drop takes a book's jobs (every job, for "") off the queue, only those queued by
// "by" unless that is "", stopping the one running if it is one of them.
func (t *transcriber) drop(bookID, by string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	kept := t.queue[:0]
	for i, j := range t.queue {
		if (bookID == "" || j.BookID == bookID) && (by == "" || j.By == by) {
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
	if n > 0 {
		t.saveQueueLocked()
	}
	return n
}

// next waits for a job it can run: one queued, the program installed, and the model
// downloaded.
func (t *transcriber) next() (transcribeJob, context.Context) {
	for {
		t.mu.Lock()
		ready := len(t.queue) > 0
		t.mu.Unlock()
		if ready && t.available() && t.installedCurrent() {
			t.mu.Lock()
			if len(t.queue) > 0 {
				j := t.queue[0]
				ctx, cancel := context.WithCancel(context.Background())
				t.running, t.progress, t.cancel, t.startedAt = true, 0, cancel, time.Now().UnixMilli()
				t.mu.Unlock()
				return j, ctx
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
		j, ctx := t.next()
		err := t.runJob(ctx, j)
		t.mu.Lock()
		if t.cancel != nil {
			t.cancel()
		}
		t.running, t.cancel = false, nil
		if len(t.queue) > 0 && t.queue[0] == j {
			t.queue = t.queue[1:]
		}
		t.saveQueueLocked()
		switch {
		case err != nil && ctx.Err() == nil:
			t.lastError = fmt.Sprintf("%s, chapter %d: %v", j.BookID, j.Chapter+1, err)
			log.Printf("transcribe %s", t.lastError)
		case err == nil:
			t.lastError = "" // working again
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
		if st := chapterStatus(t.s.transcribedOf(b), c); st != nil && st.Status == statusDone {
			return err // keep the finished one
		}
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
	return joinSentences(out), nil
}

// Whisper cuts where it likes, often mid-sentence ("as far as I" / "could tell."), which
// reads badly along with the audio. joinSentences joins a passage to the next while it
// has not reached the end of a sentence, the two follow on closely, and the result
// stays a readable length.
const (
	joinGapMs   = 1500
	joinMaxText = 400
)

func joinSentences(ps []Passage) []Passage {
	out := make([]Passage, 0, len(ps))
	for _, p := range ps {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if !endsSentence(last.Text) && p.StartMs-last.EndMs <= joinGapMs && len(last.Text)+len(p.Text) < joinMaxText {
				last.Text += " " + p.Text
				last.EndMs = p.EndMs
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func endsSentence(s string) bool {
	s = strings.TrimRight(s, `"')]»”’ `)
	return s == "" || strings.ContainsAny(s[len(s)-1:], ".!?") || strings.HasSuffix(s, "…")
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
		// Absolute (the list file lives in a temporary folder, and ffmpeg resolves
		// relative entries against it), as a file: URL so a ':' in a name is not taken
		// for a protocol. The list is read line by line, so a line break cannot be quoted.
		abs, err := filepath.Abs(f.path)
		if err != nil {
			return err
		}
		if strings.ContainsAny(abs, "\r\n") {
			return fmt.Errorf("the file name %q has a line break in it", f.Name)
		}
		fmt.Fprintf(&list, "file 'file:%s'\n", strings.ReplaceAll(abs, "'", `'\''`))
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

// backendLines is how many of whisper-cli's lines about its compute device (a GPU
// through Vulkan or CUDA, or none) go to the log: enough to see what a run is using.
var backendLines atomic.Int32

func noteBackend(line string) {
	l := strings.ToLower(line)
	if (strings.Contains(l, "ggml_vulkan") || strings.Contains(l, "ggml_cuda") || strings.Contains(l, "backend")) && backendLines.Add(1) <= 6 {
		log.Printf("whisper-cli: %s", clip(line, 200))
	}
}

var progressLine = regexp.MustCompile(`progress\s*=\s*(\d+)%`)

func (p *progressWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexAny(p.buf, "\r\n")
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(p.buf[:i]))
		p.buf = p.buf[i+1:]
		if m := progressLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			p.report(float64(min(n, 100)) / 100)
		} else if line != "" {
			noteBackend(line)
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

// downloading serialises downloads of the same file (every model brings the voice
// model with it).
var downloading sync.Map // path -> *sync.Mutex

// downloadStall is how long a download may go without a byte before it is given up.
const downloadStall = 2 * time.Minute

// download fetches url to path unless it is there already.
func download(url, path string, progress func(got, total int64)) error {
	mu, _ := downloading.LoadOrStore(path, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Hugging Face answers with a redirect to its file store; the redirect carries the
	// file's SHA-256 as X-Linked-Etag.
	var want string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stall := time.AfterFunc(downloadStall, cancel)
	defer stall.Stop()
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 30 * time.Second, ResponseHeaderTimeout: time.Minute}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if want == "" && req.Response != nil {
			want = req.Response.Header.Get("X-Linked-Etag")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
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
	if len(want) != 64 {
		return errors.New("the download came without a checksum to check it against")
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	part := f.Name()
	h := sha256.New()
	var got int64
	buf := bufio.NewReaderSize(res.Body, 1<<20)
	chunk := make([]byte, 1<<20)
	for {
		n, rerr := buf.Read(chunk)
		if n > 0 {
			stall.Reset(downloadStall)
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
			if ctx.Err() != nil {
				return errors.New("the download stalled")
			}
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
	case !strings.EqualFold(want, sum):
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
	Queue     []transcribeJob           `json:"queue"`   // the first 500 waiting
	Waiting   []WaitingBook             `json:"waiting"` // all of them, by book, in queue order
	Models    []ModelState              `json:"models,omitempty"`
	LastError string                    `json:"lastError,omitempty"`
	Downloads map[string]*modelDownload `json:"downloads,omitempty"`
}

type RunningJob struct {
	transcribeJob
	Progress  float64 `json:"progress"`
	StartedAt int64   `json:"startedAt"`
	Mine      bool    `json:"mine,omitempty"` // queued by the asker, who may stop it
}

// WaitingBook is a book's chapters waiting their turn; mine of them were queued by
// the asker.
type WaitingBook struct {
	BookID   string `json:"bookId"`
	Chapters int    `json:"chapters"`
	Mine     int    `json:"mine,omitempty"`
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
	st := TranscriptionStatus{Available: bin != "", VAD: vad, Settings: t.settings(), Ready: t.installedCurrent(),
		Queue: []transcribeJob{}, Waiting: []WaitingBook{}}
	t.mu.Lock()
	at := map[string]int{}
	for i, j := range t.queue {
		if s.lib.bookFor(a, j.BookID) == nil {
			continue
		}
		mine := j.By == user
		if !a.admin() {
			j.By = ""
		}
		if i == 0 && t.running {
			st.Running = &RunningJob{j, t.progress, t.startedAt, mine}
			continue
		}
		if len(st.Queue) < 500 {
			st.Queue = append(st.Queue, j)
		}
		k, ok := at[j.BookID]
		if !ok {
			k = len(st.Waiting)
			at[j.BookID] = k
			st.Waiting = append(st.Waiting, WaitingBook{BookID: j.BookID})
		}
		st.Waiting[k].Chapters++
		if mine {
			st.Waiting[k].Mine++
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
	if err := s.checkLibraries(in.AutoLibraries); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t := s.trans
	t.mu.Lock()
	err := writeJSON(filepath.Join(t.dir, "transcription.json"), in)
	if err == nil {
		t.cfg = in
	}
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
	// Transcripts are shared, so only an admin redoes finished ones, and a listener has
	// a limit on how much they can have waiting.
	quota := 0
	if !s.acct(user).admin() {
		if in.Again {
			writeError(w, http.StatusForbidden, "only an admin can transcribe a book again")
			return
		}
		quota = listenerQuota
	}
	writeJSONResponse(w, 200, map[string]int{"queued": s.trans.enqueue([]queueRequest{{b, in.Chapters}}, in.Again, user, quota)})
}

// listenerQuota is how many chapters one listener may have waiting.
const listenerQuota = 2000

func (s *server) cancelBook(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	// A listener stops what they asked for; an admin stops the book.
	by := user
	if s.acct(user).admin() {
		by = ""
	}
	writeJSONResponse(w, 200, map[string]int{"removed": s.trans.drop(b.ID, by)})
}

// transcribeLibrary (admin) queues every chapter of every book not yet transcribed.
func (s *server) transcribeLibrary(w http.ResponseWriter, r *http.Request, user, token string) {
	if !s.trans.available() {
		writeError(w, http.StatusConflict, "this server cannot transcribe: whisper.cpp is not installed")
		return
	}
	var reqs []queueRequest
	for _, b := range s.lib.snapshot().Books {
		if full := s.lib.book(b.ID); full != nil {
			reqs = append(reqs, queueRequest{book: full})
		}
	}
	writeJSONResponse(w, 200, map[string]int{"queued": s.trans.enqueue(reqs, false, user, 0)})
}

func (s *server) clearQueue(w http.ResponseWriter, r *http.Request, user, token string) {
	n := s.trans.drop("", "")
	s.trans.mu.Lock()
	s.trans.lastError = ""
	s.trans.mu.Unlock()
	writeJSONResponse(w, 200, map[string]int{"removed": n})
}
