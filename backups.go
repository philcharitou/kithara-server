package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Backups: what cannot be rebuilt from the audiobook folders (accounts, everyone's
// progress, bookmarks, playlists and listening, transcripts, saved details and covers,
// settings), as a zip. One can be downloaded at any time; with daily backups on, the
// server also writes one each day at a set hour into a folder (backups/ in the data
// folder unless another is chosen) and keeps the newest few.

type BackupSettings struct {
	Daily bool   `json:"daily"`
	Hour  int    `json:"hour"`          // local time, 0–23
	Keep  int    `json:"keep"`          // how many to keep; 0 is the default
	Dir   string `json:"dir,omitempty"` // an absolute folder; "" is backups/ in the data folder
}

const defaultBackupsKept = 7

type backups struct {
	s    *server
	mu   sync.Mutex
	cfg  BackupSettings
	last BackupResult
	wake chan struct{}
}

type BackupResult struct {
	At    int64  `json:"at,omitempty"`
	File  string `json:"file,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *server) backupSettingsPath() string { return filepath.Join(s.store.dir, "backups.json") }

func newBackups(s *server) *backups {
	b := &backups{s: s, wake: make(chan struct{}, 1)}
	if err := readJSON(s.backupSettingsPath(), &b.cfg); err != nil {
		log.Printf("backups.json: %v", err)
	}
	return b
}

func (b *backups) settings() BackupSettings {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.cfg
	if c.Keep <= 0 {
		c.Keep = defaultBackupsKept
	}
	return c
}

func (b *backups) dir() string {
	if d := b.settings().Dir; d != "" {
		return d
	}
	return filepath.Join(b.s.store.dir, "backups")
}

// writeBackup writes the backup zip of the data folder dir to w.
func writeBackup(dir string, w io.Writer) error {
	zw := zip.NewWriter(w)
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		keep := rel == "users.json" || rel == "libraries.json" || rel == "settings.json" || rel == "ids.json" ||
			rel == "collections.json" || rel == "oidc.json" || rel == "transcription.json" || rel == "backups.json" ||
			rel == "hooks.json" || strings.HasPrefix(rel, "transcripts/") ||
			strings.HasPrefix(rel, "ids-") || strings.HasPrefix(rel, "users/") || strings.HasPrefix(rel, "metadata/") ||
			strings.HasPrefix(rel, "covers/") && strings.Contains(path.Base(rel), "-match.")
		if !keep || strings.HasSuffix(rel, ".tmp") {
			return nil
		}
		return zipFile(zw, rel, p, zip.Deflate)
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

var backupName = regexp.MustCompile(`^kithara-backup-\d{4}-\d{2}-\d{2}-\d{4}\.zip$`)

// now writes a backup into the folder, keeps the newest few, and says how it went.
func (b *backups) now() (BackupResult, error) {
	dir := b.dir()
	res := BackupResult{At: time.Now().UnixMilli()}
	err := func() error {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		name := "kithara-backup-" + time.Now().Format("2006-01-02-1504") + ".zip"
		f, err := os.CreateTemp(dir, ".kithara-backup-*.tmp")
		if err != nil {
			return err
		}
		err = writeBackup(b.s.store.dir, f)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(f.Name(), filepath.Join(dir, name))
		}
		if err != nil {
			os.Remove(f.Name())
			return err
		}
		res.File = name
		return b.prune(dir)
	}()
	if err != nil {
		res.Error = err.Error()
	}
	b.mu.Lock()
	b.last = res
	b.mu.Unlock()
	if err != nil {
		log.Printf("backup: %v", err)
		b.s.notify(eventBackupFailed, "Backup failed: "+err.Error(), map[string]any{"error": err.Error()})
	} else {
		b.s.notify(eventBackupDone, "Backed up to "+res.File, map[string]any{"file": res.File, "dir": dir})
	}
	return res, err
}

// prune keeps the newest backups the settings ask for, and only files this server named.
func (b *backups) prune(dir string) error {
	files, err := b.list(dir)
	if err != nil {
		return err
	}
	for _, f := range files[min(len(files), b.settings().Keep):] {
		if err := os.Remove(filepath.Join(dir, f.Name)); err != nil {
			return err
		}
	}
	return nil
}

type BackupFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	At   int64  `json:"at"`
}

// list is the backups in dir, newest first.
func (b *backups) list(dir string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []BackupFile{}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && backupName.MatchString(e.Name()) && info.Mode().IsRegular() {
			out = append(out, BackupFile{e.Name(), info.Size(), info.ModTime().UnixMilli()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// run makes the daily backup: at the set hour, once a day, and on start when today's
// was missed (the server was off at that hour).
func (b *backups) run() {
	for {
		c := b.settings()
		if c.Daily {
			now := time.Now()
			files, _ := b.list(b.dir())
			today := now.Format("2006-01-02")
			done := len(files) > 0 && strings.HasPrefix(files[0].Name, "kithara-backup-"+today)
			b.mu.Lock()
			tried := time.Since(time.UnixMilli(b.last.At))
			b.mu.Unlock()
			// A failure is tried again an hour later, not every minute.
			if !done && now.Hour() >= c.Hour && tried > time.Hour {
				_, _ = b.now()
			}
		}
		select {
		case <-time.After(time.Minute):
		case <-b.wake:
		}
	}
}

// ------------------------------------------------------------------ routes

// backupDownload sends a fresh backup (GET /api/backup).
func (s *server) backupDownload(w http.ResponseWriter, r *http.Request, user, token string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition("kithara-backup-"+time.Now().Format("2006-01-02")+".zip"))
	if err := writeBackup(s.store.dir, w); err != nil {
		log.Printf("backup: %v", err)
	}
}

func (s *server) backupStatus(w http.ResponseWriter, r *http.Request, user, token string) {
	b := s.backups
	files, err := b.list(b.dir())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	b.mu.Lock()
	last := b.last
	b.mu.Unlock()
	writeJSONResponse(w, 200, map[string]any{"settings": b.settings(), "dir": b.dir(), "last": last, "files": files})
}

func (s *server) putBackupSettings(w http.ResponseWriter, r *http.Request, user, token string) {
	var in BackupSettings
	if err := readBody(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad settings")
		return
	}
	in.Dir = strings.TrimSpace(in.Dir)
	if in.Hour < 0 || in.Hour > 23 || in.Keep < 0 || in.Keep > 365 {
		writeError(w, http.StatusBadRequest, "the hour is 0 to 23, and up to 365 are kept")
		return
	}
	if in.Dir != "" {
		if !filepath.IsAbs(in.Dir) {
			writeError(w, http.StatusBadRequest, "give the folder's full path, as the server sees it")
			return
		}
		in.Dir = filepath.Clean(in.Dir)
		for _, li := range s.lib.libraries(s.acct(user)) {
			if within(in.Dir, li.Path) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("not inside the library %q: backups would show up as files there", li.Name))
				return
			}
		}
		if err := os.MkdirAll(in.Dir, 0o700); err != nil {
			writeError(w, http.StatusBadRequest, "cannot use that folder: "+err.Error())
			return
		}
	}
	b := s.backups
	b.mu.Lock()
	err := writeJSON(s.backupSettingsPath(), in)
	if err == nil {
		b.cfg = in
	}
	b.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
	s.backupStatus(w, r, user, token)
}

func (s *server) backupNow(w http.ResponseWriter, r *http.Request, user, token string) {
	res, err := s.backups.now()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONResponse(w, http.StatusCreated, res)
}

// backupFile sends or deletes one kept backup, by its name.
func (s *server) backupFile(w http.ResponseWriter, r *http.Request, user, token string) {
	name := r.PathValue("name")
	if !backupName.MatchString(name) {
		writeError(w, http.StatusNotFound, "no such backup")
		return
	}
	p := filepath.Join(s.backups.dir(), name)
	if r.Method == http.MethodDelete {
		if err := os.Remove(p); err != nil {
			writeError(w, http.StatusNotFound, "no such backup")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such backup")
		return
	}
	defer f.Close()
	info, _ := f.Stat()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(name))
	http.ServeContent(w, r, name, info.ModTime(), f)
}
