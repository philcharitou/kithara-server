package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sign-in through an identity provider (OpenID Connect: Authelia, Authentik, Keycloak,
// Google and the like), alongside passwords.
//
// The server is the OIDC client. A browser or an app opens /api/oidc/start, which sends
// it on to the provider; the provider sends it back to /api/oidc/callback with a code,
// which the server trades for an ID token, checks, and turns into one of its own
// accounts. It then hands the browser or app a one-time code, not a token: the token
// is collected with POST /api/oidc/exchange, which needs the PKCE verifier the caller
// chose at the start, so a code caught on the way (another app claiming the kithara://
// scheme, say) is useless to whoever caught it.
//
// An account is found by the provider's subject (bound on first sign-in), else by the
// configured claim (preferred_username unless set otherwise) matching a user name.
// With autoCreate, a name not yet known becomes a new plain user.

type OIDCConfig struct {
	Issuer        string `json:"issuer"`
	ClientID      string `json:"clientId"`
	ClientSecret  string `json:"clientSecret,omitempty"`
	PublicURL     string `json:"publicUrl"`               // how browsers reach this server, e.g. https://books.example.com
	UsernameClaim string `json:"usernameClaim,omitempty"` // default preferred_username
	AutoCreate    bool   `json:"autoCreate,omitempty"`
	ButtonText    string `json:"buttonText,omitempty"`
}

func (c OIDCConfig) enabled() bool { return c.Issuer != "" && c.ClientID != "" && c.PublicURL != "" }

func (c OIDCConfig) claim() string {
	if c.UsernameClaim != "" {
		return c.UsernameClaim
	}
	return "preferred_username"
}

const (
	oidcPendingTTL = 10 * time.Minute // from start to callback
	oidcCodeTTL    = 2 * time.Minute  // from callback to exchange
	oidcMaxPending = 1000
	oidcKeysTTL    = time.Hour
)

type oidcPending struct {
	nonce, verifier string // ours, with the provider
	challenge       string // the caller's, with us
	target, device  string
	at              time.Time
}

type oidcCode struct {
	user, device, challenge string
	at                      time.Time
}

type oidcDiscovery struct {
	Issuer   string `json:"issuer"`
	AuthURL  string `json:"authorization_endpoint"`
	TokenURL string `json:"token_endpoint"`
	JWKSURL  string `json:"jwks_uri"`
}

type oidcBox struct {
	mu      sync.Mutex
	path    string
	cfg     OIDCConfig
	disc    *oidcDiscovery
	keys    map[string]crypto.PublicKey
	keysAt  time.Time
	pending map[string]oidcPending // state -> sign-in under way
	codes   map[string]oidcCode    // one-time code -> account
	client  *http.Client
}

func (s *server) loadOIDC() {
	b := &oidcBox{path: filepath.Join(s.store.dir, "oidc.json"), pending: map[string]oidcPending{}, codes: map[string]oidcCode{},
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err := readJSON(b.path, &b.cfg); err != nil {
		log.Printf("oidc.json: %v", err)
	}
	s.oidc = b
}

func (b *oidcBox) config() OIDCConfig {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg
}

// ------------------------------------------------------------------ settings (admin)

// oidcSettings shows the set-up, with the client secret left out.
func (s *server) oidcSettings(w http.ResponseWriter, r *http.Request, user, token string) {
	c := s.oidc.config()
	has := c.ClientSecret != ""
	c.ClientSecret = ""
	writeJSONResponse(w, 200, map[string]any{"config": c, "hasSecret": has,
		"callbackUrl": strings.TrimSuffix(c.PublicURL, "/") + "/api/oidc/callback"})
}

// putOIDCSettings saves the set-up. An empty clientSecret keeps the one saved; an
// empty issuer turns sign-in through a provider off.
func (s *server) putOIDCSettings(w http.ResponseWriter, r *http.Request, user, token string) {
	var in OIDCConfig
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad settings")
		return
	}
	in.Issuer = strings.TrimSuffix(strings.TrimSpace(in.Issuer), "/")
	in.PublicURL = strings.TrimSuffix(strings.TrimSpace(in.PublicURL), "/")
	in.ClientID, in.UsernameClaim = strings.TrimSpace(in.ClientID), strings.TrimSpace(in.UsernameClaim)
	in.ButtonText = clip(strings.TrimSpace(in.ButtonText), 40)
	if in.Issuer != "" {
		for _, u := range []string{in.Issuer, in.PublicURL} {
			if err := checkWebURL(u); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		if in.ClientID == "" {
			writeError(w, http.StatusBadRequest, "a client id is needed")
			return
		}
	}
	b := s.oidc
	b.mu.Lock()
	if in.ClientSecret == "" && in.Issuer != "" {
		in.ClientSecret = b.cfg.ClientSecret
	}
	err := writeJSON(b.path, in)
	if err == nil {
		_ = os.Chmod(b.path, 0o600)
		b.cfg, b.disc, b.keys = in, nil, nil
	}
	b.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.oidcSettings(w, r, user, token)
}

// checkWebURL accepts https addresses, and plain http only to this machine.
func checkWebURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q is not a web address", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	if host := u.Hostname(); u.Scheme == "http" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return nil
	}
	return fmt.Errorf("%q must be https", raw)
}

