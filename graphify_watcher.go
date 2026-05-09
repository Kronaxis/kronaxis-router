package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// graphifyWatcher watches configured roots and re-ingests changed files.
// Debounces in-memory: many writes within debounceWindow coalesce into one
// re-ingest of the affected file. Cheap when nothing changes; cheap when
// many things change at once.
//
// Skips the same dir/file patterns the ingester skips. New files also
// re-ingest (chunks are upserted on path+idx, deletes leave stale rows --
// future-work: a `WHERE source_path NOT IN <walked>` cleanup pass).
type graphifyWatcher struct {
	emb     Embedder
	db      *sql.DB
	roots   []string
	exclude map[string]struct{}
	logger  func(string, ...any)

	mu      sync.Mutex
	pending map[string]time.Time
}

func newGraphifyWatcher(db *sql.DB, emb Embedder, gcfg GraphifyConfig) *graphifyWatcher {
	exMap := map[string]struct{}{
		".git": {}, ".venv": {}, "node_modules": {}, "vendor": {},
		"graphify-out": {}, "__pycache__": {}, "dist": {}, "build": {},
		".next": {}, ".cache": {}, ".terraform": {}, "target": {},
		"bin": {}, "obj": {},
	}
	for _, e := range gcfg.IngestExcludes {
		exMap[e] = struct{}{}
	}
	return &graphifyWatcher{
		emb:     emb,
		db:      db,
		roots:   gcfg.IngestRoots,
		exclude: exMap,
		logger:  func(f string, a ...any) { logger.Printf(f, a...) },
		pending: map[string]time.Time{},
	}
}

// Run blocks until ctx is cancelled. Returns the first init error.
func (w *graphifyWatcher) Run(ctx context.Context) error {
	if len(w.roots) == 0 {
		w.logger("graphify watcher: no roots configured, exiting")
		return nil
	}
	wat, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer wat.Close()

	added := 0
	for _, root := range w.roots {
		if err := w.addRecursive(wat, root); err != nil {
			w.logger("graphify watcher: walk %s: %v", root, err)
			continue
		}
		added++
	}
	w.logger("graphify watcher: watching %d roots", added)

	flushTicker := time.NewTicker(2 * time.Second)
	defer flushTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-wat.Events:
			if !ok {
				return nil
			}
			w.handleEvent(wat, ev)
		case err, ok := <-wat.Errors:
			if !ok {
				return nil
			}
			w.logger("graphify watcher: error %v", err)
		case <-flushTicker.C:
			w.flushPending(ctx)
		}
	}
}

func (w *graphifyWatcher) addRecursive(wat *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		name := info.Name()
		if _, ex := w.exclude[name]; ex {
			return filepath.SkipDir
		}
		if strings.HasPrefix(name, ".") && name != "." && path != root {
			return filepath.SkipDir
		}
		return wat.Add(path)
	})
}

func (w *graphifyWatcher) handleEvent(wat *fsnotify.Watcher, ev fsnotify.Event) {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
		return
	}
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			name := info.Name()
			if _, ex := w.exclude[name]; !ex && !strings.HasPrefix(name, ".") {
				_ = w.addRecursive(wat, ev.Name)
			}
			return
		}
	}
	if !shouldIngestFile(ev.Name) {
		return
	}
	w.mu.Lock()
	w.pending[ev.Name] = time.Now()
	w.mu.Unlock()
}

func (w *graphifyWatcher) flushPending(ctx context.Context) {
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.mu.Unlock()
		return
	}
	due := []string{}
	cutoff := time.Now().Add(-2 * time.Second)
	for path, t := range w.pending {
		if t.Before(cutoff) {
			due = append(due, path)
			delete(w.pending, path)
		}
	}
	w.mu.Unlock()
	if len(due) == 0 {
		return
	}
	stats, err := graphifyIngest(ctx, w.db, w.emb, IngestOpts{
		Roots:     due,
		BatchSize: 16,
	})
	if err != nil {
		w.logger("graphify watcher: re-ingest %d files: %v", len(due), err)
		return
	}
	w.logger("graphify watcher: re-ingested %d files (%d chunks) in %s",
		len(due), stats.ChunksWritten, stats.FinishedAt.Sub(stats.StartedAt).Round(time.Millisecond))
}
