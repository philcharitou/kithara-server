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
	Libraries []string `json:"libraries,omitempty"`   // empty: all of them
	Subject   string   `json:"oidcSubject,omitempty"` // issuer and subject at the identity provider
}

type tokenRecord struct {
	User       string `json:"user"`
	Device     string `json:"device,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
	// The device sends its own listening sessions (POST /api/sessions), so the server
	// no longer estimates them from its progress uploads.
	Reports bool `json:"reportsSessions,omitempty"`
}

// Account is a user as the API and the command line show it.
type Account struct {
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Libraries []string `json:"libraries,omitempty"`
	Devices   int      `json:"devices"`
	SSO       bool     `json:"sso,omitempty"` // linked to someone at the identity provider
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

// New user names start and end with a letter, digit, _, @ or - (so never "." or ".."
// and no trailing dot, which Windows drops), since each names a folder under users/.
var userNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._@-]{0,62}[A-Za-z0-9_@-])?$`)

// windowsDevice matches names Windows reserves for devices, as folder names (CON, NUL,
// COM1, also with an extension).
var windowsDevice = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\.|$)`)

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

// accountQuick is account without re-reading the users file or counting devices: what
// each request needs once withUser has looked the token up.
func (s *store) accountQuick(name string) Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.roleLocked(name)
}

// roleLocked is a user's account without the device count; the caller holds s.mu.
func (s *store) roleLocked(name string) Account {
	rec := s.userDB[name]
	a := Account{Name: name, Role: rec.Role, Libraries: rec.Libraries, SSO: rec.Subject != ""}
	if a.Role == "" {
		a.Role = roleUser
	}
	return a
}

func (s *store) accountLocked(name string) Account {
	a := s.roleLocked(name)
	for _, t := range s.tokens {
		if t.User == name {
			a.Devices++
		}
	}
	return a
}

// setPassword creates a user, or changes an existing one's password. The first user
// on a server is its admin; later ones are plain users until made otherwise.
func (s *store) setPassword(name, password string) error { return s.putPassword(name, password, false) }

// addUser creates a user, and fails if the name is taken (in any case).
func (s *store) addUser(name, password string) error { return s.putPassword(name, password, true) }

func (s *store) putPassword(name, password string, newOnly bool) error {
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
	if exists && newOnly {
		return fmt.Errorf("there is already a user %q", name)
	}
	if !exists {
		// The rules apply to new names; existing ones keep working.
		if !userNameRe.MatchString(name) || windowsDevice.MatchString(name) {
			return fmt.Errorf("user names are letters, digits and . _ @ - (1 to 64 characters, starting with a letter or digit, not ending in a dot)")
		}
		// Folders are case-insensitive on some systems: Bob and bob would share one.
		for other := range s.userDB {
			if strings.EqualFold(other, name) {
				return fmt.Errorf("there is already a user %q", other)
			}
		}
		rec.Role = roleUser
		if len(s.userDB) == 0 {
			rec.Role = roleAdmin
		}
	}
	rec.Salt, rec.Hash, rec.Iter = hex.EncodeToString(salt), hex.EncodeToString(hash), pbkdfIterations
	// The folder first: a name the filesystem refuses must not leave a half-made user.
	if err := os.MkdirAll(filepath.Join(s.dir, "users", name), 0o755); err != nil {
		return err
	}
	s.userDB[name] = rec
	// A password change signs every device out; they ask for it again.
	s.dropTokensLocked(name)
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
	if err := s.dropUser(name); err != nil {
		return err
	}
	// The listening state is small and precious; keep it aside rather than delete it,
	// once any write to it in progress is done (without s.mu, so nobody else waits).
	defer s.lockUser(name)()
	old := filepath.Join(s.dir, "users", name)
	if _, err := os.Stat(old); err == nil {
		_ = os.Rename(old, old+".removed-"+time.Now().Format("20060102-150405"))
	}
	return nil
}

// dropUser takes a user and their devices out of the users and tokens files.
func (s *store) dropUser(name string) error {
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
	s.dropTokensLocked(name)
	if err := s.saveTokens(); err != nil {
		return err
	}
	return s.saveUsers()
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

// dropTokensLocked signs every device of a user out; the caller holds s.mu.
func (s *store) dropTokensLocked(name string) {
	for t, tr := range s.tokens {
		if tr.User == name {
			delete(s.tokens, t)
		}
	}
}

// tokenInfo is what a token was issued to.
func (s *store) tokenInfo(token string) tokenRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[token]
}

// markReporting notes that a device sends its own sessions.
func (s *store) markReporting(token string) error {
	s.lockFresh()
	defer s.mu.Unlock()
	rec, ok := s.tokens[token]
	if !ok || rec.Reports {
		return nil
	}
	rec.Reports = true
	s.tokens[token] = rec
	return s.saveTokens()
}

// hasDevice reports whether the user still has the device with this id signed in.
func (s *store) hasDevice(user, id string) bool {
	s.lockFresh()
	defer s.mu.Unlock()
	for t, rec := range s.tokens {
		if rec.User == user && deviceID(t) == id {
			return true
		}
	}
	return false
}

// maxDevices is how many devices one user may have signed in; signing in on one more
// signs out the one used least recently.
const maxDevices = 50

func (s *store) issueToken(user, device string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	now := time.Now().UnixMilli()
	s.lockFresh()
	defer s.mu.Unlock()
	var mine []string
	for t, rec := range s.tokens {
		if rec.User == user {
			mine = append(mine, t)
		}
	}
	if len(mine) >= maxDevices {
		sort.Slice(mine, func(i, j int) bool { return s.tokens[mine[i]].LastSeenAt < s.tokens[mine[j]].LastSeenAt })
		for _, t := range mine[:len(mine)-maxDevices+1] {
			delete(s.tokens, t)
		}
	}
	s.tokens[token] = tokenRecord{User: user, Device: clip(device, 64), CreatedAt: now, LastSeenAt: now}
	return token, s.saveTokens()
}

// linkSecret is mixed into a user's signed links: it changes with their password, so
// changing it voids every link handed out before.
func (s *store) linkSecret(user string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userDB[user].Salt
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
