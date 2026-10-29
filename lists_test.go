package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCollectionsAndPlaylists(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	books := catalogueOf(t, ts, admin, "").Books
	gruffalo, other := bookNamed(t, books, "Gruffalo"), bookNamed(t, books, "Salt and Starlight")

	// Only admins make collections; a kid sees only the books in them they may see.
	if res, _ := call(t, ts, "POST", "/api/collections", kid, map[string]any{"name": "Mine"}); res.StatusCode != 403 {
		t.Fatalf("a user made a collection: %d", res.StatusCode)
	}
	res, body := call(t, ts, "POST", "/api/collections", admin, map[string]any{"name": "Bedtime", "bookIds": []string{other.ID, gruffalo.ID, other.ID}})
	var made BookList
	json.Unmarshal(body, &made)
	if res.StatusCode != 201 || len(made.BookIDs) != 2 || made.BookIDs[0] != other.ID {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	call(t, ts, "POST", "/api/collections", admin, map[string]any{"name": "Grown-ups", "bookIds": []string{other.ID}})
	var seen struct{ Collections []BookList }
	_, body = call(t, ts, "GET", "/api/collections", kid, nil)
	json.Unmarshal(body, &seen)
	if len(seen.Collections) != 1 || len(seen.Collections[0].BookIDs) != 1 || seen.Collections[0].BookIDs[0] != gruffalo.ID {
		t.Fatalf("the kid sees %s", body)
	}

	// Playlists are each user's own, and only of books they may see.
	if res, _ := call(t, ts, "POST", "/api/playlists", kid, map[string]any{"name": "Sneaky", "bookIds": []string{other.ID}}); res.StatusCode != 400 {
		t.Fatalf("a playlist of a hidden book: %d", res.StatusCode)
	}
	_, body = call(t, ts, "POST", "/api/playlists", kid, map[string]any{"name": "Car", "bookIds": []string{gruffalo.ID}})
	json.Unmarshal(body, &made)
	var pl struct{ Playlists []BookList }
	_, body = call(t, ts, "GET", "/api/playlists", admin, nil)
	json.Unmarshal(body, &pl)
	if len(pl.Playlists) != 0 {
		t.Fatalf("the admin sees the kid's playlist: %s", body)
	}
	res, body = call(t, ts, "PATCH", "/api/playlists/"+made.ID, kid, map[string]any{"name": "Car trips"})
	var edited BookList
	json.Unmarshal(body, &edited)
	if res.StatusCode != 200 || edited.Name != "Car trips" || len(edited.BookIDs) != 1 {
		t.Fatalf("edit: %d %s", res.StatusCode, body)
	}
	if res, _ := call(t, ts, "DELETE", "/api/playlists/"+made.ID, admin, nil); res.StatusCode != 404 {
		t.Fatalf("someone else deleted a playlist: %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "DELETE", "/api/playlists/"+made.ID, kid, nil); res.StatusCode != 204 {
		t.Fatalf("delete: %d", res.StatusCode)
	}
}

// A device that reports its own sessions has them stored as sent (again and again,
// idempotently), and the server stops estimating for it.
func TestReportedSessions(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	b := catalogueOf(t, ts, kid, "").Books[0]
	now := time.Now().UnixMilli()
	ls := ListeningSession{ID: "s1", BookID: b.ID, StartedAt: now - 600000, EndedAt: now - 300000, EndPositionMs: 300000, ListenedMs: 300000}
	for range 2 {
		if res, body := call(t, ts, "POST", "/api/sessions", kid, map[string]any{"sessions": []ListeningSession{ls}}); res.StatusCode != 200 {
			t.Fatalf("report: %d %s", res.StatusCode, body)
		}
		ls.EndedAt, ls.ListenedMs = now, 600000
	}
	bad := ls
	bad.ID, bad.ListenedMs = "s2", 10*60*60*1000
	if res, _ := call(t, ts, "POST", "/api/sessions", kid, map[string]any{"sessions": []ListeningSession{bad}}); res.StatusCode != 400 {
		t.Fatalf("an impossible session was taken: %d", res.StatusCode)
	}
	// Progress from this device no longer adds estimated sessions.
	call(t, ts, "PUT", "/api/progress/"+b.ID, kid, Progress{PositionMs: 1000, UpdatedAt: now - 60000})
	call(t, ts, "PUT", "/api/progress/"+b.ID, kid, Progress{PositionMs: 50000, UpdatedAt: now})
	var got struct{ Sessions []ListeningSession }
	_, body := call(t, ts, "GET", "/api/sessions", kid, nil)
	json.Unmarshal(body, &got)
	if len(got.Sessions) != 1 || got.Sessions[0].ListenedMs != 600000 || got.Sessions[0].Device != "tablet" {
		t.Fatalf("sessions: %s", body)
	}
}

func TestSignOutVoidsLinks(t *testing.T) {
	ts, _, st, _ := twoLibraryServer(t)
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	b := catalogueOf(t, ts, kid, "").Books[0]
	_, body := call(t, ts, "POST", "/api/books/"+b.ID+"/stream-link", kid, nil)
	var link struct{ Files []struct{ URL string } }
	json.Unmarshal(body, &link)
	if res, _ := call(t, ts, "GET", link.Files[0].URL, "", nil); res.StatusCode != 200 {
		t.Fatalf("link gave %d", res.StatusCode)
	}
	if _, err := st.revokeDevices("kid", deviceID(kid)); err != nil {
		t.Fatal(err)
	}
	if res, _ := call(t, ts, "GET", link.Files[0].URL, "", nil); res.StatusCode != 403 {
		t.Fatalf("a link outlived its device: %d", res.StatusCode)
	}
}

// Failures from strangers lock an account's new sign-ins, but not from an address that
// has signed in to it before.
func TestKnownAddressesNotLockedOut(t *testing.T) {
	ts, srv, _, _ := twoLibraryServer(t)
	srv.trustProxy = true
	srv.ipLimit = newLoginLimiter(1000, 15*time.Minute)
	auth := func(ip, pw string) int {
		res, _ := call(t, ts, "POST", "/api/auth", "", map[string]string{"username": "kid", "password": pw}, "X-Forwarded-For", ip)
		return res.StatusCode
	}
	if code := auth("10.0.0.1", "battery staple"); code != 200 {
		t.Fatalf("home sign-in: %d", code)
	}
	for i := range 31 {
		auth("192.0.2."+string(rune('1'+i%9)), "guess")
	}
	if code := auth("198.51.100.7", "battery staple"); code != 429 {
		t.Fatalf("a new address was not held back: %d", code)
	}
	if code := auth("10.0.0.1", "battery staple"); code != 200 {
		t.Fatalf("the owner was locked out at home: %d", code)
	}
}

// ------------------------------------------------------------------ OIDC

// fakeProvider is an identity provider issuing RS256 ID tokens.
type fakeProvider struct {
	*httptest.Server
	key          *rsa.PrivateKey
	sub, name    string
	nonce, chall string
	tamper       bool
}

func newFakeProvider(t *testing.T) *fakeProvider {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &fakeProvider{key: key, sub: "abc-123", name: "kid"}
	m := http.NewServeMux()
	m.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"issuer": p.URL, "authorization_endpoint": p.URL + "/auth",
			"token_endpoint": p.URL + "/token", "jwks_uri": p.URL + "/jwks"})
	})
	m.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	})
	m.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p.nonce, p.chall = q.Get("nonce"), q.Get("code_challenge")
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=granted&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	m.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "granted" || pkceChallenge(r.Form.Get("code_verifier")) != p.chall {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": p.idToken()})
	})
	p.Server = httptest.NewServer(m)
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProvider) idToken() string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	signed := enc(map[string]string{"alg": "RS256", "kid": "k1"}) + "." + enc(map[string]any{
		"iss": p.URL, "aud": "kithara", "sub": p.sub, "preferred_username": p.name,
		"exp": time.Now().Add(time.Minute).Unix(), "nonce": p.nonce})
	sum := sha256.Sum256([]byte(signed))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if p.tamper {
		sig[0] ^= 1
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// oidcSignIn runs the flow as an app would, returning the token or the error sent back.
func oidcSignIn(t *testing.T, ts *httptest.Server, extra string) (token, errMsg string) {
	t.Helper()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	verifier := strings.Repeat("v", 50)
	next := ts.URL + "/api/oidc/start?redirect=kithara://oidc&challenge=" + pkceChallenge(verifier) + extra
	for !strings.HasPrefix(next, "kithara:") {
		res, err := noFollow.Get(next)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 302 {
			t.Fatalf("%s: %d", next, res.StatusCode)
		}
		loc, _ := res.Location()
		next = loc.String()
	}
	u, _ := url.Parse(next)
	if e := u.Query().Get("error"); e != "" {
		return "", e
	}
	res, body := call(t, ts, "POST", "/api/oidc/exchange", "", map[string]string{"code": u.Query().Get("code"), "verifier": verifier, "device": "phone"})
	if res.StatusCode != 200 {
		t.Fatalf("exchange: %d %s", res.StatusCode, body)
	}
	var out struct{ Token string }
	json.Unmarshal(body, &out)
	// The code is good once only.
	if res, _ := call(t, ts, "POST", "/api/oidc/exchange", "", map[string]string{"code": u.Query().Get("code"), "verifier": verifier}); res.StatusCode != 401 {
		t.Fatalf("a code worked twice: %d", res.StatusCode)
	}
	return out.Token, ""
}

func TestOIDCSignIn(t *testing.T) {
	ts, srv, st, _ := twoLibraryServer(t)
	p := newFakeProvider(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	set := func(c OIDCConfig) {
		t.Helper()
		res, body := call(t, ts, "PUT", "/api/settings/oidc", admin, c)
		if res.StatusCode != 200 || strings.Contains(string(body), "s3cret") {
			t.Fatalf("settings: %d %s", res.StatusCode, body)
		}
	}
	set(OIDCConfig{Issuer: p.URL, ClientID: "kithara", ClientSecret: "s3cret", PublicURL: ts.URL})

	// Nobody is linked by name unless the admin says so...
	if _, msg := oidcSignIn(t, ts, ""); msg == "" {
		t.Fatal("an existing account was taken by name")
	}
	c := srv.oidc.config()
	c.MatchByName, c.ClientSecret = true, ""
	set(c)
	if srv.oidc.config().ClientSecret != "s3cret" {
		t.Fatal("an empty secret cleared the saved one")
	}
	// ...and even then, never an admin.
	p.name = "phil"
	if _, msg := oidcSignIn(t, ts, ""); msg == "" {
		t.Fatal("an admin account was taken by name")
	}
	// A listener, by name, then by subject after a rename at the provider.
	p.name = "kid"
	if token, msg := oidcSignIn(t, ts, ""); msg != "" || st.userFor(token) != "kid" {
		t.Fatalf("sign-in by name: %q", msg)
	}
	p.name = "someone-else"
	if token, _ := oidcSignIn(t, ts, ""); st.userFor(token) != "kid" {
		t.Fatal("not found by subject")
	}
	// Someone else claiming the kid's name gets nowhere; nor does a stranger.
	p.sub, p.name = "intruder", "kid"
	if _, msg := oidcSignIn(t, ts, ""); msg == "" {
		t.Fatal("a second subject took over an account")
	}
	p.name = "newcomer"
	if _, msg := oidcSignIn(t, ts, ""); msg == "" {
		t.Fatal("an unknown name signed in without autoCreate")
	}
	// The admin links their own account from its page; after that the provider signs
	// them in to it.
	var link struct{ Code string }
	_, body := call(t, ts, "POST", "/api/oidc/link", admin, nil)
	json.Unmarshal(body, &link)
	p.sub, p.name = "phil-at-idp", "whatever"
	if token, msg := oidcSignIn(t, ts, "&link="+link.Code); msg != "" || st.userFor(token) != "phil" {
		t.Fatalf("linking: %q", msg)
	}
	if token, _ := oidcSignIn(t, ts, ""); st.userFor(token) != "phil" {
		t.Fatal("not signed in to the linked account")
	}
	if _, msg := oidcSignIn(t, ts, "&link="+link.Code); msg == "" {
		t.Fatal("a link code worked twice")
	}
	// autoCreate makes a plain account, and never touches an existing one.
	c = srv.oidc.config()
	c.AutoCreate, c.MatchByName, c.ClientSecret = true, false, ""
	set(c)
	p.sub, p.name = "new-1", "newcomer"
	if token, _ := oidcSignIn(t, ts, ""); st.userFor(token) != "newcomer" || st.accountQuick("newcomer").admin() {
		t.Fatal("autoCreate")
	}
	p.sub, p.name = "new-2", "Newcomer"
	if _, msg := oidcSignIn(t, ts, ""); msg == "" {
		t.Fatal("autoCreate reused an existing account")
	}
	p.tamper, p.sub = true, "abc-123"
	if _, msg := oidcSignIn(t, ts, ""); msg == "" {
		t.Fatal("a forged signature was accepted")
	}
}
