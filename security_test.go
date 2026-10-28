package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A .kithara-id that is not a plain id (one that would climb out of the data folder
// when used in a file name) is ignored, and the book gets a fresh id.
func TestSidecarIDMustBePlain(t *testing.T) {
	root, data := t.TempDir(), t.TempDir()
	audio, _ := os.ReadFile(sampleAudio(t))
	dir := filepath.Join(root, "Book")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "book.m4b"), audio, 0o644)
	os.WriteFile(filepath.Join(dir, sidecarName), []byte("../users\n"), 0o644)
	lib := newLibrary(root, data)
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	id := lib.snapshot().Books[0].ID
	if !validID(id) || strings.Contains(id, ".") {
		t.Fatalf("book got id %q", id)
	}
	// The planted file is replaced by the new id.
	if side, _ := os.ReadFile(filepath.Join(dir, sidecarName)); strings.TrimSpace(string(side)) != id {
		t.Fatalf("sidecar holds %q, want %q", side, id)
	}
}

func TestUserNames(t *testing.T) {
	st, _ := openStore(t.TempDir())
	for _, bad := range []string{".", "..", ".hidden", "-x", "a/b", ""} {
		if err := st.setPassword(bad, "longenough"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := st.setPassword("Bob", "longenough"); err != nil {
		t.Fatal(err)
	}
	if err := st.setPassword("bob", "longenough"); err == nil {
		t.Fatal("bob accepted beside Bob")
	}
	if err := st.setPassword("Bob", "a new password"); err != nil {
		t.Fatalf("changing Bob's own password: %v", err)
	}
}

func TestSignedLinksPurposeAndRevocation(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	b := catalogueOf(t, ts, kid, "").Books[0]
	_, body := call(t, ts, "POST", "/api/books/"+b.ID+"/stream-link", kid, nil)
	var link struct{ Files []struct{ URL string } }
	json.Unmarshal(body, &link)
	audioURL := link.Files[0].URL
	if res, _ := call(t, ts, "GET", audioURL, "", nil); res.StatusCode != 200 {
		t.Fatalf("audio link gave %d", res.StatusCode)
	}
	// The same signature does not open the zip download.
	q := audioURL[strings.Index(audioURL, "?"):]
	if res, _ := call(t, ts, "GET", "/api/books/"+b.ID+"/download"+q, "", nil); res.StatusCode != 403 {
		t.Fatalf("an audio link opened the download: %d", res.StatusCode)
	}
	// A password change voids links handed out before it.
	call(t, ts, "PUT", "/api/me/password", kid, map[string]string{"current": "battery staple", "new": "a new password"})
	if res, _ := call(t, ts, "GET", audioURL, "", nil); res.StatusCode != 403 {
		t.Fatalf("a link outlived a password change: %d", res.StatusCode)
	}
}

func TestStoredSizesAreCapped(t *testing.T) {
	ts, _, st, _ := twoLibraryServer(t)
	long := strings.Repeat("x", 5000)
	res, body := call(t, ts, "POST", "/api/auth", "", map[string]string{"username": "kid", "password": "battery staple", "device": long})
	if res.StatusCode != 200 {
		t.Fatalf("auth: %d %s", res.StatusCode, body)
	}
	for _, d := range st.devices("kid", "") {
		if len([]rune(d.Name)) > 100 {
			t.Fatalf("device name kept at %d characters", len(d.Name))
		}
	}
	token := loginAs(t, ts, "kid", "battery staple", "x")
	b := catalogueOf(t, ts, token, "").Books[0]
	_, body = call(t, ts, "POST", "/api/bookmarks/"+b.ID, token, Bookmark{PositionMs: 1, Label: long, CreatedAt: 1})
	var m Bookmark
	json.Unmarshal(body, &m)
	if len([]rune(m.Label)) != 500 {
		t.Fatalf("bookmark label kept at %d characters", len(m.Label))
	}
	// Signing in again and again keeps at most maxDevices devices.
	for range maxDevices + 3 {
		st.issueToken("kid", "again")
	}
	if n := len(st.devices("kid", "")); n != maxDevices {
		t.Fatalf("%d devices kept", n)
	}
}

func TestCatalogueGzipped(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	token := loginAs(t, ts, "phil", "correct horse", "laptop")
	req, _ := http.NewRequest("GET", ts.URL+"/api/library", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := http.DefaultTransport.RoundTrip(req) // no transparent decompression
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("not gzipped: %v", res.Header)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	var cat Catalogue
	if err := json.Unmarshal(plain, &cat); err != nil || len(cat.Books) != 5 {
		t.Fatalf("gzipped catalogue: %v, %d books", err, len(cat.Books))
	}
}

// A file with no embedded art is not handed to ffmpeg again on every scan, and editing
// one book does not rescan the others.
func TestCoverMarkerAndSingleRebuild(t *testing.T) {
	ts, srv, _, data := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	b := bookNamed(t, catalogueOf(t, ts, admin, "").Books, "Winter Counting") // no cover anywhere
	if _, err := os.Stat(filepath.Join(data, "covers", b.ID+".none")); err != nil {
		t.Fatalf("no marker for a book without art: %v", err)
	}
	srv.lib.mu.RLock()
	scanned := srv.lib.lastScanAt
	srv.lib.mu.RUnlock()
	_, body := call(t, ts, "PUT", "/api/books/"+b.ID+"/metadata", admin, MatchCandidate{Title: "Winter Counting, Revised"})
	var got Book
	json.Unmarshal(body, &got)
	if got.Title != "Winter Counting, Revised" {
		t.Fatalf("edit: %s", body)
	}
	srv.lib.mu.RLock()
	again := srv.lib.lastScanAt
	srv.lib.mu.RUnlock()
	if again != scanned {
		t.Fatal("an edit to one book rescanned every library")
	}
	if n := len(catalogueOf(t, ts, admin, "").Books); n != 5 {
		t.Fatalf("catalogue has %d books after a single rebuild", n)
	}
}
