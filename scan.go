package main

import (
	"bytes"
	"compress/gzip"
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
	"runtime"
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
	dir       string   // the book's folder (for a lone file, the folder it sits in)
	root      *libRoot // the library it was scanned from
	loose     bool     // an audio file beside other books, not a folder of its own
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
	combos map[string]catalogueView // for users limited to several libraries, built on demand
	gen    int                      // scans so far, so a view built from an older one is not kept

	storedSeen fileStamp // libraries.json as last read
	storedRead bool

	lastScanAt int64 // when the last scan finished, and how long it took
	lastScanMs int64
	problems   []Problem // what the last scan could not use
	probe      *probeCache
}

// libRoot is one library folder.
type libRoot struct {
	name     string
	path     string
	idsPath  string
	ids      map[string]string // relative path -> id, for books that cannot hold a sidecar
	books    []Book            // from its last good scan
	fixed    bool              // from --library or the environment, not libraries.json
	idsDirty bool              // ids changed since ids-<name>.json was written
	problems []Problem         // files its last good scan skipped
	lastErr  string            // why the last scan of it failed, if it did
}

// catalogueView is a catalogue as served: its JSON, built once per scan, and its ETag.
type catalogueView struct {
	body []byte
	gz   []byte // body, gzipped once per scan
	etag string
	info LibraryInfo
}

// LibraryInfo is one entry of GET /api/libraries.
type LibraryInfo struct {
	Name       string `json:"name"`
	Books      int    `json:"books"`
	DurationMs int64  `json:"durationMs"`
	UpdatedAt  int64  `json:"updatedAt"`

	// For admins only: where it is, whether it was set on the command line (and so
	// cannot be removed from the web page), and what went wrong scanning it.
	Path  string `json:"path,omitempty"`
	Fixed bool   `json:"fixed,omitempty"`
	Error string `json:"error,omitempty"`
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
	l := &library{dataDir: dataDir, byID: map[string]*Book{}, view: map[string]catalogueView{}, combos: map[string]catalogueView{}}
	l.probe = openProbeCache(filepath.Join(dataDir, "probe-cache.json"))
	for i, sp := range specs {
		r := l.newRoot(sp, i == 0)
		r.fixed = true
		l.roots = append(l.roots, r)
	}
	if err := l.syncStored(); err != nil {
		log.Printf("libraries.json: %v", err)
	}
	return l
}

// newRoot sets up one library folder; the first of a server takes over the ids.json of
// a single-folder setup.
func (l *library) newRoot(sp libSpec, first bool) *libRoot {
	r := &libRoot{name: sp.Name, path: sp.Path, ids: map[string]string{}}
	if r.name == "" {
		abs, _ := filepath.Abs(sp.Path)
		r.name = filepath.Base(abs)
	}
	r.idsPath = filepath.Join(l.dataDir, "ids-"+slug(r.name)+".json")
	if _, err := os.Stat(r.idsPath); err != nil && first {
		_ = readJSON(filepath.Join(l.dataDir, "ids.json"), &r.ids)
		r.idsDirty = len(r.ids) > 0 // written to ids-<name>.json on the first scan
	} else {
		_ = readJSON(r.idsPath, &r.ids)
	}
	for rel, id := range r.ids {
		if !validID(id) {
			delete(r.ids, rel)
		}
	}
	return r
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
	if name == "" {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return l.view[""], true
	}
	r, ok := l.catalogueRoot(name)
	if !ok {
		return catalogueView{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.view[r.name], true
}

// catalogueFor is catalogue as one account may see it: an unknown library and one the
// account may not see are alike not there, and without a name, everything it may see.
func (l *library) catalogueFor(a Account, name string) (catalogueView, bool) {
	if name != "" || a.admin() || len(a.Libraries) == 0 {
		v, ok := l.catalogue(name)
		if ok && name != "" && !a.sees(v.info.Name) {
			return catalogueView{}, false
		}
		return v, ok
	}
	l.mu.RLock()
	var names []string
	for _, r := range l.roots {
		if a.sees(r.name) {
			names = append(names, r.name)
		}
	}
	key := strings.Join(names, "\x00")
	v, ok := l.combos[key]
	cat, gen := l.cat, l.gen
	l.mu.RUnlock()
	if ok {
		return v, true
	}
	var mine []Book
	for _, b := range cat.Books {
		if a.sees(b.Library) {
			mine = append(mine, b)
		}
	}
	v, err := buildView("", mine)
	if err != nil {
		return catalogueView{}, false
	}
	l.mu.Lock()
	if l.gen == gen {
		l.combos[key] = v
	}
	l.mu.Unlock()
	return v, true
}

// libraries lists the libraries an account may see.
func (l *library) libraries(a Account) []LibraryInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]LibraryInfo, 0, len(l.roots))
	for _, r := range l.roots {
		if !a.sees(r.name) {
			continue
		}
		info := l.view[r.name].info
		info.Name = r.name // also before its first scan
		if a.admin() {
			info.Path, info.Fixed, info.Error = r.path, r.fixed, r.lastErr
		}
		out = append(out, info)
	}
	return out
}

