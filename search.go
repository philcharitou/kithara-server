package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Search: the apps ask this server, and it asks the plugins (docs/server-plugins.md).
// What a plugin answers is passed on as plain text, trimmed; the only links that reach
// the apps are covers, as addresses on this server that it signed, so a phone never
// contacts a plugin's sources and the server fetches only what a plugin pointed at.

const (
	searchTimeout    = 10 * time.Second
	maxSearchResults = 100
	maxCoverBytes    = 5 << 20
	coverCacheDays   = 30
)

type SearchSourceOut struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	EmptyQueryTitle string `json:"emptyQueryTitle,omitempty"`
	CanDownload     bool   `json:"canDownload"`
}

type SearchResult struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
	Cover    string `json:"cover,omitempty"`
	Year     int    `json:"year,omitempty"`
}

type SearchPage struct {
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"` // -1: the source doesn't say
	HasMore bool           `json:"hasMore"`
}

type SearchPreview struct {
	SearchResult
	Description   string  `json:"description,omitempty"`
	LengthSeconds float64 `json:"lengthSeconds,omitempty"`
	SizeBytes     int64   `json:"sizeBytes,omitempty"`
	Files         int     `json:"files,omitempty"`
	Format        string  `json:"format,omitempty"`
	Language      string  `json:"language,omitempty"`
}

// searchable is the plugin, if it is on, offers search, and this account may use it.
func (s *server) searchable(a Account, id string) (Plugin, bool) {
	pl, ok := s.plugins.get(id)
	if !ok || !pl.Enabled || pl.Info == nil || pl.Info.Search == nil || (pl.Search != allowEveryone && !a.admin()) {
		return Plugin{}, false
	}
	return pl, true
}

func (pl Plugin) mayDownload(a Account) bool {
	return pl.Info != nil && pl.Info.Downloads && (pl.Download == allowEveryone || a.admin())
}

// request is what every call to a plugin about a user's search carries: the admin's
// settings over the plugin's defaults, and the user's name if the admin allows it.
func (pl Plugin) request(user string, fields map[string]any) map[string]any {
	settings := map[string]string{}
	if pl.Info != nil {
		for _, f := range pl.Info.Settings {
			if f.Default != "" {
				settings[f.ID] = f.Default
			}
		}
	}
	for k, v := range pl.Settings {
		settings[k] = v
	}
	fields["settings"] = settings
	if pl.SendUser {
		fields["user"] = user
	}
	return fields
}

var (
	codeRe  = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)
	spaceRe = regexp.MustCompile(`\s+`)
	breakRe = regexp.MustCompile(`(?i)<br\s*/?>|</p>`)
	paraRe  = regexp.MustCompile(`\n\s*\n`)
)

// plain makes a plugin's text safe to show anywhere: no markup, no control characters,
// one line, at most n characters.
func plain(s string, n int) string {
	s = html.UnescapeString(tagRe.ReplaceAllString(codeRe.ReplaceAllString(s, " "), " "))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return clip(strings.TrimSpace(spaceRe.ReplaceAllString(s, " ")), n)
}

// paragraphs is plain, but keeps paragraph breaks (descriptions).
func paragraphs(s string, n int) string {
	s = breakRe.ReplaceAllString(codeRe.ReplaceAllString(s, " "), "\n\n")
	var out []string
	for _, p := range paraRe.Split(s, -1) {
		if p = plain(p, n); p != "" {
			out = append(out, p)
		}
	}
	return clip(strings.Join(out, "\n\n"), n)
}

// cleanResult checks one result; it is dropped (false) without an id or a title.
func (s *server) cleanResult(source string, r SearchResult) (SearchResult, bool) {
	r.ID = strings.TrimSpace(r.ID)
	r.Title, r.Author, r.Narrator = plain(r.Title, 300), plain(r.Author, 200), plain(r.Narrator, 200)
	if r.ID == "" || len(r.ID) > 300 || strings.ContainsFunc(r.ID, func(c rune) bool { return c < 0x20 || c == 0x7f }) || r.Title == "" {
		return SearchResult{}, false
	}
	if r.Year < 0 || r.Year > 9999 {
		r.Year = 0
	}
	r.Cover = s.coverLink(source, r.Cover)
	return r, true
}

// ------------------------------------------------------------------ covers

func (s *server) coverSig(source, raw string) string {
	m := hmac.New(sha256.New, s.linkKey)
	m.Write([]byte("plugin-cover\x00" + source + "\x00" + raw))
	return hex.EncodeToString(m.Sum(nil))
}

// coverLink turns a plugin's cover address into one on this server, signed so the server
// fetches only addresses a plugin gave. Anything but http(s) is dropped.
func (s *server) coverLink(source, raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if raw == "" || len(raw) > 2000 || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	q := url.Values{"u": {raw}, "sig": {s.coverSig(source, raw)}}
	return "/api/search/" + url.PathEscape(source) + "/cover?" + q.Encode()
}

func (s *server) coverCacheDir() string { return filepath.Join(s.store.dir, "cache", "plugin-covers") }

// coverClient fetches covers for a plugin at pluginURL. A cover may come from the
// internet, or from the plugin's own address (a plugin may serve its own covers), but not
// from anywhere else on this machine or the local network: a source's results must not be
// able to make the server knock on a router or a cloud's metadata address. The check is on
// the address actually dialled, so redirects and names that resolve inward are caught.
func coverClient(ctx context.Context, pluginURL string) *http.Client {
	allowed := map[string]bool{}
	if u, err := url.Parse(pluginURL); err == nil {
		if ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname()); err == nil {
			for _, ip := range ips {
				allowed[ip.IP.String()] = true
			}
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		if ip := net.ParseIP(host); ip != nil && inward(ip) && !allowed[ip.String()] {
			return errors.New("it points at this machine or the local network")
		}
		return nil
	}}
	return &http.Client{Timeout: 15 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: nil, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return nil
		}}
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// inward is an address on this machine or a private network (Tailscale's included).
func inward(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip)
}

