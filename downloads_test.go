package main

import (
	"encoding/json"
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

// dlPlugin searches, and downloads by writing files into the server's incoming folder.
// Result "handoff" answers with a link for another plugin; "broken" fails; "hold" never ends.
type dlPlugin struct {
	mu       sync.Mutex
	name     string
	accepts  []string
	incoming string
	audio    []byte
	state    map[string]string // download id -> state reported
	forgot   []string
	given    []map[string]any
}

func (f *dlPlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in map[string]any
	json.NewDecoder(r.Body).Decode(&in)
	switch {
	case r.URL.Path == "/kithara/plugin":
		info := map[string]any{"name": f.name, "apiVersion": 1, "downloads": true, "accepts": f.accepts}
		if f.accepts == nil {
			info["search"] = map[string]any{"name": f.name}
		}
		json.NewEncoder(w).Encode(info)
	case r.URL.Path == "/kithara/preview":
		json.NewEncoder(w).Encode(map[string]any{"id": in["id"], "title": "The Wind: in the Willows?", "author": "Kenneth Grahame"})
	case r.URL.Path == "/kithara/download":
		f.given = append(f.given, in)
		id, _ := in["downloadId"].(string)
		folder := filepath.Join(f.incoming, in["folder"].(string))
		switch in["id"] {
		case "handoff":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "open": "ftp://example.org/willows.zip"})
			return
		case "refused":
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "not available"})
			return
		case "broken":
			f.state[id] = "FAILED"
		case "hold":
			f.state[id] = "DOWNLOADING"
		default:
			os.MkdirAll(filepath.Join(folder, "Disc 1"), 0o755)
			os.WriteFile(filepath.Join(folder, "Disc 1", "01.m4b"), f.audio, 0o644)
			os.WriteFile(filepath.Join(folder, "cover.jpg"), []byte("not really a picture"), 0o644)
			os.WriteFile(filepath.Join(folder, "setup.exe"), []byte("MZ"), 0o644)
			os.WriteFile(filepath.Join(folder, ".hidden"), []byte("x"), 0o644)
			os.Symlink("/etc/passwd", filepath.Join(folder, "passwd.mp3"))
			f.state[id] = "FINISHED"
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	case strings.HasPrefix(r.URL.Path, "/kithara/downloads/"):
		id := strings.TrimPrefix(r.URL.Path, "/kithara/downloads/")
		if r.Method == "DELETE" {
			f.forgot = append(f.forgot, id)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"state": f.state[id], "progress": 0.5, "error": "the source went away"})
	default:
		http.NotFound(w, r)
	}
}

