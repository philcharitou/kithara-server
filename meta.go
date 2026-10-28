package main

import (
	"encoding/json"
	"encoding/xml"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// bookMeta is what one source knows about a book. Empty fields mean "no opinion", so
// sources are laid over each other weakest first and the strongest non-empty value
// wins:
//
//	folder names < file tags < desc.txt / reader.txt < .opf < metadata.json
//
// metadata.json is the format audiobookshelf writes, so a library it has tidied up
// comes across intact.
type bookMeta struct {
	Title, Subtitle, Author, Narrator, Series string
	SeriesIndex                               *float64
	Description, Publisher, PublishedYear     string
	Language, ISBN, ASIN                      string
	Genres                                    []string
	Chapters                                  []Chapter
}

func (b *Book) apply(m bookMeta) {
	set := func(dst *string, v string) {
		if v = strings.TrimSpace(v); v != "" {
			*dst = v
		}
	}
	set(&b.Title, m.Title)
	set(&b.Subtitle, m.Subtitle)
	set(&b.Author, m.Author)
	set(&b.Narrator, m.Narrator)
	set(&b.Series, m.Series)
	set(&b.Description, m.Description)
	set(&b.Publisher, m.Publisher)
	set(&b.PublishedYear, m.PublishedYear)
	set(&b.Language, m.Language)
	set(&b.ISBN, m.ISBN)
	set(&b.ASIN, m.ASIN)
	if m.SeriesIndex != nil {
		b.SeriesIndex = m.SeriesIndex
	}
	if len(m.Genres) > 0 {
		b.Genres = m.Genres
	}
}

// ------------------------------------------------------------------ file tags

func tagMeta(tags map[string]string, single bool) bookMeta {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(tags[k]); v != "" {
				return v
			}
		}
		return ""
	}
	var m bookMeta
	// A folder's name is the safer title for a multi-file book: per-file "title" tags
	// are usually chapter names. A single file's own title tag is the book.
	if single {
		m.Title = get("album", "title")
	} else {
		m.Title = get("album")
	}
	m.Subtitle = get("subtitle")
	m.Author = get("artist", "album_artist", "author")
	m.Narrator = get("composer", "narrator", "performer")
	m.Series = get("series", "mvnm", "grouping")
	if s := get("series-part", "series_index", "mvin", "part"); s != "" {
		m.SeriesIndex = parseIndex(strings.Split(s, "/")[0])
	}
	m.Description = get("description", "comment", "synopsis")
	m.Publisher = get("publisher", "label")
	m.PublishedYear = yearOf(get("date", "year", "originaldate", "release_date"))
	m.Language = get("language", "lang")
	m.ISBN = get("isbn")
	m.ASIN = get("asin", "audible_asin")
	if g := get("genre"); g != "" {
		m.Genres = splitList(g)
	}
	return m
}

// ------------------------------------------------------------------ folder names

var (
	narratorRe = regexp.MustCompile(`\s*\{([^}]+)\}\s*`)
	yearRe     = regexp.MustCompile(`^\(?((?:1[5-9]|20)\d\d)\)?\s*(?:-\s+|\s)`)
	sequenceRe = regexp.MustCompile(`(?i)^(?:(?:book|vol\.?|volume|no\.?|#)\s*)?(\d+(?:\.\d+)?)\s*(?:-|\.|:)\s+`)
)

// folderMeta reads what the folder layout says: the book's own folder (or file) name
// for title, year, series number and {narrator}, and the folders above it for author
// and series, in the usual Author/Series/Book shape:
//
//	Author/Book
//	Author/Series/3 - Book {Narrator}
//	Author/Series/(1999) Book
func folderMeta(name string, above []string) bookMeta {
	var m bookMeta
	switch n := len(above); {
	case n == 1:
		m.Author = above[0]
	case n >= 2:
		m.Author, m.Series = above[n-2], above[n-1]
	}
	if sub := narratorRe.FindStringSubmatch(name); sub != nil {
		m.Narrator = sub[1]
		name = narratorRe.ReplaceAllString(name, " ")
	}
	name = strings.TrimSpace(name)
	if sub := yearRe.FindStringSubmatch(name); sub != nil && len(name) > len(sub[0]) {
		m.PublishedYear = sub[1]
		name = strings.TrimSpace(name[len(sub[0]):])
	}
	if sub := sequenceRe.FindStringSubmatch(name); sub != nil && len(name) > len(sub[0]) {
		m.SeriesIndex = parseIndex(sub[1])
		name = strings.TrimSpace(name[len(sub[0]):])
	}
	m.Title = name
	return m
}