// searchCover serves a result's cover, fetched once and kept for a month.
func (s *server) searchCover(w http.ResponseWriter, r *http.Request, user, token string) {
	source, raw := r.PathValue("source"), r.URL.Query().Get("u")
	pl, ok := s.searchable(s.acct(user), source)
	if !ok || !hmac.Equal([]byte(r.URL.Query().Get("sig")), []byte(s.coverSig(source, raw))) {
		writeError(w, http.StatusNotFound, "no such cover")
		return
	}
	sum := sha256.Sum256([]byte(source + "\x00" + raw))
	path := filepath.Join(s.coverCacheDir(), hex.EncodeToString(sum[:16]))
	if fi, err := os.Stat(path); err != nil || time.Since(fi.ModTime()) > coverCacheDays*24*time.Hour {
		if err := fetchCover(r.Context(), pl.URL, raw, path); err != nil {
			writeError(w, http.StatusBadGateway, "the cover could not be fetched: "+err.Error())
			return
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	w.Header().Set("Content-Type", http.DetectContentType(head[:n]))
	f.Seek(0, io.SeekStart)
	http.ServeContent(w, r, "", time.Time{}, f)
}

func fetchCover(ctx context.Context, pluginURL, raw, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "kithara-server/"+serverVersion)
	res, err := coverClient(ctx, pluginURL).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New(res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxCoverBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxCoverBytes {
		return errors.New("over 5 MB")
	}
	if !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return errors.New("not a picture")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cover-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// trimCoverCache lets covers nobody has looked at for a month go.
func (s *server) trimCoverCache() {
	entries, _ := os.ReadDir(s.coverCacheDir())
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > coverCacheDays*24*time.Hour {
			os.Remove(filepath.Join(s.coverCacheDir(), e.Name()))
		}
	}
}

// ------------------------------------------------------------------ routes

func (s *server) searchSources(w http.ResponseWriter, r *http.Request, user, token string) {
	a := s.acct(user)
	out := []SearchSourceOut{}
	for _, pl := range s.plugins.snapshot() {
		if pl, ok := s.searchable(a, pl.ID); ok && pl.Error == "" {
			src := pl.Info.Search
			out = append(out, SearchSourceOut{ID: pl.ID, Name: src.Name, Description: src.Description,
				EmptyQueryTitle: src.EmptyQueryTitle, CanDownload: pl.mayDownload(a)})
		}
	}
	slices.SortFunc(out, func(x, y SearchSourceOut) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	writeJSONResponse(w, 200, map[string]any{"sources": out})
}

// search: ?source=ID&q=WORDS&page=N. An empty q asks the source for what it shows first
// (its emptyQueryTitle).
func (s *server) search(w http.ResponseWriter, r *http.Request, user, token string) {
	q := r.URL.Query()
	pl, ok := s.searchable(s.acct(user), q.Get("source"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such source")
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 0 || page > 1000 {
		writeError(w, http.StatusBadRequest, "page is 0 to 1000")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	var in struct {
		Results []SearchResult `json:"results"`
		Total   *int           `json:"total"`
		HasMore bool           `json:"hasMore"`
	}
	body := pl.request(user, map[string]any{"query": plain(q.Get("q"), 200), "page": page})
	if err := s.plugins.call(ctx, pl, "POST", "/kithara/search", body, &in); err != nil {
		log.Printf("plugin %s: search: %v", pl.Info.Name, err)
		writeError(w, http.StatusBadGateway, pl.Info.Search.Name+" didn't answer: "+err.Error())
		return
	}
	out := SearchPage{Results: []SearchResult{}, Total: -1, HasMore: in.HasMore}
	if in.Total != nil && *in.Total >= 0 {
		out.Total = *in.Total
	}
	for _, res := range in.Results[:min(len(in.Results), maxSearchResults)] {
		if res, ok := s.cleanResult(pl.ID, res); ok {
			out.Results = append(out.Results, res)
		}
	}
	writeJSONResponse(w, 200, out)
}

// searchPreview is one result in full.
func (s *server) searchPreview(w http.ResponseWriter, r *http.Request, user, token string) {
	pl, ok := s.searchable(s.acct(user), r.PathValue("source"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such source")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	var in SearchPreview
	if err := s.plugins.call(ctx, pl, "POST", "/kithara/preview", pl.request(user, map[string]any{"id": r.PathValue("id")}), &in); err != nil {
		log.Printf("plugin %s: preview: %v", pl.Info.Name, err)
		writeError(w, http.StatusBadGateway, pl.Info.Search.Name+" didn't answer: "+err.Error())
		return
	}
	res, ok := s.cleanResult(pl.ID, in.SearchResult)
	if !ok {
		writeError(w, http.StatusBadGateway, pl.Info.Search.Name+" answered without an id or a title")
		return
	}
	out := SearchPreview{SearchResult: res, Description: paragraphs(in.Description, 5000),
		LengthSeconds: max(in.LengthSeconds, 0), SizeBytes: max(in.SizeBytes, 0), Files: max(in.Files, 0),
		Format: plain(in.Format, 40), Language: plain(in.Language, 40)}
	writeJSONResponse(w, 200, out)
}
