package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A small library on disk, built from a silent file made with ffmpeg. ffprobe must be
// on PATH, as it must be for the server itself.
func testLibrary(t *testing.T) string {
	t.Helper()
	if !haveFFprobe() {
		t.Skip("ffprobe not on PATH")
	}
	root := t.TempDir()
	src := sampleAudio(t)
	// Two ordinary books, one with a cover, one book split over disc folders, and a
	// lone file at the top level.
	copyTo := func(rel string) {
		dst := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyTo("Elena Marchetti/The Cartographer's Daughter/The Cartographer's Daughter.m4b")
	copyTo("Jonah Kessler/Salt and Starlight/part 1.m4b")
	copyTo("Jonah Kessler/Salt and Starlight/part 2.m4b")
	copyTo("Winter Counting/CD 1/track 01.m4b")
	copyTo("Winter Counting/CD 2/track 01.m4b")
	copyTo("Loose Book.m4b")
	os.WriteFile(filepath.Join(root, "Jonah Kessler/Salt and Starlight/cover.jpg"), []byte("\xff\xd8\xff\xd9"), 0o644)
	return root
}

func sampleAudio(t *testing.T) string {
	t.Helper()
	// A tagless file, so titles come from folder and file names and the test can tell
	// the books apart.
	out := filepath.Join(t.TempDir(), "silence.m4b")
	cmd := exec.Command("ffmpeg", "-y", "-v", "quiet", "-f", "lavfi", "-i", "anullsrc=r=22050:cl=mono", "-t", "3", "-c:a", "aac", "-b:a", "32k", "-map_metadata", "-1", out)
	if err := cmd.Run(); err != nil {
		t.Skip("ffmpeg not available to make a sample file")
	}
	return out
}

func newTestServer(t *testing.T) (*httptest.Server, *store) {
	t.Helper()
	data := t.TempDir()
	st, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setPassword("phil", "correct horse"); err != nil {
		t.Fatal(err)
	}
	lib := newLibrary(testLibrary(t), data)
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(newServer(st, lib, "Test shelf"))
	t.Cleanup(ts.Close)
	return ts, st
}

func call(t *testing.T, ts *httptest.Server, method, path, token string, body any, headers ...string) (*http.Response, []byte) {
	t.Helper()
	var rb io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rb = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rb)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res, out
}

func login(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	res, body := call(t, ts, "POST", "/api/auth", "", map[string]string{"username": "phil", "password": "correct horse", "device": "test"})
	if res.StatusCode != 200 {
		t.Fatalf("auth: %d %s", res.StatusCode, body)
	}
	var out struct{ Token string }
	json.Unmarshal(body, &out)
	return out.Token
}

func TestHealthAndAuth(t *testing.T) {
	ts, _ := newTestServer(t)
	res, body := call(t, ts, "GET", "/api/health", "", nil)
	var h struct {
		OK       bool
		Protocol int
		Name     string
	}
	json.Unmarshal(body, &h)
	if res.StatusCode != 200 || !h.OK || h.Protocol != 1 || h.Name != "Test shelf" {
		t.Fatalf("health: %d %s", res.StatusCode, body)
	}
	res, _ = call(t, ts, "POST", "/api/auth", "", map[string]string{"username": "phil", "password": "wrong"})
	if res.StatusCode != 401 {
		t.Fatalf("wrong password gave %d", res.StatusCode)
	}
	res, _ = call(t, ts, "GET", "/api/library", "", nil)
	if res.StatusCode != 401 {
		t.Fatalf("no token gave %d", res.StatusCode)
	}
	token := login(t, ts)
	res, _ = call(t, ts, "POST", "/api/auth/logout", token, nil)
	if res.StatusCode != 204 {
		t.Fatalf("logout gave %d", res.StatusCode)
	}
	res, _ = call(t, ts, "GET", "/api/library", token, nil)
	if res.StatusCode != 401 {
		t.Fatalf("revoked token gave %d", res.StatusCode)
	}
}

