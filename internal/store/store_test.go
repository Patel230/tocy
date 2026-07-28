package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ev(src, key, model, session string, ts time.Time, in, out int64) source.UsageEvent {
	return source.UsageEvent{
		Source: src, DedupKey: key, Model: model, SessionID: session,
		TS: ts, Input: in, Output: out,
	}
}

func TestMigrateVersionAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var v int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != 2 {
		t.Fatalf("user_version = %d, want 2", v)
	}
	s.Close()

	// Reopening an already-migrated DB must succeed and keep the version.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if err := s2.DB.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != 2 {
		t.Fatalf("user_version after reopen = %d, want 2", v)
	}
	var n int
	if err := s2.DB.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='ix_session'").Scan(&n); err != nil {
		t.Fatalf("index check: %v", err)
	}
	if n != 1 {
		t.Fatal("ix_session index missing after migration")
	}
}

func TestInsertEventsDedup(t *testing.T) {
	s := openTest(t)
	ts := time.Unix(1700000000, 0)
	batch := []source.UsageEvent{
		ev("codex", "k1", "gpt-5", "sess1", ts, 100, 10),
		ev("codex", "k2", "gpt-5", "sess1", ts, 200, 20),
		ev("codex", "k1", "gpt-5", "sess1", ts, 999, 99), // dup within batch
	}
	n, err := s.InsertEvents(batch)
	if err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted = %d, want 2", n)
	}
	// Re-insert of the whole batch is a no-op.
	n, err = s.InsertEvents(batch)
	if err != nil {
		t.Fatalf("re-InsertEvents: %v", err)
	}
	if n != 0 {
		t.Fatalf("re-insert = %d, want 0", n)
	}
	// Same dedup key under another source is a distinct event.
	n, err = s.InsertEvents([]source.UsageEvent{ev("opencode", "k1", "m", "s", ts, 1, 1)})
	if err != nil || n != 1 {
		t.Fatalf("cross-source insert = (%d, %v), want (1, nil)", n, err)
	}
}

func TestAggregateGroupingAndFilters(t *testing.T) {
	s := openTest(t)
	base := time.Unix(1700000000, 0)
	cost := 0.5
	events := []source.UsageEvent{
		ev("codex", "a", "gpt-5", "s1", base, 100, 10),
		ev("codex", "b", "gpt-5", "s1", base.Add(time.Hour), 50, 5),
		ev("claude-code", "a", "opus", "s2", base.Add(2*time.Hour), 30, 3),
	}
	events[2].RawCost = &cost
	if _, err := s.InsertEvents(events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}

	rows, err := s.Aggregate(AggOpts{GroupBy: "tool"})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	byKey := map[string]AggRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	if r := byKey["codex"]; r.Input != 150 || r.Output != 15 || r.Events != 2 || r.HasRawCost {
		t.Fatalf("codex row = %+v", r)
	}
	if r := byKey["claude-code"]; r.Input != 30 || !r.HasRawCost || r.RawCost != 0.5 {
		t.Fatalf("claude-code row = %+v", r)
	}

	// Time-window and source filters.
	rows, err = s.Aggregate(AggOpts{Since: base.Add(30 * time.Minute), Source: "codex"})
	if err != nil {
		t.Fatalf("filtered Aggregate: %v", err)
	}
	if len(rows) != 1 || rows[0].Events != 1 || rows[0].Input != 50 {
		t.Fatalf("filtered rows = %+v", rows)
	}

	if _, err := s.Aggregate(AggOpts{GroupBy: "bogus"}); err == nil {
		t.Fatal("Aggregate with bad GroupBy: want error")
	}
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	base := time.Unix(1700000000, 0)
	events := []source.UsageEvent{
		ev("codex", "a", "gpt-5", "s1", base, 100, 10),
		ev("codex", "b", "gpt-5", "s1", base.Add(time.Hour), 50, 5),
		ev("codex", "c", "gpt-5-mini", "s1", base.Add(2*time.Hour), 10, 1),
		ev("codex", "d", "gpt-5", "", base, 7, 7), // no session -> excluded
	}
	if _, err := s.InsertEvents(events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	rows, err := s.Sessions(AggOpts{})
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	// One row per (session, source, model): s1 mixes two models.
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.SessionID != "s1" {
			t.Fatalf("unexpected session %q", r.SessionID)
		}
		switch r.Model {
		case "gpt-5":
			if r.Input != 150 || r.Events != 2 || r.FirstTS != base.Unix() || r.LastTS != base.Add(time.Hour).Unix() {
				t.Fatalf("gpt-5 row = %+v", r)
			}
		case "gpt-5-mini":
			if r.Input != 10 || r.Events != 1 {
				t.Fatalf("gpt-5-mini row = %+v", r)
			}
		default:
			t.Fatalf("unexpected model %q", r.Model)
		}
	}
}

func TestPruneAndEarliestEvent(t *testing.T) {
	s := openTest(t)

	// Empty DB: zero time, no error.
	early, err := s.EarliestEvent()
	if err != nil || !early.IsZero() {
		t.Fatalf("EarliestEvent on empty DB = (%v, %v), want (zero, nil)", early, err)
	}

	base := time.Unix(1700000000, 0)
	events := []source.UsageEvent{
		ev("codex", "old", "m", "s", base, 1, 1),
		ev("codex", "new", "m", "s", base.Add(48*time.Hour), 1, 1),
	}
	if _, err := s.InsertEvents(events); err != nil {
		t.Fatalf("InsertEvents: %v", err)
	}
	early, err = s.EarliestEvent()
	if err != nil || !early.Equal(base) {
		t.Fatalf("EarliestEvent = (%v, %v), want %v", early, err, base)
	}

	n, err := s.Prune(base.Add(24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("Prune = (%d, %v), want (1, nil)", n, err)
	}
	early, err = s.EarliestEvent()
	if err != nil || !early.Equal(base.Add(48*time.Hour)) {
		t.Fatalf("EarliestEvent after prune = (%v, %v)", early, err)
	}
}

func TestFileStateRoundTrip(t *testing.T) {
	s := openTest(t)

	got, err := s.GetFileState("/nope")
	if err != nil || got != nil {
		t.Fatalf("GetFileState missing = (%v, %v), want (nil, nil)", got, err)
	}

	fs := source.FileState{
		Path: "/tmp/a.jsonl", Source: "codex",
		Inode: 42, Size: 1000, Mtime: 1700000000, Offset: 500, State: `{"seq":3}`,
	}
	if err := s.SaveFileState(fs); err != nil {
		t.Fatalf("SaveFileState: %v", err)
	}
	got, err = s.GetFileState(fs.Path)
	if err != nil {
		t.Fatalf("GetFileState: %v", err)
	}
	if got == nil || *got != fs {
		t.Fatalf("round trip = %+v, want %+v", got, fs)
	}

	// Upsert overwrites.
	fs.Offset = 1000
	fs.State = `{"seq":7}`
	if err := s.SaveFileState(fs); err != nil {
		t.Fatalf("SaveFileState update: %v", err)
	}
	got, _ = s.GetFileState(fs.Path)
	if got.Offset != 1000 || got.State != `{"seq":7}` {
		t.Fatalf("after update = %+v", got)
	}
}
