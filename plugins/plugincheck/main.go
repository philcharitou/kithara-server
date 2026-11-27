// Command plugincheck tries a Kithara server plugin the way the server will use it, and
// says what is wrong. Run it while writing a plugin:
//
//	go run . -url http://localhost:9000 -secret … -query dickens
//	go run . -url http://localhost:9000 -secret … -incoming /srv/incoming -download
//
// With -download it also downloads the first result into a folder of the incoming folder
// (which the plugin must see at the same place, or give -plugin-incoming), waits for it,
// and checks the files. The folder is removed afterwards unless -keep is given.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	base     = flag.String("url", "http://localhost:9000", "the plugin's address")
	secret   = flag.String("secret", os.Getenv("KITHARA_PLUGIN_SECRET"), "the plugin's secret (default $KITHARA_PLUGIN_SECRET)")
	query    = flag.String("query", "", "words to search for, besides the empty search")
	download = flag.Bool("download", false, "also download the first result")
	incoming = flag.String("incoming", "", "the incoming folder, as this program sees it (for -download)")
	timeout  = flag.Duration("timeout", 10*time.Minute, "how long a download may take")
	keep     = flag.Bool("keep", false, "keep the downloaded files")
	settings = flag.String("settings", "{}", `settings to send, as JSON: '{"language":"English"}'`)
)

var failures int

func pass(format string, a ...any) { fmt.Printf("  ok    "+format+"\n", a...) }
func warn(format string, a ...any) { fmt.Printf("  note  "+format+"\n", a...) }
func bad(format string, a ...any) {
	failures++
	fmt.Printf("  FAIL  "+format+"\n", a...)
}

var client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// call is a call as the server makes it; it returns the status and the decoded answer.
func call(method, path, auth string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(*base, "/")+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil {
		return res.StatusCode, err
	}
	if len(raw) > 1<<20 {
		return res.StatusCode, errors.New("the answer is over 1 MB (the server refuses those)")
	}
	if took := time.Since(start); took > 10*time.Second && (strings.Contains(path, "search") || strings.Contains(path, "preview")) {
		bad("%s took %s; the server gives up after 10 s", path, took.Round(time.Millisecond))
	}
	if out != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, fmt.Errorf("not the JSON expected: %w: %.200s", err, raw)
		}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return res.StatusCode, fmt.Errorf("answered %d: %.200s", res.StatusCode, raw)
	}
	return res.StatusCode, nil
}

type info struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	APIVersion int    `json:"apiVersion"`
	Search     *struct {
		Name, Description, EmptyQueryTitle string
	} `json:"search"`
	Downloads bool     `json:"downloads"`
	Accepts   []string `json:"accepts"`
	Settings  []struct {
		ID, Title, Type, Default string
		Choices                  []string
	} `json:"settings"`
}

type result struct {
	ID, Title, Author, Narrator, Cover string
}

func main() {
	flag.Parse()
	var sent map[string]string
	if err := json.Unmarshal([]byte(*settings), &sent); err != nil {
		fmt.Println("-settings is not a JSON object of strings:", err)
		os.Exit(2)
	}
	fmt.Println("Checking", *base)

	fmt.Println("\nDescribing itself (GET /kithara/plugin)")
	if code, _ := call("GET", "/kithara/plugin", "", nil, nil); code != 401 && code != 403 {
		bad("answered %d without the secret; it must refuse (401) anyone else", code)
	} else {
		pass("refuses calls without the secret")
	}
	var in info
	if _, err := call("GET", "/kithara/plugin", *secret, nil, &in); err != nil {
		bad("with the secret: %v", err)
		finish()
	}
	checkInfo(in)

	var first *result
	if in.Search != nil {
		fmt.Println("\nSearching (POST /kithara/search)")
		first = searchCheck("", 0, sent)
		if *query != "" {
			if r := searchCheck(*query, 0, sent); r != nil {
				first = r
			}
		}
		if first != nil {
			fmt.Println("\nOne result (POST /kithara/preview)")
			previewCheck(first.ID, sent)
		}
	}
	if *download {
		fmt.Println("\nDownloading (POST /kithara/download, GET and DELETE /kithara/downloads/{id})")
		switch {
		case !in.Downloads:
			warn("it doesn't offer downloads")
		case first == nil:
			bad("there is no result to download")
		case *incoming == "":
			bad("give -incoming: the incoming folder, which the plugin must share")
		default:
			downloadCheck(first.ID, sent)
		}
	}
	finish()
}

func finish() {
	if failures > 0 {
		fmt.Printf("\n%d problem(s). The server would refuse or mishandle what is marked FAIL.\n", failures)
		os.Exit(1)
	}
	fmt.Println("\nAll good.")
	os.Exit(0)
}