func TestPluginDownloads(t *testing.T) {
	downloadPoll = 50 * time.Millisecond
	ts, srv, _, _ := twoLibraryServer(t)
	audio, _ := os.ReadFile(sampleAudio(t))
	source := &dlPlugin{name: "Old books", incoming: srv.downloads.incoming, audio: audio, state: map[string]string{}}
	taker := &dlPlugin{name: "Fetcher", accepts: []string{"ftp"}, incoming: srv.downloads.incoming, audio: audio, state: map[string]string{}}
	sp, tp := httptest.NewServer(source), httptest.NewServer(taker)
	defer sp.Close()
	defer tp.Close()
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	add := func(u string) string {
		res, body := call(t, ts, "POST", "/api/plugins", admin, map[string]string{"url": u})
		if res.StatusCode != 201 {
			t.Fatalf("add: %s", body)
		}
		var w webPlugin
		json.Unmarshal(body, &w)
		return w.Plugin.ID
	}
	src, fetcher := add(sp.URL), add(tp.URL)
	start := func(token, id, library string) (int, Download, string) {
		res, body := call(t, ts, "POST", "/api/downloads", token, map[string]string{"source": src, "id": id, "library": library})
		var dl Download
		json.Unmarshal(body, &dl)
		return res.StatusCode, dl, string(body)
	}
	wait := func(id, state string) Download {
		t.Helper()
		for range 200 {
			if dl, _ := srv.downloads.get(id); dl.State == state {
				return dl
			}
			time.Sleep(20 * time.Millisecond)
		}
		dl, _ := srv.downloads.get(id)
		t.Fatalf("%s never reached %s: %+v", id, state, dl)
		return Download{}
	}

	// Listeners may not, until the admin allows it; and only into their libraries.
	if code, _, _ := start(kid, "willows", "Kids"); code != 403 {
		t.Fatalf("a listener downloaded from an admins-only source: %d", code)
	}
	call(t, ts, "PATCH", "/api/plugins/"+src, admin, map[string]any{"download": allowEveryone})
	if code, _, _ := start(kid, "willows", "Audiobooks"); code != 400 {
		t.Fatalf("a listener downloaded into a library they can't see: %d", code)
	}

	// A good download: checked, moved in as Author/Title, scanned, and forgotten by the plugin.
	code, dl, body := start(kid, "willows", "Kids")
	if code != 201 || dl.State != dlQueued || dl.Title != "The Wind: in the Willows?" || dl.Library != "Kids" {
		t.Fatalf("start: %d %s", code, body)
	}
	done := wait(dl.ID, dlFinished)
	root, _ := srv.lib.catalogueRoot("Kids")
	dir := filepath.Join(root.path, "Kenneth Grahame", "The Wind - in the Willows")
	if _, err := os.Stat(filepath.Join(dir, "Disc 1", "01.m4b")); err != nil {
		t.Fatalf("not in the library: %v", err)
	}
	for _, name := range []string{"setup.exe", ".hidden", "passwd.mp3"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("%s went into the library", name)
		}
	}
	if b := srv.lib.book(done.BookID); b == nil || b.Library != "Kids" || b.Title != "The Wind: in the Willows?" || b.Author != "Kenneth Grahame" {
		t.Fatalf("the book: %+v, for %+v", b, done)
	}
	if !slices.Contains(done.Skipped, "setup.exe") || !slices.Contains(done.Skipped, "passwd.mp3 (a link)") {
		t.Fatalf("skipped: %v", done.Skipped)
	}
	if _, err := os.Stat(filepath.Join(srv.downloads.incoming, dl.ID)); err == nil {
		t.Fatal("the incoming folder was left behind")
	}
	if left, _ := filepath.Glob(filepath.Join(root.path, ".kithara-incoming-*")); len(left) > 0 {
		t.Fatalf("staging left behind: %v", left)
	}
	source.mu.Lock()
	forgot, given := slices.Clone(source.forgot), source.given[0]
	source.mu.Unlock()
	if !slices.Contains(forgot, dl.ID) || given["folder"] != dl.ID || given["id"] != "willows" {
		t.Fatalf("the plugin saw: forgot %v, given %v", forgot, given)
	}
	// The same book again goes beside it, not over it.
	_, again, _ := start(kid, "willows", "Kids")
	wait(again.ID, dlFinished)
	if _, err := os.Stat(dir + " (2)"); err != nil {
		t.Fatalf("the second copy: %v", err)
	}

	// A refusal is an error straight away; a failure later is FAILED, with the reason.
	if code, _, body := start(kid, "refused", "Kids"); code != 502 || !strings.Contains(body, "not available") {
		t.Fatalf("refused: %d %s", code, body)
	}
	_, broken, _ := start(kid, "broken", "Kids")
	if got := wait(broken.ID, dlFailed); !strings.Contains(got.Error, "the source went away") {
		t.Fatalf("failed: %+v", got)
	}

	// A hand-off goes to the plugin that takes the link's kind, if the user may use it.
	if code, _, body := start(kid, "handoff", "Kids"); code != 502 || !strings.Contains(body, "no plugin you may use takes those") {
		t.Fatalf("hand-off to an admins-only plugin: %d %s", code, body)
	}
	code, handed, body := start(admin, "handoff", "Audiobooks")
	if code != 201 || handed.Plugin != fetcher {
		t.Fatalf("hand-off: %d %s", code, body)
	}
	taker.mu.Lock()
	link := taker.given[0]["link"]
	taker.mu.Unlock()
	if link != "ftp://example.org/willows.zip" {
		t.Fatalf("the taker was given %v", link)
	}

	// A listener has 3 running at most, even when they ask for many at once; they see only
	// their own; cancelling tells the plugin.
	var (
		held  []string
		heldM sync.Mutex
		wg    sync.WaitGroup
	)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, h, _ := start(kid, "hold", "Kids"); code == 201 {
				heldM.Lock()
				held = append(held, h.ID)
				heldM.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(held) != 3 {
		t.Fatalf("%d downloads started at once by a listener", len(held))
	}
	var list struct{ Downloads []Download }
	_, b := call(t, ts, "GET", "/api/downloads", kid, nil)
	json.Unmarshal(b, &list)
	if slices.ContainsFunc(list.Downloads, func(d Download) bool { return d.User != "kid" || d.Link != "" }) {
		t.Fatalf("the list: %s", b)
	}
	if res, _ := call(t, ts, "DELETE", "/api/downloads/"+held[0], kid, nil); res.StatusCode != 204 {
		t.Fatalf("cancel: %d", res.StatusCode)
	}
	source.mu.Lock()
	cancelled := slices.Contains(source.forgot, held[0])
	source.mu.Unlock()
	if _, ok := srv.downloads.get(held[0]); ok || !cancelled {
		t.Fatalf("cancelled: still listed %v, plugin told %v", ok, cancelled)
	}
	if res, _ := call(t, ts, "DELETE", "/api/downloads/"+handed.ID, kid, nil); res.StatusCode != 404 {
		t.Fatalf("a listener cancelled someone else's download: %d", res.StatusCode)
	}
}

