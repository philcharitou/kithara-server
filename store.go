package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The wire types, straight from the protocol document.

type Progress struct {
	BookID     string   `json:"bookId"`
	PositionMs int64    `json:"positionMs"`
	UpdatedAt  int64    `json:"updatedAt"`
	Finished   bool     `json:"finished"`
	Speed      *float64 `json:"speed,omitempty"`

	// Kept by the server (clients may send them; the earliest start is kept): when the
	// book was first started, and when it was last finished.
	StartedAt  int64 `json:"startedAt,omitempty"`
	FinishedAt int64 `json:"finishedAt,omitempty"`
}

type Bookmark struct {
	ID         string `json:"id"`
	BookID     string `json:"bookId"`
	PositionMs int64  `json:"positionMs"`
	Label      string `json:"label"`
	CreatedAt  int64  `json:"createdAt"`
}

// What is kept on disk, all under the data directory:
//
//	users.json               name -> password hash
//	tokens.json              token -> {user, device, createdAt}
//	users/<name>/progress.json
//	users/<name>/bookmarks.json
//	users/<name>/sessions.json   listening sessions, worked out from progress
//
// Every write goes to a temp file and is renamed into place, so a crash mid-write
// leaves the previous file intact.
type store struct {
	dir    string
	mu     sync.Mutex
	userDB map[string]userRecord  // name -> record
	tokens map[string]tokenRecord // token -> record

	// What users.json and tokens.json looked like when last read or written, so that
	// changes made by the command line while the server runs are picked up.
	usersSeen, tokensSeen fileStamp
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{info.ModTime(), info.Size()}
}

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "users"), 0o755); err != nil {
		return nil, err
	}
	s := &store{dir: dir}
	if err := s.reload(); err != nil {
		return nil, err
	}
	if err := s.migrateRoles(); err != nil {
		return nil, err
	}
	return s, nil
}

// reload reads users.json and tokens.json if they changed since last seen; the caller
// holds s.mu (or owns s outright).
func (s *store) reload() error {
	up, tp := filepath.Join(s.dir, "users.json"), filepath.Join(s.dir, "tokens.json")
	if st := stampOf(up); st != s.usersSeen || s.userDB == nil {
		db := map[string]userRecord{}
		if err := readJSON(up, &db); err != nil {
			return err
		}
		s.userDB, s.usersSeen = db, st
	}
	if st := stampOf(tp); st != s.tokensSeen || s.tokens == nil {
		tokens := map[string]tokenRecord{}
		if err := readJSON(tp, &tokens); err != nil {
			return err
		}
		s.tokens, s.tokensSeen = tokens, st
	}
	return nil
}

// lockFresh takes s.mu with users and tokens up to date. A file that will not read
// (half-written by hand) leaves what was there before.
func (s *store) lockFresh() {
	s.mu.Lock()
	if err := s.reload(); err != nil {
		log.Printf("users and tokens: %v", err)
	}
}

func (s *store) saveUsers() error {
	p := filepath.Join(s.dir, "users.json")
	err := writeJSON(p, s.userDB)
	s.usersSeen = stampOf(p)
	return err
}

func (s *store) saveTokens() error {
	p := filepath.Join(s.dir, "tokens.json")
	err := writeJSON(p, s.tokens)
	s.tokensSeen = stampOf(p)
	return err
}

// ------------------------------------------------------------------ progress

func (s *store) progressPath(user string) string {
	return filepath.Join(s.dir, "users", user, "progress.json")
}

func (s *store) bookmarksPath(user string) string {
	return filepath.Join(s.dir, "users", user, "bookmarks.json")
}

func (s *store) allProgress(user string) ([]Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]Progress{}
	if err := readJSON(s.progressPath(user), &m); err != nil {
		return nil, err
	}
	out := make([]Progress, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BookID < out[j].BookID })
	return out, nil
}

