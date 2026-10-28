package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Transcripts: what is said in a book, passage by passage, on the book's own timeline.
// They are made here by whisper.cpp (transcribe.go) or on a phone and sent up, and kept
// per book, shared by everyone who may see it: it is the same audio for all of them.
//
// What is stored is a flat list of passages plus the stretches that have been
// transcribed, each with its model and whether it finished. Chapters are laid over
// that when asked, from the book's chapters at that moment, so editing a book's
// chapters keeps its transcript. Changing the audio itself does not: a transcript made
// from other files is dropped.

type Passage struct {
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	Text    string `json:"text"`
}

// transcribed is one stretch (a chapter, when it was made) that has been through
// speech-to-text.
type transcribed struct {
	StartMs   int64  `json:"startMs"`
	EndMs     int64  `json:"endMs"`
	Status    string `json:"status"` // DONE, or PARTIAL when a run stopped part-way
	Model     string `json:"model"`
	Engine    string `json:"engine,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

type transcriptFile struct {
	Audio    string        `json:"audio"` // audioPrint of the files it was made from
	Done     []transcribed `json:"transcribed"`
	Passages []Passage     `json:"passages"`
}

const (
	statusDone    = "DONE"
	statusPartial = "PARTIAL"

	maxChapterPassages = 20000
	maxPassageText     = 2000
	chapterSlack       = 2000 // ms a passage may stray past its chapter's edges
)

func (s *server) transcriptPath(id string) string {
	return filepath.Join(s.store.dir, "transcripts", id+".json")
}

// audioPrint identifies a book's audio by its files' names, sizes and lengths.
func audioPrint(b *Book) string {
	h := sha256.New()
	for _, f := range b.Files {
		fmt.Fprintf(h, "%s\x00%d\x00%d\n", f.Name, f.SizeBytes, f.DurationMs)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// loadTranscript reads a book's transcript; one made from other audio comes back
// empty. The caller holds s.tmu.
func (s *server) loadTranscript(b *Book) (transcriptFile, error) {
	var t transcriptFile
	if err := readJSON(s.transcriptPath(b.ID), &t); err != nil {
		return transcriptFile{}, err
	}
	if t.Audio != audioPrint(b) {
		return transcriptFile{Audio: audioPrint(b)}, nil
	}
	return t, nil
}

// saveChapter replaces what is stored for chapter c with ps (made by entry's model).
func (s *server) saveChapter(b *Book, c Chapter, entry transcribed, ps []Passage) error {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	t, err := s.loadTranscript(b)
	if err != nil {
		return err
	}
	in := func(start, end int64) bool { mid := (start + end) / 2; return mid >= c.StartMs && mid < c.EndMs }
	t.Done = slices.DeleteFunc(t.Done, func(d transcribed) bool { return in(d.StartMs, d.EndMs) })
	t.Passages = slices.DeleteFunc(t.Passages, func(p Passage) bool { return p.StartMs >= c.StartMs && p.StartMs < c.EndMs })
	entry.StartMs, entry.EndMs = c.StartMs, c.EndMs
	t.Done = append(t.Done, entry)
	t.Passages = append(t.Passages, ps...)
	sort.Slice(t.Done, func(i, j int) bool { return t.Done[i].StartMs < t.Done[j].StartMs })
	sort.SliceStable(t.Passages, func(i, j int) bool { return t.Passages[i].StartMs < t.Passages[j].StartMs })
	return writeJSONCompact(s.transcriptPath(b.ID), t)
}

// chapterRanges is the book's chapters, or the whole book as one when it has none.
func chapterRanges(b *Book) []Chapter {
	if len(b.Chapters) > 0 {
		return b.Chapters
	}
	return []Chapter{{Title: b.Title, StartMs: 0, EndMs: b.DurationMs}}
}

// chapterAt is the index of the chapter holding ms.
func chapterAt(chs []Chapter, ms int64) int {
	i := sort.Search(len(chs), func(i int) bool { return chs[i].StartMs > ms }) - 1
	return max(i, 0)
}

type ChapterTranscript struct {
	Index     int    `json:"index"`
	Status    string `json:"status"`
	Model     string `json:"model"`
	Engine    string `json:"engine,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

type TranscriptPassage struct {
	ChapterIndex int `json:"chapterIndex"`
	Passage
}

type TranscriptView struct {
	BookID   string              `json:"bookId"`
	Chapters []ChapterTranscript `json:"chapters"`
	Passages []TranscriptPassage `json:"passages"`
}

// chapterStatus says how much of c has been transcribed, by which model: DONE when one
// finished run covers it, PARTIAL when only some of it has been, nil when none.
func chapterStatus(t transcriptFile, c Chapter) *ChapterTranscript {
	var best *transcribed
	var most int64
	for i := range t.Done {
		d := &t.Done[i]
		if d.Status == statusDone && d.StartMs <= c.StartMs+chapterSlack && d.EndMs >= c.EndMs-chapterSlack {
			return &ChapterTranscript{Status: statusDone, Model: d.Model, Engine: d.Engine, UpdatedAt: d.UpdatedAt}
		}
		if ov := min(d.EndMs, c.EndMs) - max(d.StartMs, c.StartMs); ov > most {
			best, most = d, ov
		}
	}
	if best == nil {
		return nil
	}
	return &ChapterTranscript{Status: statusPartial, Model: best.Model, Engine: best.Engine, UpdatedAt: best.UpdatedAt}
}

// transcriptView lays a transcript over the book's chapters as they are now.
func transcriptView(b *Book, t transcriptFile) TranscriptView {
	chs := chapterRanges(b)
	v := TranscriptView{BookID: b.ID, Chapters: []ChapterTranscript{}, Passages: make([]TranscriptPassage, 0, len(t.Passages))}
	for i, c := range chs {
		if st := chapterStatus(t, c); st != nil {
			st.Index = i
			v.Chapters = append(v.Chapters, *st)
		}
	}
	for _, p := range t.Passages {
		v.Passages = append(v.Passages, TranscriptPassage{chapterAt(chs, p.StartMs), p})
	}
	return v
}

// ------------------------------------------------------------------ routes

// writeMaybeGzip sends v as JSON, gzipped when the client takes it: a long book's
// transcript is a megabyte or two of text.
func writeMaybeGzip(w http.ResponseWriter, r *http.Request, v any) {
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		writeJSONResponse(w, 200, v)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Encoding", "gzip")
	h.Set("Vary", "Accept-Encoding")
	zw := gzip.NewWriter(w)
	_ = json.NewEncoder(zw).Encode(v)
	_ = zw.Close()
}

func (s *server) getTranscript(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	s.tmu.Lock()
	t, err := s.loadTranscript(b)
	s.tmu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeMaybeGzip(w, r, transcriptView(b, t))
}

// putChapterTranscript takes one chapter's transcript from an app:
// {"status", "model", "engine", "passages": [{startMs, endMs, text}]}. A chapter
// already transcribed in full is only replaced by an admin, so one listener cannot
// overwrite what everyone reads.
func (s *server) putChapterTranscript(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	chs := chapterRanges(b)
	i, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || i < 0 || i >= len(chs) {
		writeError(w, http.StatusNotFound, "no such chapter")
		return
	}
	c := chs[i]
	var in struct {
		Status, Model, Engine string
		Passages              []Passage
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "send {status, model, passages: [{startMs, endMs, text}]}")
		return
	}
	in.Model, in.Engine = clip(strings.TrimSpace(in.Model), 64), clip(strings.TrimSpace(in.Engine), 32)
	if (in.Status != statusDone && in.Status != statusPartial) || in.Model == "" || len(in.Passages) > maxChapterPassages {
		writeError(w, http.StatusBadRequest, "status is DONE or PARTIAL, model is needed, and a chapter has at most 20,000 passages")
		return
	}
	ps := make([]Passage, 0, len(in.Passages))
	for _, p := range in.Passages {
		p.Text = clip(strings.TrimSpace(p.Text), maxPassageText)
		if p.Text == "" {
			continue
		}
		if p.StartMs < c.StartMs-chapterSlack || p.EndMs > c.EndMs+chapterSlack || p.EndMs < p.StartMs {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("a passage at %d ms is outside chapter %d", p.StartMs, i))
			return
		}
		p.StartMs, p.EndMs = max(p.StartMs, c.StartMs), min(p.EndMs, c.EndMs)
		ps = append(ps, p)
	}
	if !s.acct(user).admin() {
		s.tmu.Lock()
		t, err := s.loadTranscript(b)
		s.tmu.Unlock()
		if st := chapterStatus(t, c); err == nil && st != nil && st.Status == statusDone {
			writeError(w, http.StatusConflict, "that chapter is already transcribed")
			return
		}
	}
	if in.Engine == "" {
		in.Engine = "app"
	}
	entry := transcribed{Status: in.Status, Model: in.Model, Engine: in.Engine, UpdatedAt: time.Now().UnixMilli()}
	if err := s.saveChapter(b, c, entry, ps); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.getTranscript(w, r, user, token)
}