// ------------------------------------------------------------------ the flow

// oidcInfo tells the sign-in page and the apps whether to offer the button.
func (s *server) oidcInfo(w http.ResponseWriter, r *http.Request) {
	c := s.oidc.config()
	text := c.ButtonText
	if text == "" {
		text = "Sign in with single sign-on"
	}
	writeJSONResponse(w, 200, map[string]any{"enabled": c.enabled(), "buttonText": text})
}

// oidcTarget is where the one-time code goes: this server's page, or an app's
// kithara: address.
func oidcTarget(raw string) (string, bool) {
	if raw == "" {
		return "/", true
	}
	u, err := url.Parse(raw)
	return raw, err == nil && strings.EqualFold(u.Scheme, "kithara") && u.Fragment == ""
}

// withParams adds the code (or error) to the target: in the fragment for the page, so
// it stays out of logs and Referer headers; in the query for an app.
func withParams(target string, v url.Values) string {
	if target == "/" {
		return "/#" + v.Encode()
	}
	if strings.Contains(target, "?") {
		return target + "&" + v.Encode()
	}
	return target + "?" + v.Encode()
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// oidcStart: ?challenge= (the caller's PKCE S256 challenge), ?redirect= (an app's
// kithara: address; the page leaves it out) and ?device= (a name for the device).
func (s *server) oidcStart(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	target, ok := oidcTarget(q.Get("redirect"))
	challenge := q.Get("challenge")
	if !ok || len(challenge) != 43 {
		writeError(w, http.StatusBadRequest, "a challenge (PKCE S256) is needed, and only kithara: addresses may be sent back to")
		return
	}
	// From here on, trouble goes back to the page or app, to show there.
	fail := func(msg string) {
		http.Redirect(w, r, withParams(target, url.Values{"error": {msg}}), http.StatusFound)
	}
	b := s.oidc
	c := b.config()
	if !c.enabled() {
		fail("sign-in through an identity provider is not set up")
		return
	}
	disc, err := b.discover(r.Context(), c)
	if err != nil {
		log.Printf("oidc: %v", err)
		fail("the identity provider did not answer; try again later")
		return
	}
	state, p := randomString(24), oidcPending{nonce: randomString(24), verifier: randomString(48),
		challenge: challenge, target: target, device: clip(q.Get("device"), 64), at: time.Now()}
	b.mu.Lock()
	b.prune(time.Now())
	full := len(b.pending) >= oidcMaxPending
	if !full {
		b.pending[state] = p
	}
	b.mu.Unlock()
	if full {
		fail("busy; try again in a moment")
		return
	}
	v := url.Values{
		"response_type": {"code"}, "client_id": {c.ClientID}, "redirect_uri": {c.PublicURL + "/api/oidc/callback"},
		"scope": {"openid profile email"}, "state": {state}, "nonce": {p.nonce},
		"code_challenge": {pkceChallenge(p.verifier)}, "code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(disc.AuthURL, "?") {
		sep = "&"
	}
	http.Redirect(w, r, disc.AuthURL+sep+v.Encode(), http.StatusFound)
}

// prune drops sign-ins and codes that took too long; the caller holds mu.
func (b *oidcBox) prune(now time.Time) {
	for k, p := range b.pending {
		if now.Sub(p.at) > oidcPendingTTL {
			delete(b.pending, k)
		}
	}
	for k, c := range b.codes {
		if now.Sub(c.at) > oidcCodeTTL {
			delete(b.codes, k)
		}
	}
}

func (s *server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b := s.oidc
	b.mu.Lock()
	b.prune(time.Now())
	p, ok := b.pending[q.Get("state")]
	delete(b.pending, q.Get("state"))
	b.mu.Unlock()
	if !ok {
		writeError(w, http.StatusBadRequest, "this sign-in has expired; start again")
		return
	}
	fail := func(msg string) {
		http.Redirect(w, r, withParams(p.target, url.Values{"error": {msg}}), http.StatusFound)
	}
	if e := q.Get("error"); e != "" {
		fail("the identity provider said: " + clip(e, 100))
		return
	}
	c := b.config()
	claims, err := b.redeem(r.Context(), c, q.Get("code"), p)
	if err != nil {
		log.Printf("oidc: %v", err)
		fail("the identity provider's answer could not be checked")
		return
	}
	user, err := s.store.oidcAccount(c, claims)
	if err != nil {
		fail(err.Error())
		return
	}
	code := randomString(24)
	b.mu.Lock()
	b.codes[code] = oidcCode{user: user, device: p.device, challenge: p.challenge, at: time.Now()}
	b.mu.Unlock()
	http.Redirect(w, r, withParams(p.target, url.Values{"code": {code}}), http.StatusFound)
}

// oidcExchange trades {code, verifier} for a token, as POST /api/auth would.
func (s *server) oidcExchange(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code, Verifier, Device string }
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "code and verifier are required")
		return
	}
	b := s.oidc
	b.mu.Lock()
	b.prune(time.Now())
	c, ok := b.codes[in.Code]
	delete(b.codes, in.Code) // one try, right or wrong
	b.mu.Unlock()
	if !ok || subtle.ConstantTimeCompare([]byte(pkceChallenge(in.Verifier)), []byte(c.challenge)) != 1 {
		writeError(w, http.StatusUnauthorized, "this sign-in has expired; start again")
		return
	}
	device := c.device
	if device == "" {
		device = in.Device
	}
	token, err := s.store.issueToken(c.user, device)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, 200, map[string]string{"token": token, "user": c.user, "role": s.acct(c.user).Role})
}

