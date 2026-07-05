// Package source defines the common interface every AI-CLI usage source
// (claude-code, codex, opencode, ...) implements, plus shared helpers.
package source

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"time"
)

// UsageEvent is one normalized token-usage record from any tool.
type UsageEvent struct {
	Source    string     // tool name, e.g. "claude-code"
	DedupKey  string     // unique within Source; UNIQUE(source, dedup_key) in DB
	Model     string     // e.g. "claude-opus-4-7"
	SessionID string
	Project   string     // cwd / project path if known
	TS        time.Time  // UTC
	Input     int64
	Output    int64
	CacheRead int64
	CacheWrite int64
	Reasoning int64
	RawCost   *float64 // source-provided cost (opencode); nil elsewhere
}

// Total returns the total token count across all classes.
func (e UsageEvent) Total() int64 {
	return e.Input + e.Output + e.CacheRead + e.CacheWrite + e.Reasoning
}

// FileState tracks incremental-ingest progress for one source file.
type FileState struct {
	Path   string
	Source string
	Inode  uint64
	Size   int64
	Mtime  int64 // unix seconds
	Offset int64 // byte offset already parsed
	State  string // parser-specific JSON (e.g. codex cumulative totals)
}

// Source is one ingestible AI CLI tool.
type Source interface {
	// Name is the stable tool identifier stored in the DB.
	Name() string
	// Detect reports whether the tool's data dir exists (and its root).
	Detect() (found bool, root string)
	// ScanTargets lists files (or DBs) to ingest.
	ScanTargets() ([]string, error)
	// Parse reads path starting at st.Offset, emitting events, and returns
	// the updated state (offset advanced past fully-parsed lines).
	Parse(path string, st *FileState, emit func(UsageEvent)) (FileState, error)
	// WatchDirs returns dirs for fsnotify in watch mode; empty => poll-only.
	WatchDirs() []string
}

// MaxLine is the largest JSONL line we accept (10 MB).
const MaxLine = 10 << 20

// TailLines reads path from offset, invoking fn for each newline-terminated
// line, and returns the new offset. A trailing partial line (no newline, e.g.
// a write in flight) is only consumed if it is already complete valid JSON;
// otherwise it is left for the next scan.
func TailLines(path string, offset int64, fn func(line []byte)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}
	r := bufio.NewReaderSize(f, 512<<10)
	pos := offset
	for {
		line, err := r.ReadBytes('\n')
		if err == nil {
			pos += int64(len(line))
			if len(line) <= MaxLine {
				fn(line)
			}
			continue
		}
		if err == io.EOF {
			// Leftover partial line: consume only if complete JSON.
			if len(line) > 0 && len(line) <= MaxLine && json.Valid(line) {
				fn(line)
				pos += int64(len(line))
			}
			return pos, nil
		}
		return pos, err
	}
}
