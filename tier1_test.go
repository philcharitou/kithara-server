package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ------------------------------------------------------------------ sign-in limits

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 3; i++ {
		if l.retryAfter("k", now) != 0 {
			t.Fatalf("blocked after %d failures", i)
		}
		l.fail("k", now.Add(time.Duration(i)*time.Second))
	}
	if wait := l.retryAfter("k", now.Add(3*time.Second)); wait <= 0 || wait > time.Minute {
		t.Fatalf("not blocked after 3 failures (wait %s)", wait)
	}
	if l.retryAfter("other", now) != 0 {
		t.Fatal("one key's failures blocked another")
	}
	// The oldest failure ages out and one more try is allowed.
	if l.retryAfter("k", now.Add(time.Minute)) != 0 {
		t.Fatal("still blocked once the window passed")
	}
	// A right password takes its attempt back.
	l.forget("k", now.Add(2*time.Second))
	if l.retryAfter("k", now.Add(3*time.Second)) != 0 {
		t.Fatal("forget did not take the attempt back")
	}
}

func TestLoginThrottledOverHTTP(t *testing.T) {
	data := t.TempDir()
	st, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setPassword("phil", "correct horse"); err != nil {
		t.Fatal(err)
	}
	srv := newServer(st, newLibrary(t.TempDir(), data), "Test")
	srv.ipLimit = newLoginLimiter(2, time.Hour)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	for i := 0; i < 2; i++ {
		if res, _ := call(t, ts, "POST", "/api/auth", "", map[string]string{"username": "phil", "password": "wrong"}); res.StatusCode != 401 {
			t.Fatalf("wrong password %d gave %d", i, res.StatusCode)
		}
	}
	// Now even the right password waits, and says for how long.
	res, _ := call(t, ts, "POST", "/api/auth", "", map[string]string{"username": "phil", "password": "correct horse"})
	if res.StatusCode != 429 || res.Header.Get("Retry-After") == "" {
		t.Fatalf("throttled sign-in gave %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
}

// ------------------------------------------------------------------ metadata

func TestFolderMeta(t *testing.T) {
	idx := func(m bookMeta) float64 {
		if m.SeriesIndex == nil {
			return -1
		}
		return *m.SeriesIndex
	}
	cases := []struct {
		name                  string
		above                 []string
		title, author, series string
		index                 float64
		year, narrator        string
	}{
		{"The Hobbit", nil, "The Hobbit", "", "", -1, "", ""},
		{"The Hobbit", []string{"Tolkien"}, "The Hobbit", "Tolkien", "", -1, "", ""},
		{"1 - The Fellowship {Rob Inglis}", []string{"Tolkien", "The Lord of the Rings"}, "The Fellowship", "Tolkien", "The Lord of the Rings", 1, "", "Rob Inglis"},
		{"(1954) Book 2 - The Two Towers", []string{"Fantasy", "Tolkien", "LOTR"}, "The Two Towers", "Tolkien", "LOTR", 2, "1954", ""},
		{"1984", nil, "1984", "", "", -1, "", ""},
		{"2.5. Interlude", nil, "Interlude", "", "", 2.5, "", ""},
	}
	for _, c := range cases {
		m := folderMeta(c.name, c.above)
		if m.Title != c.title || m.Author != c.author || m.Series != c.series || idx(m) != c.index || m.PublishedYear != c.year || m.Narrator != c.narrator {
			t.Errorf("%q %v: got %+v (index %v)", c.name, c.above, m, idx(m))
		}
	}
}

func TestParseOPF(t *testing.T) {
	opf := `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>The Fellowship of the Ring</dc:title>
    <dc:creator opf:role="aut">J. R. R. Tolkien</dc:creator>
    <dc:creator opf:role="nrt">Rob Inglis</dc:creator>
    <dc:description>&lt;p&gt;The first part.&lt;/p&gt;</dc:description>
    <dc:publisher>Recorded Books</dc:publisher>
    <dc:date>1954-07-29</dc:date>
    <dc:language>en</dc:language>
    <dc:subject>Fantasy</dc:subject>
    <dc:identifier opf:scheme="ISBN">9780618260515</dc:identifier>
    <dc:identifier opf:scheme="ASIN">B007978NPG</dc:identifier>
    <meta name="calibre:series" content="The Lord of the Rings"/>
    <meta name="calibre:series_index" content="1"/>
  </metadata>
</package>`
	m := parseOPF([]byte(opf))
	if m.Title != "The Fellowship of the Ring" || m.Author != "J. R. R. Tolkien" || m.Narrator != "Rob Inglis" ||
		m.Description != "The first part." || m.Publisher != "Recorded Books" || m.PublishedYear != "1954" ||
		m.Language != "en" || len(m.Genres) != 1 || m.ISBN != "9780618260515" || m.ASIN != "B007978NPG" ||
		m.Series != "The Lord of the Rings" || m.SeriesIndex == nil || *m.SeriesIndex != 1 {
		t.Fatalf("opf: %+v", m)
	}
}

func TestSidecarMetadataInCatalogue(t *testing.T) {
	root := testLibrary(t)
	salt := filepath.Join(root, "Jonah Kessler/Salt and Starlight")
	os.WriteFile(filepath.Join(salt, "desc.txt"), []byte("From desc.txt"), 0o644)
	os.WriteFile(filepath.Join(salt, "metadata.json"), []byte(`{
		"title": "Salt & Starlight", "subtitle": "A Novel", "authors": ["Jonah Kessler", "A. N. Other"],
		"narrators": ["Mara Quill"], "series": ["Tides #2"], "genres": ["Literary"], "publishedYear": "2019",
		"asin": "B0TEST", "chapters": [{"start": 0, "end": 1.5, "title": "Low tide"}, {"start": 1.5, "end": 99, "title": "High tide"}]
	}`), 0o644)
	lib := newLibrary(root, t.TempDir())
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]Book{}
	for _, b := range lib.snapshot().Books {
		byTitle[b.Title] = b
	}
	b, ok := byTitle["Salt & Starlight"]
	if !ok {
		t.Fatalf("metadata.json title not used: %v", keys(byTitle))
	}
	if b.Subtitle != "A Novel" || b.Author != "Jonah Kessler, A. N. Other" || b.Narrator != "Mara Quill" ||
		b.Series != "Tides" || b.SeriesIndex == nil || *b.SeriesIndex != 2 || b.PublishedYear != "2019" ||
		b.ASIN != "B0TEST" || len(b.Genres) != 1 || b.Description != "From desc.txt" {
		t.Fatalf("book: %+v", b)
	}
	if len(b.Chapters) != 2 || b.Chapters[0].Title != "Low tide" || b.Chapters[1].EndMs != b.DurationMs {
		t.Fatalf("chapters from metadata.json, clamped to %d: %+v", b.DurationMs, b.Chapters)
	}
	// With no tags and no metadata files, the folder above names the author.
	if a := byTitle["The Cartographer's Daughter"].Author; a != "Elena Marchetti" {
		t.Fatalf("author from folder: %q", a)
	}
}

// ------------------------------------------------------------------ libraries

func TestMultipleLibraries(t *testing.T) {
	data := t.TempDir()
	books, kids := testLibrary(t), t.TempDir()
	src := sampleAudio(t)
	audio, _ := os.ReadFile(src)
	os.MkdirAll(filepath.Join(kids, "Gruffalo"), 0o755)
	os.WriteFile(filepath.Join(kids, "Gruffalo", "gruffalo.m4b"), audio, 0o644)

	st, _ := openStore(data)
	st.setPassword("phil", "correct horse")
	lib := newLibraries([]libSpec{{Path: books}, {Name: "Kids", Path: kids}}, data)
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(newServer(st, lib, "Test"))
	t.Cleanup(ts.Close)
	token := login(t, ts)

	_, body := call(t, ts, "GET", "/api/library", token, nil)
	var all Catalogue
	json.Unmarshal(body, &all)
	if len(all.Books) != 5 {
		t.Fatalf("want 5 books across both, got %d", len(all.Books))
	}
	_, body = call(t, ts, "GET", "/api/library?library=kids", token, nil)
	var only Catalogue
	json.Unmarshal(body, &only)
	if len(only.Books) != 1 || only.Books[0].Library != "Kids" || only.Books[0].Title != "Gruffalo" {
		t.Fatalf("kids only: %s", body)
	}
	if res, _ := call(t, ts, "GET", "/api/library?library=nope", token, nil); res.StatusCode != 404 {
		t.Fatalf("unknown library gave %d", res.StatusCode)
	}
	_, body = call(t, ts, "GET", "/api/libraries", token, nil)
	var libs struct{ Libraries []LibraryInfo }
	json.Unmarshal(body, &libs)
	if len(libs.Libraries) != 2 || libs.Libraries[0].Books != 4 || libs.Libraries[1].Name != "Kids" || libs.Libraries[1].Books != 1 {
		t.Fatalf("libraries: %s", body)
	}

	// A missing folder keeps its books rather than emptying the catalogue.
	os.RemoveAll(kids)
	if err := lib.rescan(); err == nil || !strings.Contains(err.Error(), "Kids") {
		t.Fatalf("missing folder: err %v", err)
	}
	if lib.count() != 5 {
		t.Fatalf("books of an unreadable folder dropped: %d left", lib.count())
	}
}

func TestLegacyIDsCarryOver(t *testing.T) {
	data, root := t.TempDir(), testLibrary(t)
	// A single-folder server from before, whose ids live only in ids.json.
	old := newLibrary(root, data)
	if err := old.rescan(); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, b := range old.snapshot().Books {
		before[b.Title] = b.ID
	}
	ids := map[string]string{}
	readJSON(old.roots[0].idsPath, &ids)
	os.Remove(old.roots[0].idsPath)
	writeJSON(filepath.Join(data, "ids.json"), ids)
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == sidecarName {
			os.Remove(p)
		}
		return nil
	})
	again := newLibraries([]libSpec{{Name: "Audiobooks", Path: root}}, data)
	if err := again.rescan(); err != nil {
		t.Fatal(err)
	}
	for _, b := range again.snapshot().Books {
		if before[b.Title] != b.ID {
			t.Fatalf("%q lost its id moving to named libraries", b.Title)
		}
	}
}

