package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/lakshmanpatel/tocy/internal/source"
	"github.com/lakshmanpatel/tocy/internal/store"
)

// fakeSrc is a minimal Source over a directory of *.log files; every line in
// a target file becomes one event keyed by path+line number.
type fakeSrc struct {
	name string
	root string // dir (or file, for watchRoots tests); "" = not detected
}

func (f *fakeSrc) Name() string { return f.name }

func (f *fakeSrc) Detect() (bool, string) {
	if f.root == "" {
		return false, ""
	}
	if _, err := os.Stat(f.root); err != nil {
		return false, ""
	}
	return true, f.root
}

func (f *fakeSrc) ScanTargets() ([]string, error) {
	return filepath.Glob(filepath.Join(f.root, "*.log"))
}

func (f *fakeSrc) Parse(path string, st *source.FileState, emit func(source.UsageEvent)) (source.FileState, error) {
	line := 0
	off, err := source.TailLines(path, st.Offset, func([]byte) {
		line++
		emit(source.UsageEvent{
			Source:   f.name,
			DedupKey: fmt.Sprintf("%s:%d:%d", path, st.Offset, line),
			Model:    "fake-model",
			TS:       time.Now().UTC(),
			Input:    1,
		})
	})
	ns := *st
	ns.Offset = off
	return ns, err
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tocy.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func eventCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

func waitForCount(t *testing.T, st *store.Store, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if eventCount(t, st) >= want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d events, have %d", want, eventCount(t, st))
}

func TestWatchRoots(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "usage.db")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots := watchRoots([]source.Source{
		&fakeSrc{name: "missing", root: ""},                      // not detected
		&fakeSrc{name: "gone", root: filepath.Join(dir, "nope")}, // stat fails
		&fakeSrc{name: "dir", root: dir},                         // dir kept as-is
		&fakeSrc{name: "file", root: file},                       // file → parent dir
	})
	if len(roots) != 2 {
		t.Fatalf("roots = %v, want 2 entries", roots)
	}
	if roots[0] != dir || roots[1] != dir {
		t.Errorf("roots = %v, want both %q", roots, dir)
	}
}

func TestAddRecursive(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// A plain file must not be registered.
	if err := os.WriteFile(filepath.Join(root, "a", "f.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("fsnotify unavailable: %v", err)
	}
	defer func() { _ = w.Close() }()
	addRecursive(w, root)

	got := w.WatchList()
	if len(got) != 4 { // root, a, a/b, a/b/c
		t.Fatalf("WatchList = %v, want 4 dirs", got)
	}
	for _, p := range got {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			t.Errorf("watched %q is not a directory", p)
		}
	}
}

func TestPollLoopScansOnceAndStops(t *testing.T) {
	st := openTestStore(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.log"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs := []source.Source{&fakeSrc{name: "fake", root: dir}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: pollLoop must scan once, then return
	done := make(chan error, 1)
	go func() { done <- pollLoop(ctx, st, srcs, time.Hour, 1) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("pollLoop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pollLoop did not return after cancel")
	}
	if n := eventCount(t, st); n != 2 {
		t.Errorf("events = %d, want 2", n)
	}
}

func TestRunWatchRescansOnFileEvent(t *testing.T) {
	st := openTestStore(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "first.log"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs := []source.Source{&fakeSrc{name: "fake", root: dir}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	// interval is huge so only fsnotify (not the ticker) can trigger rescans.
	go func() { done <- runWatch(ctx, st, srcs, time.Hour) }()

	waitForCount(t, st, 1, 5*time.Second) // initial scan
	if err := os.WriteFile(filepath.Join(dir, "second.log"), []byte("two\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForCount(t, st, 3, 10*time.Second) // debounced fsnotify rescan

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWatch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWatch did not return after cancel")
	}
}