var (
	settingID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)
	linkKind  = regexp.MustCompile(`^[a-z0-9.+-]{1,40}$`)
)

func checkInfo(in info) {
	switch {
	case strings.TrimSpace(in.Name) == "":
		bad("no name")
	case len([]rune(in.Name)) > 80:
		warn("the name is cut to 80 characters")
	default:
		pass("name %q, version %q", in.Name, in.Version)
	}
	if in.APIVersion < 1 {
		bad("no apiVersion (1)")
	}
	if in.Search == nil && !in.Downloads {
		bad("offers neither search nor downloads")
	}
	if in.Search != nil {
		pass("search: %q", in.Search.Name)
	}
	if len(in.Accepts) > 0 && !in.Downloads {
		bad("accepts links but offers no downloads")
	}
	for _, k := range in.Accepts {
		if !linkKind.MatchString(k) {
			bad("%q is not a kind of link (lower-case letters, digits, . + -)", k)
		}
	}
	seen := map[string]bool{}
	for _, s := range in.Settings {
		switch {
		case !settingID.MatchString(s.ID) || seen[s.ID]:
			bad("setting %q: an id is 1 to 40 letters, digits, _ . -, each once", s.ID)
		case !slices.Contains([]string{"text", "secret", "number", "toggle", "choice"}, s.Type):
			bad("setting %s: the type %q is not text, secret, number, toggle or choice", s.ID, s.Type)
		case s.Type == "choice" && (len(s.Choices) == 0 || len(s.Choices) > 50):
			bad("setting %s: a choice has 1 to 50 choices", s.ID)
		case s.Type == "choice" && s.Default != "" && !slices.Contains(s.Choices, s.Default):
			bad("setting %s: the default %q is not among the choices", s.ID, s.Default)
		}
		seen[s.ID] = true
	}
	if len(in.Settings) > 20 {
		bad("more than 20 settings")
	} else if len(in.Settings) > 0 {
		pass("%d setting(s)", len(in.Settings))
	}
}

func searchCheck(q string, page int, sent map[string]string) *result {
	var out struct {
		Results []result `json:"results"`
		Total   *int     `json:"total"`
		HasMore bool     `json:"hasMore"`
	}
	label := fmt.Sprintf("%q", q)
	if q == "" {
		label = "nothing typed"
	}
	if _, err := call("POST", "/kithara/search", *secret, map[string]any{"query": q, "page": page, "settings": sent}, &out); err != nil {
		bad("search for %s: %v", label, err)
		return nil
	}
	kept := 0
	for _, r := range out.Results {
		switch {
		case r.ID == "" || strings.TrimSpace(r.Title) == "":
			bad("a result without an id or a title is dropped: %+v", r)
		case len(r.ID) > 300:
			bad("result id over 300 characters is dropped")
		default:
			kept++
		}
		if r.Cover != "" && !strings.HasPrefix(r.Cover, "http://") && !strings.HasPrefix(r.Cover, "https://") {
			bad("cover %q is not an http(s) address, so the server drops it", r.Cover)
		}
	}
	if len(out.Results) > 100 {
		bad("%d results on a page; the server keeps 100", len(out.Results))
	}
	total := "unknown"
	if out.Total != nil {
		total = fmt.Sprint(*out.Total)
	}
	pass("search for %s: %d results (total %s, more: %v)", label, kept, total, out.HasMore)
	if out.HasMore {
		var next struct{ Results []result }
		if _, err := call("POST", "/kithara/search", *secret, map[string]any{"query": q, "page": page + 1, "settings": sent}, &next); err != nil {
			bad("page 2 of %s: %v", label, err)
		} else if len(next.Results) > 0 && len(out.Results) > 0 && next.Results[0].ID == out.Results[0].ID {
			bad("page 2 of %s starts with the same result as page 1", label)
		} else {
			pass("page 2 of %s: %d results", label, len(next.Results))
		}
	}
	if kept == 0 {
		if q == "" {
			warn("nothing to show before the user types; the Search page will be empty at first")
		}
		return nil
	}
	return &out.Results[0]
}

func previewCheck(id string, sent map[string]string) {
	var p struct {
		result
		Description   string  `json:"description"`
		LengthSeconds float64 `json:"lengthSeconds"`
		SizeBytes     int64   `json:"sizeBytes"`
		Files         int     `json:"files"`
		Format        string  `json:"format"`
	}
	if _, err := call("POST", "/kithara/preview", *secret, map[string]any{"id": id, "settings": sent}, &p); err != nil {
		bad("preview of %s: %v", id, err)
		return
	}
	if p.ID == "" || strings.TrimSpace(p.Title) == "" {
		bad("the preview has no id or no title; the server refuses it")
		return
	}
	pass("%q by %q: %.0f min, %d file(s), %d MB, %s", p.Title, p.Author, p.LengthSeconds/60, p.Files, p.SizeBytes>>20, p.Format)
	if p.Description == "" {
		warn("no description")
	}
}

