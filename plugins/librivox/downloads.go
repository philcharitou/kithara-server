package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A download fetches a recording's files, one after another, into the folder the server
// made for it in the incoming folder. Two run at once; the rest wait. What the plugin
// knows of its downloads lives in memory: after a restart it answers FAILED for the
// ones it had, and the user starts them again.

const (
	running    = 2
	fileTries  = 3
	keepDone   = time.Hour // a finished or failed download the server never asked about again
	errUnknown = "the plugin doesn't know this download (it may have restarted); start it again"
)

type job struct {
	id, folder, item, format string

	state   atomic.Value // QUEUED, DOWNLOADING, FINISHED or FAILED
	size    atomic.Int64 // bytes in all, once known
	done    atomic.Int64 // bytes so far
	started atomic.Int64 // unix ms, when downloading began
	ended   atomic.Int64
	err     atomic.Value
	cancel  context.CancelFunc
}

type downloader struct {
	archive  *archive
	incoming string
	files    *http.Client // no overall timeout: an audiobook can take a while
	slots    chan struct{}
	mu       sync.Mutex
	jobs     map[string]*job
}

func newDownloader(a *archive, incoming string) *downloader {
	return &downloader{archive: a, incoming: incoming, slots: make(chan struct{}, running), jobs: map[string]*job{},
		files: &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second}}}
}

func (d *downloader) start(id, folder, item, format string) error {
	dir := filepath.Join(d.incoming, folder)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("the folder %s isn't there: is the incoming folder shared with the server at %s?", folder, d.incoming)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.jobs[id]; ok {
		return errors.New("that download is already under way")
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{id: id, folder: dir, item: item, format: format, cancel: cancel}
	j.state.Store("QUEUED")
	d.jobs[id] = j
	go d.run(ctx, j)
	d.tidy()
	return nil
}

// tidy drops downloads that ended long ago and that the server never told it to forget.
// The caller holds mu.
func (d *downloader) tidy() {
	for id, j := range d.jobs {
		if end := j.ended.Load(); end > 0 && time.Since(time.UnixMilli(end)) > keepDone {
			delete(d.jobs, id)
		}
	}
}

func (d *downloader) run(ctx context.Context, j *job) {
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	case <-ctx.Done():
		return
	}
	j.state.Store("DOWNLOADING")
	j.started.Store(time.Now().UnixMilli())
	err := d.fetch(ctx, j)
	j.ended.Store(time.Now().UnixMilli())
	switch {
	case ctx.Err() != nil:
		return // forgotten while running
	case err != nil:
		log.Printf("download %s (%s): %v", j.id, j.item, err)
		j.err.Store(err.Error())
		j.state.Store("FAILED")
	default:
		j.state.Store("FINISHED")
	}
}

func (d *downloader) fetch(ctx context.Context, j *job) error {
	it, err := d.archive.item(ctx, j.item)
	if err != nil {
		return err
	}
	files, _ := it.audio(j.format)
	if len(files) == 0 {
		return errors.New("this recording has no audio files to download")
	}
	cover, hasCover := it.cover()
	var size int64
	for _, f := range files {
		size += f.bytes()
	}
	j.size.Store(size)
	for _, f := range files {
		if err := d.file(ctx, j, f, filepath.Base(f.Name)); err != nil {
			return err
		}
	}
	if hasCover {
		if err := d.file(ctx, j, cover, "cover"+strings.ToLower(filepath.Ext(cover.Name))); err != nil {
			log.Printf("download %s: the cover: %v", j.id, err) // the book is fine without
		}
	}
	return nil
}

// file downloads one file into the job's folder, as name, trying again after a failure.
func (d *downloader) file(ctx context.Context, j *job, f file, name string) error {
	if name == "." || name == "/" || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("an odd file name: %q", f.Name)
	}
	var err error
	for try := 0; try < fileTries; try++ {
		before := j.done.Load()
		if err = d.fileOnce(ctx, j, f, name); err == nil || ctx.Err() != nil {
			return err
		}
		j.done.Store(before) // count the bytes again on the next try
		log.Printf("download %s: %s: %v (trying again)", j.id, f.Name, err)
		time.Sleep(time.Duration(try+1) * 3 * time.Second)
	}
	return fmt.Errorf("%s: %w", f.Name, err)
}

func (d *downloader) fileOnce(ctx context.Context, j *job, f file, name string) error {
	src := d.archive.base + "/download/" + url.PathEscape(j.item) + "/" + escapePath(f.Name)
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "kithara-plugin-librivox (+https://github.com/philcharitou/kithara-server)")
	res, err := d.files.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("the Internet Archive answered %s", res.Status)
	}
	// Written as .part, and renamed when whole: the server only takes what is finished.
	part := filepath.Join(j.folder, name+".part")
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.TeeReader(res.Body, counter{&j.done}))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, filepath.Join(j.folder, name))
}

// escapePath escapes each part of an Archive file name ("disc 1/01 Chapter.mp3").
func escapePath(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

type counter struct{ n *atomic.Int64 }

func (c counter) Write(p []byte) (int, error) {
	c.n.Add(int64(len(p)))
	return len(p), nil
}

// status answers GET /kithara/downloads/{id}.
func (d *downloader) status(id string) map[string]any {
	d.mu.Lock()
	j, ok := d.jobs[id]
	d.mu.Unlock()
	if !ok {
		return map[string]any{"state": "FAILED", "error": errUnknown}
	}
	state, _ := j.state.Load().(string)
	out := map[string]any{"state": state, "sizeBytes": j.size.Load()}
	if size, done := j.size.Load(), j.done.Load(); size > 0 {
		out["progress"] = min(float64(done)/float64(size), 1)
	}
	if start := j.started.Load(); state == "DOWNLOADING" && start > 0 {
		if secs := time.Since(time.UnixMilli(start)).Seconds(); secs > 1 {
			out["bytesPerSecond"] = int64(float64(j.done.Load()) / secs)
		}
	}
	if e, _ := j.err.Load().(string); e != "" {
		out["error"] = e
	}
	return out
}

// forget stops a download if it is running and drops it. Its folder is the server's,
// which removes it; a half-written file is removed here.
func (d *downloader) forget(id string) {
	d.mu.Lock()
	j, ok := d.jobs[id]
	delete(d.jobs, id)
	d.mu.Unlock()
	if ok {
		j.cancel()
	}
}
