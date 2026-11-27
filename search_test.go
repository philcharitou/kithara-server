package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// searchPlugin is a plugin that searches: two good results and two it should not pass on.
type searchPlugin struct {
	mu        sync.Mutex
	asked     []map[string]any
	coverHits int
	url       string
}

func (f *searchPlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/cover.png" {
		f.coverHits++
		var b bytes.Buffer
		png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4)))
		w.Write(b.Bytes())
		return
	}
	if r.Header.Get("Authorization") != "Bearer pw" {
		w.WriteHeader(401)
		return
	}
	var in map[string]any
	json.NewDecoder(r.Body).Decode(&in)
	switch r.URL.Path {
	case "/kithara/plugin":
		json.NewEncoder(w).Encode(map[string]any{"name": "Old books", "apiVersion": 1,
			"search":   map[string]any{"name": "Old books", "emptyQueryTitle": "New this week"},
			"settings": []map[string]any{{"id": "lang", "type": "text", "default": "en"}, {"id": "key", "type": "secret"}}})
	case "/kithara/search":
		f.asked = append(f.asked, in)
		json.NewEncoder(w).Encode(map[string]any{"total": 4, "hasMore": true, "results": []map[string]any{
			{"id": "a/1", "title": "<b>Pride</b> &amp; Prejudice\n", "author": "Jane Austen", "cover": f.url + "/cover.png", "year": 1813},
			{"id": "b", "title": "Emma", "cover": "javascript:alert(1)"},
			{"id": "", "title": "no id"},
			{"id": "c", "title": "   "},
		}})
	case "/kithara/preview":
		f.asked = append(f.asked, in)
		json.NewEncoder(w).Encode(map[string]any{"id": in["id"], "title": "Pride and Prejudice", "lengthSeconds": -5, "files": 61,
			"description": "<p>First paragraph.</p><p>Second<br>line.</p><script>x</script>"})
	default:
		http.NotFound(w, r)
	}
}

