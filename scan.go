package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Wire types for /api/library, as in the protocol document. Fields the app does not
// need on the wire (paths on disk) are kept alongside and never serialised.

type Book struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Author      string    `json:"author,omitempty"`
	Narrator    string    `json:"narrator,omitempty"`
	Series      string    `json:"series,omitempty"`
	SeriesIndex *float64  `json:"seriesIndex,omitempty"`
	Description string    `json:"description,omitempty"`
	DurationMs  int64     `json:"durationMs"`
	CoverURL    string    `json:"coverUrl,omitempty"`
	UpdatedAt   int64     `json:"updatedAt"`
	Files       []File    `json:"files"`
	Chapters    []Chapter `json:"chapters"`

	// Additions to the protocol's book (clients ignore fields they do not know).
	Library       string   `json:"library,omitempty"`
	Subtitle      string   `json:"subtitle,omitempty"`
	Genres        []string `json:"genres,omitempty"`
	Publisher     string   `json:"publisher,omitempty"`
	PublishedYear string   `json:"publishedYear,omitempty"`
	Language      string   `json:"language,omitempty"`
	ISBN          string   `json:"isbn,omitempty"`
	ASIN          string   `json:"asin,omitempty"`

	coverPath string
}

type File struct {
	Index      int    `json:"index"`
	Name       string `json:"name"`
	URL        string `json:"url"`
	DurationMs int64  `json:"durationMs"`
	SizeBytes  int64  `json:"sizeBytes"`
	MimeType   string `json:"mimeType,omitempty"`

	path string
}

