package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Notifications: webhooks for anyone handy. When something happens (a book arrives, a
// transcription or a backup finishes or fails, a file job is done), the server POSTs it
// to each hook that asked for that event: as JSON for scripts, Home Assistant, n8n and
// the like, or ready-made for ntfy or Discord.
//
// JSON hooks get {"event", "at", "server", "message", "data"}.

const (
	eventBookAdded         = "book.added"
	eventTranscriptionDone = "transcription.finished"
	eventTranscriptionFail = "transcription.failed"
	eventBackupDone        = "backup.finished"
	eventBackupFailed      = "backup.failed"
	eventFilesDone         = "files.finished"
	eventFilesFailed       = "files.failed"
	eventScanProblems      = "scan.problems"
	eventTest              = "test"
)

var hookEvents = []string{eventBookAdded, eventTranscriptionDone, eventTranscriptionFail, eventBackupDone, eventBackupFailed,
	eventFilesDone, eventFilesFailed, eventScanProblems}

type Hook struct {
	ID     string   `json:"id"`
	Name   string   `json:"name,omitempty"`
	URL    string   `json:"url"`
	Format string   `json:"format"` // json, ntfy or discord
	Events []string `json:"events"` // empty: all of them
}

type hookEvent struct {
	Event   string         `json:"event"`
	At      int64          `json:"at"`
	Server  string         `json:"server"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type hooks struct {
	mu     sync.Mutex
	path   string
	list   []Hook
	queue  chan delivery
	client *http.Client
}

type delivery struct {
	hook Hook
	ev   hookEvent
}

func newHooks(dir string) *hooks {
	h := &hooks{path: filepath.Join(dir, "hooks.json"), queue: make(chan delivery, 256),
		client: &http.Client{Timeout: 10 * time.Second}}
	if err := readJSON(h.path, &h.list); err != nil {
		log.Printf("hooks.json: %v", err)
	}
	go h.deliver()
	return h
}

// notify sends an event to the hooks that want it. It never waits on the network.
func (s *server) notify(event, message string, data map[string]any) {
	ev := hookEvent{Event: event, At: time.Now().UnixMilli(), Server: s.serverName(), Message: message, Data: data}
	h := s.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, hk := range h.list {
		if len(hk.Events) > 0 && !slices.Contains(hk.Events, event) && event != eventTest {
			continue
		}
		select {
		case h.queue <- delivery{hk, ev}:
		default:
			log.Printf("hook %s: too many waiting; dropped %s", hk.URL, event)
		}
	}
}

// deliver posts events one at a time, trying each three times.
func (h *hooks) deliver() {
	for d := range h.queue {
		var err error
		for try, wait := 0, 2*time.Second; try < 3; try, wait = try+1, wait*4 {
			if err = h.post(d); err == nil {
				break
			}
			time.Sleep(wait)
		}
		if err != nil {
			log.Printf("hook %s (%s): %v", d.hook.URL, d.ev.Event, err)
		}
	}
}

func (h *hooks) post(d delivery) error {
	var body []byte
	headers := map[string]string{"Content-Type": "application/json", "User-Agent": "kithara-server/" + serverVersion}
	switch d.hook.Format {
	case "ntfy":
		body = []byte(d.ev.Message)
		headers["Content-Type"] = "text/plain; charset=utf-8"
		headers["Title"] = d.ev.Server + ": " + d.ev.Event
		headers["Tags"] = "headphones"
	case "discord":
		body, _ = json.Marshal(map[string]string{"content": "**" + d.ev.Server + "**: " + d.ev.Message})
	default:
		body, _ = json.Marshal(d.ev)
	}
	req, err := http.NewRequest("POST", d.hook.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("answered %s", res.Status)
	}
	return nil
}

// ------------------------------------------------------------------ routes (admin)

func (s *server) listHooks(w http.ResponseWriter, r *http.Request, user, token string) {
	h := s.hooks
	h.mu.Lock()
	list := append([]Hook{}, h.list...)
	h.mu.Unlock()
	writeJSONResponse(w, 200, map[string]any{"hooks": list, "events": hookEvents})
}

func (s *server) addHook(w http.ResponseWriter, r *http.Request, user, token string) {
	var in Hook
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "send {url, format, events}")
		return
	}
	in.URL, in.Name = strings.TrimSpace(in.URL), clip(strings.TrimSpace(in.Name), 60)
	if u, err := url.Parse(in.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeError(w, http.StatusBadRequest, "a hook is an http:// or https:// address")
		return
	}
	if in.Format == "" {
		in.Format = "json"
	}
	if !slices.Contains([]string{"json", "ntfy", "discord"}, in.Format) {
		writeError(w, http.StatusBadRequest, "the format is json, ntfy or discord")
		return
	}
	for _, e := range in.Events {
		if !slices.Contains(hookEvents, e) {
			writeError(w, http.StatusBadRequest, "no such event: "+e)
			return
		}
	}
	in.ID = newID()
	h := s.hooks
	h.mu.Lock()
	if len(h.list) >= 20 {
		h.mu.Unlock()
		writeError(w, http.StatusBadRequest, "20 hooks at most")
		return
	}
	list := append(append([]Hook{}, h.list...), in)
	err := writeJSON(h.path, list)
	if err == nil {
		h.list = list
	}
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusCreated, in)
}

func (s *server) deleteHook(w http.ResponseWriter, r *http.Request, user, token string) {
	h := s.hooks
	h.mu.Lock()
	list := slices.DeleteFunc(append([]Hook{}, h.list...), func(hk Hook) bool { return hk.ID == r.PathValue("id") })
	err := writeJSON(h.path, list)
	if err == nil {
		h.list = list
	}
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testHook sends a test event to one hook, straight away, and says how it went.
func (s *server) testHook(w http.ResponseWriter, r *http.Request, user, token string) {
	h := s.hooks
	h.mu.Lock()
	i := slices.IndexFunc(h.list, func(hk Hook) bool { return hk.ID == r.PathValue("id") })
	var hk Hook
	if i >= 0 {
		hk = h.list[i]
	}
	h.mu.Unlock()
	if i < 0 {
		writeError(w, http.StatusNotFound, "no such hook")
		return
	}
	ev := hookEvent{Event: eventTest, At: time.Now().UnixMilli(), Server: s.serverName(), Message: "A test from " + s.serverName() + ". Notifications work."}
	if err := h.post(delivery{hk, ev}); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
