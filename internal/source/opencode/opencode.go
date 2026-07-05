// Package opencode parses the opencode SQLite store:
// ~/.local/share/opencode/opencode.db (message table, data JSON column).
//
// Unlike the JSONL sources this is not byte-offset tailable, so the
// incremental cursor is the max message.time_created (epoch ms) already
// processed, persisted as JSON in FileState.State. FileState.Offset stays 0
// on purpose: ingest's truncation heuristic (size < offset) would otherwise
// fire on every scan and wipe the state. The cursor query uses >= and relies
// on the store's UNIQUE(source, dedup_key) to drop re-read boundary rows.
//
// Streaming caveat: an assistant row is INSERTed at turn start and its data
// (tokens/cost) UPDATEd as the reply streams. We only ingest rows whose
// time.completed is set, and pin the cursor at the oldest still-incomplete
// row so it is re-read once finished; rows incomplete for >24h are treated
// as abandoned (crashed session) and skipped so they cannot pin forever.
package opencode

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"

	_ "modernc.org/sqlite"
)

const name = "opencode"

const abandonAfter = 24 * time.Hour

type Src struct{ dbPath string }

// New returns the source at $XDG_DATA_HOME/opencode/opencode.db.
func New() *Src {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return &Src{dbPath: filepath.Join(dataHome, "opencode", "opencode.db")}
}

// NewWithDB is used by tests and fixtures.
func NewWithDB(path string) *Src { return &Src{dbPath: path} }

func (s *Src) Name() string { return name }

func (s *Src) Detect() (bool, string) {
	fi, err := os.Stat(s.dbPath)
	return err == nil && fi.Size() > 0, s.dbPath
}

func (s *Src) ScanTargets() ([]string, error) {
	if ok, _ := s.Detect(); !ok {
		return nil, nil
	}
	return []string{s.dbPath}, nil
}

// WatchDirs: poll-only. In WAL mode new rows land in opencode.db-wal, so
// fsnotify on the main file would miss them anyway.
func (s *Src) WatchDirs() []string { return nil }

// AlwaysScan tells ingest to skip the size/mtime unchanged check: with WAL
// the main db file's stat often doesn't budge until a checkpoint.
func (s *Src) AlwaysScan() bool { return true }

type cursorState struct {
	CursorMS int64 `json:"cursor_ms"`
	// Pinned records that CursorMS points at a still-streaming row, so the
	// next scan must query even if file stats look unchanged (the UPDATE
	// that completes it may land between our stat and a checkpoint).
	Pinned bool `json:"pinned,omitempty"`
	// Stats of the db and its -wal at last scan: if all are unchanged and
	// nothing is pinned, there is nothing new and we skip the full-scan
	// cursor query (the message table has no bare time_created index, so
	// every query walks all rows — too heavy for a watch tick).
	DBSize     int64 `json:"db_size,omitempty"`
	DBMtimeNS  int64 `json:"db_mtime_ns,omitempty"`
	WalSize    int64 `json:"wal_size,omitempty"`
	WalMtimeNS int64 `json:"wal_mtime_ns,omitempty"`
}

// statOf returns (size, mtime ns) or zeros if the file is absent.
func statOf(path string) (int64, int64) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return fi.Size(), fi.ModTime().UnixNano()
}

// msgData is the subset of the message.data JSON we need.
type msgData struct {
	Role   string  `json:"role"`
	Cost   float64 `json:"cost"`
	Tokens *struct {
		Input     int64 `json:"input"`
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Write int64 `json:"write"`
			Read  int64 `json:"read"`
		} `json:"cache"`
	} `json:"tokens"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Path       *struct {
		CWD string `json:"cwd"`
	} `json:"path"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

func (s *Src) Parse(path string, st *source.FileState, emit func(source.UsageEvent)) (source.FileState, error) {
	ns := *st
	ns.Offset = 0 // see package comment

	var cur cursorState
	if st.State != "" {
		_ = json.Unmarshal([]byte(st.State), &cur)
	}

	// Stats are taken BEFORE the query: a write racing the query bumps
	// mtime past these values, forcing a (deduped) rescan next tick.
	dbSize, dbMt := statOf(path)
	walSize, walMt := statOf(path + "-wal")
	if st.State != "" && !cur.Pinned &&
		cur.DBSize == dbSize && cur.DBMtimeNS == dbMt &&
		cur.WalSize == walSize && cur.WalMtimeNS == walMt {
		return ns, nil // nothing changed on disk since last scan
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return ns, err
	}
	defer db.Close()

	rows, err := db.Query(
		`SELECT id, session_id, time_created, data FROM message
		 WHERE time_created >= ? ORDER BY time_created, id`, cur.CursorMS)
	if err != nil {
		return ns, err
	}
	defer rows.Close()

	var (
		maxSeen  = cur.CursorMS
		pinned   int64 // oldest incomplete assistant row; 0 = none
		staleCut = time.Now().Add(-abandonAfter).UnixMilli()
	)
	for rows.Next() {
		var (
			id, sessionID string
			tc            int64
			data          []byte
		)
		if err := rows.Scan(&id, &sessionID, &tc, &data); err != nil {
			return ns, err
		}
		if tc > maxSeen {
			maxSeen = tc
		}
		var d msgData
		if json.Unmarshal(data, &d) != nil || d.Role != "assistant" {
			continue
		}
		if d.Time.Completed == 0 {
			// Still streaming: pin the cursor here so we re-read it once
			// finished — unless it has been dangling long enough to be a
			// crashed session that will never complete.
			if tc >= staleCut && (pinned == 0 || tc < pinned) {
				pinned = tc
			}
			continue
		}
		if d.Tokens == nil || d.ModelID == "" {
			continue
		}
		t := d.Tokens
		total := t.Input + t.Output + t.Reasoning + t.Cache.Read + t.Cache.Write
		if total == 0 {
			continue // aborted before any usage
		}
		project := ""
		if d.Path != nil {
			project = d.Path.CWD
		}
		cost := d.Cost
		emit(source.UsageEvent{
			Source:     name,
			DedupKey:   id, // opencode message ids are globally unique
			Model:      d.ModelID,
			SessionID:  sessionID,
			Project:    project,
			TS:         time.UnixMilli(d.Time.Created).UTC(),
			Input:      t.Input,
			Output:     t.Output,
			CacheRead:  t.Cache.Read,
			CacheWrite: t.Cache.Write,
			Reasoning:  t.Reasoning,
			RawCost:    &cost, // opencode computes cost itself (0 = free/plan)
		})
	}
	if err := rows.Err(); err != nil {
		return ns, err
	}

	next := maxSeen
	if pinned > 0 && pinned < next {
		next = pinned
	}
	nc := cursorState{
		CursorMS: next, Pinned: pinned > 0,
		DBSize: dbSize, DBMtimeNS: dbMt, WalSize: walSize, WalMtimeNS: walMt,
	}
	if b, merr := json.Marshal(nc); merr == nil {
		ns.State = string(b)
	}
	return ns, nil
}
