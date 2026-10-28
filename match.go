package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Metadata matching: looking a book up with a public catalogue (Audible, Open Library,
// Google Books) and keeping what the admin picks.
//
// The server only ever talks to these services when an admin asks it to, for one
// book at a time. What is picked, or typed in by hand, is kept in the data folder
// (metadata/<bookId>.json, in audiobookshelf's metadata.json format, and
// covers/<bookId>-match.*), never written into the library, so a read-only library
// works and removing the file undoes it. It is laid over everything read from the
// library itself.

// MatchCandidate is one result of a lookup, and the body of PUT /api/books/{id}/metadata.
type MatchCandidate struct {
	Provider      string   `json:"provider,omitempty"`
	Title         string   `json:"title,omitempty"`
	Subtitle      string   `json:"subtitle,omitempty"`
	Authors       []string `json:"authors,omitempty"`
	Narrators     []string `json:"narrators,omitempty"`
	Series        string   `json:"series,omitempty"`
	SeriesIndex   *float64 `json:"seriesIndex,omitempty"`
	Description   string   `json:"description,omitempty"`
	Publisher     string   `json:"publisher,omitempty"`
	PublishedYear string   `json:"publishedYear,omitempty"`
	Language      string   `json:"language,omitempty"`
	ISBN          string   `json:"isbn,omitempty"`
	ASIN          string   `json:"asin,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	CoverURL      string   `json:"coverUrl,omitempty"`
	DurationMs    int64    `json:"durationMs,omitempty"`
}

// MatchQuery is what to look for.
type MatchQuery struct {
	Provider string // audible (default), openlibrary, google
	Title    string
	Author   string
	ASIN     string // audible: look this product up directly
	Region   string // audible: us (default), uk, ca, au, de, fr, it, es, in, jp
}

var providers = []string{"audible", "openlibrary", "google"}

var audibleRegions = map[string]string{
	"us": "com", "uk": "co.uk", "ca": "ca", "au": "com.au", "de": "de", "fr": "fr",
	"it": "it", "es": "es", "in": "in", "jp": "co.jp",
}

// matcher does the lookups. Its client and base addresses are swappable for tests.
type matcher struct {
	client      *http.Client
	audibleURL  func(region string) string
	openLibrary string
	google      string
	googleKey   string // optional; without one Google answers 429 when busy
}

func newMatcher() *matcher {
	return &matcher{
		client: &http.Client{Timeout: 15 * time.Second},
		audibleURL: func(region string) string {
			return "https://api.audible." + audibleRegions[region] + "/1.0/catalog/products"
		},
		openLibrary: "https://openlibrary.org/search.json",
		google:      "https://www.googleapis.com/books/v1/volumes",
		googleKey:   os.Getenv("KITHARA_GOOGLE_BOOKS_KEY"),
	}
}

func (m *matcher) search(q MatchQuery) ([]MatchCandidate, error) {
	if q.Provider == "" {
		q.Provider = "audible"
	}
	if strings.TrimSpace(q.Title) == "" && q.ASIN == "" {
		return nil, errors.New("a title (or, for audible, an asin) is needed to search")
	}
	switch q.Provider {
	case "audible":
		if q.Region == "" {
			q.Region = "us"
		}
		if _, ok := audibleRegions[q.Region]; !ok {
			return nil, fmt.Errorf("unknown audible region %q", q.Region)
		}
		return m.audible(q)
	case "openlibrary":
		return m.openlibrary(q)
	case "google":
		return m.googleBooks(q)
	}
	return nil, fmt.Errorf("unknown provider %q (have %s)", q.Provider, strings.Join(providers, ", "))
}

func (m *matcher) getJSON(u string, v any) error {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "kithara-server/"+serverVersion)
	req.Header.Set("Accept", "application/json")
	res, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s answered %s", req.URL.Host, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(v)
}

// ------------------------------------------------------------------ Audible

type audibleProduct struct {
	ASIN             string                  `json:"asin"`
	Title            string                  `json:"title"`
	Subtitle         string                  `json:"subtitle"`
	Authors          []struct{ Name string } `json:"authors"`
	Narrators        []struct{ Name string } `json:"narrators"`
	Publisher        string                  `json:"publisher_name"`
	Summary          string                  `json:"publisher_summary"`
	ReleaseDate      string                  `json:"release_date"`
	Language         string                  `json:"language"`
	RuntimeLengthMin int64                   `json:"runtime_length_min"`
	Series           []struct {
		Title    string `json:"title"`
		Sequence string `json:"sequence"`
	} `json:"series"`
	Images          map[string]string `json:"product_images"`
	CategoryLadders []struct {
		Ladder []struct{ Name string } `json:"ladder"`
	} `json:"category_ladders"`
}

const audibleGroups = "contributors,product_desc,product_extended_attrs,product_attrs,media,series,category_ladders"

func (m *matcher) audible(q MatchQuery) ([]MatchCandidate, error) {
	base := m.audibleURL(q.Region)
	var products []audibleProduct
	if q.ASIN != "" {
		var one struct{ Product audibleProduct }
		if err := m.getJSON(base+"/"+url.PathEscape(q.ASIN)+"?response_groups="+audibleGroups+"&image_sizes=500,1024", &one); err != nil {
			return nil, err
		}
		if one.Product.ASIN != "" {
			products = append(products, one.Product)
		}
	} else {
		v := url.Values{"title": {q.Title}, "num_results": {"10"}, "products_sort_by": {"Relevance"},
			"response_groups": {audibleGroups}, "image_sizes": {"500,1024"}}
		if q.Author != "" {
			v.Set("author", q.Author)
		}
		var many struct{ Products []audibleProduct }
		if err := m.getJSON(base+"?"+v.Encode(), &many); err != nil {
			return nil, err
		}
		products = many.Products
	}
	out := []MatchCandidate{}
	for _, p := range products {
		c := MatchCandidate{Provider: "audible", ASIN: p.ASIN, Title: p.Title, Subtitle: p.Subtitle,
			Publisher: p.Publisher, Description: stripHTML(p.Summary), PublishedYear: yearOf(p.ReleaseDate),
			Language: p.Language, DurationMs: p.RuntimeLengthMin * 60_000}
		for _, a := range p.Authors {
			if name, ok := audibleAuthor(a.Name); ok {
				c.Authors = append(c.Authors, name)
			}
		}
		for _, n := range p.Narrators {
			c.Narrators = append(c.Narrators, n.Name)
		}
		if len(p.Series) > 0 {
			c.Series, c.SeriesIndex = p.Series[0].Title, parseIndex(p.Series[0].Sequence)
		}
		for _, size := range []string{"1024", "500"} {
			if u := p.Images[size]; u != "" {
				c.CoverURL = u
				break
			}
		}
		seen := map[string]bool{}
		for _, l := range p.CategoryLadders {
			for _, g := range l.Ladder {
				if !seen[g.Name] {
					seen[g.Name] = true
					c.Genres = append(c.Genres, g.Name)
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// audibleAuthor drops the translators, editors and the like that Audible lists among
// the authors, written "Name - Translator".
func audibleAuthor(name string) (string, bool) {
	for _, sep := range []string{" - ", " – ", " — "} {
		if i := strings.LastIndex(name, sep); i > 0 {
			switch strings.ToLower(strings.TrimSpace(name[i+len(sep):])) {
			case "author", "":
				return strings.TrimSpace(name[:i]), true
			default:
				return "", false
			}
		}
	}
	return strings.TrimSpace(name), name != ""
}

// ------------------------------------------------------------------ Open Library

func (m *matcher) openlibrary(q MatchQuery) ([]MatchCandidate, error) {
	v := url.Values{"title": {q.Title}, "limit": {"10"},
		"fields": {"title,subtitle,author_name,first_publish_year,publisher,isbn,language,subject,cover_i"}}
	if q.Author != "" {
		v.Set("author", q.Author)
	}
	var res struct {
		Docs []struct {
			Title     string   `json:"title"`
			Subtitle  string   `json:"subtitle"`
			Authors   []string `json:"author_name"`
			Year      int      `json:"first_publish_year"`
			Publisher []string `json:"publisher"`
			ISBN      []string `json:"isbn"`
			Language  []string `json:"language"`
			Subject   []string `json:"subject"`
			Cover     int64    `json:"cover_i"`
		} `json:"docs"`
	}
	if err := m.getJSON(m.openLibrary+"?"+v.Encode(), &res); err != nil {
		return nil, err
	}
	out := []MatchCandidate{}
	for _, d := range res.Docs {
		c := MatchCandidate{Provider: "openlibrary", Title: d.Title, Subtitle: d.Subtitle, Authors: d.Authors}
		if d.Year > 0 {
			c.PublishedYear = strconv.Itoa(d.Year)
		}
		if len(d.Publisher) > 0 {
			c.Publisher = d.Publisher[0]
		}
		if len(d.ISBN) > 0 {
			c.ISBN = d.ISBN[0]
		}
		if len(d.Language) > 0 {
			c.Language = d.Language[0]
		}
		if len(d.Subject) > 5 {
			d.Subject = d.Subject[:5]
		}
		c.Genres = d.Subject
		if d.Cover > 0 {
			c.CoverURL = fmt.Sprintf("https://covers.openlibrary.org/b/id/%d-L.jpg", d.Cover)
		}
		out = append(out, c)
	}
	return out, nil
}

// ------------------------------------------------------------------ Google Books

func (m *matcher) googleBooks(q MatchQuery) ([]MatchCandidate, error) {
	query := "intitle:" + q.Title
	if q.Author != "" {
		query += " inauthor:" + q.Author
	}
	var res struct {
		Items []struct {
			VolumeInfo struct {
				Title       string   `json:"title"`
				Subtitle    string   `json:"subtitle"`
				Authors     []string `json:"authors"`
				Publisher   string   `json:"publisher"`
				Published   string   `json:"publishedDate"`
				Description string   `json:"description"`
				Language    string   `json:"language"`
				Categories  []string `json:"categories"`
				Identifiers []struct {
					Type, Identifier string
				} `json:"industryIdentifiers"`
				Images map[string]string `json:"imageLinks"`
			} `json:"volumeInfo"`
		} `json:"items"`
	}
	v := url.Values{"q": {query}, "maxResults": {"10"}}
	if m.googleKey != "" {
		v.Set("key", m.googleKey)
	}
	if err := m.getJSON(m.google+"?"+v.Encode(), &res); err != nil {
		return nil, err
	}
	out := []MatchCandidate{}
	for _, it := range res.Items {
		vi := it.VolumeInfo
		c := MatchCandidate{Provider: "google", Title: vi.Title, Subtitle: vi.Subtitle, Authors: vi.Authors,
			Publisher: vi.Publisher, PublishedYear: yearOf(vi.Published), Description: stripHTML(vi.Description),
			Language: vi.Language, Genres: vi.Categories}
		for _, id := range vi.Identifiers {
			if id.Type == "ISBN_13" || (id.Type == "ISBN_10" && c.ISBN == "") {
				c.ISBN = id.Identifier
			}
		}
		for _, size := range []string{"extraLarge", "large", "medium", "thumbnail"} {
			if u := vi.Images[size]; u != "" {
				c.CoverURL = strings.Replace(u, "http://", "https://", 1)
				break
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// ------------------------------------------------------------------ keeping a match

func overridePath(dataDir, id string) string {
	return filepath.Join(dataDir, "metadata", id+".json")
}

// overrideMeta reads the metadata kept for a book, and when it was saved (0: none).
func overrideMeta(dataDir, id string) (bookMeta, int64) {
	p := overridePath(dataDir, id)
	info, err := os.Stat(p)
	if err != nil {
		return bookMeta{}, 0
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return bookMeta{}, 0
	}
	return parseABSMetadata(data), info.ModTime().UnixMilli()
}

// overrideCover finds a cover kept for a book.
func overrideCover(dataDir, id string) (string, int64) {
	for _, ext := range []string{".jpg", ".png", ".webp"} {
		p := filepath.Join(dataDir, "covers", id+"-match"+ext)
		if info, err := os.Stat(p); err == nil {
			return p, info.ModTime().UnixMilli()
		}
	}
	return "", 0
}

var errNothingToKeep = errors.New("nothing to keep: every field is empty")

// saveOverride keeps c as the book's metadata, in audiobookshelf's format.
func saveOverride(dataDir, id string, c MatchCandidate) error {
	out := map[string]any{}
	put := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	put("title", c.Title)
	put("subtitle", c.Subtitle)
	put("description", c.Description)
	put("publisher", c.Publisher)
	put("publishedYear", c.PublishedYear)
	put("language", c.Language)
	put("isbn", c.ISBN)
	put("asin", c.ASIN)
	if len(c.Authors) > 0 {
		out["authors"] = c.Authors
	}
	if len(c.Narrators) > 0 {
		out["narrators"] = c.Narrators
	}
	if len(c.Genres) > 0 {
		out["genres"] = c.Genres
	}
	if s := strings.TrimSpace(c.Series); s != "" {
		if c.SeriesIndex != nil {
			s += " #" + strconv.FormatFloat(*c.SeriesIndex, 'f', -1, 64)
		}
		out["series"] = []string{s}
	}
	if len(out) == 0 {
		return errNothingToKeep
	}
	// Edited chapters are kept separately (putChapters); details replace details only.
	old := map[string]any{}
	if readJSON(overridePath(dataDir, id), &old) == nil && old["chapters"] != nil {
		out["chapters"] = old["chapters"]
	}
	return writeJSON(overridePath(dataDir, id), out)
}

// clearOverride forgets the metadata and cover kept for a book.
func clearOverride(dataDir, id string) {
	_ = os.Remove(overridePath(dataDir, id))
	for {
		p, _ := overrideCover(dataDir, id)
		if p == "" || os.Remove(p) != nil {
			return
		}
	}
}

// coverHosts are where the providers keep their artwork. Covers are fetched only from
// these, over https, so a cover address in a request cannot point the server at
// something on its own network.
var coverHosts = []string{"media-amazon.com", "ssl-images-amazon.com", "images-amazon.com",
	"openlibrary.org", "archive.org", "books.google.com", "googleusercontent.com", "googleapis.com"}

func coverAllowed(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range coverHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// saveCover downloads a cover for a book and keeps it beside its metadata.
func (m *matcher) saveCover(dataDir, id, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || !coverAllowed(u) {
		return fmt.Errorf("covers are only fetched over https from the providers' image hosts, not %q", rawURL)
	}
	client := *m.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !coverAllowed(req.URL) {
			return fmt.Errorf("cover redirected to %s", req.URL.Host)
		}
		return nil
	}
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("User-Agent", "kithara-server/"+serverVersion)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("cover: %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 15<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 15<<20 {
		return errors.New("cover is over 15 MB")
	}
	return saveCoverData(dataDir, id, data)
}

// saveCoverData keeps a picture as a book's cover, whatever its source.
func saveCoverData(dataDir, id string, data []byte) error {
	var ext string
	switch http.DetectContentType(data) {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/webp":
		ext = ".webp"
	default:
		return errors.New("cover is not a JPEG, PNG or WebP image")
	}
	dir := filepath.Join(dataDir, "covers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for {
		p, _ := overrideCover(dataDir, id)
		if p == "" || os.Remove(p) != nil {
			break
		}
	}
	tmp := filepath.Join(dir, id+"-match"+ext+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, id+"-match"+ext))
}
