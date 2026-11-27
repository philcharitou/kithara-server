package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeArchive stands in for archive.org: one LibriVox recording, one that isn't LibriVox's.
type fakeArchive struct {
	mu      sync.Mutex
	queries []string
	slow    chan struct{} // when set, file downloads wait on it
}

func (f *fakeArchive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/advancedsearch.php":
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.Query().Get("q")+" | "+r.URL.Query().Get("sort[]")+" | page "+r.URL.Query().Get("page"))
		f.mu.Unlock()
		io.WriteString(w, `{"response":{"numFound":30,"docs":[
			{"identifier":"frankenstein_shelley","title":"Frankenstein","creator":"Mary W. Shelley"},
			{"identifier":"emma_austen","title":"Emma","creator":["Jane Austen","Someone else"]}]}}`)
	case r.URL.Path == "/metadata/frankenstein_shelley":
		io.WriteString(w, `{"metadata":{"identifier":"frankenstein_shelley","title":"Frankenstein","creator":"Mary W. Shelley",
			"description":"<a href=\"http://librivox.org\">LibriVox</a> recording.","runtime":"8:14:41","language":"eng",
			"collection":["librivoxaudio","audio_bookspoetry"]},
			"files":[
			{"name":"Frankenstein_64kb_librivox.m4b","format":"Audiobook","size":"12"},
			{"name":"frankenstein_00_shelley_64kb.mp3","format":"64Kbps MP3","size":"5"},
			{"name":"frankenstein_01_shelley_64kb.mp3","format":"64Kbps MP3","size":"5"},
			{"name":"frankenstein_00_shelley.mp3","format":"VBR MP3","size":"9"},
			{"name":"Frankenstein_1109.jpg","format":"JPEG","size":"300"},
			{"name":"Frankenstein_1109_thumb.jpg","format":"JPEG","size":"400"},
			{"name":"__ia_thumb.jpg","format":"Item Tile","size":"50"}]}`)
	case r.URL.Path == "/metadata/someones_holiday_video":
		io.WriteString(w, `{"metadata":{"identifier":"someones_holiday_video","title":"Holiday","collection":"opensource_movies"},"files":[]}`)
	case strings.HasPrefix(r.URL.Path, "/download/frankenstein_shelley/"):
		if f.slow != nil {
			<-f.slow
		}
		name := strings.TrimPrefix(r.URL.Path, "/download/frankenstein_shelley/")
		io.WriteString(w, strings.Repeat("x", map[string]int{"Frankenstein_64kb_librivox.m4b": 12, "Frankenstein_1109.jpg": 300}[name]+
			map[string]int{"frankenstein_00_shelley_64kb.mp3": 5, "frankenstein_01_shelley_64kb.mp3": 5}[name]))
	default:
		http.NotFound(w, r)
	}
}

const testSecret = "a-secret-of-some-length"

func newTestPlugin(t *testing.T) (*httptest.Server, *fakeArchive, string) {
	t.Helper()
	fa := &fakeArchive{}
	archiveSrv := httptest.NewServer(fa)
	t.Cleanup(archiveSrv.Close)
	incoming := t.TempDir()
	a := newArchive(archiveSrv.URL)
	p := &plugin{secret: testSecret, incoming: incoming, archive: a, dl: newDownloader(a, incoming)}
	srv := httptest.NewServer(p.routes())
	t.Cleanup(srv.Close)
	return srv, fa, incoming
}

