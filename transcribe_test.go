package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWhisper is a stand-in for whisper.cpp's whisper-cli: it insists on a real WAV,
// logs its arguments, and writes -oj output with one passage and one stretch of noise.
const fakeWhisper = `#!/bin/sh
case "$1" in --help) echo "  -oj, --output-json  -vm, --vad-model --vad"; exit 0;; esac
echo "$@" >> "$KITHARA_FAKE_LOG"
while [ $# -gt 0 ]; do
  case "$1" in -f) wav="$2"; shift;; -of) of="$2"; shift;; esac
  shift
done
[ -s "$wav" ] || { echo "no audio at $wav" >&2; exit 3; }
echo "whisper_print_progress_callback: progress =  50%" >&2
cat > "$of.json" <<'J'
{"transcription":[{"offsets":{"from":100,"to":900},"text":" Hello there."},{"offsets":{"from":900,"to":1500},"text":" [BLANK_AUDIO]"}]}
J
`

func withFakeWhisper(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "whisper-cli")
	if err := os.WriteFile(bin, []byte(fakeWhisper), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("KITHARA_WHISPER", bin)
	t.Setenv("KITHARA_FAKE_LOG", logPath)
	os.MkdirAll(filepath.Join(data, "models"), 0o755)
	os.WriteFile(filepath.Join(data, "models", "ggml-small.en.bin"), []byte("model"), 0o644)
	return logPath
}

func TestServerTranscription(t *testing.T) {
	ts, _, _, data := twoLibraryServer(t)
	calls := withFakeWhisper(t, data)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	b := bookNamed(t, catalogueOf(t, ts, admin, "").Books, "The Cartographer's Daughter")

	var health struct{ Features []string }
	_, body := call(t, ts, "GET", "/api/health", "", nil)
	json.Unmarshal(body, &health)
	if !strings.Contains(strings.Join(health.Features, ","), "transcription") {
		t.Fatalf("health: %s", body)
	}
	res, body := call(t, ts, "POST", "/api/books/"+b.ID+"/transcribe", admin, nil)
	if res.StatusCode != 200 || !strings.Contains(string(body), `"queued":1`) {
		t.Fatalf("queue: %d %s", res.StatusCode, body)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var st TranscriptionStatus
		_, body = call(t, ts, "GET", "/api/transcription", admin, nil)
		json.Unmarshal(body, &st)
		if st.LastError != "" {
			t.Fatalf("transcription failed: %s", st.LastError)
		}
		if st.Running == nil && len(st.Queue) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still running: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var v TranscriptView
	_, body = call(t, ts, "GET", "/api/books/"+b.ID+"/transcript", admin, nil)
	json.Unmarshal(body, &v)
	if len(v.Chapters) != 1 || v.Chapters[0].Status != statusDone || v.Chapters[0].Model != "small.en" || v.Chapters[0].Engine != "whisper.cpp" {
		t.Fatalf("chapters: %s", body)
	}
	if len(v.Passages) != 1 || v.Passages[0].Text != "Hello there." || v.Passages[0].StartMs != 100 {
		t.Fatalf("passages (noise must be dropped): %s", body)
	}
	log, _ := os.ReadFile(calls)
	if !strings.Contains(string(log), "-l en") || !strings.Contains(string(log), "ggml-small.en.bin") {
		t.Fatalf("whisper-cli was called with %s", log)
	}
	// Done chapters are not queued again unless asked.
	if _, body := call(t, ts, "POST", "/api/books/"+b.ID+"/transcribe", admin, nil); !strings.Contains(string(body), `"queued":0`) {
		t.Fatalf("queued again: %s", body)
	}
	// Search finds it.
	_, body = call(t, ts, "GET", "/api/transcripts/search?q=hello", admin, nil)
	if !strings.Contains(string(body), b.ID) {
		t.Fatalf("search: %s", body)
	}
}

// A stretch across two files is cut out of both.
func TestDecodeAcrossFiles(t *testing.T) {
	ts, srv, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	b := srv.lib.book(bookNamed(t, catalogueOf(t, ts, admin, "").Books, "Salt and Starlight").ID)
	if len(b.Files) != 2 {
		t.Fatalf("%d files", len(b.Files))
	}
	cut := b.Files[0].DurationMs
	wav := filepath.Join(t.TempDir(), "x.wav")
	if err := decodeRange(context.Background(), b, cut-1000, cut+1000, wav); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(wav)
	// Two seconds of 16 kHz 16-bit mono, give or take a frame.
	if n := info.Size(); n < 60000 || n > 68000 {
		t.Fatalf("got %d bytes for two seconds", n)
	}
}

func TestTranscriptUploads(t *testing.T) {
	ts, _, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")
	b := catalogueOf(t, ts, kid, "").Books[0]
	up := func(token, status, text string) int {
		res, _ := call(t, ts, "PUT", "/api/books/"+b.ID+"/transcript/chapters/0", token, map[string]any{
			"status": status, "model": "base.en", "passages": []Passage{{StartMs: 10, EndMs: 900, Text: text}}})
		return res.StatusCode
	}
	if code := up(kid, statusPartial, "first"); code != 200 {
		t.Fatalf("partial upload: %d", code)
	}
	if code := up(kid, statusDone, "second"); code != 200 {
		t.Fatalf("finishing a partial chapter: %d", code)
	}
	if code := up(kid, statusDone, "vandal"); code != 409 {
		t.Fatalf("a listener replaced a done chapter: %d", code)
	}
	if code := up(admin, statusDone, "fixed"); code != 200 {
		t.Fatalf("admin replace: %d", code)
	}
	var v TranscriptView
	_, body := call(t, ts, "GET", "/api/books/"+b.ID+"/transcript", kid, nil)
	json.Unmarshal(body, &v)
	if len(v.Passages) != 1 || v.Passages[0].Text != "fixed" || v.Chapters[0].Engine != "app" {
		t.Fatalf("transcript: %s", body)
	}
	if res, _ := call(t, ts, "PUT", "/api/books/"+b.ID+"/transcript/chapters/0", kid, map[string]any{
		"status": statusDone, "model": "x", "passages": []Passage{{StartMs: 999999, EndMs: 1000000, Text: "far"}}}); res.StatusCode != 400 {
		t.Fatalf("a passage outside its chapter: %d", res.StatusCode)
	}
}
