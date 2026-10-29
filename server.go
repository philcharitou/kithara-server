package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The protocol version this server speaks (docs/kithara-sync-protocol.md in the app
// repository).
const protocolVersion = 1

type server struct {
	store *store
	lib   *library
	mux   *http.ServeMux

	// Failed sign-ins: a few per address, more per account (several devices, one
	// forgetful owner) before both stop for a while.
	ipLimit, userLimit *loginLimiter
	authSlots          chan struct{} // password checks running at once (each is PBKDF2)
	authWaiting        atomic.Int32  // and waiting for a slot
	trustProxy         bool          // take the client address from X-Forwarded-For

	match    *matcher
	linkKey  []byte // signs download and audio links
	started  time.Time
	settings *settingsBox
	lists    listStore
	known    *knownClients // addresses that have signed in to an account before
	oidc     *oidcBox
	trans    *transcriber
	tmu      sync.Mutex // transcript files
}

func newServer(st *store, lib *library, name string) *server {
	s := &server{store: st, lib: lib, mux: http.NewServeMux(), authSlots: make(chan struct{}, 4),
		ipLimit: newLoginLimiter(10, 15*time.Minute), userLimit: newLoginLimiter(30, 15*time.Minute), match: newMatcher(), linkKey: newLinkKey(), started: time.Now(), known: newKnownClients()}
	s.loadSettings(name, 0)
	s.loadOIDC()
	s.trans = newTranscriber(s)
	m := s.mux
	m.HandleFunc("GET /api/health", s.health)
	m.HandleFunc("POST /api/auth", s.auth)
	m.HandleFunc("POST /api/auth/logout", s.withUser(s.logout))
	m.HandleFunc("GET /api/oidc", s.oidcInfo)
	m.HandleFunc("GET /api/oidc/start", s.oidcStart)
	m.HandleFunc("GET /api/oidc/callback", s.oidcCallback)
	m.HandleFunc("POST /api/oidc/exchange", s.oidcExchange)
	m.HandleFunc("POST /api/oidc/link", s.withUser(s.oidcLinkStart))
	m.HandleFunc("DELETE /api/me/oidc", s.withUser(s.oidcUnlink))
	m.HandleFunc("GET /api/library", s.withUser(s.libraryHandler))
	m.HandleFunc("GET /api/libraries", s.withUser(s.librariesHandler))
	m.HandleFunc("POST /api/libraries", s.withAdmin(s.addLibraryHandler))
	m.HandleFunc("DELETE /api/libraries/{name}", s.withAdmin(s.removeLibraryHandler))
	m.HandleFunc("GET /api/fs", s.withAdmin(s.browse))
	m.HandleFunc("POST /api/rescan", s.withAdmin(s.rescan))
	m.HandleFunc("GET /api/progress", s.withUser(s.allProgress))
	m.HandleFunc("PUT /api/progress/{bookId}", s.withUser(s.putProgress))
	m.HandleFunc("DELETE /api/progress/{bookId}", s.withUser(s.deleteProgress))
	m.HandleFunc("GET /api/sessions", s.withUser(s.sessionsHandler))
	m.HandleFunc("POST /api/sessions", s.withUser(s.reportSessions))
	s.listRoutes(m, "collections", true)
	s.listRoutes(m, "playlists", false)
	m.HandleFunc("GET /api/books/{bookId}/transcript", s.withUser(s.getTranscript))
	m.HandleFunc("PUT /api/books/{bookId}/transcript/chapters/{index}", s.withUser(s.putChapterTranscript))
	m.HandleFunc("DELETE /api/books/{bookId}/transcript", s.withAdmin(s.deleteTranscript))
	m.HandleFunc("GET /api/transcripts/search", s.withUser(s.searchTranscripts))
	m.HandleFunc("GET /api/transcription", s.withUser(s.transcriptionStatus))
	m.HandleFunc("POST /api/books/{bookId}/transcribe", s.withUser(s.transcribeBook))
	m.HandleFunc("DELETE /api/books/{bookId}/transcribe", s.withUser(s.cancelBook))
	m.HandleFunc("POST /api/transcription/library", s.withAdmin(s.transcribeLibrary))
	m.HandleFunc("DELETE /api/transcription/queue", s.withAdmin(s.clearQueue))
	m.HandleFunc("PUT /api/settings/transcription", s.withAdmin(s.putTranscribeSettings))
	m.HandleFunc("POST /api/transcription/models/{id}", s.withAdmin(s.fetchModelHandler))
	m.HandleFunc("DELETE /api/transcription/models/{id}", s.withAdmin(s.deleteModel))
	m.HandleFunc("GET /api/stats", s.withUser(s.statsHandler))
	m.HandleFunc("GET /api/bookmarks", s.withUser(s.allBookmarks))
	m.HandleFunc("POST /api/bookmarks/{bookId}", s.withUser(s.addBookmark))
	m.HandleFunc("DELETE /api/bookmarks/{bookId}/{id}", s.withUser(s.deleteBookmark))
	m.HandleFunc("GET /audio/{bookId}/{index}", s.signedOr("audio", s.audio))
	m.HandleFunc("HEAD /audio/{bookId}/{index}", s.signedOr("audio", s.audio))
	m.HandleFunc("GET /covers/{bookId}", s.withUser(s.cover))
	m.HandleFunc("HEAD /covers/{bookId}", s.withUser(s.cover))
	m.HandleFunc("GET /api/books/{bookId}/download", s.signedOr("zip", s.download))
	m.HandleFunc("POST /api/books/{bookId}/download-link", s.withUser(s.downloadLink))
	m.HandleFunc("POST /api/books/{bookId}/stream-link", s.withUser(s.streamLink))
	m.HandleFunc("PUT /api/me/password", s.withUser(s.changePassword))

	m.HandleFunc("GET /api/me", s.withUser(s.me))
	m.HandleFunc("GET /api/status", s.withUser(s.status))
	m.HandleFunc("GET /api/devices", s.withUser(s.listDevices))
	m.HandleFunc("DELETE /api/devices/{id}", s.withUser(s.revokeDevice))

	m.HandleFunc("GET /api/users", s.withAdmin(s.listUsers))
	m.HandleFunc("POST /api/users", s.withAdmin(s.createUser))
	m.HandleFunc("PATCH /api/users/{name}", s.withAdmin(s.updateUser))
	m.HandleFunc("DELETE /api/users/{name}", s.withAdmin(s.deleteUser))

	m.HandleFunc("GET /api/books/{bookId}/match", s.withAdmin(s.matchBook))
	m.HandleFunc("PUT /api/books/{bookId}/metadata", s.withAdmin(s.putMetadata))
	m.HandleFunc("DELETE /api/books/{bookId}/metadata", s.withAdmin(s.deleteMetadata))
	m.HandleFunc("PUT /api/books/{bookId}/cover", s.withAdmin(s.putCover))
	m.HandleFunc("DELETE /api/books/{bookId}/cover", s.withAdmin(s.deleteCover))
	m.HandleFunc("PUT /api/books/{bookId}/chapters", s.withAdmin(s.putChapters))
	m.HandleFunc("PUT /api/libraries/{name}/files", s.withAdmin(s.upload))
	m.HandleFunc("GET /api/problems", s.withAdmin(s.problems))
	m.HandleFunc("GET /api/settings", s.withAdmin(s.getSettings))
	m.HandleFunc("PUT /api/settings", s.withAdmin(s.putSettings))
	m.HandleFunc("GET /api/settings/oidc", s.withAdmin(s.oidcSettings))
	m.HandleFunc("PUT /api/settings/oidc", s.withAdmin(s.putOIDCSettings))
	m.HandleFunc("GET /api/backup", s.withAdmin(s.backup))
	m.HandleFunc("GET /api/activity", s.withAdmin(s.activity))
	web := s.webHandler()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.Method != "GET" && r.Method != "HEAD" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		web.ServeHTTP(w, r)
	})
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	// Request bodies are small JSON, so a client cannot hold a connection open by
	// sending one byte at a time. Uploads lift this once the admin check has passed.
	// (Not for plain GETs: a read deadline there would cut long audio streams.)
	if r.Method != "GET" && r.Method != "HEAD" || r.ContentLength != 0 {
		_ = http.NewResponseController(w).SetReadDeadline(start.Add(30 * time.Second))
	}
	rec := &statusRecorder{ResponseWriter: w, status: 200}
	s.mux.ServeHTTP(rec, r)
	if !strings.HasPrefix(r.URL.Path, "/audio/") || rec.status >= 400 {
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

// Unwrap lets http.ResponseController reach the connection beneath.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// ------------------------------------------------------------------ helpers

type userHandler func(w http.ResponseWriter, r *http.Request, user, token string)

// withUser turns a bearer token into a user, or answers 401.
func (s *server) withUser(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := ""
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token = strings.TrimSpace(auth[7:])
		}
		user := ""
		if token != "" {
			user = s.store.userFor(token)
		}
		if user == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kithara"`)
			writeError(w, http.StatusUnauthorized, "sign in again")
			return
		}
		h(w, r, user, token)
	}
}