func TestCollectIncoming(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	if _, _, audio, err := collectIncoming(dir); err != nil || audio {
		t.Fatalf("no audio: %v %v", audio, err)
	}
	if _, _, _, err := collectIncoming(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing folder was fine")
	}
	os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "b", "c", "deep.mp3"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "a", "b", "ok.mp3"), []byte("x"), 0o644)
	files, skipped, audio, err := collectIncoming(dir)
	if err != nil || !audio || len(files) != 2 || !slices.Contains(skipped, "a/b/c/ (too deep)") {
		t.Fatalf("files %v, skipped %v, %v", files, skipped, err)
	}
	for in, want := range map[string]string{"AC/DC: Live?": "AC DC - Live", "  ..  ": "x", `a<b>"c"|d`: "a(b)'c' d"} {
		if got := folderName(in, "x"); got != want {
			t.Errorf("folderName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := linkKind("https://example.org/a/b.ZIP?x=1"); got != "zip" {
		t.Errorf("linkKind: %q", got)
	}
	if got := linkKind("ftp://example.org/"); got != "ftp" {
		t.Errorf("linkKind: %q", got)
	}
	if got := linkKinds("https://example.org/a/b.zip"); strings.Join(got, ",") != "zip,https" {
		t.Errorf("linkKinds: %v", got)
	}
}

// The incoming folder may be set to somewhere that holds other things: only the server's
// own download folders are ever tidied away.
func TestTrimIncomingKeepsOthers(t *testing.T) {
	_, srv, _, _ := twoLibraryServer(t)
	in := srv.downloads.incoming
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"d_0123456789ab", "My audiobooks", "d_notmine", "notes.txt"} {
		p := filepath.Join(in, name)
		if strings.HasSuffix(name, ".txt") {
			os.MkdirAll(in, 0o755)
			os.WriteFile(p, []byte("x"), 0o644)
		} else {
			os.MkdirAll(p, 0o755)
		}
		os.Chtimes(p, old, old)
	}
	srv.downloads.trimIncoming()
	for name, want := range map[string]bool{"d_0123456789ab": false, "My audiobooks": true, "d_notmine": true, "notes.txt": true} {
		if _, err := os.Stat(filepath.Join(in, name)); (err == nil) != want {
			t.Errorf("%s: kept %v, want %v", name, err == nil, want)
		}
	}
}

// A restart while files were being moved in carries on: the book still appears once,
// whole, from the hidden staging folder.
func TestIntakeResumes(t *testing.T) {
	_, srv, _, _ := twoLibraryServer(t)
	audio, _ := os.ReadFile(sampleAudio(t))
	root, _ := srv.lib.catalogueRoot("Kids")
	d := srv.downloads
	dl := Download{ID: "d_00000000000a", User: "phil", Library: "Kids", Title: "Half Moved", Author: "Someone", State: dlMoving}
	d.mu.Lock()
	d.list = append(d.list, dl)
	d.mu.Unlock()
	staging := filepath.Join(root.path, ".kithara-incoming-"+dl.ID)
	os.MkdirAll(staging, 0o755)
	os.WriteFile(filepath.Join(staging, "01.m4b"), audio, 0o644) // moved before the restart
	src := filepath.Join(d.incoming, dl.ID)
	os.MkdirAll(src, 0o755)
	os.WriteFile(filepath.Join(src, "02.m4b"), audio, 0o644) // still to move
	d.intake(dl.ID)
	got, _ := d.get(dl.ID)
	dir := filepath.Join(root.path, "Someone", "Half Moved")
	if got.State != dlFinished || got.BookID == "" {
		t.Fatalf("after resuming: %+v", got)
	}
	for _, f := range []string{"01.m4b", "02.m4b", "metadata.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if _, err := os.Stat(staging); err == nil {
		t.Error("the staging folder was left behind")
	}
}
