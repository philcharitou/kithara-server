package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Users, their roles and the libraries they may see, and the devices they are signed
// in on.
//
// An admin may rescan, manage users and devices, and edit or match metadata; a user
// listens. A user may be limited to some of the libraries (a child to "Kids"); books
// outside them do not exist as far as that user's devices are concerned. Admins see
// everything.

const (
	roleAdmin = "admin"
	roleUser  = "user"
)

type userRecord struct {
	Salt      string   `json:"salt"`
	Hash      string   `json:"hash"`
	Iter      int      `json:"iter"`
	Role      string   `json:"role,omitempty"`
	Libraries []string `json:"libraries,omitempty"` // empty: all of them
}

type tokenRecord struct {
	User       string `json:"user"`
	Device     string `json:"device,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
}

// Account is a user as the API and the command line show it.
type Account struct {
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Libraries []string `json:"libraries,omitempty"`
	Devices   int      `json:"devices"`
}

func (a Account) admin() bool { return a.Role == roleAdmin }

// sees reports whether the account may see books of the named library.
func (a Account) sees(library string) bool {
	if a.admin() || len(a.Libraries) == 0 {
		return true
	}
	for _, l := range a.Libraries {
		if strings.EqualFold(l, library) {
			return true
		}
	}
	return false
}

// Device is one signed-in device. Its id is derived from the token, so it can be
// shown and revoked without the token itself ever leaving the server.
type Device struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
	Current    bool   `json:"current,omitempty"`
}

const pbkdfIterations = 600_000

var userNameRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// migrateRoles gives a users.json from before roles existed its admins: everyone, as
// everyone could do everything then. The caller holds s.mu (or owns s outright).
func (s *store) migrateRoles() error {
	for _, rec := range s.userDB {
		if rec.Role != "" {
			return nil
		}
	}
	if len(s.userDB) == 0 {
		return nil
	}
	var names []string
	for name, rec := range s.userDB {
		rec.Role = roleAdmin
		s.userDB[name] = rec
		names = append(names, name)
	}
	sort.Strings(names)
	log.Printf("users from an older version are now admins: %s (make any of them a plain user with `kithara-server user role NAME user`)", strings.Join(names, ", "))
	return s.saveUsers()
}

// ------------------------------------------------------------------ users

func (s *store) users() []string {
	s.lockFresh()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.userDB))
	for n := range s.userDB {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *store) accounts() []Account {
	s.lockFresh()
	defer s.mu.Unlock()
	out := make([]Account, 0, len(s.userDB))
	for name := range s.userDB {
		out = append(out, s.accountLocked(name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *store) account(name string) (Account, bool) {
	s.lockFresh()
	defer s.mu.Unlock()
	if _, ok := s.userDB[name]; !ok {
		return Account{}, false
	}
	return s.accountLocked(name), true
}

func (s *store) accountLocked(name string) Account {
	rec := s.userDB[name]
	a := Account{Name: name, Role: rec.Role, Libraries: rec.Libraries}
	if a.Role == "" {
		a.Role = roleUser
	}
	for _, t := range s.tokens {
		if t.User == name {
			a.Devices++
		}
	}
	return a
}

// setPassword creates a user, or changes an existing one's password. The first user
// on a server is its admin; later ones are plain users until made otherwise.
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
	s.lockFresh()
	defer s.mu.Unlock()
	rec, exists := s.userDB[name]
	if !exists {
		rec.Role = roleUser
		if len(s.userDB) == 0 {
			rec.Role = roleAdmin
		}
	}
	rec.Salt, rec.Hash, rec.Iter = hex.EncodeToString(salt), hex.EncodeToString(hash), pbkdfIterations
	s.userDB[name] = rec
	if err := os.MkdirAll(filepath.Join(s.dir, "users", name), 0o755); err != nil {
		return err
	}
	// A password change signs every device out; they ask for it again.
	for t, tr := range s.tokens {
		if tr.User == name {
			delete(s.tokens, t)
		}
	}
	if err := s.saveTokens(); err != nil {
		return err
	}
	return s.saveUsers()
}

// otherAdmins counts admins other than name; the caller holds s.mu.
func (s *store) otherAdmins(name string) int {
	n := 0
	for u, rec := range s.userDB {
		if u != name && rec.Role == roleAdmin {
			n++
		}
	}
	return n
}

var errLastAdmin = errors.New("that is the last admin; make someone else an admin first")

func (s *store) setRole(name, role string) error {
	if role != roleAdmin && role != roleUser {
		return fmt.Errorf("a role is %q or %q", roleAdmin, roleUser)
	}
	s.lockFresh()
	defer s.mu.Unlock()
	rec, ok := s.userDB[name]
	if !ok {
		return fmt.Errorf("no user %q", name)
	}
	if rec.Role == roleAdmin && role != roleAdmin && s.otherAdmins(name) == 0 {
		return errLastAdmin
	}
	rec.Role = role
	s.userDB[name] = rec
	return s.saveUsers()
}

// setLibraries limits a user to the named libraries; none means all of them.
func (s *store) setLibraries(name string, libraries []string) error {
	s.lockFresh()
	defer s.mu.Unlock()
	rec, ok := s.userDB[name]
	if !ok {
		return fmt.Errorf("no user %q", name)
	}
	rec.Libraries = nil
	for _, l := range libraries {
		if l = strings.TrimSpace(l); l != "" {
			rec.Libraries = append(rec.Libraries, l)
		}
	}
	s.userDB[name] = rec
	return s.saveUsers()
}

func (s *store) removeUser(name string) error {
	s.lockFresh()
	defer s.mu.Unlock()
	rec, ok := s.userDB[name]
	if !ok {
		return fmt.Errorf("no user %q", name)
	}
	if rec.Role == roleAdmin && len(s.userDB) > 1 && s.otherAdmins(name) == 0 {
		return errLastAdmin
	}
	delete(s.userDB, name)
	for t, tr := range s.tokens {
		if tr.User == name {
			delete(s.tokens, t)
		}
	}
	if err := s.saveTokens(); err != nil {
		return err
	}
	if err := s.saveUsers(); err != nil {
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
	s.lockFresh()
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

// ------------------------------------------------------------------ tokens and devices

func (s *store) issueToken(user, device string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := time.Now().UnixMilli()
	s.lockFresh()
	defer s.mu.Unlock()
	s.tokens[token] = tokenRecord{User: user, Device: device, CreatedAt: now, LastSeenAt: now}
	return token, s.saveTokens()
}

// seenEvery is how stale a device's last-seen time may get before a request writes it
// out, so that tokens.json is not rewritten on every request.
const seenEvery = int64(time.Hour / time.Millisecond)

// userFor returns the user a bearer token belongs to, or "".
func (s *store) userFor(token string) string {
	s.lockFresh()
	defer s.mu.Unlock()
	rec, ok := s.tokens[token]
	if !ok {
		return ""
	}
	if _, stillThere := s.userDB[rec.User]; !stillThere {
		return ""
	}
	if now := time.Now().UnixMilli(); now-rec.LastSeenAt > seenEvery {
		rec.LastSeenAt = now
		s.tokens[token] = rec
		if err := s.saveTokens(); err != nil {
			log.Printf("tokens: %v", err)
		}
	}
	return rec.User
}

func (s *store) revokeToken(token string) error {
	s.lockFresh()
	defer s.mu.Unlock()
	delete(s.tokens, token)
	return s.saveTokens()
}

func deviceID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:6])
}

// devices lists a user's signed-in devices, most recently seen first; current is the
// token of the asking device, so it can be marked.
func (s *store) devices(user, current string) []Device {
	s.lockFresh()
	defer s.mu.Unlock()
	out := []Device{}
	for t, rec := range s.tokens {
		if rec.User == user {
			out = append(out, Device{ID: deviceID(t), Name: rec.Device, CreatedAt: rec.CreatedAt, LastSeenAt: rec.LastSeenAt, Current: t == current})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeenAt != out[j].LastSeenAt {
			return out[i].LastSeenAt > out[j].LastSeenAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// revokeDevices signs out a user's device by id, or all of them with id "". It
// returns how many were signed out.
func (s *store) revokeDevices(user, id string) (int, error) {
	s.lockFresh()
	defer s.mu.Unlock()
	n := 0
	for t, rec := range s.tokens {
		if rec.User == user && (id == "" || deviceID(t) == id) {
			delete(s.tokens, t)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.saveTokens()
}