// bookFor is book, for an account: nil when it may not see it.
func (l *library) bookFor(a Account, id string) *Book {
	b := l.book(id)
	if b == nil || !a.sees(b.Library) {
		return nil
	}
	return b
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
	return l.rescanLocked()
}

// rescanLocked is rescan for a caller that holds scanMu.
func (l *library) rescanLocked() error {
	start := time.Now()
	if err := l.syncStored(); err != nil {
		log.Printf("libraries.json: %v", err)
	}
	if !haveFFprobe() {
		return fmt.Errorf("ffprobe is not on PATH; install ffmpeg to read durations and chapters")
	}
	l.probe.beginScan()
	var errs []error
	for _, r := range l.roots {
		books, err := l.scanRoot(r)
		l.mu.Lock()
		if err != nil {
			r.lastErr = err.Error()
		} else {
			r.books, r.lastErr = books, ""
		}
		l.mu.Unlock()
		if err != nil {
			errs = append(errs, fmt.Errorf("library %q (%s): %w", r.name, r.path, err))
			continue
		}
		if r.idsDirty {
			_ = writeJSON(r.idsPath, r.ids)
			r.idsDirty = false
		}
	}
	if err := l.publish(); err != nil {
		return err
	}
	// Forget probes of files no longer there, unless a library could not be read
	// (its files are still there, just out of reach for now).
	l.probe.save(len(errs) == 0)
	l.mu.Lock()
	l.lastScanAt, l.lastScanMs = time.Now().UnixMilli(), time.Since(start).Milliseconds()
	l.mu.Unlock()
	log.Printf("scanned %d books in %s", l.count(), time.Since(start).Round(time.Millisecond))
	if len(errs) > 0 {
		return scanError{errors.Join(errs...)}
	}
	return nil
}

// scanError is a scan that went ahead but could not read every library folder.
type scanError struct{ error }

func (e scanError) Unwrap() error { return e.error }

