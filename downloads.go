package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Downloads: bringing a search result into a library (docs/server-plugins.md). The
// server makes a folder for the download in the incoming folder, asks the plugin to fill
// it, and asks how it is going every few seconds while one is running. When the plugin
// says it is done, the server checks the files, moves them into the library as
// Author/Title, rescans, and tells the plugin to forget it. Only the server writes into
// library folders.

const (
	dlQueued      = "QUEUED"
	dlDownloading = "DOWNLOADING"
	dlPaused      = "PAUSED"
	dlMoving      = "MOVING" // the server is moving the files into the library
	dlFinished    = "FINISHED"
	dlFailed      = "FAILED"

	maxListenerActive   = 3 // downloads a listener may have running at once
	maxDownloadsKept    = 200
	maxIncomingBytes    = 50 << 30
	incomingKeptOnError = 24 * time.Hour

	eventDownloadDone   = "download.finished"
	eventDownloadFailed = "download.failed"
)

// downloadPoll is how often running downloads are asked about (shorter in tests).
var downloadPoll = 3 * time.Second

// downloadIDRe is the name of a folder the server made in the incoming folder; nothing
// else there is ever removed.
var downloadIDRe = regexp.MustCompile(`^d_[0-9a-f]{12}$`)

type Download struct {
	ID       string `json:"id"`
	User     string `json:"user"`
	Source   string `json:"source"`         // the plugin searched
	Plugin   string `json:"plugin"`         // the plugin downloading: the source's, or one it handed a link to
	ResultID string `json:"resultId"`       // the result's id at the source
	Link     string `json:"link,omitempty"` // when handed off
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Library  string `json:"library"`
	// The rest of the result's details, written beside the files (metadata.json) so the
	// book is named as the user saw it, whatever the files' tags say.
	Narrator    string `json:"narrator,omitempty"`
	Year        int    `json:"year,omitempty"`
	Language    string `json:"language,omitempty"`
	Description string `json:"description,omitempty"`

	State          string   `json:"state"`
	Progress       float64  `json:"progress"`
	BytesPerSecond int64    `json:"bytesPerSecond,omitempty"`
	SizeBytes      int64    `json:"sizeBytes,omitempty"`
	Error          string   `json:"error,omitempty"`
	Unavailable    bool     `json:"unavailable,omitempty"` // the plugin isn't answering
	Skipped        []string `json:"skipped,omitempty"`     // files left out of the library
	BookID         string   `json:"bookId,omitempty"`
	CreatedAt      int64    `json:"createdAt"`
	UpdatedAt      int64    `json:"updatedAt"`
}

func (d Download) active() bool {
	return d.State == dlQueued || d.State == dlDownloading || d.State == dlPaused
}

type downloads struct {
	s        *server
	mu       sync.Mutex
	path     string
	list     []Download
	incoming string
	wake     chan struct{}
	moving   map[string]bool
	starting map[string]int // per user: downloads being started (the plugin is being asked)
	intakeMu sync.Mutex     // one move into a library, and its rescan, at a time
}

func newDownloads(s *server) *downloads {
	d := &downloads{s: s, path: filepath.Join(s.store.dir, "downloads.json"), wake: make(chan struct{}, 1), moving: map[string]bool{}, starting: map[string]int{},
		incoming: envOr("KITHARA_INCOMING", filepath.Join(s.store.dir, "incoming"))}
	if err := readJSON(d.path, &d.list); err != nil {
		log.Printf("downloads.json: %v", err)
	}
	return d
}

func (d *downloads) poke() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// change updates one download and saves the list; false if it is gone.
func (d *downloads) change(id string, f func(*Download)) (Download, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.list, func(x Download) bool { return x.ID == id })
	if i < 0 {
		return Download{}, false
	}
	f(&d.list[i])
	d.list[i].UpdatedAt = time.Now().UnixMilli()
	if err := writeJSONCompact(d.path, d.list); err != nil {
		log.Printf("downloads.json: %v", err)
	}
	return d.list[i], true
}

func (d *downloads) get(id string) (Download, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.list, func(x Download) bool { return x.ID == id })
	if i < 0 {
		return Download{}, false
	}
	return d.list[i], true
}

// run asks the plugins about running downloads every few seconds, and sleeps while
// there are none.
func (d *downloads) run() {
	for {
		if d.pollOnce() {
			time.Sleep(downloadPoll)
			continue
		}
		d.trimIncoming()
		select {
		case <-d.wake:
		case <-time.After(time.Hour):
		}
	}
}

