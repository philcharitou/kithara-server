package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The web page: a small admin and listener console served from the binary itself, at
// /. It signs in like an app and uses the same API; nothing here is privileged.

//go:embed web
var webFiles embed.FS

func (s *server) webHandler() http.Handler {
	sub, _ := fs.Sub(webFiles, "web")
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Scripts and styles only from here; images also from the metadata providers
		// (match results show their covers) and from blob: (covers fetched with the token).
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: https:; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------ download links

// Download links let something that cannot send a bearer header (a browser's own
// download, a phone's download manager) fetch one book's zip for a few minutes. They
// are signed with a key that lives only in memory, so a restart voids them all.

const downloadLinkTTL = 10 * time.Minute

func newLinkKey() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

func (s *server) linkSig(user, bookID string, exp int64) string {
	m := hmac.New(sha256.New, s.linkKey)
	m.Write([]byte(user + "\x00" + bookID + "\x00" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// downloadLink answers with a signed, short-lived address for the book's zip.
func (s *server) downloadLink(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.lib.bookFor(s.acct(user), r.PathValue("bookId"))
	if b == nil {
		writeError(w, http.StatusNotFound, "no such book")
		return
	}
	exp := time.Now().Add(downloadLinkTTL).Unix()
	q := url.Values{"u": {user}, "exp": {strconv.FormatInt(exp, 10)}, "sig": {s.linkSig(user, b.ID, exp)}}
	writeJSONResponse(w, 200, map[string]any{
		"url":       "/api/books/" + url.PathEscape(b.ID) + "/download?" + q.Encode(),
		"expiresAt": exp * 1000,
	})
}

// downloadRoute serves the zip to a bearer token or to a valid signed link.
func (s *server) downloadRoute(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("sig") == "" {
		s.withUser(s.download)(w, r)
		return
	}
	user := q.Get("u")
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	want := s.linkSig(user, r.PathValue("bookId"), exp)
	if err != nil || time.Now().Unix() > exp || subtle.ConstantTimeCompare([]byte(want), []byte(q.Get("sig"))) != 1 {
		writeError(w, http.StatusForbidden, "this download link is not valid or has expired")
		return
	}
	// The user must still exist (and still see the book: download checks).
	if _, ok := s.store.account(user); !ok {
		writeError(w, http.StatusForbidden, "this download link is not valid or has expired")
		return
	}
	s.download(w, r, user, "")
}