func downloadCheck(id string, sent map[string]string) {
	raw := make([]byte, 6)
	rand.Read(raw)
	dlID := "d_check" + hex.EncodeToString(raw)
	dir := filepath.Join(*incoming, dlID)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		bad("making %s: %v", dir, err)
		return
	}
	os.Chmod(dir, 0o777)
	if !*keep {
		defer os.RemoveAll(dir)
	}
	var ans struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Open  string `json:"open"`
	}
	if _, err := call("POST", "/kithara/download", *secret, map[string]any{"downloadId": dlID, "folder": dlID, "id": id, "settings": sent}, &ans); err != nil {
		bad("starting it: %v", err)
		return
	}
	switch {
	case !ans.OK:
		bad("it refused: %s", ans.Error)
		return
	case ans.Open != "":
		u, _ := url.Parse(ans.Open)
		pass("it hands off a link of kind %q; a plugin that accepts that kind would download it", u.Scheme)
		return
	}
	pass("started %s", dlID)
	deadline, last := time.Now().Add(*timeout), ""
	for {
		var st struct {
			State          string  `json:"state"`
			Progress       float64 `json:"progress"`
			BytesPerSecond int64   `json:"bytesPerSecond"`
			Error          string  `json:"error"`
		}
		if _, err := call("GET", "/kithara/downloads/"+url.PathEscape(dlID), *secret, nil, &st); err != nil {
			bad("asking how it is going: %v (the server would show the plugin as not answering)", err)
			return
		}
		now := fmt.Sprintf("%s %3.0f%%", st.State, st.Progress*100)
		if now != last {
			fmt.Printf("        %s  %d KB/s\n", now, st.BytesPerSecond>>10)
			last = now
		}
		switch st.State {
		case "QUEUED", "DOWNLOADING", "PAUSED":
		case "FINISHED":
			checkFiles(dir)
			forgetCheck(dlID)
			return
		case "FAILED":
			bad("it failed: %s", st.Error)
			forgetCheck(dlID)
			return
		default:
			bad("state %q is not QUEUED, DOWNLOADING, PAUSED, FINISHED or FAILED", st.State)
			return
		}
		if time.Now().After(deadline) {
			bad("not finished after %s", *timeout)
			forgetCheck(dlID)
			return
		}
		time.Sleep(3 * time.Second)
	}
}

var audio = []string{".m4b", ".m4a", ".mp4", ".aac", ".mp3", ".ogg", ".oga", ".opus", ".flac", ".wav", ".wma", ".mka"}
var alsoKept = []string{".jpg", ".jpeg", ".png", ".webp", ".cue", ".nfo", ".opf", ".txt", ".json"}

// checkFiles says what the server would take into the library, as it would.
func checkFiles(dir string) {
	var sounds, kept, left int
	var size int64
	filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		ext := strings.ToLower(filepath.Ext(path))
		switch {
		case strings.HasPrefix(e.Name(), "."):
			warn("%s is hidden; the server leaves it out", rel)
		case e.Type()&fs.ModeSymlink != 0:
			bad("%s is a link; the server leaves it out", rel)
			left++
		case e.IsDir():
			if strings.Count(filepath.ToSlash(rel), "/") >= 2 {
				bad("%s is more than two folders deep; the server leaves it out", rel)
				return fs.SkipDir
			}
		case slices.Contains(audio, ext):
			sounds++
			kept++
		case slices.Contains(alsoKept, ext):
			kept++
		default:
			warn("%s would be left out (not audio, a picture or a file the scanner reads)", rel)
			left++
		}
		if fi, err := e.Info(); err == nil && !e.IsDir() {
			size += fi.Size()
			if fi.Mode().Perm()&0o044 == 0 {
				bad("%s can't be read by others; the server runs as another user", rel)
			}
		}
		return nil
	})
	if sounds == 0 {
		bad("no audio files in %s; the server fails such a download", dir)
		return
	}
	pass("%d file(s) the server would take (%d audio, %d MB), %d left out", kept, sounds, size>>20, left)
}

func forgetCheck(id string) {
	if _, err := call("DELETE", "/kithara/downloads/"+url.PathEscape(id), *secret, nil, nil); err != nil {
		bad("forgetting it: %v", err)
		return
	}
	pass("forgot it when asked")
}
