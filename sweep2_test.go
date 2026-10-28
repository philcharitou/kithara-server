package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Users made before the name rules tightened can still change their password.
func TestLegacyUserNamesStillWork(t *testing.T) {
	data := t.TempDir()
	writeJSON(filepath.Join(data, "users.json"), map[string]map[string]any{
		"_phil": {"salt": "00", "hash": "00", "iter": 1, "role": "admin"},
	})
	st, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.setPassword("_phil", "a new password"); err != nil {
		t.Fatalf("legacy name: %v", err)
	}
	if !st.checkPassword("_phil", "a new password") {
		t.Fatal("password not changed")
	}
	for _, bad := range []string{"_new", "bob.", "CON", "nul.txt", "Com1"} {
		if err := st.setPassword(bad, "longenough"); err == nil {
			t.Errorf("new user %q accepted", bad)
		}
	}
}

// Parallel attempts cannot all pass the limit before any is counted.
func TestLimiterTryIsAtomic(t *testing.T) {
	l := newLoginLimiter(5, time.Hour)
	now := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	passed := 0
	for range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.try("k", now) == 0 {
				mu.Lock()
				passed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if passed != 5 {
		t.Fatalf("%d of 200 parallel attempts passed a limit of 5", passed)
	}
}

// Progress dated far in the future adds no listening (it would let one user grow
// their sessions file without end).
func TestNoSessionsFromTheFuture(t *testing.T) {
	st, _ := openStore(t.TempDir())
	st.setPassword("phil", "correct horse")
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	st.putProgress("phil", "", Progress{BookID: "b", PositionMs: 0, UpdatedAt: future})
	st.putProgress("phil", "", Progress{BookID: "b", PositionMs: 60000, UpdatedAt: future + 60000})
	if list, _ := st.sessions("phil", "", 0); len(list) != 0 {
		t.Fatalf("%d sessions from the future", len(list))
	}
}

// Links in a library folder: audio that leads outside the library is not served, and a
// sidecar that is a link is not read (it could show the server's own files).
func TestLinksInLibraryFolders(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root, outside, data := t.TempDir(), t.TempDir(), t.TempDir()
	audio, _ := os.ReadFile(sampleAudio(t))
	secret := filepath.Join(outside, "secret.txt")
	os.WriteFile(secret, []byte("TOKEN-abc"), 0o600)
	os.WriteFile(filepath.Join(outside, "other.m4b"), audio, 0o644)

	book := filepath.Join(root, "Book")
	os.MkdirAll(book, 0o755)
	os.WriteFile(filepath.Join(book, "part 1.m4b"), audio, 0o644)
	os.Symlink(secret, filepath.Join(book, "desc.txt"))
	os.Symlink(filepath.Join(outside, "other.m4b"), filepath.Join(book, "part 2.m4b"))
	// A link to audio inside the library is fine.
	inside := filepath.Join(root, "Inside")
	os.MkdirAll(inside, 0o755)
	os.Symlink(filepath.Join(book, "part 1.m4b"), filepath.Join(inside, "linked.m4b"))

	lib := newLibrary(root, data)
	if err := lib.rescan(); err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]Book{}
	for _, b := range lib.snapshot().Books {
		byTitle[b.Title] = b
	}
	b := byTitle["Book"]
	if len(b.Files) != 1 || strings.Contains(b.Description, "TOKEN") {
		t.Fatalf("book: %d files, description %q", len(b.Files), b.Description)
	}
	if len(byTitle["Inside"].Files) != 1 {
		t.Fatalf("a link inside the library was dropped: %+v", byTitle)
	}
}

// A file without embedded art is tried once; its marker stays as it was on later scans.
func TestCoverMarkerStopsRetries(t *testing.T) {
	ts, srv, _, data := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	b := bookNamed(t, catalogueOf(t, ts, admin, "").Books, "Winter Counting")
	marker := filepath.Join(data, "covers", b.ID+".none")
	before, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	srv.lib.rescan()
	after, _ := os.Stat(marker)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("the marker was rewritten: ffmpeg ran again")
	}
	// Any change to the file (here its time) tries again.
	f := srv.lib.book(b.ID).Files[0].path // paths on disk are not in the JSON
	future := time.Now().Add(time.Hour)
	os.Chtimes(f, future, future)
	srv.lib.rescan()
	if again, _ := os.Stat(marker); again.ModTime().Equal(before.ModTime()) {
		t.Fatal("a changed file was not tried again")
	}
}

// A settings file edited by hand cannot make rescans run back to back.
func TestSettingsValidatedOnLoad(t *testing.T) {
	data := t.TempDir()
	os.WriteFile(filepath.Join(data, "settings.json"), []byte(`{"rescanMinutes": -5, "name": "x"}`), 0o644)
	st, _ := openStore(data)
	srv := newServer(st, newLibrary(t.TempDir(), data), "Default")
	srv.settings.interval = 10 * time.Minute
	if got := srv.rescanEvery(); got != 10*time.Minute {
		t.Fatalf("rescan every %s", got)
	}
}