// pollOnce asks about each running download once; it says whether any is still running.
func (d *downloads) pollOnce() bool {
	d.mu.Lock()
	list := slices.Clone(d.list)
	d.mu.Unlock()
	busy := false
	for _, dl := range list {
		switch {
		case dl.State == dlMoving:
			busy = true
			d.startIntake(dl.ID)
		case dl.active():
			busy = true
			d.poll(dl)
		}
	}
	return busy
}

func (d *downloads) poll(dl Download) {
	pl, ok := d.s.plugins.get(dl.Plugin)
	if !ok {
		d.fail(dl.ID, "the plugin doing it was removed")
		return
	}
	var st struct {
		State          string  `json:"state"`
		Progress       float64 `json:"progress"`
		BytesPerSecond int64   `json:"bytesPerSecond"`
		SizeBytes      int64   `json:"sizeBytes"`
		Error          string  `json:"error"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()
	err := d.s.plugins.call(ctx, pl, "GET", "/kithara/downloads/"+url.PathEscape(dl.ID), nil, &st)
	if err != nil {
		d.change(dl.ID, func(x *Download) { x.Unavailable = true })
		return
	}
	switch st.State {
	case dlFailed:
		d.fail(dl.ID, "the plugin says: "+plain(st.Error, 300))
		d.forget(dl)
	case dlFinished:
		d.change(dl.ID, func(x *Download) { x.State, x.Progress, x.BytesPerSecond, x.Unavailable = dlMoving, 1, 0, false })
		d.startIntake(dl.ID)
	case dlQueued, dlDownloading, dlPaused:
		d.change(dl.ID, func(x *Download) {
			x.State, x.Unavailable = st.State, false
			x.Progress = min(max(st.Progress, 0), 1)
			x.BytesPerSecond, x.SizeBytes = max(st.BytesPerSecond, 0), max(st.SizeBytes, 0)
		})
	default:
		d.change(dl.ID, func(x *Download) { x.Unavailable = true })
		log.Printf("plugin %s: download %s: unknown state %q", pl.URL, dl.ID, clip(st.State, 40))
	}
}

func (d *downloads) fail(id, why string) {
	dl, ok := d.change(id, func(x *Download) { x.State, x.Error, x.BytesPerSecond, x.Unavailable = dlFailed, why, 0, false })
	if ok {
		d.s.notify(eventDownloadFailed, fmt.Sprintf("Could not bring %s into %s: %s", dl.Title, dl.Library, why),
			map[string]any{"downloadId": dl.ID, "title": dl.Title, "library": dl.Library, "user": dl.User, "error": why})
	}
}

// forget tells the plugin to drop a download and its files; failures don't matter.
func (d *downloads) forget(dl Download) {
	if pl, ok := d.s.plugins.get(dl.Plugin); ok {
		ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
		defer cancel()
		if err := d.s.plugins.call(ctx, pl, "DELETE", "/kithara/downloads/"+url.PathEscape(dl.ID), nil, nil); err != nil {
			log.Printf("plugin %s: forgetting download %s: %v", pl.URL, dl.ID, err)
		}
	}
}

// ------------------------------------------------------------------ into the library

func (d *downloads) startIntake(id string) {
	d.mu.Lock()
	if d.moving[id] {
		d.mu.Unlock()
		return
	}
	d.moving[id] = true
	d.mu.Unlock()
	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.moving, id)
			d.mu.Unlock()
		}()
		d.intake(id)
	}()
}

func (d *downloads) intake(id string) {
	d.intakeMu.Lock()
	defer d.intakeMu.Unlock()
	dl, ok := d.get(id)
	if !ok || dl.State != dlMoving {
		return
	}
	root, ok := d.s.lib.catalogueRoot(dl.Library)
	if !ok {
		d.fail(id, "the library "+dl.Library+" is gone")
		return
	}
	// The files gather in a hidden folder in the library (the scanner skips those, and it
	// is on the same disk), which becomes the book's folder in one rename: a scan never
	// sees half a book. A restart in the middle carries on where it was.
	src, staging := filepath.Join(d.incoming, dl.ID), filepath.Join(root.path, ".kithara-incoming-"+dl.ID)
	_, err := os.Stat(staging)
	resumed := err == nil
	files, skipped, audio, err := collectIncoming(src)
	switch {
	case errors.Is(err, fs.ErrNotExist) && resumed:
		err = nil // all of it was moved before the restart
	case errors.Is(err, fs.ErrNotExist):
		err = errors.New("the plugin left no files")
	case err == nil && !audio && !resumed:
		err = errors.New("the download holds no audio files")
	}
	if err != nil {
		d.fail(id, err.Error())
		return
	}
	undo := func(why string) {
		os.RemoveAll(staging)
		d.fail(id, why)
	}
	for _, rel := range files {
		if err := moveFile(filepath.Join(src, rel), filepath.Join(staging, rel)); err != nil {
			undo("moving the files into the library: " + err.Error())
			return
		}
	}
	if !hasMetadataFile(staging) {
		if err := writeJSON(filepath.Join(staging, "metadata.json"), dl.metadata()); err != nil {
			log.Printf("download %s: metadata.json: %v", id, err)
		}
	}
	dest := filepath.Join(root.path, folderName(dl.Title, "Untitled"))
	if dl.Author != "" {
		dest = filepath.Join(root.path, folderName(dl.Author, "Unknown author"), folderName(dl.Title, "Untitled"))
	}
	dest = freeName(dest)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		undo("making the author's folder: " + err.Error())
		return
	}
	if err := os.Rename(staging, dest); err != nil {
		undo("naming the book's folder: " + err.Error())
		return
	}
	if err := d.s.lib.rescan(); err != nil {
		log.Printf("rescan after download %s: %v", id, err)
	}
	bookID := d.s.lib.bookInDir(dl.Library, dest)
	d.forget(dl)
	if err := os.RemoveAll(src); err != nil {
		log.Printf("incoming %s: %v", src, err)
	}
	dl, _ = d.change(id, func(x *Download) { x.State, x.BookID, x.Skipped, x.Error = dlFinished, bookID, skipped, "" })
	d.s.notify(eventDownloadDone, fmt.Sprintf("%s is in %s", dl.Title, dl.Library),
		map[string]any{"downloadId": dl.ID, "bookId": bookID, "title": dl.Title, "library": dl.Library, "user": dl.User, "skipped": skipped})
}

// hasMetadataFile says whether a book's folder brings its own details: a metadata.json
// or an .opf beside the audio.
func hasMetadataFile(dir string) bool {
	entries, _ := os.ReadDir(dir)
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool {
		return e.Type().IsRegular() && (strings.EqualFold(e.Name(), "metadata.json") || strings.EqualFold(filepath.Ext(e.Name()), ".opf"))
	})
}

// metadata is the result's details in audiobookshelf's metadata.json, which the scanner
// reads before the files' tags.
func (dl Download) metadata() map[string]any {
	m := map[string]any{"title": dl.Title}
	if dl.Author != "" {
		m["authors"] = []string{dl.Author}
	}
	if dl.Narrator != "" {
		m["narrators"] = []string{dl.Narrator}
	}
	if dl.Year > 0 {
		m["publishedYear"] = strconv.Itoa(dl.Year)
	}
	if dl.Language != "" {
		m["language"] = dl.Language
	}
	if dl.Description != "" {
		m["description"] = dl.Description
	}
	return m
}

// incomingKeep are the files that go into a library beside the audio: covers, and what
// the scanner reads for details and chapters.
var incomingKeep = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".cue": true, ".nfo": true, ".opf": true, ".txt": true, ".json": true}

// collectIncoming lists the files of a download that may go into a library, as paths
// under dir, and those it leaves out, and says whether any is audio. Links, hidden files
// and anything unusual stay out.
func collectIncoming(dir string) (files, skipped []string, audio bool, err error) {
	var total int64
	err = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasPrefix(e.Name(), "."):
			if e.IsDir() {
				return fs.SkipDir
			}
			return nil
		case e.Type()&fs.ModeSymlink != 0:
			skipped = append(skipped, rel+" (a link)")
			return nil
		case e.IsDir():
			if strings.Count(rel, "/") >= 2 {
				skipped = append(skipped, rel+"/ (too deep)")
				return fs.SkipDir
			}
			return nil
		case !e.Type().IsRegular():
			skipped = append(skipped, rel)
			return nil
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		_, isAudio := audioExt[ext]
		if !isAudio && !incomingKeep[ext] {
			skipped = append(skipped, rel)
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); total > maxIncomingBytes {
			return errors.New("the download is over 50 GB")
		}
		audio = audio || isAudio
		files = append(files, filepath.FromSlash(rel))
		return nil
	})
	return files, skipped, audio, err
}

var badNameChars = strings.NewReplacer("/", " ", `\`, " ", ":", " -", "*", " ", "?", "", `"`, "'", "<", "(", ">", ")", "|", " ")

// folderName makes a title or an author safe as a folder name on any system.
func folderName(s, empty string) string {
	s = strings.Trim(plain(badNameChars.Replace(s), 120), " .")
	if s == "" {
		return empty
	}
	return s
}

// freeName is path, or path (2), (3)… when it is taken.
func freeName(path string) string {
	out := path
	for i := 2; ; i++ {
		if _, err := os.Lstat(out); errors.Is(err, fs.ErrNotExist) {
			return out
		}
		out = fmt.Sprintf("%s (%d)", path, i)
	}
}

// moveFile renames, or copies and removes where the incoming folder is another disk.
func moveFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(to)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(to)
		return err
	}
	os.Remove(from) // the plugin's own file; it is told to forget it anyway
	return nil
}

