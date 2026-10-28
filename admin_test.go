package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A server over two libraries with an admin (phil, the first user) and a child who
// sees only "Kids".
func twoLibraryServer(t *testing.T) (*httptest.Server, *server, *store, string) {
	t.Helper()
	data := t.TempDir()
	books, kids := testLibrary(t), t.TempDir()
	audio, _ := os.ReadFile(sampleAudio(t))
	os.MkdirAll(filepath.Join(kids, "Gruffalo"), 0o755)
	os.WriteFile(filepath.Join(kids, "Gruffalo", "gruffalo.m4b"), audio, 0o644)
	st, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	st.setPassword("phil", "correct horse")
	st.setPassword("kid", "battery staple")
	st.setLibraries("kid", []string{"kids"})
	lib := newLibraries([]libSpec{{Name: "Audiobooks", Path: books}, {Name: "Kids", Path: kids}}, data)
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	srv := newServer(st, lib, "Test")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv, st, data
}

func loginAs(t *testing.T, ts *httptest.Server, user, password, device string) string {
	t.Helper()
	res, body := call(t, ts, "POST", "/api/auth", "", map[string]string{"username": user, "password": password, "device": device})
	if res.StatusCode != 200 {
		t.Fatalf("auth %s: %d %s", user, res.StatusCode, body)
	}
	var out struct{ Token, Role string }
	json.Unmarshal(body, &out)
	return out.Token
}

func catalogueOf(t *testing.T, ts *httptest.Server, token, query string) Catalogue {
	t.Helper()
	res, body := call(t, ts, "GET", "/api/library"+query, token, nil)
	if res.StatusCode != 200 {
		t.Fatalf("library%s: %d %s", query, res.StatusCode, body)
	}
	var cat Catalogue
	json.Unmarshal(body, &cat)
	return cat
}

func TestRolesAndLibraryAccess(t *testing.T) {
	ts, _, st, _ := twoLibraryServer(t)
	if a, _ := st.account("phil"); !a.admin() {
		t.Fatal("the first user is not an admin")
	}
	if a, _ := st.account("kid"); a.admin() {
		t.Fatal("a later user is an admin")
	}
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")

	all := catalogueOf(t, ts, admin, "")
	if len(all.Books) != 5 {
		t.Fatalf("admin sees %d books, want 5", len(all.Books))
	}
	mine := catalogueOf(t, ts, kid, "")
	if len(mine.Books) != 1 || mine.Books[0].Library != "Kids" {
		t.Fatalf("kid sees %+v", mine.Books)
	}
	if res, _ := call(t, ts, "GET", "/api/library?library=Audiobooks", kid, nil); res.StatusCode != 404 {
		t.Fatalf("kid asking for a hidden library gave %d", res.StatusCode)
	}
	_, body := call(t, ts, "GET", "/api/libraries", kid, nil)
	if !strings.Contains(string(body), `"Kids"`) || strings.Contains(string(body), "Audiobooks") {
		t.Fatalf("kid's libraries: %s", body)
	}

	var hidden Book
	for _, b := range all.Books {
		if b.Library == "Audiobooks" {
			hidden = b
			break
		}
	}
	for _, c := range []struct{ method, path string }{
		{"GET", hidden.Files[0].URL},
		{"PUT", "/api/progress/" + hidden.ID},
		{"POST", "/api/bookmarks/" + hidden.ID},
		{"GET", "/api/books/" + hidden.ID + "/download"},
	} {
		if res, _ := call(t, ts, c.method, c.path, kid, Progress{PositionMs: 1}); res.StatusCode != 404 {
			t.Errorf("kid %s %s gave %d, want 404", c.method, c.path, res.StatusCode)
		}
	}

	for _, c := range []struct{ method, path string }{
		{"POST", "/api/rescan"}, {"GET", "/api/users"}, {"GET", "/api/books/" + mine.Books[0].ID + "/match"},
		{"DELETE", "/api/books/" + mine.Books[0].ID + "/metadata"}, {"GET", "/api/devices?user=phil"},
	} {
		if res, _ := call(t, ts, c.method, c.path, kid, nil); res.StatusCode != 403 {
			t.Errorf("kid %s %s gave %d, want 403", c.method, c.path, res.StatusCode)
		}
	}
	if res, _ := call(t, ts, "POST", "/api/rescan", admin, nil); res.StatusCode != 200 {
		t.Fatalf("admin rescan gave %d", res.StatusCode)
	}

	// The last admin stays an admin.
	if err := st.setRole("phil", roleUser); err != errLastAdmin {
		t.Fatalf("demoting the last admin: %v", err)
	}
	if err := st.removeUser("phil"); err != errLastAdmin {
		t.Fatalf("removing the last admin: %v", err)
	}
}

