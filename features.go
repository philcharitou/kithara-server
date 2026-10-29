package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Routes behind the web page's player and admin tools: signed audio links, a user's
// own password, uploads, covers, chapters, scan problems, settings, backups and
// listening activity.

// ------------------------------------------------------------------ signed links

// signedOr serves h to a bearer token, or to a request carrying a valid signed link
// (?u, ?exp, ?sig) for this purpose and the book in its path.
func (s *server) signedOr(purpose string, h userHandler) http.HandlerFunc {
	bearer := s.withUser(h)
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("sig") == "" {
			bearer(w, r)
			return
		}
		user, device := q.Get("u"), q.Get("d")
		exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
		want := s.linkSig(user, device, purpose, r.PathValue("bookId"), exp)
		// The device that asked must still be signed in (so the user still exists); the
		// handler checks they may still see the book.
		if err != nil || !s.store.hasDevice(user, device) || time.Now().Unix() > exp || subtle.ConstantTimeCompare([]byte(want), []byte(q.Get("sig"))) != 1 {
			writeError(w, http.StatusForbidden, "this link is not valid or has expired")
			return
		}
		h(w, r, user, "")
	}
}

const streamLinkTTL = 12 * time.Hour

// streamLink answers with signed addresses for each of a book's files, good for long
// enough to listen to it in one sitting.
func (s *server) streamLink(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	q, exp := s.signedQuery(user, token, "audio", b.ID, streamLinkTTL)
	files := make([]map[string]any, 0, len(b.Files))
	for _, f := range b.Files {
		files = append(files, map[string]any{"index": f.Index, "url": f.URL + "?" + q, "durationMs": f.DurationMs})
	}
	writeJSONResponse(w, 200, map[string]any{"files": files, "expiresAt": exp * 1000})
}

// ------------------------------------------------------------------ own password

// changePassword lets a user change their own password. Every device is signed out
// by the change; the one asking gets a fresh token back so it stays signed in.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request, user, token string) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readBody(r, &in); err != nil || in.New == "" {
		writeError(w, http.StatusBadRequest, "current and new passwords are required")
		return
	}
	ok, answered := s.passwordAttempt(w, r, user, in.Current)
	if answered {
		return
	}
	if !ok {
		writeError(w, http.StatusForbidden, "the current password is wrong")
		return
	}
	device := s.store.tokenInfo(token).Device
	if err := s.store.setPassword(user, in.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fresh, err := s.store.issueToken(user, device)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]string{"token": fresh})
}

// ------------------------------------------------------------------ uploads (admin)

// uploadable is what may be uploaded into a library: audio, and the pictures and
// metadata files the scanner reads beside it.
func uploadable(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := audioExt[ext]; ok {
		return true
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".opf", ".json", ".txt":
		return true
	}
	return false
}

// upload writes one file into a library, at ?path= relative to its folder (folders
// are made as needed). The body is the file itself. Existing files are kept unless
// ?overwrite=1. The caller rescans when a batch is done.
func (s *server) upload(w http.ResponseWriter, r *http.Request, user, token string) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{}) // an admin: as long as it takes
	root, ok := s.lib.catalogueRoot(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such library")
		return
	}
	raw := strings.ReplaceAll(r.URL.Query().Get("path"), `\`, "/")
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			writeError(w, http.StatusBadRequest, "that path is not allowed")
			return
		}
	}
	rel := path.Clean("/" + raw)[1:]
	if rel == "" || rel == "." {
		writeError(w, http.StatusBadRequest, "give the file's path inside the library, like Author/Title/part 1.mp3")
		return
	}
	// No hidden files or folders (that covers .kithara-id), and nothing Windows refuses.
	for _, part := range strings.Split(rel, "/") {
		if strings.HasPrefix(part, ".") || strings.ContainsAny(part, `:*?"<>|`) {
			writeError(w, http.StatusBadRequest, "that path is not allowed")
			return
		}
	}
	if !uploadable(rel) {
		writeError(w, http.StatusBadRequest, "only audio, cover pictures and metadata files can be uploaded")
		return
	}
	dst := filepath.Join(root.path, filepath.FromSlash(rel))
	if !within(dst, root.path) {
		writeError(w, http.StatusBadRequest, "that path is not allowed")
		return
	}
	if _, err := os.Stat(dst); err == nil && r.URL.Query().Get("overwrite") != "1" {
		writeError(w, http.StatusConflict, "a file with that name is already there")
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot make the folder: "+err.Error())
		return
	}
	// A linked folder inside the library must not lead the file somewhere else.
	realDir, err1 := filepath.EvalSymlinks(filepath.Dir(dst))
	realRoot, err2 := filepath.EvalSymlinks(root.path)
	if err1 != nil || err2 != nil || !within(realDir, realRoot) {
		writeError(w, http.StatusBadRequest, "that path leads outside the library")
		return
	}
	tmp := dst + ".kithara-upload"
	f, err := os.Create(tmp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot write into the library: "+err.Error())
		return
	}
	n, err := io.Copy(f, r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		writeError(w, http.StatusInternalServerError, "upload failed: "+err.Error())
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	log.Printf("%s uploaded %s to %q (%d bytes)", user, rel, root.name, n)
	writeJSONResponse(w, http.StatusCreated, map[string]any{"path": rel, "bytes": n})
}