// bookInDir is the book in a library whose folder is dir, or one below it.
func (l *library) bookInDir(library, dir string) string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for id, b := range l.byID {
		if strings.EqualFold(b.Library, library) && (b.dir == dir || strings.HasPrefix(b.dir, dir+string(filepath.Separator))) {
			return id
		}
	}
	return ""
}

// trimIncoming removes folders of downloads that are over and were left behind (after a
// failure they stay a day, so an admin can look).
func (d *downloads) trimIncoming() {
	entries, _ := os.ReadDir(d.incoming)
	for _, e := range entries {
		if !e.IsDir() || !downloadIDRe.MatchString(e.Name()) {
			continue
		}
		dl, ok := d.get(e.Name())
		if ok && (dl.active() || dl.State == dlMoving) {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > incomingKeptOnError {
			os.RemoveAll(filepath.Join(d.incoming, e.Name()))
		}
	}
}

// ------------------------------------------------------------------ routes

// startDownload: {source, id, library}. The server asks the source for the result
// first, for the names the folder takes.
func (s *server) startDownload(w http.ResponseWriter, r *http.Request, user, token string) {
	var in struct{ Source, ID, Library string }
	if err := readBody(r, &in); err != nil || in.Source == "" || in.ID == "" {
		writeError(w, http.StatusBadRequest, "send {source, id, library}")
		return
	}
	a := s.acct(user)
	pl, ok := s.searchable(a, in.Source)
	if !ok {
		writeError(w, http.StatusNotFound, "no such source")
		return
	}
	if !pl.mayDownload(a) {
		writeError(w, http.StatusForbidden, "you can't bring books in from "+pl.Info.Search.Name)
		return
	}
	root, ok := s.lib.catalogueRoot(in.Library)
	if !ok || !a.sees(root.name) {
		writeError(w, http.StatusBadRequest, "no such library")
		return
	}
	d := s.downloads
	d.mu.Lock()
	running := d.starting[user]
	for _, x := range d.list {
		if x.User == user && (x.active() || x.State == dlMoving) {
			running++
		}
	}
	if !a.admin() && running >= maxListenerActive {
		d.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("%d downloads at once at most; wait for one to finish", maxListenerActive))
		return
	}
	d.starting[user]++ // until it is on the list, or refused
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.starting[user]--; d.starting[user] <= 0 {
			delete(d.starting, user)
		}
		d.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), pluginCallTimeout)
	defer cancel()
	var pv SearchPreview
	if err := s.plugins.call(ctx, pl, "POST", "/kithara/preview", pl.request(user, map[string]any{"id": in.ID}), &pv); err != nil {
		writeError(w, http.StatusBadGateway, pl.Info.Search.Name+" didn't answer: "+err.Error())
		return
	}
	dl := Download{ID: "d_" + newID(), User: user, Source: pl.ID, Plugin: pl.ID, ResultID: in.ID, Library: root.name,
		Title: plain(pv.Title, 300), Author: plain(pv.Author, 200), Narrator: plain(pv.Narrator, 200),
		Language: plain(pv.Language, 40), Description: paragraphs(pv.Description, 5000), State: dlQueued}
	if pv.Year > 0 && pv.Year <= 9999 {
		dl.Year = pv.Year
	}
	if dl.Title == "" {
		dl.Title = in.ID
	}
	folder := filepath.Join(d.incoming, dl.ID)
	err := os.MkdirAll(folder, 0o755)
	if err == nil {
		err = os.Chmod(folder, 0o777) // the plugin may write as another user
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the incoming folder: "+err.Error())
		return
	}
	give := func(p Plugin, fields map[string]any) (string, error) {
		fields["downloadId"], fields["folder"] = dl.ID, dl.ID
		var ans struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
			Open  string `json:"open"`
		}
		if err := s.plugins.call(ctx, p, "POST", "/kithara/download", p.request(user, fields), &ans); err != nil {
			return "", errors.New(p.Info.Name + " didn't answer: " + err.Error())
		}
		if !ans.OK {
			return "", errors.New(p.Info.Name + " can't: " + plain(ans.Error, 300))
		}
		return strings.TrimSpace(ans.Open), nil
	}
	link, err := give(pl, map[string]any{"id": in.ID})
	if err == nil && link != "" {
		// A hand-off: a plugin that takes this kind of link downloads it instead.
		next, ok := s.plugins.taker(a, link)
		if !ok {
			err = fmt.Errorf("%s found it as a %s link, and no plugin you may use takes those", pl.Info.Search.Name, linkKind(link))
		} else if _, err = give(next, map[string]any{"link": link}); err == nil {
			dl.Plugin, dl.Link = next.ID, clip(link, 2000)
		}
	}
	if err != nil {
		os.RemoveAll(folder)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	now := time.Now().UnixMilli()
	dl.CreatedAt, dl.UpdatedAt = now, now
	d.mu.Lock()
	d.list = append(d.list, dl)
	if over := len(d.list) - maxDownloadsKept; over > 0 {
		// The oldest that are over go first; running ones are never dropped.
		for i := 0; i < len(d.list) && over > 0; {
			if x := d.list[i]; !x.active() && x.State != dlMoving {
				d.list = slices.Delete(d.list, i, i+1)
				over--
				continue
			}
			i++
		}
	}
	err = writeJSONCompact(d.path, d.list)
	d.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d.poke()
	writeJSONResponse(w, http.StatusCreated, dl)
}