func TestParseLibSpec(t *testing.T) {
	for in, want := range map[string]libSpec{
		"/srv/books":        {Path: "/srv/books"},
		"Kids=/srv/kids":    {Name: "Kids", Path: "/srv/kids"},
		`C:\Books`:          {Path: `C:\Books`},
		"/srv/a=b":          {Path: "/srv/a=b"},
		`Talks=D:\Lectures`: {Name: "Talks", Path: `D:\Lectures`},
	} {
		if got := parseLibSpec(in); got != want {
			t.Errorf("%q: got %+v", in, got)
		}
	}
}

// ------------------------------------------------------------------ listening

func TestListenedEstimate(t *testing.T) {
	minute := int64(60_000)
	p := func(pos, at int64, finished bool) Progress {
		return Progress{PositionMs: pos, UpdatedAt: at, Finished: finished}
	}
	two := 2.0
	fast := Progress{PositionMs: 40 * minute, UpdatedAt: 60 * minute, Speed: &two}
	for name, c := range map[string]struct {
		prev, next Progress
		want       int64
	}{
		"plain listening":            {p(0, 0, false), p(20*minute, 30*minute, false), 20 * minute},
		"at 2x takes half the time":  {p(0, 0, false), fast, 20 * minute},
		"a seek far ahead":           {p(0, 0, false), p(5*60*minute, 10*minute, false), 0},
		"rewinding":                  {p(30*minute, 0, false), p(10*minute, 5*minute, false), 0},
		"marked finished days later": {p(60*minute, 0, false), p(10*60*minute, 3*24*60*minute, true), 0},
		"listened to the end":        {p(60*minute, 0, false), p(75*minute, 20*minute, true), 15 * minute},
		"a stretch is capped":        {p(0, 0, false), p(8*60*minute, 9*60*minute, false), 3 * 60 * minute},
	} {
		if got := listened(c.prev, c.next); got != c.want {
			t.Errorf("%s: got %d minute, want %d", name, got/minute, c.want/minute)
		}
	}
}