// ------------------------------------------------------------------ talking to the provider

func (b *oidcBox) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s: %s", u, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(v)
}

// discover reads the provider's configuration, once per set-up.
func (b *oidcBox) discover(ctx context.Context, c OIDCConfig) (*oidcDiscovery, error) {
	b.mu.Lock()
	d := b.disc
	b.mu.Unlock()
	if d != nil {
		return d, nil
	}
	d = &oidcDiscovery{}
	if err := b.getJSON(ctx, c.Issuer+"/.well-known/openid-configuration", d); err != nil {
		return nil, err
	}
	if strings.TrimSuffix(d.Issuer, "/") != c.Issuer {
		return nil, fmt.Errorf("the provider calls itself %q, not %q", d.Issuer, c.Issuer)
	}
	for _, u := range []string{d.AuthURL, d.TokenURL, d.JWKSURL} {
		if err := checkWebURL(strings.SplitN(u, "?", 2)[0]); err != nil {
			return nil, err
		}
	}
	b.mu.Lock()
	if b.cfg == c {
		b.disc = d
	}
	b.mu.Unlock()
	return d, nil
}

// redeem trades the provider's code for an ID token and returns its checked claims.
func (b *oidcBox) redeem(ctx context.Context, c OIDCConfig, code string, p oidcPending) (map[string]any, error) {
	disc, err := b.discover(ctx, c)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {c.PublicURL + "/api/oidc/callback"}, "code_verifier": {p.verifier}, "client_id": {c.ClientID}}
	req, err := http.NewRequestWithContext(ctx, "POST", disc.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(c.ClientSecret))
	}
	res, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok); err != nil || res.StatusCode != 200 || tok.IDToken == "" {
		return nil, fmt.Errorf("token endpoint: %s %s %v", res.Status, tok.Error, err)
	}
	return b.verify(ctx, c, disc, tok.IDToken, p.nonce, time.Now())
}