// ------------------------------------------------------------------ sidecar files

// sidecarMeta reads the metadata files kept in a book's folder, weakest first, and
// returns them merged along with the newest modification time among them, so editing
// one moves the book's updatedAt.
func sidecarMeta(dir string) (bookMeta, int64) {
	var m bookMeta
	var newest int64
	read := func(name string) []byte {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.Size() > 4<<20 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		newest = max(newest, info.ModTime().UnixMilli())
		return data
	}
	if data := read("desc.txt"); data != nil {
		m.Description = strings.TrimSpace(string(data))
	}
	if data := read("reader.txt"); data != nil {
		m.Narrator = strings.TrimSpace(string(data))
	}
	if opf := findOPF(dir); opf != "" {
		if data := read(opf); data != nil {
			merge(&m, parseOPF(data))
		}
	}
	if data := read("metadata.json"); data != nil {
		merge(&m, parseABSMetadata(data))
	}
	return m, newest
}

// merge lays src over dst the same way Book.apply does.
func merge(dst *bookMeta, src bookMeta) {
	var b Book
	b.apply(*dst)
	b.apply(src)
	chapters := dst.Chapters
	if len(src.Chapters) > 0 {
		chapters = src.Chapters
	}
	*dst = bookMeta{Title: b.Title, Subtitle: b.Subtitle, Author: b.Author, Narrator: b.Narrator,
		Series: b.Series, SeriesIndex: b.SeriesIndex, Description: b.Description, Publisher: b.Publisher,
		PublishedYear: b.PublishedYear, Language: b.Language, ISBN: b.ISBN, ASIN: b.ASIN, Genres: b.Genres,
		Chapters: chapters}
}

// findOPF prefers metadata.opf, else the only .opf in the folder.
func findOPF(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "metadata.opf")); err == nil {
		return "metadata.opf"
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.opf"))
	if len(matches) == 1 {
		return filepath.Base(matches[0])
	}
	return ""
}