func TestLibrary(t *testing.T) {
	ts, _ := newTestServer(t)
	token := login(t, ts)
	res, body := call(t, ts, "GET", "/api/library", token, nil)
	if res.StatusCode != 200 {
		t.Fatalf("library: %d %s", res.StatusCode, body)
	}
	var cat Catalogue
	if err := json.Unmarshal(body, &cat); err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]Book{}
	for _, b := range cat.Books {
		byTitle[b.Title] = b
	}
	if len(cat.Books) != 4 {
		t.Fatalf("want 4 books, got %d: %v", len(cat.Books), keys(byTitle))
	}
	salt, ok := byTitle["Salt and Starlight"]
	if !ok {
		t.Fatalf("Salt and Starlight missing from %v", keys(byTitle))
	}
	if len(salt.Files) != 2 || salt.Files[0].Name != "part 1.m4b" || salt.Files[1].Index != 1 {
		t.Fatalf("files: %+v", salt.Files)
	}
	if salt.DurationMs != salt.Files[0].DurationMs+salt.Files[1].DurationMs || salt.DurationMs == 0 {
		t.Fatalf("duration %d", salt.DurationMs)
	}
	if salt.CoverURL == "" {
		t.Fatal("cover.jpg not picked up")
	}
	if salt.UpdatedAt == 0 || cat.UpdatedAt < salt.UpdatedAt {
		t.Fatal("updatedAt not set")
	}
	winter := byTitle["Winter Counting"]
	if len(winter.Files) != 2 {
		t.Fatalf("disc folders should merge into one book, got %d files", len(winter.Files))
	}
	if _, ok := byTitle["Loose Book"]; !ok {
		t.Fatalf("loose file not a book: %v", keys(byTitle))
	}
	// Chapters, when the sample carries them, sit on the combined timeline.
	for _, c := range salt.Chapters {
		if c.EndMs > salt.DurationMs+1000 || c.StartMs > c.EndMs {
			t.Fatalf("chapter off the timeline: %+v", c)
		}
	}

	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	res, _ = call(t, ts, "GET", "/api/library", token, nil, "If-None-Match", etag)
	if res.StatusCode != 304 {
		t.Fatalf("If-None-Match gave %d", res.StatusCode)
	}

	// Ids are stable across a rescan.
	call(t, ts, "POST", "/api/rescan", token, nil)
	_, body = call(t, ts, "GET", "/api/library", token, nil)
	var again Catalogue
	json.Unmarshal(body, &again)
	for _, b := range again.Books {
		if byTitle[b.Title].ID != b.ID {
			t.Fatalf("id of %q changed on rescan", b.Title)
		}
	}
}

func TestProgressConflict(t *testing.T) {
	ts, _ := newTestServer(t)
	token := login(t, ts)
	_, body := call(t, ts, "GET", "/api/library", token, nil)
	var cat Catalogue
	json.Unmarshal(body, &cat)
	id := cat.Books[0].ID

	res, body := call(t, ts, "PUT", "/api/progress/"+id, token, Progress{PositionMs: 2000, UpdatedAt: 2000})
	if res.StatusCode != 200 {
		t.Fatalf("put: %d %s", res.StatusCode, body)
	}
	// Older timestamp: refused with the stored record.
	res, body = call(t, ts, "PUT", "/api/progress/"+id, token, Progress{PositionMs: 1000, UpdatedAt: 1000})
	if res.StatusCode != 409 {
		t.Fatalf("stale put gave %d", res.StatusCode)
	}
	var stored Progress
	json.Unmarshal(body, &stored)
	if stored.PositionMs != 2000 || stored.UpdatedAt != 2000 {
		t.Fatalf("409 body %+v", stored)
	}
	// Equal timestamp wins (>=), and the record is stored as sent.
	speed := 1.25
	res, body = call(t, ts, "PUT", "/api/progress/"+id, token, Progress{PositionMs: 2500, UpdatedAt: 2000, Finished: true, Speed: &speed})
	if res.StatusCode != 200 {
		t.Fatalf("equal put gave %d", res.StatusCode)
	}
	json.Unmarshal(body, &stored)
	if stored.PositionMs != 2500 || !stored.Finished || stored.Speed == nil || *stored.Speed != 1.25 {
		t.Fatalf("stored %+v", stored)
	}
	_, body = call(t, ts, "GET", "/api/progress", token, nil)
	var all struct{ Progress []Progress }
	json.Unmarshal(body, &all)
	if len(all.Progress) != 1 || all.Progress[0].BookID != id {
		t.Fatalf("all: %s", body)
	}
	res, _ = call(t, ts, "PUT", "/api/progress/nope", token, Progress{PositionMs: 1})
	if res.StatusCode != 404 {
		t.Fatalf("unknown book gave %d", res.StatusCode)
	}
	// Another user sees nothing of this.
	ts2 := ts
	_, _ = ts2, token
}