// verify checks an ID token's signature against the provider's keys, and its issuer,
// audience, lifetime and nonce.
func (b *oidcBox) verify(ctx context.Context, c OIDCConfig, disc *oidcDiscovery, raw, nonce string, now time.Time) (map[string]any, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	var head struct{ Alg, Kid string }
	if err := decodeSegment(parts[0], &head); err != nil {
		return nil, err
	}
	key, err := b.key(ctx, disc, head.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	if err := checkSignature(head.Alg, key, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := decodeSegment(parts[1], &claims); err != nil {
		return nil, err
	}
	iss, _ := claims["iss"].(string)
	exp, _ := claims["exp"].(float64)
	got, _ := claims["nonce"].(string)
	var aud []string
	switch a := claims["aud"].(type) {
	case string:
		aud = []string{a}
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok {
				aud = append(aud, s)
			}
		}
	}
	azp, _ := claims["azp"].(string)
	switch {
	case strings.TrimSuffix(iss, "/") != c.Issuer:
		return nil, fmt.Errorf("issuer %q", iss)
	case !contains(aud, c.ClientID) || len(aud) > 1 && azp != c.ClientID:
		return nil, fmt.Errorf("audience %v", aud)
	case float64(now.Add(-time.Minute).Unix()) > exp:
		return nil, errors.New("expired")
	case subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) != 1:
		return nil, errors.New("wrong nonce")
	}
	return claims, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func decodeSegment(seg string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// key finds the provider's signing key by id, fetching the key set again (at most
// once a minute) when the id is new: providers rotate keys.
func (b *oidcBox) key(ctx context.Context, disc *oidcDiscovery, kid string) (crypto.PublicKey, error) {
	b.mu.Lock()
	k, ok := b.keys[kid]
	stale := time.Since(b.keysAt)
	b.mu.Unlock()
	if ok && stale < oidcKeysTTL {
		return k, nil
	}
	if !ok && b.keys != nil && stale < time.Minute {
		return nil, fmt.Errorf("no key %q", kid)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := b.getJSON(ctx, disc.JWKSURL, &set); err != nil {
		return nil, err
	}
	keys := map[string]crypto.PublicKey{}
	for _, j := range set.Keys {
		if pk, err := j.public(); err == nil && (j.Use == "" || j.Use == "sig") {
			keys[j.Kid] = pk
		}
	}
	b.mu.Lock()
	b.keys, b.keysAt = keys, time.Now()
	b.mu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("no key %q", kid)
}

type jwk struct {
	Kty, Kid, Use, Crv string
	N, E, X, Y         string
}

func (j jwk) public() (crypto.PublicKey, error) {
	dec := base64.RawURLEncoding.DecodeString
	switch j.Kty {
	case "RSA":
		n, err1 := dec(j.N)
		e, err2 := dec(j.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			return nil, errors.New("bad RSA key")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch j.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		default:
			return nil, errors.New("unknown curve")
		}
		x, err1 := dec(j.X)
		y, err2 := dec(j.Y)
		size := (curve.Params().BitSize + 7) / 8
		if err1 != nil || err2 != nil || len(x) != size || len(y) != size {
			return nil, errors.New("bad EC key")
		}
		// The uncompressed point form, which ecdsa.ParseUncompressedPublicKey checks is
		// on the curve.
		return ecdsa.ParseUncompressedPublicKey(curve, append(append([]byte{4}, x...), y...))
	case "OKP":
		x, err := dec(j.X)
		if err != nil || j.Crv != "Ed25519" || len(x) != ed25519.PublicKeySize {
			return nil, errors.New("bad OKP key")
		}
		return ed25519.PublicKey(x), nil
	}
	return nil, errors.New("unknown key type")
}

// checkSignature verifies a JWS signature for the algorithms providers use.
func checkSignature(alg string, key crypto.PublicKey, signed, sig []byte) error {
	digest := func(h hash.Hash) []byte { h.Write(signed); return h.Sum(nil) }
	bad := errors.New("bad signature")
	switch k := key.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return errors.New("RSA key too short")
		}
		switch alg {
		case "RS256":
			return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest(sha256.New()), sig)
		case "RS384":
			return rsa.VerifyPKCS1v15(k, crypto.SHA384, digest(sha512.New384()), sig)
		case "RS512":
			return rsa.VerifyPKCS1v15(k, crypto.SHA512, digest(sha512.New()), sig)
		case "PS256":
			return rsa.VerifyPSS(k, crypto.SHA256, digest(sha256.New()), sig, nil)
		}
	case *ecdsa.PublicKey:
		size := (k.Curve.Params().BitSize + 7) / 8
		var d []byte
		switch {
		case alg == "ES256" && k.Curve == elliptic.P256():
			d = digest(sha256.New())
		case alg == "ES384" && k.Curve == elliptic.P384():
			d = digest(sha512.New384())
		default:
			return fmt.Errorf("algorithm %q", alg)
		}
		if len(sig) != 2*size {
			return bad
		}
		r, s := new(big.Int).SetBytes(sig[:size]), new(big.Int).SetBytes(sig[size:])
		if !ecdsa.Verify(k, d, r, s) {
			return bad
		}
		return nil
	case ed25519.PublicKey:
		if alg == "EdDSA" && ed25519.Verify(k, signed, sig) {
			return nil
		}
		return bad
	}
	return fmt.Errorf("algorithm %q", alg)
}