// withAdmin is withUser for admins only; anyone else signed in gets 403.
func (s *server) withAdmin(h userHandler) http.HandlerFunc {
	return s.withUser(func(w http.ResponseWriter, r *http.Request, user, token string) {
		if !s.acct(user).admin() {
			writeError(w, http.StatusForbidden, "only an admin can do that")
			return
		}
		h(w, r, user, token)
	})
}

// maxAuthWaiting is how many password checks may queue for a slot.
const maxAuthWaiting = 64

// passwordAttempt checks a password the careful way: the attempt is counted against
// the client's address and the account before the (deliberately slow) check, so
// parallel guesses cannot slip under the limit, and a right password takes it back.
// At most len(authSlots) checks run at once. When over the limit, or the client gave
// up while waiting, it answers the request itself and returns answered.
func (s *server) passwordAttempt(w http.ResponseWriter, r *http.Request, user, password string) (ok, answered bool) {
	now := time.Now()
	addr := limitKey(clientIP(r, s.trustProxy))
	ipKey, userKey := "ip:"+addr, "user:"+clip(strings.ToLower(user), 64)
	if wait := s.ipLimit.try(ipKey, now); wait > 0 {
		tooMany(w, wait, "too many failed attempts; try again later")
		return false, true
	}
	// From an address that has signed in to this account before, only the address's
	// own limit applies, so failures from elsewhere cannot lock the owner out.
	known := s.known.has(user, addr, now)
	if !known {
		if wait := s.userLimit.try(userKey, now); wait > 0 {
			s.ipLimit.forget(ipKey, now)
			tooMany(w, wait, "too many failed attempts; try again later")
			return false, true
		}
	}
	// A flood of checks waits its turn, but only so many at once: past that, refuse.
	if s.authWaiting.Add(1) > maxAuthWaiting {
		s.authWaiting.Add(-1)
		writeError(w, http.StatusServiceUnavailable, "busy; try again in a moment")
		return false, true
	}
	select {
	case s.authSlots <- struct{}{}:
		s.authWaiting.Add(-1)
	case <-r.Context().Done():
		s.authWaiting.Add(-1)
		writeError(w, http.StatusServiceUnavailable, "busy; try again in a moment")
		return false, true
	}
	ok = s.store.checkPassword(user, password)
	<-s.authSlots
	if ok {
		s.ipLimit.forget(ipKey, now)
		if !known {
			s.userLimit.forget(userKey, now)
		}
		s.known.add(user, addr, now)
	}
	return ok, false
}

