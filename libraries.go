package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Libraries added while the server runs: from the web page, the API or `kithara-server
// library add`. They are kept in libraries.json in the data folder, beside the ones
// given with --library (which stay fixed: the command line is the owner's to change).
// Every rescan reads libraries.json again, so a change made on the command line while
// the server runs takes effect on its next scan.

func (l *library) storedPath() string { return filepath.Join(l.dataDir, "libraries.json") }

func (l *library) readStored() ([]libSpec, error) {
	var specs []libSpec
	err := readJSON(l.storedPath(), &specs)
	return specs, err
}

// syncStored brings the roots in line with libraries.json: new entries are added,
// entries gone from the file are dropped. The caller holds scanMu (or owns l).
func (l *library) syncStored() error {
	st := stampOf(l.storedPath())
	if st == l.storedSeen && l.storedRead {
		return nil
	}
	specs, err := l.readStored()
	if err != nil {
		return err
	}
	l.storedSeen, l.storedRead = st, true
	want := map[string]libSpec{}
	for _, sp := range specs {
		want[strings.ToLower(sp.Name)] = sp
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.roots[:0:0]
	have := map[string]bool{}
	for _, r := range l.roots {
		key := strings.ToLower(r.name)
		if sp, ok := want[key]; r.fixed || ok && sp.Path == r.path {
			kept = append(kept, r)
			have[key] = true
		}
	}
	for _, sp := range specs {
		if !have[strings.ToLower(sp.Name)] {
			kept = append(kept, l.newRoot(sp, len(kept) == 0))
			have[strings.ToLower(sp.Name)] = true
		}
	}
	l.roots = kept
	return nil
}

var (
	errLibraryExists = errors.New("there is already a library with that name")
	errLibraryFixed  = errors.New("that library is set on the server's command line (--library or KITHARA_LIBRARY); change it there")
	errNoLibrary     = errors.New("no such library")
)

// checkLibraryFolder makes sure path is a readable folder that is neither inside nor
// around an existing library (which would list its books twice).
func (l *library) checkLibraryFolder(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("give the folder's full path, as the server sees it")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("the server cannot see %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is a file, not a folder", path)
	}
	if _, err := os.ReadDir(path); err != nil {
		return "", fmt.Errorf("the server cannot read %s: %w", path, err)
	}
	// Not the server's own data, and nothing that holds it, nor a whole home folder or
	// disk: uploads write into a library.
	data, _ := filepath.Abs(l.dataDir)
	home, _ := os.UserHomeDir()
	if within(path, data) || within(data, path) || path == home || filepath.Dir(path) == path {
		return "", fmt.Errorf("%s cannot be a library: choose the folder that holds your audiobooks", path)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, r := range l.roots {
		other, _ := filepath.Abs(r.path)
		if within(path, other) || within(other, path) {
			return "", fmt.Errorf("that folder overlaps the library %q (%s)", r.name, r.path)
		}
	}
	return path, nil
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// addLibrary keeps a new library in libraries.json and scans it in.
func (l *library) addLibrary(name, path string) error {
	if err := l.storeLibrary(name, path); err != nil {
		return err
	}
	return l.rescan()
}

// storeLibrary checks a new library and keeps it in libraries.json, without scanning.
func (l *library) storeLibrary(name, path string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(filepath.Clean(path))
	}
	if name == "" || strings.ContainsAny(name, `/\`) || len(name) > 64 {
		return errors.New("a library name is 1 to 64 characters, without slashes")
	}
	path, err := l.checkLibraryFolder(path)
	if err != nil {
		return err
	}
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	if _, ok := l.catalogueRoot(name); ok {
		return errLibraryExists
	}
	specs, err := l.readStored()
	if err != nil {
		return err
	}
	if err := writeJSON(l.storedPath(), append(specs, libSpec{Name: name, Path: path})); err != nil {
		return err
	}
	return l.syncStored()
}

// removeLibrary drops a library added at run time. Its books leave the catalogue (and
// so every device); its files and the listening positions on them are untouched, and
// adding the same folder back brings both back.
func (l *library) removeLibrary(name string) error {
	if err := l.unstoreLibrary(name); err != nil {
		return err
	}
	return l.rescan()
}

// unstoreLibrary takes a library out of libraries.json, without scanning.
func (l *library) unstoreLibrary(name string) error {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	r, ok := l.catalogueRoot(name)
	if !ok {
		return errNoLibrary
	}
	if r.fixed {
		return errLibraryFixed
	}
	specs, err := l.readStored()
	if err != nil {
		return err
	}
	kept := specs[:0]
	for _, sp := range specs {
		if !strings.EqualFold(sp.Name, r.name) {
			kept = append(kept, sp)
		}
	}
	if err := writeJSON(l.storedPath(), kept); err != nil {
		return err
	}
	return l.syncStored()
}

func (l *library) catalogueRoot(name string) (*libRoot, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, r := range l.roots {
		if strings.EqualFold(r.name, name) {
			return r, true
		}
	}
	return nil, false
}

// ------------------------------------------------------------------ routes (admin)

func (s *server) addLibraryHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	var in struct{ Name, Path string }
	if err := readBody(r, &in); err != nil || strings.TrimSpace(in.Path) == "" {
		writeError(w, http.StatusBadRequest, "a folder path is required")
		return
	}
	err := s.lib.addLibrary(in.Name, strings.TrimSpace(in.Path))
	switch {
	case errors.Is(err, errLibraryExists):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil && !errors.As(err, new(scanError)):
		// A scan error (some library unreadable) still added it; anything else did not.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, li := range s.lib.libraries(s.acct(user)) {
		if strings.EqualFold(li.Name, in.Name) || in.Name == "" && li.Path == filepath.Clean(strings.TrimSpace(in.Path)) {
			writeJSONResponse(w, http.StatusCreated, li)
			return
		}
	}
	writeJSONResponse(w, http.StatusCreated, map[string]string{"name": in.Name})
}

func (s *server) removeLibraryHandler(w http.ResponseWriter, r *http.Request, user, token string) {
	err := s.lib.removeLibrary(r.PathValue("name"))
	switch {
	case errors.Is(err, errNoLibrary):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, errLibraryFixed):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil && !errors.As(err, new(scanError)):
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// browse lists the folders in a folder on the server, for picking a library. Admins
// only; names only, never file contents.
func (s *server) browse(w http.ResponseWriter, r *http.Request, user, token string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = s.lib.browseStart()
	}
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, "give a full path")
		return
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read "+path+": "+err.Error())
		return
	}
	type dir struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	dirs := []dir{}
	audio := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "@eaDir" || name == "$RECYCLE.BIN" || name == "System Volume Information" {
			continue
		}
		if !e.IsDir() {
			if _, ok := audioExt[strings.ToLower(filepath.Ext(name))]; ok {
				audio++
			}
			continue
		}
		dirs = append(dirs, dir{Name: name, Path: filepath.Join(path, name)})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	writeJSONResponse(w, 200, map[string]any{"path": path, "parent": parent, "dirs": dirs, "audio": audio, "separator": string(filepath.Separator)})
}

// browseStart is where the folder picker starts: the folder the first library is in,
// else the filesystem root (the home folder on Windows).
func (l *library) browseStart() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	for _, r := range l.roots {
		if abs, err := filepath.Abs(r.path); err == nil {
			if _, err := os.Stat(filepath.Dir(abs)); err == nil {
				return filepath.Dir(abs)
			}
		}
	}
	if runtime.GOOS == "windows" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return `C:\`
	}
	return "/"
}