// ------------------------------------------------------------------ accounts

var errNoOIDCAccount = errors.New("there is no account here for you; ask the server's admin to make one")

// oidcAccount is the account an ID token's claims sign in to.
func (s *store) oidcAccount(c OIDCConfig, claims map[string]any) (string, error) {
	sub, _ := claims["sub"].(string)
	name, _ := claims[c.claim()].(string)
	if sub == "" {
		return "", errors.New("the identity provider sent no subject")
	}
	if c.claim() == "email" {
		if v, _ := claims["email_verified"].(bool); !v {
			return "", errors.New("your email address is not verified with the identity provider")
		}
	}
	subject := c.Issuer + " " + sub
	s.lockFresh()
	// Bound before: the provider's own id for the person, whatever their name now.
	for n, rec := range s.userDB {
		if rec.Subject == subject {
			s.mu.Unlock()
			return n, nil
		}
	}
	found := ""
	for n := range s.userDB {
		if name != "" && strings.EqualFold(n, name) {
			found = n
		}
	}
	if found != "" {
		defer s.mu.Unlock()
		rec := s.userDB[found]
		if rec.Subject != "" {
			return "", errors.New("that account is linked to someone else at the identity provider")
		}
		rec.Subject = subject
		s.userDB[found] = rec
		return found, s.saveUsers()
	}
	s.mu.Unlock()
	if !c.AutoCreate || name == "" {
		return "", errNoOIDCAccount
	}
	// A new plain user, with a password nobody knows (an admin can set one).
	pw := make([]byte, 24)
	_, _ = rand.Read(pw)
	if err := s.setPassword(name, hex.EncodeToString(pw)); err != nil {
		return "", errors.New("could not make an account named " + name + ": " + err.Error())
	}
	s.lockFresh()
	defer s.mu.Unlock()
	rec := s.userDB[name]
	rec.Subject = subject
	s.userDB[name] = rec
	return name, s.saveUsers()
}
