package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Routes beyond the sync protocol: the signed-in user's own account and devices, user
// management and metadata for admins, and whole-book downloads.

// ------------------------------------------------------------------ me and my devices

func (s *server) me(w http.ResponseWriter, r *http.Request, user, token string) {
	writeJSONResponse(w, 200, s.acct(user))
}

// deviceOwner is whose devices a request is about: the caller's own, or, for an admin,
// anyone's with ?user=NAME.
func (s *server) deviceOwner(w http.ResponseWriter, r *http.Request, user string) (string, bool) {
	who := r.URL.Query().Get("user")
	if who == "" || who == user {
		return user, true
	}
	if !s.acct(user).admin() {
		writeError(w, http.StatusForbidden, "only an admin can see other users' devices")
		return "", false
	}
	if _, ok := s.store.account(who); !ok {
		writeError(w, http.StatusNotFound, "no such user")
		return "", false
	}
	return who, true
}

func (s *server) listDevices(w http.ResponseWriter, r *http.Request, user, token string) {
	who, ok := s.deviceOwner(w, r, user)
	if !ok {
		return
	}
	writeJSONResponse(w, 200, map[string]any{"devices": s.store.devices(who, token)})
}

func (s *server) revokeDevice(w http.ResponseWriter, r *http.Request, user, token string) {
	who, ok := s.deviceOwner(w, r, user)
	if !ok {
		return
	}
	n, err := s.store.revokeDevices(who, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "no such device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------ users (admin)

func (s *server) listUsers(w http.ResponseWriter, r *http.Request, user, token string) {
	writeJSONResponse(w, 200, map[string]any{"users": s.store.accounts()})
}

type userChange struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	Role      string    `json:"role"`
	Libraries *[]string `json:"libraries"` // absent: unchanged; []: all libraries
}

// apply sets what the change names on an existing user.
func (s *server) applyUserChange(name string, in userChange) error {
	if in.Password != "" {
		if err := s.store.setPassword(name, in.Password); err != nil {
			return err
		}
	}
	if in.Role != "" {
		if err := s.store.setRole(name, in.Role); err != nil {
			return err
		}
	}
	if in.Libraries != nil {
		if err := s.checkLibraries(*in.Libraries); err != nil {
			return err
		}
		return s.store.setLibraries(name, *in.Libraries)
	}
	return nil
}

// checkLibraries refuses names that are not libraries on this server, so a typo does
// not quietly hide everything from someone.
func (s *server) checkLibraries(names []string) error {
	for _, n := range names {
		if _, ok := s.lib.catalogue(n); !ok && n != "" {
			return fmt.Errorf("no library %q", n)
		}
	}
	return nil
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request, user, token string) {
	var in userChange
	if err := readBody(r, &in); err != nil || in.Username == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if _, exists := s.store.account(in.Username); exists {
		writeError(w, http.StatusConflict, "that user already exists")
		return
	}
	if in.Libraries != nil {
		if err := s.checkLibraries(*in.Libraries); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := s.store.setPassword(in.Username, in.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Password = ""
	if err := s.applyUserChange(in.Username, in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusCreated, s.acct(in.Username))
}

func (s *server) updateUser(w http.ResponseWriter, r *http.Request, user, token string) {
	name := r.PathValue("name")
	if _, ok := s.store.account(name); !ok {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	var in userChange
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad user change")
		return
	}
	if err := s.applyUserChange(name, in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONResponse(w, 200, s.acct(name))
}

func (s *server) deleteUser(w http.ResponseWriter, r *http.Request, user, token string) {
	name := r.PathValue("name")
	if _, ok := s.store.account(name); !ok {
		writeError(w, http.StatusNotFound, "no such user")
		return
	}
	if err := s.store.removeUser(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------ download

// download sends a whole book as one zip: its files under their names in the book
// folder (disc folders kept, so CD 1/track 01 and CD 2/track 01 stay apart), and its
// cover. Audio is stored, not compressed again; it would not get smaller.
func (s *server) download(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.lib.bookFor(s.acct(user), r.PathValue("bookId"))
	if b == nil {
		writeError(w, http.StatusNotFound, "no such book")
		return
	}
	for _, f := range b.Files {
		if _, err := os.Stat(f.path); err != nil {
			writeError(w, http.StatusNotFound, "a file of this book is gone; rescan")
			return
		}
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(b.Title+".zip"))
	zw := zip.NewWriter(w)
	add := func(name, path string) error {
		fh, err := os.Open(path)
		if err != nil {
			return err
		}
		defer fh.Close()
		info, err := fh.Stat()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Method = name, zip.Store
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, fh)
		return err
	}
	for _, f := range b.Files {
		name := f.Name
		if rel, err := filepath.Rel(b.dir, f.path); err == nil && !strings.HasPrefix(rel, "..") {
			name = filepath.ToSlash(rel)
		}
		if err := add(name, f.path); err != nil {
			// The headers are gone already; all that is left is to cut the zip short.
			log.Printf("download %s: %v", b.ID, err)
			return
		}
	}
	if b.coverPath != "" {
		if err := add("cover"+strings.ToLower(filepath.Ext(b.coverPath)), b.coverPath); err != nil {
			log.Printf("download %s cover: %v", b.ID, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		log.Printf("download %s: %v", b.ID, err)
	}
}

// contentDisposition names a download, plainly for old clients and in full (RFC 5987)
// for the rest.
func contentDisposition(name string) string {
	plain := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, plain, urlPathEscape(name))
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// ------------------------------------------------------------------ metadata (admin)

// matchBook looks the book up with a provider. Title and author default to the
// book's own, so a bare GET is the usual "find this book".
func (s *server) matchBook(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.lib.book(r.PathValue("bookId"))
	if b == nil {
		writeError(w, http.StatusNotFound, "no such book")
		return
	}
	q := r.URL.Query()
	mq := MatchQuery{Provider: q.Get("provider"), Title: q.Get("title"), Author: q.Get("author"), ASIN: q.Get("asin"), Region: q.Get("region")}
	if mq.Title == "" && mq.ASIN == "" {
		mq.Title, mq.Author = b.Title, firstAuthor(b.Author)
	}
	results, err := s.match.search(mq)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]any{"results": results})
}

func firstAuthor(a string) string {
	first, _, _ := strings.Cut(a, ",")
	return strings.TrimSpace(first)
}

// putMetadata keeps a book's metadata: a match result as it came back, or fields typed
// in by hand. Its coverUrl, when set, is fetched and kept as the book's cover.
func (s *server) putMetadata(w http.ResponseWriter, r *http.Request, user, token string) {
	id := r.PathValue("bookId")
	if s.lib.book(id) == nil {
		writeError(w, http.StatusNotFound, "no such book")
		return
	}
	var in MatchCandidate
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad metadata")
		return
	}
	cover := in.CoverURL
	in.CoverURL = ""
	// Fields replace whatever was kept before; a body with only a coverUrl changes just
	// the cover.
	if err := saveOverride(s.lib.dataDir, id, in); err != nil && !(errors.Is(err, errNothingToKeep) && cover != "") {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cover != "" {
		if err := s.match.saveCover(s.lib.dataDir, id, cover); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	// ?rescan=0 for a batch: the caller rescans once at the end.
	if r.URL.Query().Get("rescan") == "0" {
		writeJSONResponse(w, 200, map[string]bool{"saved": true})
		return
	}
	s.afterMetadataChange(w, id)
}

func (s *server) deleteMetadata(w http.ResponseWriter, r *http.Request, user, token string) {
	id := r.PathValue("bookId")
	if s.lib.book(id) == nil {
		writeError(w, http.StatusNotFound, "no such book")
		return
	}
	clearOverride(s.lib.dataDir, id)
	s.afterMetadataChange(w, id)
}

// afterMetadataChange rescans, so the catalogue (and every device's next sync) has
// the change, and answers with the book as it now is.
func (s *server) afterMetadataChange(w http.ResponseWriter, id string) {
	if err := s.lib.rescan(); err != nil {
		log.Printf("scan: %v", err)
	}
	b := s.lib.book(id)
	if b == nil {
		writeError(w, http.StatusNotFound, "the book went away during the rescan")
		return
	}
	writeJSONResponse(w, 200, b)
}