// bookOr404 is the book in the request's path, if the user may see it; otherwise it
// answers 404 and returns nil.
func (s *server) bookOr404(w http.ResponseWriter, r *http.Request, user string) *Book {
	b := s.lib.bookFor(s.acct(user), r.PathValue("bookId"))
	if b == nil {
		writeError(w, http.StatusNotFound, "no such book")
	}
	return b
}

// tooMany answers 429 with how long to wait.
func tooMany(w http.ResponseWriter, wait time.Duration, msg string) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	writeError(w, http.StatusTooManyRequests, msg)
}

// acct is the signed-in user's role and libraries (withUser has made sure the user
// exists and the users file is fresh), without the device count most requests skip.
func (s *server) acct(user string) Account {
	return s.store.accountQuick(user)
}

func writeJSONResponse(w http.ResponseWriter, status int, v any) {
	// A client that never reads the answer cannot hold it in memory forever.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Minute))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSONResponse(w, status, map[string]string{"error": msg})
}

func readBody(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

// ------------------------------------------------------------------ routes

// status is the web page's dashboard: the server, its last scan, and for admins how
// many users and devices it has.
func (s *server) status(w http.ResponseWriter, r *http.Request, user, token string) {
	s.lib.mu.RLock()
	scanAt, scanMs := s.lib.lastScanAt, s.lib.lastScanMs
	s.lib.mu.RUnlock()
	out := map[string]any{
		"name": s.serverName(), "version": serverVersion, "protocol": protocolVersion,
		"startedAt": s.started.UnixMilli(), "lastScanAt": scanAt, "lastScanMs": scanMs,
	}
	a := s.acct(user)
	if v, ok := s.lib.catalogueFor(a, ""); ok {
		out["books"] = v.info.Books
	}
	if a.admin() {
		accs := s.store.accounts()
		devices := 0
		for _, acc := range accs {
			devices += acc.Devices
		}
		out["users"] = len(accs)
		out["devices"] = devices
		out["platform"] = runtime.GOOS + "/" + runtime.GOARCH
		out["goVersion"] = runtime.Version()
		out["ffprobe"] = haveFFprobe()
	}
	writeJSONResponse(w, 200, out)
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSONResponse(w, 200, map[string]any{"ok": true, "protocol": protocolVersion, "name": s.serverName(), "version": serverVersion,
		"features": s.features()})
}

// features are the optional extensions this server offers (docs/app-integration.md).
func (s *server) features() []string {
	f := []string{"collections", "playlists", "reportedSessions", "oidc", "transcripts"}
	if s.trans.available() {
		f = append(f, "transcription")
	}
	return f
}

func (s *server) auth(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Device   string `json:"device"`
	}
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	ok, answered := s.passwordAttempt(w, r, in.Username, in.Password)
	if answered {
		return
	}
	if !ok {
		// A small pause takes the fun out of guessing.
		time.Sleep(400 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "wrong user name or password")
		return
	}
	token, err := s.store.issueToken(in.Username, in.Device)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]string{"token": token, "user": in.Username, "role": s.acct(in.Username).Role})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, user, token string) {
	if err := s.store.revokeToken(token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) libraryHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	// ?library=NAME narrows the catalogue to one library folder; without it, all of
	// them, as the protocol expects.
	view, ok := s.lib.catalogueFor(s.acct(user), r.URL.Query().Get("library"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such library")
		return
	}
	body, etag := view.body, view.etag
	h := w.Header()
	h.Set("Vary", "Accept-Encoding")
	if etag != "" {
		h.Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	h.Set("Content-Type", "application/json; charset=utf-8")
	if len(view.gz) > 0 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		body = view.gz
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

func (s *server) librariesHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	writeJSONResponse(w, 200, map[string]any{"libraries": s.lib.libraries(s.acct(user))})
}

func (s *server) rescan(w http.ResponseWriter, r *http.Request, user, token string) {
	if err := s.lib.rescan(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]any{"books": s.lib.count()})
}

func (s *server) allProgress(w http.ResponseWriter, r *http.Request, user, token string) {
	list, err := s.store.allProgress(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]any{"progress": list})
}

