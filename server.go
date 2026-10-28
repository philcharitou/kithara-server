package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// The protocol version this server speaks (docs/kithara-sync-protocol.md).
const protocolVersion = 1

type server struct {
	store *store
	lib   *library
	name  string
	mux   *http.ServeMux

	// Failed sign-ins: a few per address, more per account (several devices, one
	// forgetful owner) before both stop for a while.
	ipLimit, userLimit *loginLimiter
	trustProxy         bool // take the client address from X-Forwarded-For

	match   *matcher
	linkKey []byte // signs download links
}

func newServer(st *store, lib *library, name string) *server {
	s := &server{store: st, lib: lib, name: name, mux: http.NewServeMux(),
		ipLimit: newLoginLimiter(10, 15*time.Minute), userLimit: newLoginLimiter(30, 15*time.Minute), match: newMatcher(), linkKey: newLinkKey()}
	m := s.mux
	m.HandleFunc("GET /api/health", s.health)
	m.HandleFunc("POST /api/auth", s.auth)
	m.HandleFunc("POST /api/auth/logout", s.withUser(s.logout))
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
	m.HandleFunc("GET /api/stats", s.withUser(s.statsHandler))
	m.HandleFunc("GET /api/bookmarks", s.withUser(s.allBookmarks))
	m.HandleFunc("POST /api/bookmarks/{bookId}", s.withUser(s.addBookmark))
	m.HandleFunc("DELETE /api/bookmarks/{bookId}/{id}", s.withUser(s.deleteBookmark))
	m.HandleFunc("GET /audio/{bookId}/{index}", s.withUser(s.audio))
	m.HandleFunc("HEAD /audio/{bookId}/{index}", s.withUser(s.audio))
	m.HandleFunc("GET /covers/{bookId}", s.withUser(s.cover))
	m.HandleFunc("HEAD /covers/{bookId}", s.withUser(s.cover))
	m.HandleFunc("GET /api/books/{bookId}/download", s.downloadRoute)
	m.HandleFunc("POST /api/books/{bookId}/download-link", s.withUser(s.downloadLink))

	m.HandleFunc("GET /api/me", s.withUser(s.me))
	m.HandleFunc("GET /api/devices", s.withUser(s.listDevices))
	m.HandleFunc("DELETE /api/devices/{id}", s.withUser(s.revokeDevice))

	m.HandleFunc("GET /api/users", s.withAdmin(s.listUsers))
	m.HandleFunc("POST /api/users", s.withAdmin(s.createUser))
	m.HandleFunc("PATCH /api/users/{name}", s.withAdmin(s.updateUser))
	m.HandleFunc("DELETE /api/users/{name}", s.withAdmin(s.deleteUser))

	m.HandleFunc("GET /api/books/{bookId}/match", s.withAdmin(s.matchBook))
	m.HandleFunc("PUT /api/books/{bookId}/metadata", s.withAdmin(s.putMetadata))
	m.HandleFunc("DELETE /api/books/{bookId}/metadata", s.withAdmin(s.deleteMetadata))
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

// acct is the signed-in user's account (withUser has made sure there is one).
func (s *server) acct(user string) Account {
	a, _ := s.store.account(user)
	return a
}

func writeJSONResponse(w http.ResponseWriter, status int, v any) {
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

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSONResponse(w, 200, map[string]any{"ok": true, "protocol": protocolVersion, "name": s.name, "version": serverVersion})
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
	now := time.Now()
	ipKey, userKey := "ip:"+clientIP(r, s.trustProxy), "user:"+strings.ToLower(in.Username)
	wait := max(s.ipLimit.retryAfter(ipKey, now), s.userLimit.retryAfter(userKey, now))
	if wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many failed sign-ins; try again later")
		return
	}
	if !s.store.checkPassword(in.Username, in.Password) {
		s.ipLimit.fail(ipKey, now)
		s.userLimit.fail(userKey, now)
		// A small pause takes the fun out of guessing.
		time.Sleep(400 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "wrong user name or password")
		return
	}
	s.ipLimit.reset(ipKey)
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
	if etag != "" {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
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
	if s.lib.bookFor(s.acct(user), bookID) == nil {
		writeError(w, http.StatusNotFound, "no such book")
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
	if b := s.lib.book(bookID); b != nil && b.DurationMs > 0 && in.PositionMs > b.DurationMs {
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
	if s.lib.bookFor(s.acct(user), bookID) == nil {
		writeError(w, http.StatusNotFound, "no such book")
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
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusNotFound, "file is gone; rescan")
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