// publish builds the catalogue, its served views and the problem list from each
// library's books. The caller holds scanMu.
func (l *library) publish() error {
	var problems []Problem
	var all []Book
	seen := map[string]string{} // id -> library, to catch a book folder copied whole
	for _, r := range l.roots {
		for _, b := range r.books {
			if other, dup := seen[b.ID]; dup {
				log.Printf("skip %q in %q: same id %s as a book in %q (a copied folder? delete its %s)", b.Title, r.name, b.ID, other, sidecarName)
				problems = append(problems, Problem{Library: r.name, Path: b.dir, Error: fmt.Sprintf("same id as a book in %q: a copied folder? Delete its %s file", other, sidecarName)})
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
	byLib := map[string][]Book{}
	for _, b := range cat.Books {
		byLib[b.Library] = append(byLib[b.Library], b)
	}
	for _, r := range l.roots {
		if views[r.name], err = buildView(r.name, byLib[r.name]); err != nil {
			return err
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.roots {
		problems = append(problems, r.problems...)
		if r.lastErr != "" {
			problems = append(problems, Problem{Library: r.name, Path: r.path, Error: "cannot read the library folder: " + r.lastErr})
		}
	}
	l.cat, l.byID, l.view, l.combos, l.problems = cat, byID, views, map[string]catalogueView{}, problems
	l.gen++
	return nil
}

var errNoBook = errors.New("no such book")

// rebuildBook reads one book again (after its details, cover or chapters were
// edited) and republishes the catalogue, without walking every library.
func (l *library) rebuildBook(id string) error {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	old := l.book(id)
	if old == nil || old.root == nil {
		return errNoBook
	}
	files := make([]string, len(old.Files))
	for i, f := range old.Files {
		files[i] = f.path
	}
	fallback := filepath.Base(old.dir)
	if old.loose {
		fallback = strings.TrimSuffix(filepath.Base(files[0]), filepath.Ext(files[0]))
	}
	var plain map[string]bool
	if !old.loose {
		plain = plainFiles(old.dir)
	}
	b, err := l.buildBook(old.root, old.dir, files, fallback, old.loose, plain)
	if err != nil {
		// Its files moved or went since the last scan: read everything again.
		log.Printf("rebuild %s: %v; rescanning", id, err)
		return l.rescanLocked()
	}
	r := old.root
	l.mu.Lock()
	books := append([]Book(nil), r.books...)
	for i := range books {
		if books[i].ID == id {
			books[i] = b
			break
		}
	}
	r.books = books
	l.mu.Unlock()
	l.probe.save(false)
	return l.publish()
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
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(body)
	zw.Close()
	return catalogueView{body: body, gz: gz.Bytes(), etag: `"` + hex.EncodeToString(sum[:8]) + `"`, info: info}, nil
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
	r.problems = nil
	var paths []string
	for _, d := range dirs {
		paths = append(paths, d.files...)
	}
	l.probeAll(append(paths, loose...))
	var books []Book
	for _, d := range dirs {
		b, err := l.buildBook(r, d.path, d.files, d.name, false, d.plain)
		if err != nil {
			log.Printf("skip %s: %v", d.path, err)
			r.problems = append(r.problems, Problem{Library: r.name, Path: d.path, Error: err.Error()})
			continue
		}
		books = append(books, b)
	}
	for _, f := range loose {
		b, err := l.buildBook(r, filepath.Dir(f), []string{f}, strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)), true, nil)
		if err != nil {
			log.Printf("skip %s: %v", f, err)
			r.problems = append(r.problems, Problem{Library: r.name, Path: f, Error: err.Error()})
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
	plain map[string]bool // regular files in the folder (no links), for sidecars and covers
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
		plain := map[string]bool{}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "@eaDir" {
				continue
			}
			if e.Type().IsRegular() {
				plain[name] = true
			}
			if e.IsDir() {
				subs = append(subs, e)
			} else if isAudio(root, dir, e) {
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
						if isAudio(root, filepath.Join(dir, n), e) {
							part = append(part, filepath.Join(dir, n, e.Name()))
						}
					}
					sort.Slice(part, naturalLess(part))
					audio = append(audio, part...)
				}
				if len(audio) > 0 {
					dirs = append(dirs, bookDir{path: dir, name: filepath.Base(dir), files: audio, plain: plain})
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
				dirs = append(dirs, bookDir{path: dir, name: filepath.Base(dir), files: audio, plain: plain})
			}
			return true, nil
		}
		return childHasBooks, nil
	}
	_, err := walk(root, 0)
	return dirs, loose, err
}

// isAudio reports whether a folder entry is an audio file to serve. A link counts
// only when it leads to a file inside the library: one that leads elsewhere (another
// library, the server's own files) would hand that file to anyone who can see this one.
func isAudio(root, dir string, e os.DirEntry) bool {
	if _, ok := audioExt[strings.ToLower(filepath.Ext(e.Name()))]; !ok {
		return false
	}
	if e.Type().IsRegular() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	target, err1 := filepath.EvalSymlinks(filepath.Join(dir, e.Name()))
	base, err2 := filepath.EvalSymlinks(root)
	if err1 != nil || err2 != nil || !within(target, base) {
		return false
	}
	info, err := os.Stat(target)
	return err == nil && info.Mode().IsRegular()
}

// plainFiles lists the regular files (not links or folders) in a folder.
func plainFiles(dir string) map[string]bool {
	entries, _ := os.ReadDir(dir)
	out := map[string]bool{}
	for _, e := range entries {
		if e.Type().IsRegular() {
			out[e.Name()] = true
		}
	}
	return out
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

// probeAll probes files in parallel, one ffprobe per CPU, so the first scan of a big
// library is not one file at a time. Failures are left for buildBook to report.
func (l *library) probeAll(paths []string) {
	work := make(chan string)
	var wg sync.WaitGroup
	for range max(1, runtime.NumCPU()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				if info, err := os.Stat(p); err == nil {
					_, _ = l.probe.probe(p, info)
				}
			}
		}()
	}
	for _, p := range paths {
		work <- p
	}
	close(work)
	wg.Wait()
}

// buildBook probes the files and assembles one catalogue entry.
// A loose book is an audio file sitting beside other books rather than in a folder of
// its own; its folder belongs to the books around it.
// plain lists the regular files in a book folder (from the scan's own listing), so
// sidecars and covers are looked for without a stat per candidate name.
func (l *library) buildBook(r *libRoot, dir string, files []string, fallbackTitle string, loose bool, plain map[string]bool) (Book, error) {
	id, err := l.idFor(r, dir, files[0], loose)
	if err != nil {
		return Book{}, err
	}
	b := Book{ID: id, Title: fallbackTitle, Library: r.name, Files: []File{}, Chapters: []Chapter{}, dir: dir, root: r, loose: loose}

	// The folders above the book, for author and series: all of them for a lone file,
	// all but the book's own for a book folder.
	var above []string
	if rel, err := filepath.Rel(r.path, dir); err == nil && rel != "." {
		above = strings.Split(filepath.ToSlash(rel), "/")
		if !loose {
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
			b.apply(tagMeta(pr.Tags, len(files) == 1))
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
	if !loose {
		sm, modified := sidecarMeta(dir, plain)
		b.apply(sm)
		b.UpdatedAt = max(b.UpdatedAt, modified)
		if chs := clampChapters(sm.Chapters, b.DurationMs); len(chs) > 0 {
			b.Chapters = chs
		}
	}

	// What an admin set or matched, kept in the data folder, has the very last word.
	if om, modified := overrideMeta(l.dataDir, id); modified > 0 {
		b.apply(om)
		b.UpdatedAt = max(b.UpdatedAt, modified)
		if chs := clampChapters(om.Chapters, b.DurationMs); len(chs) > 0 {
			b.Chapters = chs
		}
	}

	// Cover: one chosen through matching, else a picture in the folder, else one
	// pulled out of the first file.
	if p, modified := overrideCover(l.dataDir, id); p != "" {
		b.coverPath = p
		b.UpdatedAt = max(b.UpdatedAt, modified)
	}
	if !loose && b.coverPath == "" {
		for _, n := range coverName {
			if !plain[n] {
				continue
			}
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
		switch id := strings.TrimSpace(string(data)); {
		case validID(id):
			l.idByPath(r, key, id) // keep the path record in step with the sidecar
			return id, nil
		case id != "":
			// Ids name files in the data folder; anything but a plain id is refused.
			log.Printf("ignoring the %s in %s: not a valid id", sidecarName, dir)
		}
	}
	// A one-file book folder was taken for a loose file before 0.4.1 and its id recorded
	// by the file's path; carry that id over so its listening position stays attached.
	preferred := ""
	l.mu.RLock()
	if _, ok := r.ids[key]; !ok {
		if fileRel, err := filepath.Rel(r.path, firstFile); err == nil {
			if id := r.ids[filepath.ToSlash(fileRel)]; validID(id) {
				preferred = id
			}
		}
	}
	l.mu.RUnlock()
	id := l.idByPath(r, key, preferred)
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
	if id, ok := r.ids[rel]; ok && validID(id) && (preferred == "" || id == preferred) {
		return id
	}
	if preferred == "" {
		preferred = newID()
	}
	r.ids[rel] = preferred
	r.idsDirty = true
	return preferred
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// validID is an id safe to use in file names and paths: letters, digits, - and _.
func validID(id string) bool { return idRe.MatchString(id) }

func newID() string {
	raw := make([]byte, 6)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// extractCover pulls embedded artwork out of an audio file with ffmpeg, once. A file
// with none is remembered (<id>.none beside the covers) until it changes, so rescans do
// not try it again.
func (l *library) extractCover(id, file string) string {
	dir := filepath.Join(l.dataDir, "covers")
	out, none := filepath.Join(dir, id+".jpg"), filepath.Join(dir, id+".none")
	src, err := os.Stat(file)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(out); err == nil && !src.ModTime().After(info.ModTime()) {
		return out
	}
	// The marker holds the file's size and time: any change at all (a copy finishing,
	// even one that restores an older time) tries again.
	stamp := fmt.Sprintf("%d %d", src.Size(), src.ModTime().UnixNano())
	if data, err := os.ReadFile(none); err == nil && string(data) == stamp {
		return ""
	}
	if !haveFFmpeg() {
		return ""
	}
	_ = os.MkdirAll(dir, 0o755)
	cmd := exec.Command("ffmpeg", "-y", "-v", "quiet", "-protocol_whitelist", "file", "-i", file,
		"-an", "-map", "0:v:0", "-frames:v", "1", "-c:v", "mjpeg", "-q:v", "3", out)
	if err := cmd.Run(); err == nil {
		if info, err := os.Stat(out); err == nil && info.Size() > 0 {
			_ = os.Remove(none)
			return out
		}
	}
	_ = os.Remove(out)
	_ = os.WriteFile(none, []byte(stamp), 0o644)
	return ""
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
	seen    map[string]bool // paths the current scan used
	dirty   bool
}

func openProbeCache(path string) *probeCache {
	c := &probeCache{path: path, entries: map[string]probeResult{}}
	_ = readJSON(path, &c.entries)
	return c
}

func (c *probeCache) beginScan() {
	c.mu.Lock()
	c.seen = map[string]bool{}
	c.mu.Unlock()
}

func (c *probeCache) get(path string) (probeResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.entries[path]
	return r, ok
}

func (c *probeCache) probe(path string, info os.FileInfo) (probeResult, error) {
	c.mu.Lock()
	if c.seen != nil {
		c.seen[path] = true
	}
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

// save writes the cache if it changed; with prune, it first forgets files the scan
// did not see (deleted, moved or renamed).
func (c *probeCache) save(prune bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prune && c.seen != nil {
		for p := range c.entries {
			if !c.seen[p] {
				delete(c.entries, p)
				c.dirty = true
			}
		}
	}
	if !c.dirty {
		return
	}
	if err := writeJSONCompact(c.path, c.entries); err == nil {
		c.dirty = false
	}
}

var (
	haveFFprobe = sync.OnceValue(func() bool { _, err := exec.LookPath("ffprobe"); return err == nil })
	haveFFmpeg  = sync.OnceValue(func() bool { _, err := exec.LookPath("ffmpeg"); return err == nil })
)

// usedTags are the tags tagMeta and chapter naming read; the rest (lyrics, long
// comments, encoder notes) are not kept in the cache.
var usedTags = map[string]bool{}

func init() {
	for _, k := range strings.Fields(`album title subtitle artist album_artist author composer narrator performer
		series mvnm grouping series-part series_index mvin part description comment synopsis publisher label
		date year originaldate release_date language lang isbn asin audible_asin genre`) {
		usedTags[k] = true
	}
}

func ffprobe(path string) (probeResult, error) {
	var out bytes.Buffer
	// Local files only: a crafted "audio" file must not make ffprobe fetch anything.
	cmd := exec.Command("ffprobe", "-v", "quiet", "-protocol_whitelist", "file", "-print_format", "json", "-show_format", "-show_chapters", path)
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
		if k = strings.ToLower(k); usedTags[k] {
			r.Tags[k] = v
		}
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