// ------------------------------------------------------------------ covers (admin)

// putCover keeps an uploaded picture as the book's cover, beside its saved metadata.
func (s *server) putCover(w http.ResponseWriter, r *http.Request, user, token string) {
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute)) // an admin
	id := r.PathValue("bookId")
	if s.bookOr404(w, r, user) == nil {
		return
	}
	data, err := readCover(r.Body)
	if err == nil {
		err = saveCoverData(s.lib.dataDir, id, data)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.afterMetadataChange(w, id)
}

func (s *server) deleteCover(w http.ResponseWriter, r *http.Request, user, token string) {
	id := r.PathValue("bookId")
	if s.bookOr404(w, r, user) == nil {
		return
	}
	removeOverrideCovers(s.lib.dataDir, id)
	s.afterMetadataChange(w, id)
}

// ------------------------------------------------------------------ chapters (admin)

// putChapters keeps an edited chapter list with the book's saved metadata. Each
// chapter gives a title and a start; ends follow from the next start and the book's
// length.
func (s *server) putChapters(w http.ResponseWriter, r *http.Request, user, token string) {
	id := r.PathValue("bookId")
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	var in struct {
		Chapters []struct {
			Title   string `json:"title"`
			StartMs int64  `json:"startMs"`
		} `json:"chapters"`
	}
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad chapter list")
		return
	}
	sort.SliceStable(in.Chapters, func(i, j int) bool { return in.Chapters[i].StartMs < in.Chapters[j].StartMs })
	var chs []map[string]any
	for i, c := range in.Chapters {
		if c.StartMs < 0 || c.StartMs >= b.DurationMs {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("chapter %d starts outside the book", i+1))
			return
		}
		end := b.DurationMs
		if i+1 < len(in.Chapters) {
			end = in.Chapters[i+1].StartMs
		}
		if end <= c.StartMs {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("chapters %d and %d start at the same time", i+1, i+2))
			return
		}
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		chs = append(chs, map[string]any{"id": i, "start": float64(c.StartMs) / 1000, "end": float64(end) / 1000, "title": title})
	}
	if err := updateOverride(s.lib.dataDir, id, func(m map[string]any) {
		if len(chs) == 0 {
			delete(m, "chapters")
		} else {
			m["chapters"] = chs
		}
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.afterMetadataChange(w, id)
}

// updateOverride edits the saved metadata file of a book in place.
func updateOverride(dataDir, id string, edit func(map[string]any)) error {
	defer lockOverride(id)()
	m := map[string]any{}
	if err := readJSON(overridePath(dataDir, id), &m); err != nil {
		return err
	}
	edit(m)
	if len(m) == 0 {
		err := os.Remove(overridePath(dataDir, id))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return writeJSON(overridePath(dataDir, id), m)
}

// ------------------------------------------------------------------ problems (admin)

// Problem is something a scan could not use.
type Problem struct {
	Library string `json:"library"`
	Path    string `json:"path"`
	Error   string `json:"error"`
}

func (s *server) problems(w http.ResponseWriter, r *http.Request, user, token string) {
	s.lib.mu.RLock()
	list := append([]Problem{}, s.lib.problems...)
	s.lib.mu.RUnlock()
	writeJSONResponse(w, 200, map[string]any{"problems": list})
}

// ------------------------------------------------------------------ settings (admin)

const maxRescanMinutes = 7 * 24 * 60 // a week

// Settings an admin can change from the web page, kept in settings.json. Each falls
// back to the command line's value when unset.
type Settings struct {
	Name          string `json:"name,omitempty"`
	RescanMinutes *int   `json:"rescanMinutes,omitempty"`
}

type settingsBox struct {
	mu       sync.Mutex
	saved    Settings
	name     string        // from --name
	interval time.Duration // from --rescan
	wake     chan struct{}
}

func (s *server) settingsPath() string { return filepath.Join(s.lib.dataDir, "settings.json") }

// serverName is the name shown in the apps and on the page.
func (s *server) serverName() string {
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	if s.settings.saved.Name != "" {
		return s.settings.saved.Name
	}
	return s.settings.name
}

func (s *server) rescanEvery() time.Duration {
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	if m := s.settings.saved.RescanMinutes; m != nil {
		return time.Duration(*m) * time.Minute
	}
	return s.settings.interval
}

// runRescans rescans on the configured interval, picking up changes to it at once.
func (s *server) runRescans() {
	for {
		every := s.rescanEvery()
		var tick <-chan time.Time
		if every > 0 {
			tick = time.After(every)
		}
		select {
		case <-tick:
			if err := s.lib.rescan(); err != nil {
				log.Printf("scan: %v", err)
			}
		case <-s.settings.wake:
		}
	}
}

func (s *server) getSettings(w http.ResponseWriter, r *http.Request, user, token string) {
	s.settings.mu.Lock()
	saved := s.settings.saved
	defaults := map[string]any{"name": s.settings.name, "rescanMinutes": int(s.settings.interval / time.Minute)}
	s.settings.mu.Unlock()
	eff := map[string]any{"name": s.serverName(), "rescanMinutes": int(s.rescanEvery() / time.Minute)}
	writeJSONResponse(w, 200, map[string]any{"saved": saved, "defaults": defaults, "effective": eff})
}

func (s *server) putSettings(w http.ResponseWriter, r *http.Request, user, token string) {
	var in Settings
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad settings")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) > 80 {
		writeError(w, http.StatusBadRequest, "the name is up to 80 characters")
		return
	}
	if in.RescanMinutes != nil && (*in.RescanMinutes < 0 || *in.RescanMinutes > maxRescanMinutes) {
		writeError(w, http.StatusBadRequest, "rescan every 0 (never) to 10080 minutes")
		return
	}
	s.settings.mu.Lock()
	err := writeJSON(s.settingsPath(), in)
	if err == nil {
		s.settings.saved = in
	}
	s.settings.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	select {
	case s.settings.wake <- struct{}{}:
	default:
	}
	s.getSettings(w, r, user, token)
}

// ------------------------------------------------------------------ activity (admin)

// activity sums every user's listening over the last ?days (default 30) and lists the
// most recent sessions across them all.
func (s *server) activity(w http.ResponseWriter, r *http.Request, user, token string) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 3650 {
		days = 30
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	type userSum struct {
		Name       string `json:"name"`
		ListenedMs int64  `json:"listenedMs"`
		Sessions   int    `json:"sessions"`
		Books      int    `json:"books"`
		LastAt     int64  `json:"lastAt,omitempty"`
		LastBookID string `json:"lastBookId,omitempty"`
	}
	type recent struct {
		ListeningSession
		User string `json:"user"`
	}
	var users []userSum
	var all []recent
	for _, name := range s.store.users() {
		list, err := s.store.sessions(name, "", since)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sum := userSum{Name: name, Sessions: len(list)}
		books := map[string]bool{}
		for _, ls := range list {
			sum.ListenedMs += ls.ListenedMs
			books[ls.BookID] = true
			if ls.EndedAt > sum.LastAt {
				sum.LastAt, sum.LastBookID = ls.EndedAt, ls.BookID
			}
			all = append(all, recent{ls, name})
		}
		sum.Books = len(books)
		users = append(users, sum)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ListenedMs > users[j].ListenedMs })
	sort.Slice(all, func(i, j int) bool { return all[i].EndedAt > all[j].EndedAt })
	if len(all) > 100 {
		all = all[:100]
	}
	if all == nil {
		all = []recent{}
	}
	writeJSONResponse(w, 200, map[string]any{"days": days, "users": users, "recent": all})
}

// loadSettings reads settings.json at start. The rescan interval from the command line
// is set afterwards, by serve.
func (s *server) loadSettings(name string) {
	s.settings = &settingsBox{name: name, wake: make(chan struct{}, 1)}
	data, err := os.ReadFile(s.settingsPath())
	if err == nil {
		if err := json.Unmarshal(data, &s.settings.saved); err != nil {
			log.Printf("settings.json: %v", err)
		}
	}
	// The file may have been edited by hand: keep it within what the page allows.
	saved := &s.settings.saved
	saved.Name = clip(strings.TrimSpace(saved.Name), 80)
	if m := saved.RescanMinutes; m != nil && (*m < 0 || *m > maxRescanMinutes) {
		log.Printf("settings.json: ignoring rescanMinutes %d", *m)
		saved.RescanMinutes = nil
	}
}
