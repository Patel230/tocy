package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/lakshmanpatel/tocy/internal/source"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY, source TEXT NOT NULL, dedup_key TEXT NOT NULL,
  ts INTEGER NOT NULL, model TEXT, session_id TEXT, project TEXT,
  input INTEGER DEFAULT 0, output INTEGER DEFAULT 0,
  cache_read INTEGER DEFAULT 0, cache_write INTEGER DEFAULT 0, reasoning INTEGER DEFAULT 0,
  raw_cost REAL, UNIQUE(source, dedup_key));
CREATE INDEX IF NOT EXISTS ix_ts ON events(ts);
CREATE INDEX IF NOT EXISTS ix_src_ts ON events(source, ts);
CREATE INDEX IF NOT EXISTS ix_model_ts ON events(model, ts);
CREATE TABLE IF NOT EXISTS ingest_files (
  path TEXT PRIMARY KEY, source TEXT NOT NULL,
  inode INTEGER, size INTEGER, mtime INTEGER, offset INTEGER DEFAULT 0,
  state TEXT, updated_at INTEGER);
`

type Store struct{ DB *sql.DB }

func DefaultPath() string {
	if p := os.Getenv("TOCY_DB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".tocy", "tocy.db")
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(3000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v < 1 {
		if _, err := s.DB.Exec(schema); err != nil {
			return err
		}
		if _, err := s.DB.Exec("PRAGMA user_version = 1"); err != nil {
			return err
		}
	}
	if v < 2 {
		// Sessions() groups and filters on session_id; index it so the
		// query stays fast as the events table grows.
		if _, err := s.DB.Exec("CREATE INDEX IF NOT EXISTS ix_session ON events(session_id, source)"); err != nil {
			return err
		}
		if _, err := s.DB.Exec("PRAGMA user_version = 2"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) InsertEvents(events []source.UsageEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO events
		(source, dedup_key, ts, model, session_id, project,
		 input, output, cache_read, cache_write, reasoning, raw_cost)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	inserted := 0
	for _, e := range events {
		var rawCost any
		if e.RawCost != nil {
			rawCost = *e.RawCost
		}
		res, err := stmt.Exec(e.Source, e.DedupKey, e.TS.Unix(), e.Model,
			e.SessionID, e.Project, e.Input, e.Output, e.CacheRead,
			e.CacheWrite, e.Reasoning, rawCost)
		if err != nil {
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, tx.Commit()
}

func (s *Store) GetFileState(path string) (*source.FileState, error) {
	row := s.DB.QueryRow(`SELECT path, source, inode, size, mtime, offset, COALESCE(state,'')
		FROM ingest_files WHERE path = ?`, path)
	var fs source.FileState
	err := row.Scan(&fs.Path, &fs.Source, &fs.Inode, &fs.Size, &fs.Mtime, &fs.Offset, &fs.State)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

func (s *Store) SaveFileState(fs source.FileState) error {
	_, err := s.DB.Exec(`INSERT INTO ingest_files (path, source, inode, size, mtime, offset, state, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
		  source=excluded.source, inode=excluded.inode, size=excluded.size,
		  mtime=excluded.mtime, offset=excluded.offset, state=excluded.state,
		  updated_at=excluded.updated_at`,
		fs.Path, fs.Source, fs.Inode, fs.Size, fs.Mtime, fs.Offset, fs.State, time.Now().Unix())
	return err
}

type AggOpts struct {
	Since   time.Time
	Until   time.Time
	GroupBy string
	Source  string
}

type AggRow struct {
	Key        string
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
	Events     int64
	RawCost    float64
	HasRawCost bool
}

type SessionRow struct {
	SessionID  string
	Source     string
	Project    string
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Reasoning  int64
	Events     int64
	RawCost    float64
	HasRawCost bool
	FirstTS    int64
	LastTS     int64
}

func dimExpr(groupBy string) (string, error) {
	switch groupBy {
	case "tool", "":
		return "source", nil
	case "model":
		return "COALESCE(model,'unknown')", nil
	case "day":
		return "strftime('%Y-%m-%d', ts, 'unixepoch', 'localtime')", nil
	case "project":
		return "COALESCE(project,'')", nil
	case "session":
		return "COALESCE(session_id,'')", nil
	default:
		return "", fmt.Errorf("unknown --by value %q (want tool|model|day|project|session)", groupBy)
	}
}

func (s *Store) Aggregate(o AggOpts) ([]AggRow, error) {
	dim, err := dimExpr(o.GroupBy)
	if err != nil {
		return nil, err
	}
	var conds []string
	var args []any
	if !o.Since.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, o.Since.Unix())
	}
	if !o.Until.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, o.Until.Unix())
	}
	if o.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, o.Source)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	q := fmt.Sprintf(`SELECT %s AS k, COALESCE(model,'unknown'),
			SUM(input), SUM(output), SUM(cache_read), SUM(cache_write), SUM(reasoning),
			COUNT(*), COALESCE(SUM(raw_cost), 0), COUNT(raw_cost)
		FROM events %s GROUP BY k, model ORDER BY k`, dim, where)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AggRow
	for rows.Next() {
		var r AggRow
		var nRaw int64
		if err := rows.Scan(&r.Key, &r.Model, &r.Input, &r.Output, &r.CacheRead,
			&r.CacheWrite, &r.Reasoning, &r.Events, &r.RawCost, &nRaw); err != nil {
			return nil, err
		}
		r.HasRawCost = nRaw > 0
		out = append(out, r)
	}
	return out, rows.Err()
}

type SourceStat struct {
	Source  string
	Events  int64
	FirstTS time.Time
	LastTS  time.Time
}

func (s *Store) Sessions(o AggOpts) ([]SessionRow, error) {
	var conds []string
	var args []any
	if !o.Since.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, o.Since.Unix())
	}
	if !o.Until.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, o.Until.Unix())
	}
	if o.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, o.Source)
	}
	conds = append(conds, "session_id != '' AND session_id IS NOT NULL")
	where := "WHERE " + strings.Join(conds, " AND ")

	// Grouped by model too (not just session_id, source) so callers can price
	// each model's tokens individually — a session mixing models can't be
	// priced correctly from a single blended row.
	q := fmt.Sprintf(`SELECT session_id, source, MAX(COALESCE(project,'')), COALESCE(model,''),
			SUM(input), SUM(output),
			SUM(cache_read), SUM(cache_write), SUM(reasoning),
			COUNT(*), COALESCE(SUM(raw_cost), 0), COUNT(raw_cost),
			MIN(ts), MAX(ts)
		FROM events %s GROUP BY session_id, source, model`, where)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		var nRaw int64
		if err := rows.Scan(&r.SessionID, &r.Source, &r.Project, &r.Model,
			&r.Input, &r.Output,
			&r.CacheRead, &r.CacheWrite, &r.Reasoning,
			&r.Events, &r.RawCost, &nRaw, &r.FirstTS, &r.LastTS); err != nil {
			return nil, err
		}
		r.HasRawCost = nRaw > 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Prune(before time.Time) (int64, error) {
	res, err := s.DB.Exec(`DELETE FROM events WHERE ts < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) EarliestEvent() (time.Time, error) {
	var ts sql.NullInt64
	err := s.DB.QueryRow("SELECT MIN(ts) FROM events").Scan(&ts)
	if err != nil || !ts.Valid || ts.Int64 == 0 {
		return time.Time{}, err
	}
	return time.Unix(ts.Int64, 0), nil
}

func (s *Store) SourceStats() (map[string]SourceStat, error) {
	rows, err := s.DB.Query(`SELECT source, COUNT(*), MIN(ts), MAX(ts) FROM events GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]SourceStat{}
	for rows.Next() {
		var st SourceStat
		var minTS, maxTS int64
		if err := rows.Scan(&st.Source, &st.Events, &minTS, &maxTS); err != nil {
			return nil, err
		}
		st.FirstTS = time.Unix(minTS, 0)
		st.LastTS = time.Unix(maxTS, 0)
		out[st.Source] = st
	}
	return out, rows.Err()
}
