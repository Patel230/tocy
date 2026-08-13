package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/lakshmanpatel/tocy/internal/ingest"
	"github.com/lakshmanpatel/tocy/internal/source"
	"github.com/lakshmanpatel/tocy/internal/store"
)

// debounce is how long the watcher waits after the last file event before
// rescanning, so a burst of writes triggers one scan instead of many.
const debounce = 1500 * time.Millisecond

func scanAndLog(st *store.Store, srcs []source.Source) {
	for _, r := range ingest.ScanAll(st, srcs) {
		ts := time.Now().Format("15:04:05")
		switch {
		case r.Err != nil:
			fmt.Printf("%s %s %s\n", color(ansiDim, ts), color(ansiBold+ansiRed, r.Source), color(ansiRed, r.Err.Error()))
		case r.NewEvents > 0:
			msg := fmt.Sprintf("+%d event(s) from %d file(s) in %s", r.NewEvents, r.Files, r.Duration.Round(time.Millisecond))
			fmt.Printf("%s %s %s\n", color(ansiDim, ts), color(ansiBold+ansiGreen, r.Source), color(ansiDim, msg))
		}
	}
}

// watchRoots returns the directory roots of all detected sources. A source
// whose root is a file (opencode's sqlite db) contributes its parent dir,
// which also catches -wal/-shm sidecar writes.
func watchRoots(srcs []source.Source) []string {
	var roots []string
	for _, s := range srcs {
		found, root := s.Detect()
		if !found || root == "" {
			continue
		}
		if fi, err := os.Stat(root); err == nil && !fi.IsDir() {
			root = filepath.Dir(root)
		}
		roots = append(roots, root)
	}
	return roots
}

// addRecursive registers root and every subdirectory with the watcher.
// fsnotify does not watch recursively, and codex/claude-code nest their
// session files in per-project / per-date subdirectories.
func addRecursive(w *fsnotify.Watcher, root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil // skip unreadable entries, keep walking
		}
		_ = w.Add(path)
		return nil
	})
}

// runWatch ingests continuously until ctx is cancelled: fsnotify events
// trigger a debounced rescan, with a periodic full rescan every interval as
// a safety net. Falls back to plain polling if the watcher can't start.
func runWatch(ctx context.Context, st *store.Store, srcs []source.Source, interval time.Duration) error {
	detected := 0
	for _, s := range srcs {
		if ok, _ := s.Detect(); ok {
			detected++
		}
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Printf("  %s  %s\n", color(ansiYellow, "⚠"),
			color(ansiDim, "file watcher unavailable ("+err.Error()+"); polling only"))
		return pollLoop(ctx, st, srcs, interval, detected)
	}
	defer func() { _ = w.Close() }()
	for _, root := range watchRoots(srcs) {
		addRecursive(w, root)
	}
	if len(w.WatchList()) == 0 {
		return pollLoop(ctx, st, srcs, interval, detected)
	}

	fmt.Printf("  %s  %s\n",
		color(ansiCyan, "⟳"),
		color(ansiDim, fmt.Sprintf("watching %d/%d tools (fsnotify, rescan every %s)", detected, len(srcs), interval)))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Debounce timer; drained until the first event arms it.
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	scanAndLog(st, srcs)
	for {
		select {
		case <-ctx.Done():
			fmt.Println("  " + color(ansiDim, "stopping"))
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return pollLoop(ctx, st, srcs, interval, detected)
			}
			if ev.Op.Has(fsnotify.Create) {
				// New project/date dir: watch it so files inside are seen.
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					addRecursive(w, ev.Name)
				}
			}
			if ev.Op.Has(fsnotify.Create | fsnotify.Write | fsnotify.Rename) {
				timer.Reset(debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return pollLoop(ctx, st, srcs, interval, detected)
			}
			fmt.Printf("%s %s\n", color(ansiDim, time.Now().Format("15:04:05")), color(ansiYellow, "watch: "+err.Error()))
		case <-timer.C:
			scanAndLog(st, srcs)
		case <-ticker.C:
			scanAndLog(st, srcs)
		}
	}
}

// pollLoop is the pre-fsnotify behavior: rescan every interval.
func pollLoop(ctx context.Context, st *store.Store, srcs []source.Source, interval time.Duration, detected int) error {
	fmt.Printf("  %s  %s\n",
		color(ansiCyan, "⟳"),
		color(ansiDim, fmt.Sprintf("watching %d/%d tools, polling every %s", detected, len(srcs), interval)))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		scanAndLog(st, srcs)
		select {
		case <-ctx.Done():
			fmt.Println("  " + color(ansiDim, "stopping"))
			return nil
		case <-ticker.C:
		}
	}
}