func TestOldUsersBecomeAdmins(t *testing.T) {
	data := t.TempDir()
	writeJSON(filepath.Join(data, "users.json"), map[string]map[string]any{
		"phil": {"salt": "00", "hash": "00", "iter": 1}, "anna": {"salt": "00", "hash": "00", "iter": 1},
	})
	st, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range st.accounts() {
		if !a.admin() {
			t.Fatalf("%s from an older users.json is %s", a.Name, a.Role)
		}
	}
	// Only once: from here on, roles are as set.
	st.setRole("anna", roleUser)
	again, _ := openStore(data)
	if a, _ := again.account("anna"); a.admin() {
		t.Fatal("migration ran again")
	}
}

func TestUserAPI(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")

	res, body := call(t, ts, "POST", "/api/users", admin, map[string]any{"username": "anna", "password": "longenough", "libraries": []string{"Kids"}})
	if res.StatusCode != 201 {
		t.Fatalf("create: %d %s", res.StatusCode, body)
	}
	if res, _ := call(t, ts, "POST", "/api/users", admin, map[string]any{"username": "anna", "password": "longenough"}); res.StatusCode != 409 {
		t.Fatalf("duplicate create gave %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "PATCH", "/api/users/anna", admin, map[string]any{"libraries": []string{"Nope"}}); res.StatusCode != 400 {
		t.Fatalf("unknown library gave %d", res.StatusCode)
	}
	anna := loginAs(t, ts, "anna", "longenough", "phone")
	if n := len(catalogueOf(t, ts, anna, "").Books); n != 1 {
		t.Fatalf("anna sees %d books", n)
	}
	// Widening her to all libraries shows on her next sync.
	res, body = call(t, ts, "PATCH", "/api/users/anna", admin, map[string]any{"libraries": []string{}, "role": "admin"})
	var a Account
	json.Unmarshal(body, &a)
	if res.StatusCode != 200 || a.Role != "admin" || len(a.Libraries) != 0 {
		t.Fatalf("patch: %d %s", res.StatusCode, body)
	}
	if n := len(catalogueOf(t, ts, anna, "").Books); n != 5 {
		t.Fatalf("anna sees %d books after the change", n)
	}
	_, body = call(t, ts, "GET", "/api/me", anna, nil)
	if !strings.Contains(string(body), `"role":"admin"`) {
		t.Fatalf("me: %s", body)
	}
	_, body = call(t, ts, "GET", "/api/users", admin, nil)
	var list struct{ Users []Account }
	json.Unmarshal(body, &list)
	if len(list.Users) != 3 {
		t.Fatalf("users: %s", body)
	}
	if res, _ := call(t, ts, "DELETE", "/api/users/anna", admin, nil); res.StatusCode != 204 {
		t.Fatalf("delete gave %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "GET", "/api/library", anna, nil); res.StatusCode != 401 {
		t.Fatalf("a removed user's token gave %d", res.StatusCode)
	}
}

func TestDevices(t *testing.T) {
	ts, _, _, data := twoLibraryServer(t)
	phone := loginAs(t, ts, "phil", "correct horse", "Pixel 9")
	laptop := loginAs(t, ts, "phil", "correct horse", "MacBook")

	_, body := call(t, ts, "GET", "/api/devices", phone, nil)
	var out struct{ Devices []Device }
	json.Unmarshal(body, &out)
	if len(out.Devices) != 2 {
		t.Fatalf("devices: %s", body)
	}
	var laptopID string
	for _, d := range out.Devices {
		if d.Current != (d.Name == "Pixel 9") {
			t.Fatalf("current marked wrong: %+v", d)
		}
		if d.Name == "MacBook" {
			laptopID = d.ID
		}
		if strings.Contains(string(body), phone) || strings.Contains(string(body), laptop) {
			t.Fatal("a token was given out in the device list")
		}
	}
	if res, _ := call(t, ts, "DELETE", "/api/devices/"+laptopID, phone, nil); res.StatusCode != 204 {
		t.Fatalf("revoke gave %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "GET", "/api/library", laptop, nil); res.StatusCode != 401 {
		t.Fatalf("revoked device gave %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "DELETE", "/api/devices/"+laptopID, phone, nil); res.StatusCode != 404 {
		t.Fatalf("second revoke gave %d", res.StatusCode)
	}
	// An admin can see and sign out someone else's devices.
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	_, body = call(t, ts, "GET", "/api/devices?user=kid", phone, nil)
	if !strings.Contains(string(body), "tablet") {
		t.Fatalf("admin view of kid's devices: %s", body)
	}

	// The command line works on the same files while the server runs: signing the kid
	// out there signs them out here.
	cli, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := cli.revokeDevices("kid", ""); err != nil || n != 1 {
		t.Fatalf("cli signout: %d %v", n, err)
	}
	if res, _ := call(t, ts, "GET", "/api/library", kid, nil); res.StatusCode != 401 {
		t.Fatalf("token signed out from the command line still works: %d", res.StatusCode)
	}
	// And a user added there can sign in here without a restart.
	if err := cli.setPassword("anna", "longenough"); err != nil {
		t.Fatal(err)
	}
	loginAs(t, ts, "anna", "longenough", "phone")
}

func TestDownloadZip(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	token := loginAs(t, ts, "phil", "correct horse", "laptop")
	byTitle := map[string]Book{}
	for _, b := range catalogueOf(t, ts, token, "").Books {
		byTitle[b.Title] = b
	}
	check := func(title string, want ...string) {
		t.Helper()
		b := byTitle[title]
		res, body := call(t, ts, "GET", "/api/books/"+b.ID+"/download", token, nil)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" ||
			!strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
			t.Fatalf("%s: %d %v", title, res.StatusCode, res.Header)
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		var names []string
		var audio int64
		for _, f := range zr.File {
			names = append(names, f.Name)
			if f.Method != zip.Store {
				t.Errorf("%s compressed", f.Name)
			}
			if !strings.HasPrefix(f.Name, "cover") {
				audio += int64(f.UncompressedSize64)
			}
		}
		sort.Strings(names)
		if strings.Join(names, "|") != strings.Join(want, "|") {
			t.Fatalf("%s: zip holds %v, want %v", title, names, want)
		}
		var size int64
		for _, f := range b.Files {
			size += f.SizeBytes
		}
		if audio != size {
			t.Fatalf("%s: %d bytes of audio in the zip, %d in the book", title, audio, size)
		}
	}
	check("Winter Counting", "CD 1/track 01.m4b", "CD 2/track 01.m4b")
	check("Salt and Starlight", "cover.jpg", "part 1.m4b", "part 2.m4b")
	check("Loose Book", "Loose Book.m4b")
}

// fakeProviders answers like Audible, Open Library and Google Books would.
func fakeProviders(t *testing.T) *matcher {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/audible/1.0/catalog/products", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("title") == "" || !strings.Contains(r.URL.Query().Get("response_groups"), "series") {
			t.Errorf("audible query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"products":[{"asin":"B0STAR","title":"Salt and Starlight","subtitle":"A Novel",
			"authors":[{"name":"Jonah Kessler"}],"narrators":[{"name":"Mara Quill"}],"publisher_name":"Tidewater",
			"publisher_summary":"<p>Two sisters &amp; a lighthouse.</p>","release_date":"2019-05-01","language":"english",
			"runtime_length_min":600,"series":[{"title":"Tides","sequence":"2"}],
			"product_images":{"500":"https://m.media-amazon.com/images/I/x.jpg"},
			"category_ladders":[{"ladder":[{"name":"Literature & Fiction"},{"name":"Literary"}]}]}]}`))
	})
	mux.HandleFunc("/audible/1.0/catalog/products/B0STAR", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"product":{"asin":"B0STAR","title":"Salt and Starlight"}}`))
	})
	mux.HandleFunc("/openlibrary", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"docs":[{"title":"Salt and Starlight","author_name":["Jonah Kessler"],"first_publish_year":2018,
			"publisher":["Tidewater"],"isbn":["9781234567897"],"cover_i":42}]}`))
	})
	mux.HandleFunc("/google", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), "intitle:Salt") {
			t.Errorf("google query: %s", r.URL.RawQuery)
		}
		w.Write([]byte(`{"items":[{"volumeInfo":{"title":"Salt and Starlight","authors":["Jonah Kessler"],
			"publishedDate":"2018-03","industryIdentifiers":[{"type":"ISBN_10","identifier":"1234567890"},{"type":"ISBN_13","identifier":"9781234567897"}],
			"imageLinks":{"thumbnail":"http://books.google.com/books/content?id=1"}}}]}`))
	})
	fake := httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	m := newMatcher()
	m.audibleURL = func(string) string { return fake.URL + "/audible/1.0/catalog/products" }
	m.openLibrary, m.google = fake.URL+"/openlibrary", fake.URL+"/google"
	return m
}

func TestProviders(t *testing.T) {
	m := fakeProviders(t)
	res, err := m.search(MatchQuery{Title: "Salt and Starlight", Author: "Jonah Kessler"})
	if err != nil || len(res) != 1 {
		t.Fatalf("audible: %v %+v", err, res)
	}
	a := res[0]
	if a.ASIN != "B0STAR" || a.Narrators[0] != "Mara Quill" || a.Series != "Tides" || *a.SeriesIndex != 2 ||
		a.Description != "Two sisters & a lighthouse." || a.PublishedYear != "2019" || len(a.Genres) != 2 ||
		a.DurationMs != 600*60_000 || a.CoverURL == "" {
		t.Fatalf("audible result: %+v", a)
	}
	if res, err := m.search(MatchQuery{ASIN: "B0STAR"}); err != nil || len(res) != 1 {
		t.Fatalf("audible by asin: %v %+v", err, res)
	}
	res, err = m.search(MatchQuery{Provider: "openlibrary", Title: "Salt and Starlight"})
	if err != nil || len(res) != 1 || res[0].ISBN != "9781234567897" || res[0].CoverURL != "https://covers.openlibrary.org/b/id/42-L.jpg" {
		t.Fatalf("openlibrary: %v %+v", err, res)
	}
	res, err = m.search(MatchQuery{Provider: "google", Title: "Salt and Starlight"})
	if err != nil || len(res) != 1 || res[0].ISBN != "9781234567897" || !strings.HasPrefix(res[0].CoverURL, "https://") {
		t.Fatalf("google: %v %+v", err, res)
	}
	if _, err := m.search(MatchQuery{Provider: "nope", Title: "x"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := m.search(MatchQuery{Region: "zz", Title: "x"}); err == nil {
		t.Fatal("unknown region accepted")
	}
}

func TestMatchAndKeepMetadata(t *testing.T) {
	ts, srv, _, data := twoLibraryServer(t)
	srv.match = fakeProviders(t)
	token := loginAs(t, ts, "phil", "correct horse", "laptop")
	var salt Book
	for _, b := range catalogueOf(t, ts, token, "").Books {
		if b.Title == "Salt and Starlight" {
			salt = b
		}
	}

	res, body := call(t, ts, "GET", "/api/books/"+salt.ID+"/match", token, nil)
	var found struct{ Results []MatchCandidate }
	json.Unmarshal(body, &found)
	if res.StatusCode != 200 || len(found.Results) != 1 {
		t.Fatalf("match: %d %s", res.StatusCode, body)
	}
	pick := found.Results[0]
	pick.CoverURL = "" // not fetched in tests; see TestCoverHosts
	res, body = call(t, ts, "PUT", "/api/books/"+salt.ID+"/metadata", token, pick)
	var b Book
	json.Unmarshal(body, &b)
	if res.StatusCode != 200 || b.Subtitle != "A Novel" || b.Narrator != "Mara Quill" || b.Series != "Tides" || b.ASIN != "B0STAR" {
		t.Fatalf("keep: %d %s", res.StatusCode, body)
	}
	if b.UpdatedAt <= salt.UpdatedAt {
		t.Fatal("updatedAt did not move, so devices would not pick the change up")
	}
	if _, err := os.Stat(overridePath(data, salt.ID)); err != nil {
		t.Fatalf("not kept in the data folder: %v", err)
	}
	// Hand edits work the same way, and replace what was kept.
	_, body = call(t, ts, "PUT", "/api/books/"+salt.ID+"/metadata", token, MatchCandidate{Title: "Salt & Starlight"})
	b = Book{}
	json.Unmarshal(body, &b)
	if b.Title != "Salt & Starlight" || b.Narrator != "" {
		t.Fatalf("hand edit: %s", body)
	}
	if res, _ := call(t, ts, "PUT", "/api/books/"+salt.ID+"/metadata", token, MatchCandidate{}); res.StatusCode != 400 {
		t.Fatalf("empty metadata gave %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "PUT", "/api/books/"+salt.ID+"/metadata", token, MatchCandidate{CoverURL: "http://192.168.1.1/admin"}); res.StatusCode != 502 {
		t.Fatalf("a cover from the local network gave %d", res.StatusCode)
	}
	_, body = call(t, ts, "DELETE", "/api/books/"+salt.ID+"/metadata", token, nil)
	b = Book{}
	json.Unmarshal(body, &b)
	if b.Subtitle != "" {
		t.Fatalf("kept metadata outlived clearing: %s", body)
	}
	if b.Title != "Salt and Starlight" {
		t.Fatalf("after clearing: %s", body)
	}
}

func TestCoverHosts(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://m.media-amazon.com/images/I/x.jpg":    true,
		"https://covers.openlibrary.org/b/id/42-L.jpg": true,
		"https://books.google.com/books/content?id=1":  true,
		"http://m.media-amazon.com/images/I/x.jpg":     false,
		"https://192.168.1.1/cover.jpg":                false,
		"https://evil-media-amazon.com/x.jpg":          false,
		"https://media-amazon.com.evil.example/x.jpg":  false,
		"https://localhost/x.jpg":                      false,
	} {
		u, _ := url.Parse(raw)
		if coverAllowed(u) != want {
			t.Errorf("%s: allowed %v", raw, !want)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	data := fs.String("data", "", "")
	admin := fs.Bool("admin", false, "")
	pos := splitArgs(fs, []string{"role", "--data", "/d", "phil", "--admin", "admin"})
	if strings.Join(pos, " ") != "role phil admin" || *data != "/d" || !*admin {
		t.Fatalf("got %v data=%q admin=%v", pos, *data, *admin)
	}
}

func TestWebPage(t *testing.T) {
	ts, _ := newTestServer(t)
	res, body := call(t, ts, "GET", "/", "", nil)
	if res.StatusCode != 200 || !strings.Contains(string(body), `<script type="module" src="app.js">`) ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("page: %d %v", res.StatusCode, res.Header)
	}
	if res, _ := call(t, ts, "GET", "/app.js", "", nil); res.StatusCode != 200 {
		t.Fatalf("app.js gave %d", res.StatusCode)
	}
	// Unknown API routes stay JSON errors, not the page.
	res, body = call(t, ts, "GET", "/api/nope", "", nil)
	if res.StatusCode != 404 || !strings.Contains(string(body), `"error"`) {
		t.Fatalf("unknown api route: %d %s", res.StatusCode, body)
	}
}

func TestDownloadLinks(t *testing.T) {
	ts, srv, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	books := catalogueOf(t, ts, admin, "").Books
	var gruffalo, other Book
	for _, b := range books {
		if b.Library == "Kids" {
			gruffalo = b
		} else {
			other = b
		}
	}
	res, body := call(t, ts, "POST", "/api/books/"+gruffalo.ID+"/download-link", kid, nil)
	var link struct {
		URL       string
		ExpiresAt int64
	}
	json.Unmarshal(body, &link)
	if res.StatusCode != 200 || link.URL == "" || strings.Contains(link.URL, kid) {
		t.Fatalf("link: %d %s", res.StatusCode, body)
	}
	if res, _ := call(t, ts, "GET", link.URL, "", nil); res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("link without a token gave %d", res.StatusCode)
	}
	for name, u := range map[string]string{
		"another book": strings.Replace(link.URL, gruffalo.ID, other.ID, 1),
		"another user": strings.Replace(link.URL, "u=kid", "u=phil", 1),
		"later expiry": strings.Replace(link.URL, "exp=", "exp=9", 1),
		"bad sig":      link.URL[:len(link.URL)-4] + "0000",
	} {
		if res, _ := call(t, ts, "GET", u, "", nil); res.StatusCode != 403 {
			t.Errorf("%s gave %d", name, res.StatusCode)
		}
	}
	// No links to books the user may not see; and a link outlives nothing.
	if res, _ := call(t, ts, "POST", "/api/books/"+other.ID+"/download-link", kid, nil); res.StatusCode != 404 {
		t.Fatalf("link to a hidden book gave %d", res.StatusCode)
	}
	exp := time.Now().Add(-time.Minute).Unix()
	q := url.Values{"u": {"kid"}, "exp": {strconv.FormatInt(exp, 10)}, "sig": {srv.linkSig("kid", gruffalo.ID, exp)}}
	if res, _ := call(t, ts, "GET", "/api/books/"+gruffalo.ID+"/download?"+q.Encode(), "", nil); res.StatusCode != 403 {
		t.Fatalf("expired link gave %d", res.StatusCode)
	}
}

func TestAudibleAuthor(t *testing.T) {
	for in, want := range map[string]string{
		"J. R. R. Tolkien":            "J. R. R. Tolkien",
		"Walter Scherf - Translator":  "",
		"Heinz-Dieter Köhler — Other": "",
		"Jane Doe - Author":           "Jane Doe",
		"Jean-Paul Sartre":            "Jean-Paul Sartre",
	} {
		got, ok := audibleAuthor(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q %v", in, got, ok)
		}
	}
}