func (s *server) putProgress(w http.ResponseWriter, r *http.Request, user, token string) {
	bookID := r.PathValue("bookId")
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	var in Progress
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad progress record")
		return
	}
	in.BookID = bookID
	if in.UpdatedAt == 0 {
		in.UpdatedAt = time.Now().UnixMilli()
	}
	if b.DurationMs > 0 && in.PositionMs > b.DurationMs {
		in.PositionMs = b.DurationMs
	}
	stored, accepted, err := s.store.putProgress(user, token, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !accepted {
		// Not an error: the other device got further. The client adopts the body.
		writeJSONResponse(w, http.StatusConflict, stored)
		return
	}
	writeJSONResponse(w, 200, stored)
}

func (s *server) deleteProgress(w http.ResponseWriter, r *http.Request, user, token string) {
	if err := s.store.deleteProgress(user, r.PathValue("bookId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) sessionsHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	list, err := s.store.sessions(user, r.URL.Query().Get("bookId"), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]any{"sessions": list})
}

// reportSessions takes {"sessions": [...]} from a device that records its own.
func (s *server) reportSessions(w http.ResponseWriter, r *http.Request, user, token string) {
	var in struct {
		Sessions []ListeningSession `json:"sessions"`
	}
	if err := readBody(r, &in); err != nil || len(in.Sessions) > maxReportBatch {
		writeError(w, http.StatusBadRequest, "send {\"sessions\": [...]}, up to 500 at a time")
		return
	}
	now := time.Now().UnixMilli()
	a := s.acct(user)
	for _, ls := range in.Sessions {
		if msg := checkReported(ls, now); msg != "" {
			writeError(w, http.StatusBadRequest, ls.ID+": "+msg)
			return
		}
		if s.lib.bookFor(a, ls.BookID) == nil {
			writeError(w, http.StatusBadRequest, ls.ID+": no such book")
			return
		}
	}
	if err := s.store.markReporting(token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.addSessions(user, s.store.tokenInfo(token).Device, deviceID(token), in.Sessions); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]int{"stored": len(in.Sessions)})
}

// stats: ?tzOffset=MINUTES east of UTC (60 for Paris in winter) decides where days
// begin; UTC without it.
func (s *server) statsHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	tz, _ := strconv.Atoi(r.URL.Query().Get("tzOffset"))
	if tz < -14*60 || tz > 14*60 {
		writeError(w, http.StatusBadRequest, "tzOffset is minutes east of UTC, -840 to 840")
		return
	}
	sessions, err := s.store.sessions(user, "", 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	progress, err := s.store.allProgress(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, buildStats(sessions, progress, tz, time.Now()))
}

func (s *server) allBookmarks(w http.ResponseWriter, r *http.Request, user, token string) {
	list, err := s.store.allBookmarks(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]any{"bookmarks": list})
}

