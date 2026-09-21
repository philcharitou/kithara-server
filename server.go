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
}

func newServer(st *store, lib *library, name string) *server {
	s := &server{store: st, lib: lib, name: name, mux: http.NewServeMux()}
	m := s.mux
	m.HandleFunc("GET /api/health", s.health)
	m.HandleFunc("POST /api/auth", s.auth)
	m.HandleFunc("POST /api/auth/logout", s.withUser(s.logout))
	m.HandleFunc("GET /api/library", s.withUser(s.libraryHandler))
	m.HandleFunc("POST /api/rescan", s.withUser(s.rescan))
	m.HandleFunc("GET /api/progress", s.withUser(s.allProgress))
	m.HandleFunc("PUT /api/progress/{bookId}", s.withUser(s.putProgress))
	m.HandleFunc("DELETE /api/progress/{bookId}", s.withUser(s.deleteProgress))
	m.HandleFunc("GET /api/bookmarks", s.withUser(s.allBookmarks))
	m.HandleFunc("POST /api/bookmarks/{bookId}", s.withUser(s.addBookmark))
	m.HandleFunc("DELETE /api/bookmarks/{bookId}/{id}", s.withUser(s.deleteBookmark))
	m.HandleFunc("GET /audio/{bookId}/{index}", s.withUser(s.audio))
	m.HandleFunc("HEAD /audio/{bookId}/{index}", s.withUser(s.audio))
	m.HandleFunc("GET /covers/{bookId}", s.withUser(s.cover))
	m.HandleFunc("HEAD /covers/{bookId}", s.withUser(s.cover))
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("kithara-server " + serverVersion + "\nThis is a Kithara sync server. Add it in the Kithara app under Add a library > Kithara sync server.\n"))
			return
		}
		writeError(w, http.StatusNotFound, "not found")
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
	if !s.store.checkPassword(in.Username, in.Password) {
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
	writeJSONResponse(w, 200, map[string]string{"token": token, "user": in.Username})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, user, token string) {
	if err := s.store.revokeToken(token); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) libraryHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	s.lib.mu.RLock()
	body, etag := s.lib.body, s.lib.etag
	s.lib.mu.RUnlock()
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
	if s.lib.book(bookID) == nil {
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
	stored, accepted, err := s.store.putProgress(user, in)
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
	if s.lib.book(bookID) == nil {
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
	b := s.lib.book(r.PathValue("bookId"))
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
	b := s.lib.book(r.PathValue("bookId"))
	if b == nil || b.coverPath == "" {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, b.coverPath)
}
