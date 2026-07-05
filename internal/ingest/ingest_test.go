package ingest

import (
	"path/filepath"
	"testing"

	"github.com/lakshmanpatel/tocy/internal/source"
	"github.com/lakshmanpatel/tocy/internal/source/claudecode"
	"github.com/lakshmanpatel/tocy/internal/store"
)

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tocy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func fixtureSources() []source.Source {
	return []source.Source{claudecode.NewWithRoot("../../testdata/claudecode/projects")}
}

func TestScanIdempotent(t *testing.T) {
	st := tempStore(t)

	r1 := ScanAll(st, fixtureSources())
	if len(r1) != 1 || r1[0].Err != nil {
		t.Fatalf("scan 1: %+v", r1)
	}
	// Fixture has 3 emitted events, 2 unique dedup keys.
	if r1[0].NewEvents != 2 {
		t.Errorf("scan 1 inserted %d events, want 2 (dup collapsed)", r1[0].NewEvents)
	}

	// Second scan: unchanged files are skipped entirely.
	r2 := ScanAll(st, fixtureSources())
	if r2[0].Err != nil {
		t.Fatalf("scan 2: %+v", r2)
	}
	if r2[0].NewEvents != 0 || r2[0].Files != 0 {
		t.Errorf("scan 2 = %d files/%d events, want 0/0", r2[0].Files, r2[0].NewEvents)
	}

	var count int64
	if err := st.DB.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("events in DB = %d, want 2", count)
	}
}

func TestAggregateByModel(t *testing.T) {
	st := tempStore(t)
	ScanAll(st, fixtureSources())

	rows, err := st.Aggregate(store.AggOpts{GroupBy: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d model rows, want 2: %+v", len(rows), rows)
	}
	byModel := map[string]store.AggRow{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	opus := byModel["claude-opus-4-7"]
	if opus.Input != 10 || opus.Output != 200 || opus.CacheWrite != 1500 || opus.CacheRead != 9000 {
		t.Errorf("opus row wrong: %+v", opus)
	}
	sonnet := byModel["claude-sonnet-5"]
	if sonnet.Input != 5 || sonnet.Output != 50 || sonnet.CacheRead != 12000 {
		t.Errorf("sonnet row wrong: %+v", sonnet)
	}
}