// linkKinds are the kinds a link may be taken as, most particular first: its file
// extension ("zip"), then the part before ":" ("https"), lower-cased.
func linkKinds(link string) []string {
	var kinds []string
	path, scheme := link, ""
	if u, err := url.Parse(link); err == nil {
		path, scheme = u.Path, strings.ToLower(u.Scheme)
	}
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."); ext != "" && linkKindRe.MatchString(ext) {
		kinds = append(kinds, ext)
	}
	if scheme != "" {
		kinds = append(kinds, scheme)
	}
	return kinds
}

// linkKind names a link's kind for a message.
func linkKind(link string) string {
	if kinds := linkKinds(link); len(kinds) > 0 {
		return kinds[0]
	}
	return "unknown"
}

// taker is a plugin this account may download from that takes the link, by its most
// particular kind.
func (p *pluginBox) taker(a Account, link string) (Plugin, bool) {
	list := p.snapshot()
	for _, kind := range linkKinds(link) {
		for _, pl := range list {
			if pl.Enabled && pl.mayDownload(a) && slices.Contains(pl.Info.Accepts, kind) {
				return pl, true
			}
		}
	}
	return Plugin{}, false
}

// listDownloads: the user's, newest first; an admin's: everyone's.
func (s *server) listDownloads(w http.ResponseWriter, r *http.Request, user, token string) {
	admin := s.acct(user).admin()
	d := s.downloads
	d.mu.Lock()
	out := []Download{}
	for _, x := range d.list {
		if admin || x.User == user {
			x.Link, x.Description = "", "" // the apps get no links
			out = append(out, x)
		}
	}
	d.mu.Unlock()
	slices.Reverse(out)
	writeJSONResponse(w, 200, map[string]any{"downloads": out})
}

// cancelDownload stops a running download, or takes one that is over off the list.
func (s *server) cancelDownload(w http.ResponseWriter, r *http.Request, user, token string) {
	d := s.downloads
	dl, ok := d.get(r.PathValue("id"))
	if !ok || (dl.User != user && !s.acct(user).admin()) {
		writeError(w, http.StatusNotFound, "no such download")
		return
	}
	if dl.State == dlMoving {
		writeError(w, http.StatusConflict, "it is being moved into the library")
		return
	}
	if dl.active() {
		d.forget(dl)
	}
	d.mu.Lock()
	d.list = slices.DeleteFunc(d.list, func(x Download) bool { return x.ID == dl.ID })
	err := writeJSONCompact(d.path, d.list)
	d.mu.Unlock()
	os.RemoveAll(filepath.Join(d.incoming, dl.ID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