type Chapter struct {
	Title   string `json:"title"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
}

type Catalogue struct {
	UpdatedAt int64  `json:"updatedAt"`
	Books     []Book `json:"books"`
}

// library is the scanned catalogue plus what it takes to rebuild it. It can be made of
// several folders ("libraries" to the user: Audiobooks, Kids, Lectures); they are
// served as one catalogue, as the protocol has it, with each book naming the folder it
// came from, and can be asked for one at a time.
type library struct {
	roots   []*libRoot
	dataDir string

	scanMu sync.Mutex // one scan at a time
	mu     sync.RWMutex
	cat    Catalogue
	byID   map[string]*Book
	view   map[string]catalogueView // "" is everything; else a root's name
	probe  *probeCache
}

// libRoot is one library folder.
type libRoot struct {
	name    string
	path    string
	idsPath string
	ids     map[string]string // relative path -> id, for books that cannot hold a sidecar
	books   []Book            // from its last good scan
}

// catalogueView is a catalogue as served: its JSON, built once per scan, and its ETag.
type catalogueView struct {
	body []byte
	etag string
	info LibraryInfo
}

// LibraryInfo is one entry of GET /api/libraries.
type LibraryInfo struct {
	Name       string `json:"name"`
	Books      int    `json:"books"`
	DurationMs int64  `json:"durationMs"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// libSpec names a library folder, from --library [NAME=]DIR.
type libSpec struct {
	Name, Path string
}

var (
	audioExt  = map[string]string{".m4b": "audio/mp4", ".m4a": "audio/mp4", ".mp4": "audio/mp4", ".aac": "audio/aac", ".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac", ".wav": "audio/wav", ".wma": "audio/x-ms-wma", ".mka": "audio/x-matroska"}
	coverName = []string{"cover.jpg", "cover.jpeg", "cover.png", "folder.jpg", "folder.png", "front.jpg", "album.jpg", "artwork.jpg"}
	discDirRe = regexp.MustCompile(`(?i)^(cd|disc|disk|part)\s*[-_]?\s*\d+$`)
	numberRe  = regexp.MustCompile(`\d+`)
)

const sidecarName = ".kithara-id"

// newLibrary is a library of one folder.
func newLibrary(root, dataDir string) *library {
	return newLibraries([]libSpec{{Path: root}}, dataDir)
}

// newLibraries sets up a library over several folders. Each keeps its path records
// in ids-<name>.json; the first takes over the ids.json of a single-folder setup.
func newLibraries(specs []libSpec, dataDir string) *library {
	l := &library{dataDir: dataDir, byID: map[string]*Book{}, view: map[string]catalogueView{}}
	l.probe = openProbeCache(filepath.Join(dataDir, "probe-cache.json"))
	for i, sp := range specs {
		r := &libRoot{name: sp.Name, path: sp.Path, ids: map[string]string{}}
		if r.name == "" {
			abs, _ := filepath.Abs(sp.Path)
			r.name = filepath.Base(abs)
		}
		r.idsPath = filepath.Join(dataDir, "ids-"+slug(r.name)+".json")
		if _, err := os.Stat(r.idsPath); err != nil && i == 0 {
			_ = readJSON(filepath.Join(dataDir, "ids.json"), &r.ids)
		} else {
			_ = readJSON(r.idsPath, &r.ids)
		}
		l.roots = append(l.roots, r)
	}
	return l
}

// parseLibSpec reads "DIR" or "NAME=DIR". A path that happens to hold "=" is still a
// path when the part before it looks like one.
func parseLibSpec(s string) libSpec {
	if name, path, ok := strings.Cut(s, "="); ok && name != "" && !strings.ContainsAny(name, `/\`) {
		return libSpec{Name: strings.TrimSpace(name), Path: path}
	}
	return libSpec{Path: s}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	if out := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-"); out != "" {
		return out
	}
	return "library"
}

func (l *library) count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.cat.Books)
}

// catalogue returns the served JSON and ETag for one library by name, or for all of
// them with "", and whether there is such a library.
func (l *library) catalogue(name string) (catalogueView, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if name == "" {
		return l.view[""], true
	}
	for _, r := range l.roots {
		if strings.EqualFold(r.name, name) {
			return l.view[r.name], true
		}
	}
	return catalogueView{}, false
}

func (l *library) libraries() []LibraryInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]LibraryInfo, 0, len(l.roots))
	for _, r := range l.roots {
		out = append(out, l.view[r.name].info)
	}
	return out
}

func (l *library) snapshot() Catalogue {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cat
}

func (l *library) book(id string) *Book {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byID[id]
}

// rescan walks the library folders and rebuilds the catalogue. Probing is cached by
// file size and modification time, so a rescan of an unchanged library is cheap.
//
// A folder that cannot be read (an unmounted share, a sleeping NAS) keeps the books of
// its last good scan: the list is authoritative to the apps, and an empty one would
// make them forget every book in it.
func (l *library) rescan() error {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	start := time.Now()
	if !haveFFprobe() {
		return fmt.Errorf("ffprobe is not on PATH; install ffmpeg to read durations and chapters")
	}
	var errs []error
	for _, r := range l.roots {
		books, err := l.scanRoot(r)
		if err != nil {
			errs = append(errs, fmt.Errorf("library %q (%s): %w", r.name, r.path, err))
			continue
		}
		r.books = books
		_ = writeJSON(r.idsPath, r.ids)
	}

	var all []Book
	seen := map[string]string{} // id -> library, to catch a book folder copied whole
	for _, r := range l.roots {
		for _, b := range r.books {
			if other, dup := seen[b.ID]; dup {
				log.Printf("skip %q in %q: same id %s as a book in %q (a copied folder? delete its %s)", b.Title, r.name, b.ID, other, sidecarName)
				continue
			}
			seen[b.ID] = r.name
			all = append(all, b)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return strings.ToLower(all[i].Title) < strings.ToLower(all[j].Title) })
	cat := Catalogue{Books: all}
	if cat.Books == nil {
		cat.Books = []Book{}
	}
	byID := map[string]*Book{}
	for i := range cat.Books {
		byID[cat.Books[i].ID] = &cat.Books[i]
	}
	views := map[string]catalogueView{}
	everything, err := buildView("", cat.Books)
	if err != nil {
		return err
	}
	cat.UpdatedAt = everything.info.UpdatedAt
	views[""] = everything
	for _, r := range l.roots {
		var mine []Book
		for _, b := range cat.Books {
			if b.Library == r.name {
				mine = append(mine, b)
			}
		}
		if views[r.name], err = buildView(r.name, mine); err != nil {
			return err
		}
	}

	l.mu.Lock()
	l.cat, l.byID, l.view = cat, byID, views
	l.mu.Unlock()
	l.probe.save()
	log.Printf("scanned %d books in %s", len(cat.Books), time.Since(start).Round(time.Millisecond))
	return errors.Join(errs...)
}

func buildView(name string, books []Book) (catalogueView, error) {
	cat := Catalogue{Books: books}
	if cat.Books == nil {
		cat.Books = []Book{}
	}
	info := LibraryInfo{Name: name, Books: len(books)}
	for _, b := range books {
		info.UpdatedAt = max(info.UpdatedAt, b.UpdatedAt)
		info.DurationMs += b.DurationMs
	}
	cat.UpdatedAt = info.UpdatedAt
	body, err := json.Marshal(cat)
	if err != nil {
		return catalogueView{}, err
	}
	sum := sha256.Sum256(body)
	return catalogueView{body: body, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, info: info}, nil
}

// scanRoot builds the books of one library folder.
func (l *library) scanRoot(r *libRoot) ([]Book, error) {
	if _, err := os.Stat(r.path); err != nil {
		return nil, err
	}
	dirs, loose, err := findBooks(r.path)
	if err != nil {
		return nil, err
	}
	var books []Book
	for _, d := range dirs {
		b, err := l.buildBook(r, d.path, d.files, d.name)
		if err != nil {
			log.Printf("skip %s: %v", d.path, err)
			continue
		}
		books = append(books, b)
	}
	for _, f := range loose {
		b, err := l.buildBook(r, filepath.Dir(f), []string{f}, strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))
		if err != nil {
			log.Printf("skip %s: %v", f, err)
			continue
		}
		books = append(books, b)
	}
	return books, nil
}

type bookDir struct {
	path  string
	name  string
	files []string
}

// findBooks decides what counts as a book: a folder with audio files directly in it,
// or a folder whose only audio lives in CD1/CD2-style subfolders, or a lone audio file
// in a folder that is otherwise a container of books.
func findBooks(root string) ([]bookDir, []string, error) {
	var dirs []bookDir
	var loose []string
	var walk func(dir string, depth int) (bool, error)
	walk = func(dir string, depth int) (bool, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false, err
		}
		var audio []string
		var subs []os.DirEntry
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "@eaDir" {
				continue
			}
			if e.IsDir() {
				subs = append(subs, e)
			} else if _, ok := audioExt[strings.ToLower(filepath.Ext(name))]; ok {
				audio = append(audio, filepath.Join(dir, name))
			}
		}
		// Disc folders: the parent is the book, the discs are its parts in order.
		if len(audio) == 0 && len(subs) > 0 {
			allDiscs := true
			for _, s := range subs {
				if !discDirRe.MatchString(s.Name()) {
					allDiscs = false
					break
				}
			}
			if allDiscs {
				names := make([]string, 0, len(subs))
				for _, s := range subs {
					names = append(names, s.Name())
				}
				sort.Slice(names, naturalLess(names))
				for _, n := range names {
					sub, _ := os.ReadDir(filepath.Join(dir, n))
					var part []string
					for _, e := range sub {
						if !e.IsDir() {
							if _, ok := audioExt[strings.ToLower(filepath.Ext(e.Name()))]; ok {
								part = append(part, filepath.Join(dir, n, e.Name()))
							}
						}
					}
					sort.Slice(part, naturalLess(part))
					audio = append(audio, part...)
				}
				if len(audio) > 0 {
					dirs = append(dirs, bookDir{path: dir, name: filepath.Base(dir), files: audio})
					return true, nil
				}
			}
		}
		childHasBooks := false
		for _, s := range subs {
			has, err := walk(filepath.Join(dir, s.Name()), depth+1)
			if err != nil {
				log.Printf("skip %s: %v", filepath.Join(dir, s.Name()), err)
				continue
			}
			childHasBooks = childHasBooks || has
		}
		if len(audio) > 0 {
			sort.Slice(audio, naturalLess(audio))
			if depth == 0 || childHasBooks {
				// Audio sitting next to book folders: each file is its own book.
				loose = append(loose, audio...)
			} else {
				dirs = append(dirs, bookDir{path: dir, name: filepath.Base(dir), files: audio})
			}
			return true, nil
		}
		return childHasBooks, nil
	}
	_, err := walk(root, 0)
	return dirs, loose, err
}

func naturalLess(items []string) func(i, j int) bool {
	key := func(s string) []any {
		base := strings.ToLower(filepath.Base(s))
		var parts []any
		last := 0
		for _, m := range numberRe.FindAllStringIndex(base, -1) {
			if m[0] > last {
				parts = append(parts, base[last:m[0]])
			}
			n, _ := strconv.Atoi(base[m[0]:m[1]])
			parts = append(parts, n)
			last = m[1]
		}
		if last < len(base) {
			parts = append(parts, base[last:])
		}
		return parts
	}
	return func(i, j int) bool {
		a, b := key(items[i]), key(items[j])
		for k := 0; k < len(a) && k < len(b); k++ {
			switch x := a[k].(type) {
			case int:
				if y, ok := b[k].(int); ok {
					if x != y {
						return x < y
					}
					continue
				}
				return true
			case string:
				if y, ok := b[k].(string); ok {
					if x != y {
						return x < y
					}
					continue
				}
				return false
			}
		}
		return len(a) < len(b)
	}
}

// buildBook probes the files and assembles one catalogue entry.
func (l *library) buildBook(r *libRoot, dir string, files []string, fallbackTitle string) (Book, error) {
	single := len(files) == 1 && filepath.Dir(files[0]) == dir && !isBookFolder(dir, files[0])
	id, err := l.idFor(r, dir, files[0], single)
	if err != nil {
		return Book{}, err
	}
	b := Book{ID: id, Title: fallbackTitle, Library: r.name, Files: []File{}, Chapters: []Chapter{}}

	// The folders above the book, for author and series: all of them for a lone file,
	// all but the book's own for a book folder.
	var above []string
	if rel, err := filepath.Rel(r.path, dir); err == nil && rel != "." {
		above = strings.Split(filepath.ToSlash(rel), "/")
		if !single {
			above = above[:len(above)-1]
		}
	}
	b.apply(folderMeta(fallbackTitle, above))

	var offset int64
	var anyChapters bool
	var perFile [][]Chapter
	for i, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return Book{}, err
		}
		pr, err := l.probe.probe(path, info)
		if err != nil {
			return Book{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if info.ModTime().UnixMilli() > b.UpdatedAt {
			b.UpdatedAt = info.ModTime().UnixMilli()
		}
		if i == 0 {
			b.apply(tagMeta(pr.Tags, single))
		}
		b.Files = append(b.Files, File{
			Index: i, Name: filepath.Base(path), URL: "/audio/" + id + "/" + strconv.Itoa(i),
			DurationMs: pr.DurationMs, SizeBytes: info.Size(), MimeType: audioExt[strings.ToLower(filepath.Ext(path))], path: path,
		})
		var chs []Chapter
		for _, c := range pr.Chapters {
			chs = append(chs, Chapter{Title: c.Title, StartMs: offset + c.StartMs, EndMs: offset + c.EndMs})
		}
		if len(chs) > 0 {
			anyChapters = true
		}
		perFile = append(perFile, chs)
		offset += pr.DurationMs
	}
	b.DurationMs = offset

	// Chapters: embedded ones when any file has them (files without get one chapter
	// each, named from their tag or file name); none at all otherwise, and the app
	// shows one per file, or none for a single file.
	if anyChapters {
		var offset int64
		for i, f := range b.Files {
			if len(perFile[i]) > 0 {
				b.Chapters = append(b.Chapters, perFile[i]...)
			} else {
				title := strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
				if pr, ok := l.probe.get(f.path); ok && pr.Tags["title"] != "" && len(b.Files) > 1 {
					title = pr.Tags["title"]
				}
				b.Chapters = append(b.Chapters, Chapter{Title: title, StartMs: offset, EndMs: offset + f.DurationMs})
			}
			offset += f.DurationMs
		}
	}

	// Metadata files in the book's folder have the last word, chapters included (the
	// chapter list audiobookshelf's editor saves into metadata.json).
	if !single {
		sm, modified := sidecarMeta(dir)
		b.apply(sm)
		b.UpdatedAt = max(b.UpdatedAt, modified)
		if chs := clampChapters(sm.Chapters, b.DurationMs); len(chs) > 0 {
			b.Chapters = chs
		}
	}

	// Cover: a picture in the folder, else one pulled out of the first file.
	if !single {
		for _, n := range coverName {
			p := filepath.Join(dir, n)
			if info, err := os.Stat(p); err == nil {
				b.coverPath = p
				if info.ModTime().UnixMilli() > b.UpdatedAt {
					b.UpdatedAt = info.ModTime().UnixMilli()
				}
				break
			}
		}
	}
	if b.coverPath == "" {
		if p := l.extractCover(id, files[0]); p != "" {
			b.coverPath = p
		}
	}
	if b.coverPath != "" {
		b.CoverURL = "/covers/" + id
	}
	return b, nil
}

// clampChapters keeps chapters from a metadata file on the book's timeline, in order.
func clampChapters(chs []Chapter, durationMs int64) []Chapter {
	var out []Chapter
	for _, c := range chs {
		if c.StartMs >= durationMs || c.EndMs <= c.StartMs {
			continue
		}
		c.EndMs = min(c.EndMs, durationMs)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartMs < out[j].StartMs })
	return out
}

// isBookFolder: a lone file in a folder named after it is still "a book folder",
// so the sidecar id can live there.
func isBookFolder(dir, file string) bool {
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	return strings.EqualFold(filepath.Base(dir), base)
}

// idFor keeps a book's id with the book: a sidecar file in its folder, so the id
// survives renames and moves, and a path-keyed record in the data folder as a second
// copy, so a lost sidecar (a restore from backup, a tidy-up) does not turn the book
// into a new one and orphan its listening position. A lone file among others has
// only the path record.
func (l *library) idFor(r *libRoot, dir, firstFile string, loose bool) (string, error) {
	if loose {
		rel, _ := filepath.Rel(r.path, firstFile)
		return l.idByPath(r, filepath.ToSlash(rel), ""), nil
	}
	rel, _ := filepath.Rel(r.path, dir)
	key := filepath.ToSlash(rel)
	p := filepath.Join(dir, sidecarName)
	if data, err := os.ReadFile(p); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			l.idByPath(r, key, id) // keep the path record in step with the sidecar
			return id, nil
		}
	}
	id := l.idByPath(r, key, "")
	if err := os.WriteFile(p, []byte(id+"\n"), 0o644); err != nil {
		log.Printf("no sidecar in %s (%v); id kept by path", dir, err)
	}
	return id, nil
}

// idByPath returns the id recorded for a path, recording preferred (or a new id)
// when there is none. A sidecar that disagrees with the record wins: the folder was
// moved here from elsewhere and its id came with it.
func (l *library) idByPath(r *libRoot, rel, preferred string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id, ok := r.ids[rel]; ok && (preferred == "" || id == preferred) {
		return id
	}
	if preferred == "" {
		preferred = newID()
	}
	r.ids[rel] = preferred
	return preferred
}

func newID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// extractCover pulls embedded artwork out of an audio file with ffmpeg, once.
func (l *library) extractCover(id, file string) string {
	dir := filepath.Join(l.dataDir, "covers")
	_ = os.MkdirAll(dir, 0o755)
	out := filepath.Join(dir, id+".jpg")
	if info, err := os.Stat(out); err == nil {
		if src, err := os.Stat(file); err == nil && !src.ModTime().After(info.ModTime()) {
			return out
		}
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return ""
	}
	cmd := exec.Command("ffmpeg", "-y", "-v", "quiet", "-i", file, "-an", "-map", "0:v:0", "-frames:v", "1", "-c:v", "mjpeg", "-q:v", "3", out)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(out)
		return ""
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		_ = os.Remove(out)
		return ""
	}
	return out
}

// ------------------------------------------------------------------ ffprobe

type probeResult struct {
	Size       int64             `json:"size"`
	ModTime    int64             `json:"mtime"`
	DurationMs int64             `json:"durationMs"`
	Tags       map[string]string `json:"tags"`
	Chapters   []Chapter         `json:"chapters"`
}

type probeCache struct {
	path    string
	mu      sync.Mutex
	entries map[string]probeResult
	dirty   bool
}

func openProbeCache(path string) *probeCache {
	c := &probeCache{path: path, entries: map[string]probeResult{}}
	_ = readJSON(path, &c.entries)
	return c
}

func (c *probeCache) get(path string) (probeResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.entries[path]
	return r, ok
}

func (c *probeCache) probe(path string, info os.FileInfo) (probeResult, error) {
	c.mu.Lock()
	if r, ok := c.entries[path]; ok && r.Size == info.Size() && r.ModTime == info.ModTime().UnixMilli() {
		c.mu.Unlock()
		return r, nil
	}
	c.mu.Unlock()
	r, err := ffprobe(path)
	if err != nil {
		return r, err
	}
	r.Size, r.ModTime = info.Size(), info.ModTime().UnixMilli()
	c.mu.Lock()
	c.entries[path] = r
	c.dirty = true
	c.mu.Unlock()
	return r, nil
}

func (c *probeCache) save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return
	}
	if err := writeJSON(c.path, c.entries); err == nil {
		c.dirty = false
	}
}

var ffprobeOnce struct {
	sync.Once
	ok bool
}

func haveFFprobe() bool {
	ffprobeOnce.Do(func() { _, err := exec.LookPath("ffprobe"); ffprobeOnce.ok = err == nil })
	return ffprobeOnce.ok
}

func ffprobe(path string) (probeResult, error) {
	var out bytes.Buffer
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_chapters", path)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return probeResult{}, fmt.Errorf("ffprobe: %w", err)
	}
	var raw struct {
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Chapters []struct {
			Start string            `json:"start_time"`
			End   string            `json:"end_time"`
			Tags  map[string]string `json:"tags"`
		} `json:"chapters"`
	}
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		return probeResult{}, err
	}
	secs, _ := strconv.ParseFloat(raw.Format.Duration, 64)
	r := probeResult{DurationMs: int64(secs*1000 + 0.5), Tags: map[string]string{}}
	for k, v := range raw.Format.Tags {
		r.Tags[strings.ToLower(k)] = v
	}
	for i, c := range raw.Chapters {
		s, _ := strconv.ParseFloat(c.Start, 64)
		e, _ := strconv.ParseFloat(c.End, 64)
		title := c.Tags["title"]
		if title == "" {
			title = fmt.Sprintf("Chapter %d", i+1)
		}
		r.Chapters = append(r.Chapters, Chapter{Title: title, StartMs: int64(s*1000 + 0.5), EndMs: int64(e*1000 + 0.5)})
	}
	if r.DurationMs == 0 {
		return r, fmt.Errorf("no duration")
	}
	return r, nil
}
