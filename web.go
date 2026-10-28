package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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

// ------------------------------------------------------------------ signed links

// Signed links let something that cannot send a bearer header (an <audio> element, a
// browser's own download, a phone's download manager) fetch one book's audio or zip
// for a while. Each is for one user, one book and one purpose, and names the device
// that asked for it (signing that device out voids it); it is signed with a key that
// lives only in memory (a restart voids them all) and with the user's password salt
// (a password change voids theirs). See signedOr in features.go.

const downloadLinkTTL = 10 * time.Minute

func newLinkKey() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

func (s *server) linkSig(user, device, purpose, bookID string, exp int64) string {
	m := hmac.New(sha256.New, s.linkKey)
	m.Write([]byte(user + "\x00" + s.store.linkSecret(user) + "\x00" + device + "\x00" + purpose + "\x00" + bookID + "\x00" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// signedQuery is the ?u=&d=&exp=&sig= of a link good for ttl, for the device token.
func (s *server) signedQuery(user, token, purpose, bookID string, ttl time.Duration) (string, int64) {
	exp, d := time.Now().Add(ttl).Unix(), deviceID(token)
	q := url.Values{"u": {user}, "d": {d}, "exp": {strconv.FormatInt(exp, 10)}, "sig": {s.linkSig(user, d, purpose, bookID, exp)}}
	return q.Encode(), exp
}

// downloadLink answers with a signed, short-lived address for the book's zip.
func (s *server) downloadLink(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.bookOr404(w, r, user)
	if b == nil {
		return
	}
	q, exp := s.signedQuery(user, token, "zip", b.ID, downloadLinkTTL)
	writeJSONResponse(w, 200, map[string]any{
		"url":       "/api/books/" + url.PathEscape(b.ID) + "/download?" + q,
		"expiresAt": exp * 1000,
	})
}
