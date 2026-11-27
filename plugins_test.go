package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakePlugin answers /kithara/plugin with info, only to the right secret.
type fakePlugin struct {
	mu     sync.Mutex
	info   any
	secret string
	status int
	calls  int
}

func (f *fakePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	switch {
	case r.Header.Get("Authorization") != "Bearer "+f.secret:
		http.Error(w, `{"error":"wrong secret"}`, http.StatusUnauthorized)
	case f.status != 0:
		w.WriteHeader(f.status)
	case r.URL.Path == "/kithara/plugin":
		json.NewEncoder(w).Encode(f.info)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakePlugin) set(change func(*fakePlugin)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

type webPlugin struct {
	Plugin       Plugin
	HasSecret    bool
	Settings     map[string]string
	SavedSecrets []string
}

func TestPlugins(t *testing.T) {
	fake := &fakePlugin{secret: "s3cret", info: map[string]any{
		"name": "Public domain", "version": "1.0.0", "apiVersion": 3,
		"search":    map[string]any{"name": "Old books", "description": "Books whose copyright ran out"},
		"downloads": true, "accepts": []string{"https"},
		"settings": []map[string]any{
			{"id": "language", "title": "Language", "type": "choice", "choices": []string{"en", "fr"}},
			{"id": "apiKey", "title": "API key", "type": "secret"},
		},
	}}
	plugin := httptest.NewServer(fake)
	defer plugin.Close()
	ts, srv, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")

	if res, _ := call(t, ts, "GET", "/api/plugins", kid, nil); res.StatusCode != 403 {
		t.Fatalf("a listener saw the plugins: %d", res.StatusCode)
	}
	for _, in := range []map[string]string{
		{"url": "ftp://x"},
		{"url": plugin.URL, "secret": "wrong"},
		{"url": plugin.URL + "/nothing-here", "secret": "s3cret"},
	} {
		if res, body := call(t, ts, "POST", "/api/plugins", admin, in); res.StatusCode < 400 {
			t.Fatalf("%v was added: %s", in, body)
		}
	}
	res, body := call(t, ts, "POST", "/api/plugins", admin, map[string]string{"url": plugin.URL + "/", "secret": "s3cret"})
	if res.StatusCode != 201 {
		t.Fatalf("add: %d %s", res.StatusCode, body)
	}
	if strings.Contains(string(body), "s3cret") {
		t.Fatalf("the secret came back: %s", body)
	}
	var added webPlugin
	json.Unmarshal(body, &added)
	pl := added.Plugin
	if !added.HasSecret || pl.URL != plugin.URL || !pl.Enabled || pl.Search != allowEveryone || pl.Download != allowAdmins ||
		pl.Info == nil || pl.Info.APIVersion != pluginAPIVersion || pl.Info.Search.Name != "Old books" || len(pl.Info.Settings) != 2 {
		t.Fatalf("added: %+v %+v", added, pl.Info)
	}
	if res, _ := call(t, ts, "POST", "/api/plugins", admin, map[string]string{"url": plugin.URL, "secret": "s3cret"}); res.StatusCode != 400 {
		t.Fatalf("the same plugin was added twice: %d", res.StatusCode)
	}

	// Settings: only the plugin's own, choices within the choices, secrets kept back.
	if res, _ := call(t, ts, "PATCH", "/api/plugins/"+pl.ID, admin, map[string]any{"settings": map[string]string{"language": "de"}}); res.StatusCode != 400 {
		t.Fatalf("a choice outside the choices was kept: %d", res.StatusCode)
	}
	if _, err := checkSettings(&PluginInfo{Settings: []PluginSetting{{ID: "n", Title: "N", Type: "number"}}}, nil, map[string]string{"n": "lots"}); err == nil {
		t.Fatal("a number setting kept words")
	}
	res, body = call(t, ts, "PATCH", "/api/plugins/"+pl.ID, admin, map[string]any{"search": allowAdmins,
		"settings": map[string]string{"language": "fr", "apiKey": "k-123", "other": "dropped"}})
	if res.StatusCode != 200 || strings.Contains(string(body), "k-123") {
		t.Fatalf("patch: %d %s", res.StatusCode, body)
	}
	var patched webPlugin
	json.Unmarshal(body, &patched)
	if patched.Plugin.Search != allowAdmins || patched.Settings["language"] != "fr" || len(patched.SavedSecrets) != 1 || patched.Settings["other"] != "" {
		t.Fatalf("patched: %s", body)
	}
	// An empty secret setting keeps the saved one.
	call(t, ts, "PATCH", "/api/plugins/"+pl.ID, admin, map[string]any{"settings": map[string]string{"language": "en", "apiKey": ""}})
	if got, _ := srv.plugins.get(pl.ID); got.Settings["apiKey"] != "k-123" || got.Settings["language"] != "en" || got.Secret != "s3cret" {
		t.Fatalf("stored: %+v", got)
	}

	// A plugin that stops answering keeps its last description and says why.
	fake.set(func(f *fakePlugin) { f.status = 500 })
	_, body = call(t, ts, "POST", "/api/plugins/"+pl.ID+"/check", admin, nil)
	var checked webPlugin
	json.Unmarshal(body, &checked)
	if checked.Plugin.Error == "" || checked.Plugin.Info == nil {
		t.Fatalf("check: %s", body)
	}
	// A new secret is checked straight away.
	fake.set(func(f *fakePlugin) { f.status, f.secret = 0, "new" })
	_, body = call(t, ts, "PATCH", "/api/plugins/"+pl.ID, admin, map[string]any{"secret": "new"})
	var rechecked webPlugin
	json.Unmarshal(body, &rechecked)
	if rechecked.Plugin.Error != "" || rechecked.Plugin.Checked <= checked.Plugin.Checked {
		t.Fatalf("after the new secret: %s", body)
	}

	if res, _ := call(t, ts, "DELETE", "/api/plugins/"+pl.ID, admin, nil); res.StatusCode != 204 {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	_, body = call(t, ts, "GET", "/api/plugins", admin, nil)
	var list struct{ Plugins []webPlugin }
	json.Unmarshal(body, &list)
	if len(list.Plugins) != 0 {
		t.Fatalf("after delete: %s", body)
	}
}

func TestPluginInfoChecks(t *testing.T) {
	src := &PluginSource{Name: "x"}
	for name, in := range map[string]PluginInfo{
		"no name":        {APIVersion: 1, Search: src},
		"no version":     {Name: "a", Search: src},
		"offers nothing": {Name: "a", APIVersion: 1},
		"bad kind":       {Name: "a", APIVersion: 1, Downloads: true, Accepts: []string{"Has Space"}},
		"accepts, no dl": {Name: "a", APIVersion: 1, Search: src, Accepts: []string{"https"}},
		"bad setting id": {Name: "a", APIVersion: 1, Search: src, Settings: []PluginSetting{{ID: "a b", Type: "text"}}},
		"bad type":       {Name: "a", APIVersion: 1, Search: src, Settings: []PluginSetting{{ID: "a", Type: "script"}}},
		"empty choice":   {Name: "a", APIVersion: 1, Search: src, Settings: []PluginSetting{{ID: "a", Type: "choice"}}},
		"same id twice":  {Name: "a", APIVersion: 1, Search: src, Settings: []PluginSetting{{ID: "a", Type: "text"}, {ID: "a", Type: "text"}}},
	} {
		if _, err := checkPluginInfo(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	got, err := checkPluginInfo(PluginInfo{Name: "  " + strings.Repeat("n", 100), APIVersion: 1, Search: &PluginSource{}})
	if err != nil || len([]rune(got.Name)) != 80 || got.Search.Name != got.Name {
		t.Fatalf("trimmed: %+v %v", got, err)
	}
}
