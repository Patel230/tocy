package opencode

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"

	_ "modernc.org/sqlite"
)

const name = "opencode"
const abandonAfter = 24 * time.Hour

func sqliteDSN(path, query string) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
}

type Src struct{ dbPath string }

func New() *Src {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, _ := os.UserHomeDir()
		dataHome = filepath.Join(home, ".local", "share")
	}
	return &Src{dbPath: filepath.Join(dataHome, "opencode", "opencode.db")}
}

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

func (s *Src) AlwaysScan() bool { return true }

type cursorState struct {
	CursorMS   int64 `json:"cursor_ms"`
	Pinned     bool  `json:"pinned,omitempty"`
	DBSize     int64 `json:"db_size,omitempty"`
	DBMtimeNS  int64 `json:"db_mtime_ns,omitempty"`
	WalSize    int64 `json:"wal_size,omitempty"`
	WalMtimeNS int64 `json:"wal_mtime_ns,omitempty"`
}

func statOf(path string) (int64, int64) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return fi.Size(), fi.ModTime().UnixNano()
}

type msgData struct {
	Role   string   `json:"role"`
	Cost   *float64 `json:"cost"`
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
	ns.Offset = 0

	var cur cursorState
	if st.State != "" {
		_ = json.Unmarshal([]byte(st.State), &cur)
	}

	dbSize, dbMt := statOf(path)
	walSize, walMt := statOf(path + "-wal")
	if st.State != "" && !cur.Pinned &&
		cur.DBSize == dbSize && cur.DBMtimeNS == dbMt &&
		cur.WalSize == walSize && cur.WalMtimeNS == walMt {
		return ns, nil
	}

	db, err := sql.Open("sqlite", sqliteDSN(path, "mode=ro&_pragma=busy_timeout(2000)"))
	if err != nil {
		return ns, err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(
		`SELECT id, session_id, time_created, data FROM message
		 WHERE time_created >= ? ORDER BY time_created, id`, cur.CursorMS)
	if err != nil {
		return ns, err
	}
	defer func() { _ = rows.Close() }()

	var (
		maxSeen  = cur.CursorMS
		pinned   int64
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
			continue
		}
		project := ""
		if d.Path != nil {
			project = d.Path.CWD
		}
		emit(source.UsageEvent{
			Source:     name,
			DedupKey:   id,
			Model:      d.ModelID,
			SessionID:  sessionID,
			Project:    project,
			TS:         time.UnixMilli(d.Time.Created).UTC(),
			Input:      t.Input,
			Output:     t.Output,
			CacheRead:  t.Cache.Read,
			CacheWrite: t.Cache.Write,
			Reasoning:  t.Reasoning,
			RawCost:    d.Cost,
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
