package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// put sends a raw body (a file upload, a picture).
func put(t *testing.T, ts interface{ Client() *http.Client }, base, path, token string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("PUT", base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res, out
}

func bookNamed(t *testing.T, books []Book, title string) Book {
	t.Helper()
	for _, b := range books {
		if b.Title == title {
			return b
		}
	}
	t.Fatalf("no book %q", title)
	return Book{}
}

func TestStreamLinks(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	books := catalogueOf(t, ts, admin, "").Books
	gruffalo, hobbit := bookNamed(t, books, "Gruffalo"), bookNamed(t, books, "Salt and Starlight")

	res, body := call(t, ts, "POST", "/api/books/"+gruffalo.ID+"/stream-link", kid, nil)
	var link struct {
		Files []struct {
			Index int
			URL   string
		}
	}
	json.Unmarshal(body, &link)
	if res.StatusCode != 200 || len(link.Files) != 1 {
		t.Fatalf("stream link: %d %s", res.StatusCode, body)
	}
	// The link plays with no token, ranges and all, as an <audio> element needs.
	res, body = call(t, ts, "GET", link.Files[0].URL, "", nil, "Range", "bytes=0-99")
	if res.StatusCode != 206 || len(body) != 100 {
		t.Fatalf("signed audio: %d, %d bytes", res.StatusCode, len(body))
	}
	if res, _ := call(t, ts, "GET", strings.Replace(link.Files[0].URL, gruffalo.ID, hobbit.ID, 1), "", nil); res.StatusCode != 403 {
		t.Fatalf("a link for another book gave %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "POST", "/api/books/"+hobbit.ID+"/stream-link", kid, nil); res.StatusCode != 404 {
		t.Fatalf("a link to a hidden book gave %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "GET", "/audio/"+gruffalo.ID+"/0", "", nil); res.StatusCode != 401 {
		t.Fatalf("audio with neither token nor link gave %d", res.StatusCode)
	}
}

func TestChangeOwnPassword(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	phone := loginAs(t, ts, "kid", "battery staple", "phone")
	web := loginAs(t, ts, "kid", "battery staple", "web")
	if res, _ := call(t, ts, "PUT", "/api/me/password", web, map[string]string{"current": "wrong one", "new": "a new password"}); res.StatusCode != 403 {
		t.Fatalf("wrong current password gave %d", res.StatusCode)
	}
	res, body := call(t, ts, "PUT", "/api/me/password", web, map[string]string{"current": "battery staple", "new": "a new password"})
	var out struct{ Token string }
	json.Unmarshal(body, &out)
	if res.StatusCode != 200 || out.Token == "" {
		t.Fatalf("change: %d %s", res.StatusCode, body)
	}
	if res, _ := call(t, ts, "GET", "/api/me", phone, nil); res.StatusCode != 401 {
		t.Fatalf("other device still signed in: %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "GET", "/api/me", out.Token, nil); res.StatusCode != 200 {
		t.Fatalf("the fresh token gave %d", res.StatusCode)
	}
	loginAs(t, ts, "kid", "a new password", "again")
}

func TestUpload(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	audio, _ := os.ReadFile(sampleAudio(t))
	up := func(token, rel string, data []byte) int {
		res, _ := put(t, ts, ts.URL, "/api/libraries/Kids/files?path="+url.QueryEscape(rel), token, data)
		return res.StatusCode
	}
	if c := up(kid, "Dr Seuss/Green Eggs/green.m4b", audio); c != 403 {
		t.Fatalf("a plain user uploading gave %d", c)
	}
	for _, bad := range []string{"../escape.m4b", "a/../../escape.m4b", "notes.exe", ".hidden.m4b", ""} {
		if c := up(admin, bad, audio); c != 400 {
			t.Errorf("%q gave %d", bad, c)
		}
	}
	if c := up(admin, "Dr Seuss/Green Eggs/green.m4b", audio); c != 201 {
		t.Fatalf("upload gave %d", c)
	}
	if c := up(admin, "Dr Seuss/Green Eggs/green.m4b", audio); c != 409 {
		t.Fatalf("a second upload of the same file gave %d", c)
	}
	if c := up(admin, "Dr Seuss/Green Eggs/cover.jpg", []byte("\xff\xd8\xff\xd9")); c != 201 {
		t.Fatalf("cover upload gave %d", c)
	}
	call(t, ts, "POST", "/api/rescan", admin, nil)
	b := bookNamed(t, catalogueOf(t, ts, admin, "?library=Kids").Books, "Green Eggs")
	if b.Author != "Dr Seuss" || b.CoverURL == "" {
		t.Fatalf("uploaded book: %+v", b)
	}
}

func TestCoverAndChapters(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	b := bookNamed(t, catalogueOf(t, ts, admin, "").Books, "Salt and Starlight")

	if res, _ := put(t, ts, ts.URL, "/api/books/"+b.ID+"/cover", admin, []byte("not a picture")); res.StatusCode != 400 {
		t.Fatalf("a non-picture cover gave %d", res.StatusCode)
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	res, body := put(t, ts, ts.URL, "/api/books/"+b.ID+"/cover", admin, png)
	var got Book
	json.Unmarshal(body, &got)
	if res.StatusCode != 200 || got.UpdatedAt <= b.UpdatedAt {
		t.Fatalf("cover: %d %s", res.StatusCode, body)
	}
	res, body = call(t, ts, "GET", got.CoverURL, admin, nil)
	if !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Fatalf("served cover is not the upload: %d", res.StatusCode)
	}

	mid := b.DurationMs / 2
	res, body = call(t, ts, "PUT", "/api/books/"+b.ID+"/chapters", admin, map[string]any{"chapters": []map[string]any{
		{"title": "Second half", "startMs": mid}, {"title": "Opening", "startMs": 0},
	}})
	got = Book{}
	json.Unmarshal(body, &got)
	if res.StatusCode != 200 || len(got.Chapters) != 2 || got.Chapters[0].Title != "Opening" || got.Chapters[0].EndMs != mid || got.Chapters[1].EndMs != b.DurationMs {
		t.Fatalf("chapters: %d %s", res.StatusCode, body)
	}
	if res, _ := call(t, ts, "PUT", "/api/books/"+b.ID+"/chapters", admin, map[string]any{"chapters": []map[string]any{{"title": "Late", "startMs": b.DurationMs + 1}}}); res.StatusCode != 400 {
		t.Fatalf("a chapter past the end gave %d", res.StatusCode)
	}
	// Saving other details keeps the edited chapters.
	_, body = call(t, ts, "PUT", "/api/books/"+b.ID+"/metadata", admin, MatchCandidate{Title: "Salt & Starlight"})
	got = Book{}
	json.Unmarshal(body, &got)
	if got.Title != "Salt & Starlight" || len(got.Chapters) != 2 {
		t.Fatalf("details save lost the chapters: %s", body)
	}
	// And a batch save can skip the rescan.
	if res, body := call(t, ts, "PUT", "/api/books/"+b.ID+"/metadata?rescan=0", admin, MatchCandidate{Title: "Salt and Starlight"}); res.StatusCode != 200 || !strings.Contains(string(body), "saved") {
		t.Fatalf("rescan=0: %d %s", res.StatusCode, body)
	}
}

func TestProblemsSettingsBackupActivity(t *testing.T) {
	ts, srv, _, data := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")

	// A file ffprobe cannot read is a problem, not a book.
	os.MkdirAll(filepath.Join(srv.lib.roots[1].path, "Broken"), 0o755)
	os.WriteFile(filepath.Join(srv.lib.roots[1].path, "Broken", "broken.mp3"), []byte("not audio"), 0o644)
	call(t, ts, "POST", "/api/rescan", admin, nil)
	_, body := call(t, ts, "GET", "/api/problems", admin, nil)
	if !strings.Contains(string(body), "Broken") {
		t.Fatalf("problems: %s", body)
	}
	if res, _ := call(t, ts, "GET", "/api/problems", kid, nil); res.StatusCode != 403 {
		t.Fatalf("a plain user reading problems gave %d", res.StatusCode)
	}

	mins := 5
	if res, body := call(t, ts, "PUT", "/api/settings", admin, Settings{Name: "Family shelf", RescanMinutes: &mins}); res.StatusCode != 200 {
		t.Fatalf("settings: %d %s", res.StatusCode, body)
	}
	_, body = call(t, ts, "GET", "/api/health", "", nil)
	if !strings.Contains(string(body), "Family shelf") || srv.rescanEvery() != 5*time.Minute {
		t.Fatalf("settings not applied: %s, every %s", body, srv.rescanEvery())
	}
	bad := -1
	if res, _ := call(t, ts, "PUT", "/api/settings", admin, Settings{RescanMinutes: &bad}); res.StatusCode != 400 {
		t.Fatalf("a negative interval gave %d", res.StatusCode)
	}

	// Some listening, so the backup and the activity have something in them.
	b := catalogueOf(t, ts, kid, "").Books[0]
	now := time.Now().UnixMilli()
	call(t, ts, "PUT", "/api/progress/"+b.ID, kid, Progress{PositionMs: 0, UpdatedAt: now - 2000})
	call(t, ts, "PUT", "/api/progress/"+b.ID, kid, Progress{PositionMs: 1000, UpdatedAt: now - 1000})

	res, body := call(t, ts, "GET", "/api/backup", admin, nil)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if res.StatusCode != 200 || err != nil {
		t.Fatalf("backup: %d %v", res.StatusCode, err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["users.json"] || !names["users/kid/progress.json"] || !names["settings.json"] || names["tokens.json"] || names["probe-cache.json"] {
		t.Fatalf("backup holds %v", names)
	}
	_ = data

	_, body = call(t, ts, "GET", "/api/activity?days=7", admin, nil)
	var act struct {
		Users []struct {
			Name       string
			ListenedMs int64
		}
		Recent []struct{ User, BookID string }
	}
	json.Unmarshal(body, &act)
	if len(act.Recent) != 1 || act.Recent[0].User != "kid" || act.Users[0].Name != "kid" || act.Users[0].ListenedMs != 1000 {
		t.Fatalf("activity: %s", body)
	}
}
