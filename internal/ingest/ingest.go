package ingest

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"
	"github.com/lakshmanpatel/tocy/internal/source/claudecode"
	"github.com/lakshmanpatel/tocy/internal/source/codex"
	"github.com/lakshmanpatel/tocy/internal/source/opencode"
	"github.com/lakshmanpatel/tocy/internal/store"
)

func Sources() []source.Source {
	return []source.Source{
		claudecode.New(),
		codex.New(),
		opencode.New(),
	}
}

type alwaysScanner interface {
	AlwaysScan() bool
}

func alwaysScan(src source.Source) bool {
	a, ok := src.(alwaysScanner)
	return ok && a.AlwaysScan()
}

// FileDetail records the outcome for one scanned file.
type FileDetail struct {
	Path      string
	NewEvents int
	Err       error
}

type Result struct {
	Source    string
	Found     bool
	Root      string
	Files     int
	NewEvents int
	Details   []FileDetail
	Err       error
	Duration  time.Duration
}

// ScanAll scans each source concurrently. Detection and file parsing are
// independent per source, and store writes are serialized for free by the
// store's single-connection SQLite pool, so running sources in parallel
// only speeds up the I/O-bound Detect/Parse work.
func ScanAll(st *store.Store, srcs []source.Source) []Result {
	results := make([]Result, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func(i int, src source.Source) {
			defer wg.Done()
			start := time.Now()
			res := Result{Source: src.Name()}
			found, root := src.Detect()
			res.Found, res.Root = found, root
			if found {
				res.Files, res.NewEvents, res.Details, res.Err = scanSource(st, src)
			}
			res.Duration = time.Since(start)
			results[i] = res
		}(i, src)
	}
	wg.Wait()
	return results
}

// scanSource ingests every target file, skipping (but reporting) files that
// fail to parse or persist so one corrupt file can't block the whole source.
func scanSource(st *store.Store, src source.Source) (files, newEvents int, details []FileDetail, err error) {
	targets, terr := src.ScanTargets()
	if terr != nil {
		return 0, 0, nil, terr
	}
	var fileErrs []error
	for _, path := range targets {
		fi, serr := os.Stat(path)
		if serr != nil {
			continue
		}
		size, mtime, ino := fi.Size(), fi.ModTime().Unix(), inodeOf(fi)

		prev, gerr := st.GetFileState(path)
		if gerr != nil {
			return files, newEvents, details, gerr
		}
		if prev == nil {
			prev = &source.FileState{Path: path, Source: src.Name()}
		} else if prev.Size == size && prev.Mtime == mtime && prev.Inode == ino && !alwaysScan(src) {
			continue
		}
		if size < prev.Offset || (prev.Inode != 0 && ino != 0 && prev.Inode != ino) {
			prev.Offset = 0
			prev.State = ""
		}

		var batch []source.UsageEvent
		ns, perr := src.Parse(path, prev, func(e source.UsageEvent) {
			batch = append(batch, e)
		})
		if perr != nil {
			fileErrs = append(fileErrs, fmt.Errorf("%s: %w", path, perr))
			details = append(details, FileDetail{Path: path, Err: perr})
			continue
		}
		n, ierr := st.InsertEvents(batch)
		if ierr != nil {
			return files, newEvents, details, ierr
		}
		ns.Path, ns.Source = path, src.Name()
		ns.Size, ns.Mtime, ns.Inode = size, mtime, ino
		if serr := st.SaveFileState(ns); serr != nil {
			return files, newEvents, details, serr
		}
		files++
		newEvents += n
		details = append(details, FileDetail{Path: path, NewEvents: n})
	}
	return files, newEvents, details, errors.Join(fileErrs...)
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
