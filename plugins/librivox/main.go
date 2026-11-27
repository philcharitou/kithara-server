// Command kithara-plugin-librivox is a Kithara server plugin: it searches LibriVox's
// public-domain audiobooks (through the Internet Archive, which keeps them) and brings
// them into a library. It is also the worked example of a plugin; see
// docs/writing-a-server-plugin.md in the server's repository.
//
// It needs a secret, the same one the admin types in Settings → Plugins, and the
// incoming folder it shares with the server:
//
//	KITHARA_PLUGIN_SECRET=…        required
//	KITHARA_INCOMING=/incoming     where the server makes a folder for each download
//	KITHARA_PLUGIN_ADDR=:9000      where it listens
package main

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const version = "1.0.0"

type plugin struct {
	secret   string
	incoming string
	archive  *archive
	dl       *downloader
}

func main() {
	p := &plugin{secret: os.Getenv("KITHARA_PLUGIN_SECRET"), incoming: envOr("KITHARA_INCOMING", "/incoming"),
		archive: newArchive(envOr("ARCHIVE_URL", "https://archive.org"))}
	if len(p.secret) < 16 {
		log.Fatal("set KITHARA_PLUGIN_SECRET to a secret of 16 characters or more (and give the server the same one)")
	}
	p.dl = newDownloader(p.archive, p.incoming)
	addr := envOr("KITHARA_PLUGIN_ADDR", ":9000")
	log.Printf("kithara-plugin-librivox %s on %s, downloading into %s", version, addr, p.incoming)
	srv := &http.Server{Addr: addr, Handler: p.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (p *plugin) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /kithara/plugin", p.info)
	m.HandleFunc("POST /kithara/search", p.search)
	m.HandleFunc("POST /kithara/preview", p.preview)
	m.HandleFunc("POST /kithara/download", p.download)
	m.HandleFunc("GET /kithara/downloads/{id}", p.status)
	m.HandleFunc("DELETE /kithara/downloads/{id}", p.forget)
	return p.auth(m)
}

// auth lets in only the server, which sends the shared secret with every call.
func (p *plugin) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(p.secret)) != 1 {
			fail(w, http.StatusUnauthorized, "wrong secret")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// request is what the server sends with every call about a search or a download.
type request struct {
	Query      string            `json:"query"`
	Page       int               `json:"page"`
	ID         string            `json:"id"`
	DownloadID string            `json:"downloadId"`
	Folder     string            `json:"folder"`
	Settings   map[string]string `json:"settings"`
}

func (p *plugin) info(w http.ResponseWriter, r *http.Request) {
	names := []string{}
	for _, l := range languages {
		names = append(names, l.Name)
	}
	reply(w, map[string]any{
		"name": "LibriVox", "version": version, "apiVersion": 1, "downloads": true,
		"search": map[string]any{
			"name":            "LibriVox",
			"description":     "Free public-domain audiobooks, read by volunteers.",
			"emptyQueryTitle": "Recently added",
		},
		"settings": []map[string]any{
			{"id": "format", "title": "Download as", "type": "choice", "choices": []string{formatM4B, formatMP3, formatBest}, "default": formatM4B},
			{"id": "language", "title": "Language", "type": "choice", "choices": names, "default": "Any",
				"hint": "Show only recordings in this language."},
		},
	})
}

func (p *plugin) search(w http.ResponseWriter, r *http.Request) {
	var in request
	if !decode(w, r, &in) {
		return
	}
	results, total, err := p.archive.search(r.Context(), in.Query, in.Settings["language"], max(in.Page, 0))
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	reply(w, map[string]any{"results": results, "total": total, "hasMore": (in.Page+1)*perPage < total})
}

var archiveID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func (p *plugin) preview(w http.ResponseWriter, r *http.Request) {
	var in request
	if !decode(w, r, &in) {
		return
	}
	if !archiveID.MatchString(in.ID) {
		fail(w, http.StatusNotFound, "no such recording")
		return
	}
	it, err := p.archive.item(r.Context(), in.ID)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	files, format := it.audio(in.Settings["format"])
	var size int64
	for _, f := range files {
		size += f.bytes()
	}
	md := it.Metadata
	description := first(md.Description)
	reply(w, map[string]any{
		"id": md.Identifier, "title": md.Title, "author": first(md.Creator), "narrator": reader(description),
		"cover":         p.archive.base + "/services/img/" + md.Identifier,
		"description":   withoutFooter(description),
		"lengthSeconds": runtime(md.Runtime), "files": len(files), "sizeBytes": size, "format": format,
		"language": languageName(first(md.Language)),
	})
}

var folderName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (p *plugin) download(w http.ResponseWriter, r *http.Request) {
	var in request
	if !decode(w, r, &in) {
		return
	}
	switch {
	case !archiveID.MatchString(in.ID):
		reply(w, map[string]any{"ok": false, "error": "no such recording"})
	case !folderName.MatchString(in.DownloadID) || !folderName.MatchString(in.Folder):
		reply(w, map[string]any{"ok": false, "error": "the download id or folder is not one this plugin can use"})
	default:
		if err := p.dl.start(in.DownloadID, in.Folder, in.ID, in.Settings["format"]); err != nil {
			reply(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		reply(w, map[string]any{"ok": true})
	}
}

func (p *plugin) status(w http.ResponseWriter, r *http.Request) {
	reply(w, p.dl.status(r.PathValue("id")))
}

func (p *plugin) forget(w http.ResponseWriter, r *http.Request) {
	p.dl.forget(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------ helpers

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "send JSON")
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
