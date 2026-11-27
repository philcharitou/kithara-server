package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The Internet Archive keeps every LibriVox recording in its "librivoxaudio" collection,
// with covers, descriptions and the audio in several formats. All of it is in the public
// domain.

type archive struct {
	base   string // https://archive.org, or a stand-in in tests
	client *http.Client
}

const perPage = 24

// Result is one book in a list of results (the plugin contract's result).
type Result struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Cover  string `json:"cover,omitempty"`
}

// languages are the choices offered in Settings, with the codes the Archive uses.
var languages = []struct{ Name, Code string }{
	{"Any", ""}, {"English", "eng"}, {"French", "fre"}, {"German", "ger"}, {"Spanish", "spa"},
	{"Italian", "ita"}, {"Dutch", "dut"}, {"Portuguese", "por"}, {"Russian", "rus"}, {"Chinese", "chi"},
	{"Japanese", "jpn"}, {"Polish", "pol"}, {"Latin", "lat"}, {"Greek", "grc"},
}

func languageName(code string) string {
	for _, l := range languages {
		if l.Code != "" && (strings.EqualFold(l.Code, code) || strings.EqualFold(l.Name, code)) {
			return l.Name
		}
	}
	return code
}

// terms keeps the words of a query: letters and digits, lower-cased, so nothing is read
// as part of the search syntax (its AND and OR are upper-case).
func terms(q string) []string {
	return strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func (a *archive) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", a.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "kithara-plugin-librivox (+https://github.com/philcharitou/kithara-server)")
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("the Internet Archive answered %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(v)
}

// search finds recordings by title or author; no words gives the newest.
func (a *archive) search(ctx context.Context, query, language string, page int) ([]Result, int, error) {
	q := "collection:librivoxaudio"
	if words := terms(query); len(words) > 0 {
		all := strings.Join(words, " AND ")
		q += fmt.Sprintf(" AND (title:(%s) OR creator:(%s))", all, all)
	}
	for _, l := range languages {
		if l.Name == language && l.Code != "" {
			q += fmt.Sprintf(" AND language:(%s OR %s)", l.Code, l.Name)
		}
	}
	sort := "downloads desc"
	if len(terms(query)) == 0 {
		sort = "addeddate desc"
	}
	v := url.Values{"q": {q}, "fl[]": {"identifier", "title", "creator"}, "rows": {strconv.Itoa(perPage)},
		"page": {strconv.Itoa(page + 1)}, "output": {"json"}, "sort[]": {sort}}
	var out struct {
		Response struct {
			NumFound int `json:"numFound"`
			Docs     []struct {
				Identifier string          `json:"identifier"`
				Title      string          `json:"title"`
				Creator    json.RawMessage `json:"creator"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := a.get(ctx, "/advancedsearch.php?"+v.Encode(), &out); err != nil {
		return nil, 0, err
	}
	results := []Result{}
	for _, d := range out.Response.Docs {
		results = append(results, Result{ID: d.Identifier, Title: d.Title, Author: first(d.Creator),
			Cover: a.base + "/services/img/" + url.PathEscape(d.Identifier)})
	}
	return results, out.Response.NumFound, nil
}

// item is a recording's details and files, from /metadata/<identifier>.
type item struct {
	Metadata struct {
		Identifier  string          `json:"identifier"`
		Title       string          `json:"title"`
		Creator     json.RawMessage `json:"creator"`
		Description json.RawMessage `json:"description"`
		Runtime     string          `json:"runtime"`
		Language    json.RawMessage `json:"language"`
		Collection  json.RawMessage `json:"collection"`
	} `json:"metadata"`
	Files []file `json:"files"`
}

type file struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	Size   string `json:"size"`
}

func (f file) bytes() int64 {
	n, _ := strconv.ParseInt(f.Size, 10, 64)
	return n
}

var errNotLibriVox = errors.New("no such LibriVox recording")

func (a *archive) item(ctx context.Context, id string) (*item, error) {
	var it item
	if err := a.get(ctx, "/metadata/"+url.PathEscape(id), &it); err != nil {
		return nil, err
	}
	// The metadata of anything on the Archive can be asked for; this plugin offers only
	// the LibriVox collection.
	if it.Metadata.Identifier == "" || !strings.Contains(string(it.Metadata.Collection), `"librivoxaudio"`) {
		return nil, errNotLibriVox
	}
	return &it, nil
}

// Formats, as offered in Settings.
const (
	formatM4B  = "One file with chapters (M4B), where there is one"
	formatMP3  = "MP3 files, 64 kbps"
	formatBest = "MP3 files, best quality"
)

// audio picks the files of a recording to download, in the asked-for format where it
// has it, and says which format that is.
func (it *item) audio(format string) ([]file, string) {
	of := func(archiveFormat string) []file {
		var out []file
		for _, f := range it.Files {
			if f.Format == archiveFormat {
				out = append(out, f)
			}
		}
		return out
	}
	order := []struct{ archive, label string }{{"64Kbps MP3", "MP3, 64 kbps"}, {"VBR MP3", "MP3"}, {"128Kbps MP3", "MP3, 128 kbps"}, {"Ogg Vorbis", "Ogg Vorbis"}}
	switch format {
	case formatM4B:
		order = append([]struct{ archive, label string }{{"Audiobook", "M4B with chapters"}}, order...)
	case formatBest:
		order = append([]struct{ archive, label string }{{"VBR MP3", "MP3"}, {"128Kbps MP3", "MP3, 128 kbps"}}, order...)
	}
	for _, o := range order {
		if files := of(o.archive); len(files) > 0 {
			return files, o.label
		}
	}
	return nil, ""
}

// cover is the recording's cover picture: the largest JPEG that isn't a thumbnail, else
// the Archive's tile.
func (it *item) cover() (file, bool) {
	var best file
	for _, f := range it.Files {
		if f.Format == "JPEG" && !strings.Contains(f.Name, "_thumb") && f.bytes() > best.bytes() {
			best = f
		}
	}
	if best.Name == "" {
		for _, f := range it.Files {
			if f.Format == "Item Tile" {
				return f, true
			}
		}
	}
	return best, best.Name != ""
}

var (
	readByRe = regexp.MustCompile(`(?i)\bread by\s+([^.<\n]{2,120}?)\s*(?:\.|<|\n|$)`)
	footerRe = regexp.MustCompile(`(?is)(<p>\s*)?(for further information, including links|to become a volunteer reader|download m4b \().*$`)
)

// reader is the narrator, from the "Read by …" LibriVox descriptions start with.
func reader(description string) string {
	if m := readByRe.FindStringSubmatch(tagRe.ReplaceAllString(description, " ")); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// withoutFooter drops what LibriVox puts at the end of every description: where to find
// more, how to volunteer, and a link to the M4B.
func withoutFooter(description string) string {
	return strings.TrimSpace(footerRe.ReplaceAllString(description, ""))
}

// runtime reads the Archive's "8:14:41" (or "36:33", or seconds) as seconds.
func runtime(s string) float64 {
	parts := strings.Split(strings.TrimSpace(s), ":")
	total := 0.0
	for _, p := range parts {
		n, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}

// first is a field the Archive gives as a string or as a list of them.
func first(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return list[0]
	}
	return ""
}

func newArchive(base string) *archive {
	return &archive{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 30 * time.Second}}
}