func TestSessionsAndStats(t *testing.T) {
	ts, _ := newTestServer(t)
	token := login(t, ts)
	_, body := call(t, ts, "GET", "/api/library", token, nil)
	var cat Catalogue
	json.Unmarshal(body, &cat)
	id := cat.Books[0].ID

	// Three pauses: 1s in, 2s in (straight on: same session), then finished an hour later.
	t0 := time.Date(2026, 10, 27, 21, 0, 0, 0, time.UTC).UnixMilli()
	put := func(pos, at int64, finished bool) Progress {
		t.Helper()
		res, body := call(t, ts, "PUT", "/api/progress/"+id, token, Progress{PositionMs: pos, UpdatedAt: at, Finished: finished})
		if res.StatusCode != 200 {
			t.Fatalf("put: %d %s", res.StatusCode, body)
		}
		var p Progress
		json.Unmarshal(body, &p)
		return p
	}
	first := put(0, t0, false)
	if first.StartedAt != 0 {
		t.Fatalf("not started at position 0: %+v", first)
	}
	put(1000, t0+1000, false)
	p := put(2000, t0+2000, false)
	if p.StartedAt != t0+1000 || p.FinishedAt != 0 {
		t.Fatalf("startedAt: %+v", p)
	}
	end := put(cat.Books[0].DurationMs, t0+3_600_000, true)
	if end.FinishedAt != t0+3_600_000 || end.StartedAt != t0+1000 {
		t.Fatalf("finishedAt: %+v", end)
	}

	_, body = call(t, ts, "GET", "/api/sessions", token, nil)
	var sess struct{ Sessions []ListeningSession }
	json.Unmarshal(body, &sess)
	if len(sess.Sessions) != 2 || sess.Sessions[0].ListenedMs != 2000 || sess.Sessions[0].Device != "test" {
		t.Fatalf("sessions: %s", body)
	}

	// 21:00 UTC is the next day in Tokyo.
	_, body = call(t, ts, "GET", "/api/stats?tzOffset=540", token, nil)
	var st Stats
	json.Unmarshal(body, &st)
	if st.TotalListenedMs != sess.Sessions[0].ListenedMs+sess.Sessions[1].ListenedMs || st.BooksFinished != 1 ||
		len(st.Days) != 1 || st.Days[0].Date != "2026-10-28" || len(st.Books) != 1 || st.Books[0].FinishedAt == 0 {
		t.Fatalf("stats: %s", body)
	}
	if res, _ := call(t, ts, "GET", "/api/stats?tzOffset=9999", token, nil); res.StatusCode != 400 {
		t.Fatalf("bad tzOffset gave %d", res.StatusCode)
	}
}

