package opencode

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"
)

func newFixtureDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE message (
		id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
		time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL,
		data TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func insert(t *testing.T, db *sql.DB, id, sid string, tc int64, data string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT OR REPLACE INTO message (id, session_id, time_created, time_updated, data)
		 VALUES (?,?,?,?,?)`, id, sid, tc, tc, data); err != nil {
		t.Fatal(err)
	}
}

func assistantJSON(created, completed int64, cost float64) string {
	comp := ""
	if completed > 0 {
		comp = fmt.Sprintf(`,"completed":%d`, completed)
	}
	return fmt.Sprintf(`{"role":"assistant","cost":%g,`+
		`"tokens":{"total":49569,"input":150,"output":28,"reasoning":111,"cache":{"write":0,"read":49280}},`+
		`"modelID":"big-pickle","providerID":"opencode",`+
		`"path":{"cwd":"/Users/x/proj","root":"/"},`+
		`"time":{"created":%d%s},"finish":"stop"}`, cost, created, comp)
}

func parseAll(t *testing.T, s *Src, st source.FileState) ([]source.UsageEvent, source.FileState) {
	t.Helper()
	var got []source.UsageEvent
	ns, err := s.Parse(s.dbPath, &st, func(e source.UsageEvent) { got = append(got, e) })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return got, ns
}

func TestParseCursorAndPinning(t *testing.T) {
	path, db := newFixtureDB(t)
	now := time.Now().UnixMilli()

	insert(t, db, "msg_u1", "ses_1", now-5000, `{"role":"user","time":{"created":1}}`)
	insert(t, db, "msg_a1", "ses_1", now-4000, assistantJSON(now-4000, now-3500, 0.25))
	insert(t, db, "msg_a2", "ses_1", now-2000, assistantJSON(now-2000, 0, 0)) // still streaming
	insert(t, db, "msg_a3", "ses_1", now-1000, assistantJSON(now-1000, now-900, 0))

	s := NewWithDB(path)
	got, st1 := parseAll(t, s, source.FileState{})
	if len(got) != 2 {
		t.Fatalf("want 2 completed events, got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.DedupKey != "msg_a1" || e.SessionID != "ses_1" || e.Model != "big-pickle" || e.Project != "/Users/x/proj" {
		t.Errorf("attribution wrong: %+v", e)
	}
	if e.Input != 150 || e.Output != 28 || e.Reasoning != 111 || e.CacheRead != 49280 || e.CacheWrite != 0 {
		t.Errorf("token classes wrong: %+v", e)
	}
	if e.Total() != 49569 {
		t.Errorf("Total() = %d, want 49569", e.Total())
	}
	if e.RawCost == nil || *e.RawCost != 0.25 {
		t.Errorf("RawCost = %v, want 0.25", e.RawCost)
	}
	if got[1].RawCost == nil || *got[1].RawCost != 0 {
		t.Errorf("zero cost must still be recorded as raw cost: %+v", got[1])
	}
	if st1.Offset != 0 {
		t.Errorf("Offset must stay 0 for db sources, got %d", st1.Offset)
	}

	// Cursor must be pinned at the incomplete row, not the max row.
	var cur cursorState
	if err := jsonUnmarshal(st1.State, &cur); err != nil || cur.CursorMS != now-2000 {
		t.Fatalf("cursor = %+v (%v), want pin at %d", cur, err, now-2000)
	}

	// Finish the streaming row; resume from saved state.
	insert(t, db, "msg_a2", "ses_1", now-2000, assistantJSON(now-2000, now-100, 0.10))
	got2, st2 := parseAll(t, s, st1)
	keys := map[string]bool{}
	for _, e := range got2 {
		keys[e.DedupKey] = true
	}
	if !keys["msg_a2"] {
		t.Fatalf("finished row not picked up on resume: %+v", got2)
	}
	if keys["msg_a1"] {
		t.Errorf("row before cursor re-emitted: %+v", got2)
	}
	// Boundary re-reads are allowed (>= cursor) — the store dedups them.
	if err := jsonUnmarshal(st2.State, &cur); err != nil || cur.CursorMS != now-1000 {
		t.Fatalf("cursor after resume = %+v, want %d", cur, now-1000)
	}
}

func TestAbandonedIncompleteDoesNotPin(t *testing.T) {
	path, db := newFixtureDB(t)
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	now := time.Now().UnixMilli()
	insert(t, db, "msg_dead", "ses_1", old, assistantJSON(old, 0, 0)) // crashed 2 days ago
	insert(t, db, "msg_ok", "ses_1", now-1000, assistantJSON(now-1000, now-900, 0))

	s := NewWithDB(path)
	got, st := parseAll(t, s, source.FileState{})
	if len(got) != 1 || got[0].DedupKey != "msg_ok" {
		t.Fatalf("want only msg_ok, got %+v", got)
	}
	var cur cursorState
	if err := jsonUnmarshal(st.State, &cur); err != nil || cur.CursorMS != now-1000 {
		t.Fatalf("stale row must not pin cursor: %+v", cur)
	}
}

func TestAlwaysScan(t *testing.T) {
	if !New().AlwaysScan() {
		t.Error("opencode must opt out of the size/mtime unchanged skip (WAL)")
	}
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}