// putProgress applies last-write-wins on updatedAt. It returns the record now stored
// and whether the incoming one was accepted (false means the caller should answer 409).
// An accepted record also adds to the user's listening sessions; token says which
// device it came from.
func (s *store) putProgress(user, token string, in Progress) (Progress, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]Progress{}
	if err := readJSON(s.progressPath(user), &m); err != nil {
		return Progress{}, false, err
	}
	cur, had := m[in.BookID]
	if had && in.UpdatedAt < cur.UpdatedAt {
		return cur, false, nil
	}
	if in.PositionMs < 0 {
		in.PositionMs = 0
	}
	for _, t := range []int64{cur.StartedAt, in.StartedAt} {
		if t > 0 && (in.StartedAt == 0 || t < in.StartedAt) {
			in.StartedAt = t
		}
	}
	if in.StartedAt == 0 && (in.PositionMs > 0 || in.Finished) {
		in.StartedAt = in.UpdatedAt
	}
	switch {
	case !in.Finished:
		in.FinishedAt = 0
	case in.FinishedAt > 0:
	case cur.Finished && cur.FinishedAt > 0:
		in.FinishedAt = cur.FinishedAt
	default:
		in.FinishedAt = in.UpdatedAt
	}
	m[in.BookID] = in
	if err := writeJSON(s.progressPath(user), m); err != nil {
		return Progress{}, false, err
	}
	if had {
		if err := s.recordListening(user, s.tokens[token].Device, cur, in); err != nil {
			log.Printf("sessions for %s: %v", user, err)
		}
	}
	return in, true, nil
}

func (s *store) deleteProgress(user, bookID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]Progress{}
	if err := readJSON(s.progressPath(user), &m); err != nil {
		return err
	}
	delete(m, bookID)
	return writeJSON(s.progressPath(user), m)
}

// ------------------------------------------------------------------ bookmarks

func (s *store) allBookmarks(user string) ([]Bookmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Bookmark
	if err := readJSON(s.bookmarksPath(user), &list); err != nil {
		return nil, err
	}
	if list == nil {
		list = []Bookmark{}
	}
	return list, nil
}

const maxBookmarks = 10000

var errTooManyBookmarks = errors.New("too many bookmarks; delete some first")

// clip shortens s to at most n characters.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// addBookmark stores a bookmark, or returns the existing one when the same
// (bookId, positionMs, createdAt) was posted before, so retries never duplicate.
func (s *store) addBookmark(user string, b Bookmark) (Bookmark, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Bookmark
	if err := readJSON(s.bookmarksPath(user), &list); err != nil {
		return Bookmark{}, err
	}
	for _, cur := range list {
		if cur.BookID == b.BookID && cur.PositionMs == b.PositionMs && cur.CreatedAt == b.CreatedAt {
			return cur, nil
		}
	}
	if len(list) >= maxBookmarks {
		return Bookmark{}, errTooManyBookmarks
	}
	b.ID = "bm_" + newID()
	b.Label = clip(b.Label, 500)
	if b.CreatedAt == 0 {
		b.CreatedAt = time.Now().UnixMilli()
	}
	list = append(list, b)
	return b, writeJSON(s.bookmarksPath(user), list)
}

// deleteBookmark reports whether the bookmark existed.
func (s *store) deleteBookmark(user, bookID, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Bookmark
	if err := readJSON(s.bookmarksPath(user), &list); err != nil {
		return false, err
	}
	kept := list[:0]
	found := false
	for _, cur := range list {
		if cur.ID == id && cur.BookID == bookID {
			found = true
			continue
		}
		kept = append(kept, cur)
	}
	if !found {
		return false, nil
	}
	return true, writeJSON(s.bookmarksPath(user), kept)
}

// ------------------------------------------------------------------ files

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// writeJSONCompact is writeJSON without the indentation, for the large files only the
// server reads (the probe cache, listening sessions).
func writeJSONCompact(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// writeAtomic writes to a temporary file and renames it into place, so a crash
// mid-write leaves the previous file intact.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