func (s *server) addBookmark(w http.ResponseWriter, r *http.Request, user, token string) {
	bookID := r.PathValue("bookId")
	if s.bookOr404(w, r, user) == nil {
		return
	}
	var in Bookmark
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad bookmark")
		return
	}
	in.BookID = bookID
	in.ID = ""
	stored, err := s.store.addBookmark(user, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusCreated, stored)
}

func (s *server) deleteBookmark(w http.ResponseWriter, r *http.Request, user, token string) {
	found, err := s.store.deleteBookmark(user, r.PathValue("bookId"), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no such bookmark")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// audio serves one file of a book with range support, via http.ServeContent.
func (s *server) audio(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.lib.bookFor(s.acct(user), r.PathValue("bookId"))
	idx, err := strconv.Atoi(r.PathValue("index"))
	if b == nil || err != nil || idx < 0 || idx >= len(b.Files) {
		writeError(w, http.StatusNotFound, "no such file")
		return
	}
	f := b.Files[idx]
	fh, err := os.Open(f.path)
	if err == nil {
		defer fh.Close()
	}
	var info os.FileInfo
	if err == nil {
		info, err = fh.Stat()
	}
	if err != nil {
		// The error names the file on disk: logged, not shown.
		log.Printf("audio %s/%d: %v", b.ID, idx, err)
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "file is gone; rescan")
		} else {
			writeError(w, http.StatusInternalServerError, "cannot read this file")
		}
		return
	}
	if f.MimeType != "" {
		w.Header().Set("Content-Type", f.MimeType)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, f.Name, info.ModTime(), fh)
}

func (s *server) cover(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.lib.bookFor(s.acct(user), r.PathValue("bookId"))
	if b == nil || b.coverPath == "" {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, b.coverPath)
}