func TestStreaks(t *testing.T) {
	day := func(d string) ListeningSession {
		at, _ := time.Parse(time.DateOnly, d)
		return ListeningSession{BookID: "b", StartedAt: at.Add(12 * time.Hour).UnixMilli(), EndedAt: at.Add(13 * time.Hour).UnixMilli(), ListenedMs: 1000}
	}
	sessions := []ListeningSession{day("2026-10-01"), day("2026-10-02"), day("2026-10-03"), day("2026-10-20"), day("2026-10-21")}
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	st := buildStats(sessions, nil, 0, now)
	if st.DaysListened != 5 || st.LongestStreakDays != 3 || st.CurrentStreakDays != 2 {
		t.Fatalf("streaks: %+v", st)
	}
	if st := buildStats(sessions, nil, 0, now.Add(48*time.Hour)); st.CurrentStreakDays != 0 {
		t.Fatalf("a lapsed streak is still current: %d", st.CurrentStreakDays)
	}
}

// A book folder holding one file named differently from the folder is a book folder:
// its folder is the book (not a series), it gets a sidecar, and an id recorded for it
// by the file's path before 0.4.1 carries over.
func TestOneFileBookFolder(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	audio, _ := os.ReadFile(sampleAudio(t))
	dir := filepath.Join(root, "J. R. R. Tolkien", "The Hobbit")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "hobbit.m4b"), audio, 0o644)
	os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("\xff\xd8\xff\xd9"), 0o644)
	// What an older version recorded: the id under the file's path.
	writeJSON(filepath.Join(data, "ids.json"), map[string]string{"J. R. R. Tolkien/The Hobbit/hobbit.m4b": "oldhobbit01"})

	lib := newLibrary(root, data)
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	books := lib.snapshot().Books
	if len(books) != 1 {
		t.Fatalf("want 1 book, got %d", len(books))
	}
	b := books[0]
	if b.Title != "The Hobbit" || b.Author != "J. R. R. Tolkien" || b.Series != "" {
		t.Fatalf("book: title %q author %q series %q", b.Title, b.Author, b.Series)
	}
	if b.ID != "oldhobbit01" {
		t.Fatalf("id %q, want the one recorded by file path", b.ID)
	}
	if side, _ := os.ReadFile(filepath.Join(dir, sidecarName)); strings.TrimSpace(string(side)) != "oldhobbit01" {
		t.Fatalf("sidecar holds %q", side)
	}
	if b.CoverURL == "" || !strings.HasSuffix(b.coverPath, "cover.jpg") {
		t.Fatalf("folder cover not used: %q", b.coverPath)
	}
}