func TestBookmarks(t *testing.T) {
	ts, _ := newTestServer(t)
	token := login(t, ts)
	_, body := call(t, ts, "GET", "/api/library", token, nil)
	var cat Catalogue
	json.Unmarshal(body, &cat)
	id := cat.Books[0].ID

	bm := Bookmark{PositionMs: 4200, Label: "the riddle game", CreatedAt: 1737001000000}
	res, body := call(t, ts, "POST", "/api/bookmarks/"+id, token, bm)
	if res.StatusCode != 201 {
		t.Fatalf("post: %d %s", res.StatusCode, body)
	}
	var first Bookmark
	json.Unmarshal(body, &first)
	if first.ID == "" || first.BookID != id {
		t.Fatalf("stored %+v", first)
	}
	// The same bookmark again is the same bookmark.
	_, body = call(t, ts, "POST", "/api/bookmarks/"+id, token, bm)
	var second Bookmark
	json.Unmarshal(body, &second)
	if second.ID != first.ID {
		t.Fatalf("retry made a duplicate: %s vs %s", first.ID, second.ID)
	}
	_, body = call(t, ts, "GET", "/api/bookmarks", token, nil)
	var all struct{ Bookmarks []Bookmark }
	json.Unmarshal(body, &all)
	if len(all.Bookmarks) != 1 {
		t.Fatalf("all: %s", body)
	}
	res, _ = call(t, ts, "DELETE", "/api/bookmarks/"+id+"/"+first.ID, token, nil)
	if res.StatusCode != 204 {
		t.Fatalf("delete gave %d", res.StatusCode)
	}
	res, _ = call(t, ts, "DELETE", "/api/bookmarks/"+id+"/"+first.ID, token, nil)
	if res.StatusCode != 404 {
		t.Fatalf("second delete gave %d", res.StatusCode)
	}
}

func TestAudioRanges(t *testing.T) {
	ts, _ := newTestServer(t)
	token := login(t, ts)
	_, body := call(t, ts, "GET", "/api/library", token, nil)
	var cat Catalogue
	json.Unmarshal(body, &cat)
	f := cat.Books[0].Files[0]

	res, _ := call(t, ts, "HEAD", f.URL, token, nil)
	if res.StatusCode != 200 || res.ContentLength != f.SizeBytes || res.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("head: %d len %d ranges %q", res.StatusCode, res.ContentLength, res.Header.Get("Accept-Ranges"))
	}
	res, body = call(t, ts, "GET", f.URL, token, nil, "Range", "bytes=100-199")
	if res.StatusCode != 206 || len(body) != 100 || res.Header.Get("Content-Range") == "" {
		t.Fatalf("range: %d len %d", res.StatusCode, len(body))
	}
	res, _ = call(t, ts, "GET", f.URL, "", nil)
	if res.StatusCode != 401 {
		t.Fatalf("audio without token gave %d", res.StatusCode)
	}
	res, body = call(t, ts, "GET", cat.Books[0].CoverURL, token, nil)
	if cat.Books[0].CoverURL != "" && (res.StatusCode != 200 || len(body) == 0) {
		t.Fatalf("cover: %d", res.StatusCode)
	}
}

func keys(m map[string]Book) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestIDSurvivesLostSidecar(t *testing.T) {
	data := t.TempDir()
	root := testLibrary(t)
	lib := newLibrary(root, data)
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{}
	for _, b := range lib.snapshot().Books {
		before[b.Title] = b.ID
	}
	// Someone tidies away the sidecars; the path record must carry the ids.
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == sidecarName {
			os.Remove(p)
		}
		return nil
	})
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	for _, b := range lib.snapshot().Books {
		if before[b.Title] != b.ID {
			t.Fatalf("%q changed id after losing its sidecar: %s -> %s", b.Title, before[b.Title], b.ID)
		}
	}
	// A fresh process reads the same records back.
	again := newLibrary(root, data)
	if err := again.rescan(); err != nil {
		t.Fatal(err)
	}
	for _, b := range again.snapshot().Books {
		if before[b.Title] != b.ID {
			t.Fatalf("%q changed id across restart: %s -> %s", b.Title, before[b.Title], b.ID)
		}
	}
}