func post(t *testing.T, srv *httptest.Server, method, path string, in, out any) int {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func TestDescribeAndSearch(t *testing.T) {
	srv, fa, _ := newTestPlugin(t)
	res, _ := http.Get(srv.URL + "/kithara/plugin")
	if res.StatusCode != 401 {
		t.Fatalf("without the secret: %d", res.StatusCode)
	}
	var info struct {
		Name       string
		APIVersion int
		Downloads  bool
		Search     struct{ EmptyQueryTitle string }
		Settings   []struct{ ID, Default string }
	}
	post(t, srv, "GET", "/kithara/plugin", nil, &info)
	if info.Name != "LibriVox" || info.APIVersion != 1 || !info.Downloads || info.Search.EmptyQueryTitle == "" || len(info.Settings) != 2 {
		t.Fatalf("info: %+v", info)
	}

	var page struct {
		Results []Result
		Total   int
		HasMore bool
	}
	post(t, srv, "POST", "/kithara/search", map[string]any{"query": "", "page": 0}, &page)
	if len(page.Results) != 2 || page.Total != 30 || !page.HasMore || page.Results[1].Author != "Jane Austen" ||
		!strings.HasSuffix(page.Results[0].Cover, "/services/img/frankenstein_shelley") {
		t.Fatalf("page: %+v", page)
	}
	// Words only, all of them, in title or author; a language narrows it; page 1 is the second.
	post(t, srv, "POST", "/kithara/search", map[string]any{"query": `shelley: "franken*" OR (x)`, "page": 1, "settings": map[string]string{"language": "French"}}, &page)
	if page.HasMore {
		t.Fatal("page 2 of 30 at 24 a page has no more")
	}
	fa.mu.Lock()
	got := slices.Clone(fa.queries)
	fa.mu.Unlock()
	want := []string{
		"collection:librivoxaudio | addeddate desc | page 1",
		"collection:librivoxaudio AND (title:(shelley AND franken AND or AND x) OR creator:(shelley AND franken AND or AND x)) AND language:(fre OR French) | downloads desc | page 2",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("queries:\n%s", strings.Join(got, "\n"))
	}
}

func TestPreview(t *testing.T) {
	srv, _, _ := newTestPlugin(t)
	var p map[string]any
	for format, want := range map[string]string{formatM4B: "M4B with chapters", formatMP3: "MP3, 64 kbps", formatBest: "MP3", "": "MP3, 64 kbps"} {
		post(t, srv, "POST", "/kithara/preview", map[string]any{"id": "frankenstein_shelley", "settings": map[string]string{"format": format}}, &p)
		if p["format"] != want {
			t.Errorf("format %q: got %v, want %s", format, p["format"], want)
		}
	}
	post(t, srv, "POST", "/kithara/preview", map[string]any{"id": "frankenstein_shelley", "settings": map[string]string{"format": formatMP3}}, &p)
	if p["lengthSeconds"] != float64(8*3600+14*60+41) || p["language"] != "English" || p["author"] != "Mary W. Shelley" || p["files"] != float64(2) || p["sizeBytes"] != float64(10) {
		t.Fatalf("preview: %v", p)
	}
	if code := post(t, srv, "POST", "/kithara/preview", map[string]any{"id": "someones_holiday_video"}, nil); code != 502 {
		t.Fatalf("something outside LibriVox: %d", code)
	}
	if code := post(t, srv, "POST", "/kithara/preview", map[string]any{"id": "../etc"}, nil); code != 404 {
		t.Fatalf("a made-up id: %d", code)
	}
}

func waitState(t *testing.T, srv *httptest.Server, id, want string) map[string]any {
	t.Helper()
	var st map[string]any
	for range 200 {
		post(t, srv, "GET", "/kithara/downloads/"+id, nil, &st)
		if st["state"] == want {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: never %s: %v", id, want, st)
	return nil
}

func TestDownload(t *testing.T) {
	srv, fa, incoming := newTestPlugin(t)
	var ans map[string]any
	post(t, srv, "POST", "/kithara/download", map[string]any{"downloadId": "d_1", "folder": "d_1", "id": "frankenstein_shelley"}, &ans)
	if ans["ok"] != false || !strings.Contains(ans["error"].(string), "isn't there") {
		t.Fatalf("without the server's folder: %v", ans)
	}
	os.Mkdir(filepath.Join(incoming, "d_1"), 0o777)
	post(t, srv, "POST", "/kithara/download", map[string]any{"downloadId": "d_1", "folder": "d_1", "id": "frankenstein_shelley",
		"settings": map[string]string{"format": formatMP3}}, &ans)
	if ans["ok"] != true {
		t.Fatalf("start: %v", ans)
	}
	st := waitState(t, srv, "d_1", "FINISHED")
	if st["progress"] != float64(1) || st["sizeBytes"] != float64(10) {
		t.Fatalf("status: %v", st)
	}
	entries, _ := os.ReadDir(filepath.Join(incoming, "d_1"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "cover.jpg,frankenstein_00_shelley_64kb.mp3,frankenstein_01_shelley_64kb.mp3" {
		t.Fatalf("files: %v", names)
	}
	if data, _ := os.ReadFile(filepath.Join(incoming, "d_1", "cover.jpg")); len(data) != 300 {
		t.Fatalf("the cover is the large JPEG, not the thumbnail: %d bytes", len(data))
	}
	post(t, srv, "DELETE", "/kithara/downloads/d_1", nil, nil)
	if st := waitState(t, srv, "d_1", "FAILED"); !strings.Contains(st["error"].(string), "doesn't know") {
		t.Fatalf("after forgetting: %v", st)
	}

	// Forgetting one that is running stops it, and leaves no half-written file.
	fa.slow = make(chan struct{})
	os.Mkdir(filepath.Join(incoming, "d_2"), 0o777)
	post(t, srv, "POST", "/kithara/download", map[string]any{"downloadId": "d_2", "folder": "d_2", "id": "frankenstein_shelley"}, &ans)
	waitState(t, srv, "d_2", "DOWNLOADING")
	post(t, srv, "DELETE", "/kithara/downloads/d_2", nil, nil)
	close(fa.slow)
	time.Sleep(100 * time.Millisecond)
	if entries, _ := os.ReadDir(filepath.Join(incoming, "d_2")); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
	// Bad ids and folders are refused.
	post(t, srv, "POST", "/kithara/download", map[string]any{"downloadId": "d_3", "folder": "../d_3", "id": "frankenstein_shelley"}, &ans)
	if ans["ok"] != false {
		t.Fatalf("a folder outside the incoming folder: %v", ans)
	}
}

func TestDescriptions(t *testing.T) {
	d := `Librivox recording of The Gift of the Magi, by O. Henry. Read by Betsie Bush .<br /><br />The story.<p>For further information, including links to online text, reader information.</p><p>To become a volunteer reader, please visit librivox.org .</p><p>Download M4B (6MB)</p>`
	if got := reader(d); got != "Betsie Bush" {
		t.Errorf("reader: %q", got)
	}
	if got := withoutFooter(d); got != "Librivox recording of The Gift of the Magi, by O. Henry. Read by Betsie Bush .<br /><br />The story." {
		t.Errorf("withoutFooter: %q", got)
	}
	if got := reader("<p>A recording.</p>"); got != "" {
		t.Errorf("no reader: %q", got)
	}
	if got := reader("Read by LibriVox volunteers"); got != "LibriVox volunteers" {
		t.Errorf("at the end: %q", got)
	}
}

func TestRuntime(t *testing.T) {
	for in, want := range map[string]float64{"8:14:41": 29681, "36:33": 2193, "630.38": 630.38, "": 0, "soon": 0} {
		if got := runtime(in); got != want {
			t.Errorf("runtime(%q) = %v, want %v", in, got, want)
		}
	}
}
