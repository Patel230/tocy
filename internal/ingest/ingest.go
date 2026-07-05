// Package ingest orchestrates scanning all detected sources into the store.
package ingest

import (
	"os"
	"syscall"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"
	"github.com/lakshmanpatel/tocy/internal/source/claudecode"
	"github.com/lakshmanpatel/tocy/internal/source/codex"
	"github.com/lakshmanpatel/tocy/internal/source/opencode"
	"github.com/lakshmanpatel/tocy/internal/store"
)

// Sources returns all known sources in ingest-priority order.
func Sources() []source.Source {
	return []source.Source{
		claudecode.New(),
		codex.New(),
		opencode.New(),
	}
}

// alwaysScanner is an optional Source capability: return true to bypass the
// size/mtime "unchanged" skip (needed for WAL SQLite dbs, whose main file
// stat often doesn't change until a checkpoint).
type alwaysScanner interface{ AlwaysScan() bool }

func alwaysScan(src source.Source) bool {
	a, ok := src.(alwaysScanner)
	return ok && a.AlwaysScan()
}

// Result summarizes one source's scan.
type Result struct {
	Source    string
	Found     bool
	Root      string
	Files     int // files with new data parsed this scan
	NewEvents int
	Err       error
	Duration  time.Duration
}

// ScanAll ingests every detected source; safe to re-run any time (idempotent).
func ScanAll(st *store.Store, srcs []source.Source) []Result {
	var results []Result
	for _, src := range srcs {
		start := time.Now()
		res := Result{Source: src.Name()}
		found, root := src.Detect()
		res.Found, res.Root = found, root
		if found {
			res.Files, res.NewEvents, res.Err = scanSource(st, src)
		}
		res.Duration = time.Since(start)
		results = append(results, res)
	}
	return results
}

func scanSource(st *store.Store, src source.Source) (files, newEvents int, err error) {
	targets, err := src.ScanTargets()
	if err != nil {
		return 0, 0, err
	}
	for _, path := range targets {
		fi, serr := os.Stat(path)
		if serr != nil {
			continue // vanished mid-scan
		}
		size, mtime, ino := fi.Size(), fi.ModTime().Unix(), inodeOf(fi)

		prev, gerr := st.GetFileState(path)
		if gerr != nil {
			return files, newEvents, gerr
		}
		if prev == nil {
			prev = &source.FileState{Path: path, Source: src.Name()}
		} else if prev.Size == size && prev.Mtime == mtime && prev.Inode == ino && !alwaysScan(src) {
			continue // unchanged
		}
		// Truncated/rotated: restart from 0 (dedup keys prevent double count).
		if size < prev.Offset || (prev.Inode != 0 && ino != 0 && prev.Inode != ino) {
			prev.Offset = 0
			prev.State = ""
		}

		var batch []source.UsageEvent
		ns, perr := src.Parse(path, prev, func(e source.UsageEvent) {
			batch = append(batch, e)
		})
		if perr != nil {
			return files, newEvents, perr
		}
		n, ierr := st.InsertEvents(batch)
		if ierr != nil {
			return files, newEvents, ierr
		}
		ns.Path, ns.Source = path, src.Name()
		ns.Size, ns.Mtime, ns.Inode = size, mtime, ino
		if serr := st.SaveFileState(ns); serr != nil {
			return files, newEvents, serr
		}
		files++
		newEvents += n
	}
	return files, newEvents, nil
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
