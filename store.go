package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
//
// Every write goes to a temp file and is renamed into place, so a crash mid-write
// leaves the previous file intact.
type store struct {
	dir    string
	mu     sync.Mutex
	userDB map[string]userRecord  // name -> record
	tokens map[string]tokenRecord // token -> record
}

type userRecord struct {
	Salt string `json:"salt"`
	Hash string `json:"hash"`
	Iter int    `json:"iter"`
}

type tokenRecord struct {
	User      string `json:"user"`
	Device    string `json:"device,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

const pbkdfIterations = 600_000

var userNameRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

func openStore(dir string) (*store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "users"), 0o755); err != nil {
		return nil, err
	}
	s := &store{dir: dir, userDB: map[string]userRecord{}, tokens: map[string]tokenRecord{}}
	if err := readJSON(filepath.Join(dir, "users.json"), &s.userDB); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "tokens.json"), &s.tokens); err != nil {
		return nil, err
	}
	return s, nil
}

// ------------------------------------------------------------------ users

func (s *store) users() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.userDB))
	for n := range s.userDB {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *store) setPassword(name, password string) error {
	if !userNameRe.MatchString(name) {
		return fmt.Errorf("user names are letters, digits and . _ @ - (1 to 64 characters)")
	}
	if len(password) < 8 {
		return errors.New("use at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, pbkdfIterations, 32)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userDB[name] = userRecord{Salt: hex.EncodeToString(salt), Hash: hex.EncodeToString(hash), Iter: pbkdfIterations}
	if err := os.MkdirAll(filepath.Join(s.dir, "users", name), 0o755); err != nil {
		return err
	}
	// A password change signs every device out; they ask for it again.
	for t, rec := range s.tokens {
		if rec.User == name {
			delete(s.tokens, t)
		}
	}
	if err := writeJSON(filepath.Join(s.dir, "tokens.json"), s.tokens); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.dir, "users.json"), s.userDB)
}

func (s *store) removeUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.userDB[name]; !ok {
		return fmt.Errorf("no user %q", name)
	}
	delete(s.userDB, name)
	for t, rec := range s.tokens {
		if rec.User == name {
			delete(s.tokens, t)
		}
	}
	if err := writeJSON(filepath.Join(s.dir, "tokens.json"), s.tokens); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(s.dir, "users.json"), s.userDB); err != nil {
		return err
	}
	// The listening state is small and precious; keep it aside rather than delete it.
	old := filepath.Join(s.dir, "users", name)
	if _, err := os.Stat(old); err == nil {
		_ = os.Rename(old, old+".removed-"+time.Now().Format("20060102-150405"))
	}
	return nil
}

func (s *store) checkPassword(name, password string) bool {
	s.mu.Lock()
	rec, ok := s.userDB[name]
	s.mu.Unlock()
	if !ok {
		// Burn the same time as a real check so a missing user is not a faster answer.
		_, _ = pbkdf2.Key(sha256.New, password, []byte("no-such-user-padding"), pbkdfIterations, 32)
		return false
	}
	salt, _ := hex.DecodeString(rec.Salt)
	want, _ := hex.DecodeString(rec.Hash)
	got, err := pbkdf2.Key(sha256.New, password, salt, rec.Iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ------------------------------------------------------------------ tokens

func (s *store) issueToken(user, device string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = tokenRecord{User: user, Device: device, CreatedAt: time.Now().UnixMilli()}
	return token, writeJSON(filepath.Join(s.dir, "tokens.json"), s.tokens)
}

// userFor returns the user a bearer token belongs to, or "".
func (s *store) userFor(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.tokens[token]
	if !ok {
		return ""
	}
	if _, stillThere := s.userDB[rec.User]; !stillThere {
		return ""
	}
	return rec.User
}

func (s *store) revokeToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
	return writeJSON(filepath.Join(s.dir, "tokens.json"), s.tokens)
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
func (s *store) putProgress(user string, in Progress) (Progress, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]Progress{}
	if err := readJSON(s.progressPath(user), &m); err != nil {
		return Progress{}, false, err
	}
	if cur, ok := m[in.BookID]; ok && in.UpdatedAt < cur.UpdatedAt {
		return cur, false, nil
	}
	if in.PositionMs < 0 {
		in.PositionMs = 0
	}
	m[in.BookID] = in
	return in, true, writeJSON(s.progressPath(user), m)
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
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return Bookmark{}, err
	}
	b.ID = "bm_" + hex.EncodeToString(raw)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
