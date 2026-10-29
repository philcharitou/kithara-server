package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ------------------------------------------------------------------ chapter names

func TestSpokenHeadings(t *testing.T) {
	title := func(s string) string {
		if h := parseHeading(s); h != nil {
			return h.title()
		}
		return ""
	}
	for in, want := range map[string]string{
		"Chapter 23. Carl woke to the sound of sirens.":                              "Chapter 23",
		"chapter twenty-three":                                                       "Chapter 23",
		"Chapter twenty three, and the crawl went on":                                "Chapter 23",
		"Chapter forty-one":                                                          "Chapter 41",
		"Part the third":                                                             "Part 3",
		"Prologue. The dungeon opened on a Tuesday.":                                 "Prologue",
		"Interlude: the showrunners confer":                                          "Interlude",
		"Chapter twelve: The Fall.":                                                  "Chapter 12: The Fall",
		"Chapter 12. The fall took him by surprise and he did not get up.":           "Chapter 12",
		"Chapter 4 - Roast Mutton":                                                   "Chapter 4: Roast Mutton",
		"Soundbooth Theater. Chapter two.":                                           "Chapter 2",
		"Carl woke to the sound of sirens and a voice in his head.":                  "",
		"This was the final chapter of a long and tiring day for everyone involved.": "",
		"Chapter and verse, he knew the rules by heart.":                             "",
		"And so the crawlers waited out the interlude in silence.":                   "",
		"This has been A Parade of Horribles, book eight of Dungeon Crawler Carl.":   "",
		"Book two. The return.":                                                      "Book 2",
	} {
		if got := title(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The Android app's real-book test, on the same transcript: the opening words of the
// 107 marks of "A Parade of Horribles", whose file numbers them Chapter 1 to 107.
func TestHeardBook(t *testing.T) {
	data, err := os.ReadFile("testdata/heard-a-parade-of-horribles.txt")
	if err != nil {
		t.Fatal(err)
	}
	heard := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	existing := make([]string, len(heard))
	for i := range existing {
		existing[i] = "Chapter " + itoa(i+1)
	}
	resolved := resolveChapterNames(heard, existing)
	names := make([]string, len(heard))
	for i := range names {
		names[i] = existing[i]
		if resolved[i] != "" {
			names[i] = resolved[i]
		}
	}
	for i, want := range map[int]string{3: "Chapter 1", 4: "Chapter 2", 12: "Chapter 10", 102: "Chapter 98", 0: "Opening credits",
		1: "Before chapter 1", 54: "Interlude", 91: "Interlude", 103: "Interlude", 105: "Interlude", 106: "Closing credits"} {
		if names[i] != want {
			t.Errorf("mark %d: %q, want %q", i, names[i], want)
		}
	}
	re := regexp.MustCompile(`^Chapter (\d+)$`)
	var numbers []int
	for _, n := range names {
		if m := re.FindStringSubmatch(n); m != nil {
			numbers = append(numbers, atoi(m[1]))
		}
	}
	if !slices.IsSorted(numbers) || len(slices.Compact(slices.Clone(numbers))) != len(numbers) {
		t.Errorf("numbers out of order or repeated: %v", numbers)
	}
	if len(numbers) < 90 {
		t.Errorf("only %d numbered", len(numbers))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// ------------------------------------------------------------------ backups

func TestBackups(t *testing.T) {
	ts, _, _, data := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	res, body := call(t, ts, "POST", "/api/backups", admin, nil)
	var made BackupResult
	json.Unmarshal(body, &made)
	if res.StatusCode != 201 || made.File == "" {
		t.Fatalf("backup now: %d %s", res.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(data, "backups", made.File)); err != nil {
		t.Fatal(err)
	}
	res, body = call(t, ts, "GET", "/api/backups/"+made.File, admin, nil)
	if res.StatusCode != 200 || !strings.HasPrefix(string(body), "PK") {
		t.Fatalf("download: %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "GET", "/api/backups/..%2Fusers.json", admin, nil); res.StatusCode != 404 {
		t.Fatalf("a file outside the backups: %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "PUT", "/api/backups/settings", admin, BackupSettings{Daily: true, Hour: 24}); res.StatusCode != 400 {
		t.Fatalf("hour 24 accepted: %d", res.StatusCode)
	}
	other := t.TempDir()
	if res, body := call(t, ts, "PUT", "/api/backups/settings", admin, BackupSettings{Daily: true, Hour: 3, Keep: 2, Dir: other}); res.StatusCode != 200 {
		t.Fatalf("settings: %d %s", res.StatusCode, body)
	}
	call(t, ts, "POST", "/api/backups", admin, nil)
	var st struct {
		Dir   string
		Files []BackupFile
	}
	_, body = call(t, ts, "GET", "/api/backups", admin, nil)
	json.Unmarshal(body, &st)
	if st.Dir != other || len(st.Files) != 1 {
		t.Fatalf("status: %s", body)
	}
}

// ------------------------------------------------------------------ notifications

func TestHooks(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	var text []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Title") != "" {
			text = append(text, r.Header.Get("Title")+" | "+string(b))
			return
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		got = append(got, m)
	}))
	defer sink.Close()
	ts, srv, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	if res, _ := call(t, ts, "POST", "/api/hooks", admin, Hook{URL: "ftp://x"}); res.StatusCode != 400 {
		t.Fatalf("ftp hook accepted: %d", res.StatusCode)
	}
	_, body := call(t, ts, "POST", "/api/hooks", admin, Hook{URL: sink.URL, Events: []string{eventBookAdded}})
	var hk Hook
	json.Unmarshal(body, &hk)
	call(t, ts, "POST", "/api/hooks", admin, Hook{URL: sink.URL + "/ntfy", Format: "ntfy", Events: []string{eventBackupDone}})
	if res, _ := call(t, ts, "POST", "/api/hooks/"+hk.ID+"/test", admin, nil); res.StatusCode != 204 {
		t.Fatalf("test: %d", res.StatusCode)
	}
	// A new book, and a backup: one event each, to the hook that wants it.
	audio, _ := os.ReadFile(sampleAudio(t))
	dir := filepath.Join(srv.lib.roots[0].path, "Fresh Arrival")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "book.m4b"), audio, 0o644)
	call(t, ts, "POST", "/api/rescan", admin, nil)
	call(t, ts, "POST", "/api/backups", admin, nil)
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n, m := len(got), len(text)
		mu.Unlock()
		if n >= 2 && m >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0]["event"] != eventTest || got[1]["event"] != eventBookAdded || !strings.Contains(got[1]["message"].(string), "Fresh Arrival") {
		t.Fatalf("json hook got %v", got)
	}
	if len(text) != 1 || !strings.Contains(text[0], "backup.finished") {
		t.Fatalf("ntfy hook got %v", text)
	}
}

// ------------------------------------------------------------------ file jobs

func waitFileJobs(t *testing.T, srv *server) FileJobResult {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		srv.files.mu.Lock()
		idle := srv.files.running == nil && len(srv.files.queue) == 0 && len(srv.files.recent) > 0
		var last FileJobResult
		if idle {
			last = srv.files.recent[0]
		}
		srv.files.mu.Unlock()
		if idle {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("file job did not finish")
	return FileJobResult{}
}

func TestMergeKeepsTheBook(t *testing.T) {
	ts, srv, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	b := *srv.lib.book(bookNamed(t, catalogueOf(t, ts, admin, "").Books, "Salt and Starlight").ID)
	// A transcript and a position, both of which must survive.
	call(t, ts, "PUT", "/api/books/"+b.ID+"/transcript/chapters/0", admin, map[string]any{
		"status": statusDone, "model": "base.en", "passages": []Passage{{StartMs: 4000, EndMs: 4800, Text: "kept"}}})
	call(t, ts, "PUT", "/api/progress/"+b.ID, admin, Progress{PositionMs: 4000, UpdatedAt: time.Now().UnixMilli()})
	if res, body := call(t, ts, "POST", "/api/books/"+b.ID+"/files/merge", admin, nil); res.StatusCode != 202 {
		t.Fatalf("merge: %d %s", res.StatusCode, body)
	}
	if r := waitFileJobs(t, srv); !r.OK {
		t.Fatalf("merge failed: %s", r.Note)
	}
	nb := srv.lib.book(b.ID)
	if nb == nil || len(nb.Files) != 1 || !strings.HasSuffix(nb.Files[0].Name, ".m4b") {
		t.Fatalf("after the merge: %+v", nb)
	}
	if d := nb.DurationMs - b.DurationMs; d > 500 || d < -500 {
		t.Fatalf("length %d, was %d", nb.DurationMs, b.DurationMs)
	}
	// No chapter marks before: the parts become the chapters.
	if want := max(len(b.Chapters), len(b.Files)); len(nb.Chapters) != want {
		t.Fatalf("%d chapters, want %d", len(nb.Chapters), want)
	}
	for _, f := range b.Files {
		if _, err := os.Stat(filepath.Join(b.dir, ".originals", f.Name)); err != nil {
			t.Fatalf("original not kept: %v", err)
		}
	}
	var v TranscriptView
	_, body := call(t, ts, "GET", "/api/books/"+b.ID+"/transcript", admin, nil)
	json.Unmarshal(body, &v)
	if len(v.Passages) != 1 || v.Passages[0].Text != "kept" {
		t.Fatalf("transcript lost: %s", body)
	}
	if res, _ := call(t, ts, "POST", "/api/books/"+b.ID+"/files/merge", admin, nil); res.StatusCode != 400 {
		t.Fatalf("merging one file: %d", res.StatusCode)
	}
}

func TestWriteTags(t *testing.T) {
	ts, srv, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	b := bookNamed(t, catalogueOf(t, ts, admin, "").Books, "The Cartographer's Daughter")
	call(t, ts, "PUT", "/api/books/"+b.ID+"/metadata?rescan=0", admin, MatchCandidate{Title: "A Different Title", Authors: []string{"Ada Writer"}, Narrators: []string{"Nora Reader"}})
	call(t, ts, "POST", "/api/rescan", admin, nil)
	if res, _ := call(t, ts, "POST", "/api/books/"+b.ID+"/files/tags", admin, nil); res.StatusCode != 202 {
		t.Fatalf("tags: %d", res.StatusCode)
	}
	if r := waitFileJobs(t, srv); !r.OK {
		t.Fatalf("tags failed: %s", r.Note)
	}
	pr, err := ffprobe(srv.lib.book(b.ID).Files[0].path)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Tags["album"] != "A Different Title" || pr.Tags["artist"] != "Ada Writer" || pr.Tags["composer"] != "Nora Reader" {
		t.Fatalf("tags in the file: %v", pr.Tags)
	}
}