func TestPluginSearch(t *testing.T) {
	fake := &searchPlugin{}
	plugin := httptest.NewServer(fake)
	defer plugin.Close()
	fake.url = plugin.URL
	ts, srv, _, _ := twoLibraryServer(t)
	admin := loginAs(t, ts, "phil", "correct horse", "laptop")
	kid := loginAs(t, ts, "kid", "battery staple", "tablet")

	var health struct{ Features []string }
	_, body := call(t, ts, "GET", "/api/health", "", nil)
	json.Unmarshal(body, &health)
	if !slices.Contains(health.Features, "pluginSearch") {
		t.Fatalf("health: %s", body)
	}
	sources := func(token string) []SearchSourceOut {
		var out struct{ Sources []SearchSourceOut }
		_, body := call(t, ts, "GET", "/api/search/sources", token, nil)
		json.Unmarshal(body, &out)
		return out.Sources
	}
	if got := sources(kid); len(got) != 0 {
		t.Fatalf("sources before any plugin: %+v", got)
	}
	res, body := call(t, ts, "POST", "/api/plugins", admin, map[string]string{"url": plugin.URL, "secret": "pw"})
	if res.StatusCode != 201 {
		t.Fatalf("add: %s", body)
	}
	var added webPlugin
	json.Unmarshal(body, &added)
	id := added.Plugin.ID
	call(t, ts, "PATCH", "/api/plugins/"+id, admin, map[string]any{"sendUser": true, "settings": map[string]string{"key": "k"}})

	got := sources(kid)
	if len(got) != 1 || got[0].ID != id || got[0].EmptyQueryTitle != "New this week" || got[0].CanDownload {
		t.Fatalf("sources: %+v", got)
	}
	res, body = call(t, ts, "GET", "/api/search?source="+id+"&q=pride&page=1", kid, nil)
	if res.StatusCode != 200 {
		t.Fatalf("search: %d %s", res.StatusCode, body)
	}
	var page SearchPage
	json.Unmarshal(body, &page)
	if len(page.Results) != 2 || page.Total != 4 || !page.HasMore {
		t.Fatalf("page: %s", body)
	}
	first, second := page.Results[0], page.Results[1]
	if first.Title != "Pride & Prejudice" || first.Year != 1813 || !strings.HasPrefix(first.Cover, "/api/search/"+id+"/cover?") || second.Cover != "" {
		t.Fatalf("results: %+v", page.Results)
	}
	fake.mu.Lock()
	asked := fake.asked[0]
	fake.mu.Unlock()
	settings, _ := asked["settings"].(map[string]any)
	if asked["query"] != "pride" || asked["page"] != float64(1) || asked["user"] != "kid" || settings["lang"] != "en" || settings["key"] != "k" {
		t.Fatalf("the plugin was asked: %v", asked)
	}

	// Covers: fetched once through the server, and only with the server's signature.
	for range 2 {
		res, body = call(t, ts, "GET", first.Cover, kid, nil)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
			t.Fatalf("cover: %d %s %q", res.StatusCode, res.Header.Get("Content-Type"), body)
		}
	}
	if fake.coverHits != 1 {
		t.Fatalf("the cover was fetched %d times", fake.coverHits)
	}
	// A cover on the local network, other than at the plugin's own address, is refused.
	inwardCover := srv.coverLink(id, "http://10.0.0.1/router.png")
	if res, body := call(t, ts, "GET", inwardCover, kid, nil); res.StatusCode != 502 || !strings.Contains(string(body), "local network") {
		t.Fatalf("a cover on the local network: %d %s", res.StatusCode, body)
	}
	forged := "/api/search/" + id + "/cover?" + url.Values{"u": {"http://127.0.0.1:1/secret"}, "sig": {"00"}}.Encode()
	if res, _ := call(t, ts, "GET", forged, kid, nil); res.StatusCode != 404 {
		t.Fatalf("a forged cover address was fetched: %d", res.StatusCode)
	}

	// The preview: paragraphs kept, markup gone, nonsense numbers dropped; ids may hold "/".
	res, body = call(t, ts, "GET", "/api/search/"+id+"/results/"+url.PathEscape("a/1"), kid, nil)
	var pv SearchPreview
	json.Unmarshal(body, &pv)
	if res.StatusCode != 200 || pv.ID != "a/1" || pv.Description != "First paragraph.\n\nSecond\n\nline." || pv.LengthSeconds != 0 || pv.Files != 61 {
		t.Fatalf("preview: %d %s", res.StatusCode, body)
	}

	// Admins only: listeners no longer see it, or reach it.
	call(t, ts, "PATCH", "/api/plugins/"+id, admin, map[string]any{"search": allowAdmins})
	if got := sources(kid); len(got) != 0 {
		t.Fatalf("a listener still sees an admins-only source: %+v", got)
	}
	if res, _ := call(t, ts, "GET", "/api/search?source="+id+"&q=x", kid, nil); res.StatusCode != 404 {
		t.Fatalf("a listener searched an admins-only source: %d", res.StatusCode)
	}
	if res, _ := call(t, ts, "GET", first.Cover, kid, nil); res.StatusCode != 404 {
		t.Fatalf("a listener fetched an admins-only source's cover: %d", res.StatusCode)
	}
	if got := sources(admin); len(got) != 1 {
		t.Fatalf("admin sources: %+v", got)
	}
	// A plugin that stops answering drops out of the sources, and a search says why.
	plugin.Close()
	srv.plugins.recheck(id)
	if got := sources(admin); len(got) != 0 {
		t.Fatalf("an unreachable source is still listed: %+v", got)
	}
	if res, body := call(t, ts, "GET", "/api/search?source="+id+"&q=x", admin, nil); res.StatusCode != 502 || !strings.Contains(string(body), "Old books didn't answer") {
		t.Fatalf("search of a stopped plugin: %d %s", res.StatusCode, body)
	}
}

func TestPlainText(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"  <i>A</i>\t&quot;B&quot;\x00 ", 50, `A "B"`},
		{"line\none", 50, "line one"},
		{"a<style>p{}</style><SCRIPT src=x>evil()</script>b", 50, "a b"},
		{strings.Repeat("é", 10), 3, "ééé"},
	} {
		if got := plain(c.in, c.n); got != c.want {
			t.Errorf("plain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