// parseABSMetadata reads audiobookshelf's metadata.json.
func parseABSMetadata(data []byte) bookMeta {
	var raw struct {
		Title         string          `json:"title"`
		Subtitle      string          `json:"subtitle"`
		Authors       []string        `json:"authors"`
		Narrators     []string        `json:"narrators"`
		Series        []string        `json:"series"`
		Genres        []string        `json:"genres"`
		PublishedYear json.RawMessage `json:"publishedYear"`
		Publisher     string          `json:"publisher"`
		Description   string          `json:"description"`
		ISBN          string          `json:"isbn"`
		ASIN          string          `json:"asin"`
		Language      string          `json:"language"`
		Chapters      []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Title string  `json:"title"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return bookMeta{}
	}
	m := bookMeta{Title: raw.Title, Subtitle: raw.Subtitle, Author: strings.Join(raw.Authors, ", "),
		Narrator: strings.Join(raw.Narrators, ", "), Genres: raw.Genres, Publisher: raw.Publisher,
		Description: raw.Description, ISBN: raw.ISBN, ASIN: raw.ASIN, Language: raw.Language}
	// "publishedYear" is a string in recent versions and a number in some older ones.
	m.PublishedYear = yearOf(strings.Trim(string(raw.PublishedYear), `"`))
	if len(raw.Series) > 0 {
		m.Series, m.SeriesIndex = splitSeries(raw.Series[0])
	}
	for _, c := range raw.Chapters {
		m.Chapters = append(m.Chapters, Chapter{Title: c.Title, StartMs: int64(c.Start*1000 + 0.5), EndMs: int64(c.End*1000 + 0.5)})
	}
	return m
}

// splitSeries reads audiobookshelf's "Name #1.5".
func splitSeries(s string) (string, *float64) {
	if i := strings.LastIndex(s, " #"); i >= 0 {
		if idx := parseIndex(s[i+2:]); idx != nil {
			return strings.TrimSpace(s[:i]), idx
		}
	}
	return strings.TrimSpace(s), nil
}

// parseOPF reads the Dublin Core metadata of an OPF file, as Calibre and
// audiobookshelf write it (EPUB 2 attributes and EPUB 3 refines both).
func parseOPF(data []byte) bookMeta {
	type text struct {
		ID    string `xml:"id,attr"`
		Role  string `xml:"role,attr"`
		Value string `xml:",chardata"`
	}
	var pkg struct {
		Metadata struct {
			Titles      []text   `xml:"title"`
			Creators    []text   `xml:"creator"`
			Contributor []text   `xml:"contributor"`
			Description string   `xml:"description"`
			Publisher   string   `xml:"publisher"`
			Date        string   `xml:"date"`
			Language    string   `xml:"language"`
			Subjects    []string `xml:"subject"`
			Identifiers []struct {
				Scheme string `xml:"scheme,attr"`
				Value  string `xml:",chardata"`
			} `xml:"identifier"`
			Metas []struct {
				ID       string `xml:"id,attr"`
				Name     string `xml:"name,attr"`
				Content  string `xml:"content,attr"`
				Property string `xml:"property,attr"`
				Refines  string `xml:"refines,attr"`
				Value    string `xml:",chardata"`
			} `xml:"meta"`
		} `xml:"metadata"`
	}
	if err := xml.Unmarshal(data, &pkg); err != nil {
		return bookMeta{}
	}
	md := pkg.Metadata
	var m bookMeta
	if len(md.Titles) > 0 {
		m.Title = md.Titles[0].Value
	}
	// EPUB 3 gives roles as <meta refines="#id" property="role">; EPUB 2 as opf:role.
	refined := map[string]string{} // "#id property" -> value
	for _, mt := range md.Metas {
		if mt.Refines != "" {
			refined[mt.Refines+" "+mt.Property] = strings.TrimSpace(mt.Value)
		}
	}
	var authors, narrators []string
	for _, c := range append(md.Creators, md.Contributor...) {
		role := c.Role
		if role == "" && c.ID != "" {
			role = refined["#"+c.ID+" role"]
		}
		switch v := strings.TrimSpace(c.Value); {
		case v == "":
		case role == "nrt":
			narrators = append(narrators, v)
		case role == "aut" || role == "":
			authors = append(authors, v)
		}
	}
	m.Author, m.Narrator = strings.Join(authors, ", "), strings.Join(narrators, ", ")
	m.Description = stripHTML(md.Description)
	m.Publisher = md.Publisher
	m.PublishedYear = yearOf(md.Date)
	m.Language = md.Language
	m.Genres = md.Subjects
	for _, id := range md.Identifiers {
		v := strings.TrimSpace(id.Value)
		switch scheme := strings.ToUpper(id.Scheme); {
		case scheme == "ISBN":
			m.ISBN = v
		case scheme == "ASIN" || scheme == "AMAZON" || scheme == "AUDIBLE":
			m.ASIN = v
		case strings.HasPrefix(strings.ToLower(v), "urn:isbn:"):
			m.ISBN = v[len("urn:isbn:"):]
		}
	}
	for _, mt := range md.Metas {
		switch {
		case mt.Name == "calibre:series":
			m.Series = mt.Content
		case mt.Name == "calibre:series_index":
			m.SeriesIndex = parseIndex(mt.Content)
		case mt.Property == "belongs-to-collection" && m.Series == "":
			m.Series = strings.TrimSpace(mt.Value)
			if mt.ID != "" {
				m.SeriesIndex = parseIndex(refined["#"+mt.ID+" group-position"])
			}
		}
	}
	return m
}

// ------------------------------------------------------------------ helpers

func parseIndex(s string) *float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return nil
	}
	return &f
}

var yearInRe = regexp.MustCompile(`\b(1[5-9]|20)\d\d\b`)

func yearOf(s string) string {
	return yearInRe.FindString(s)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' || r == '/' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	s = strings.NewReplacer("<br>", "\n", "<br/>", "\n", "<br />", "\n", "</p>", "\n\n").Replace(s)
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(s, "")))
}