// deleteTranscript (admin) forgets a book's transcript.
func (s *server) deleteTranscript(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	s.tmu.Lock()
	err := os.Remove(s.transcriptPath(b.ID))
	s.tmu.Unlock()
	if err != nil && !os.IsNotExist(err) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type TranscriptHit struct {
	BookID       string `json:"bookId"`
	ChapterIndex int    `json:"chapterIndex"`
	StartMs      int64  `json:"startMs"`
	Text         string `json:"text"`
}

const maxTranscriptHits = 200

// searchTranscripts finds ?q= in the transcripts of the books the user may see, or of
// ?bookId= alone.
func (s *server) searchTranscripts(w http.ResponseWriter, r *http.Request, user, token string) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len([]rune(q)) < 2 || len(q) > 200 {
		writeError(w, http.StatusBadRequest, "search for 2 to 200 characters")
		return
	}
	a := s.acct(user)
	var books []*Book
	if id := r.URL.Query().Get("bookId"); id != "" {
		if b := s.lib.bookFor(a, id); b != nil {
			books = append(books, b)
		}
	} else {
		entries, _ := os.ReadDir(filepath.Join(s.store.dir, "transcripts"))
		for _, e := range entries {
			if b := s.lib.bookFor(a, strings.TrimSuffix(e.Name(), ".json")); b != nil {
				books = append(books, b)
			}
		}
	}
	hits, more := []TranscriptHit{}, false
	for _, b := range books {
		s.tmu.Lock()
		t, err := s.loadTranscript(b)
		s.tmu.Unlock()
		if err != nil {
			continue
		}
		chs := chapterRanges(b)
		for _, p := range t.Passages {
			if !strings.Contains(strings.ToLower(p.Text), q) {
				continue
			}
			if len(hits) == maxTranscriptHits {
				more = true
				break
			}
			hits = append(hits, TranscriptHit{b.ID, chapterAt(chs, p.StartMs), p.StartMs, p.Text})
		}
		if more {
			break
		}
	}
	writeJSONResponse(w, 200, map[string]any{"hits": hits, "more": more})
}
