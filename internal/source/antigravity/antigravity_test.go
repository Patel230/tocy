package antigravity

import (
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lakshmanpatel/tocy/internal/source"
)

// --- tiny protobuf writer mirroring the reader in antigravity.go ---

func pvarint(num int, v uint64) []byte {
	out := binary.AppendUvarint(nil, uint64(num)<<3|0)
	return binary.AppendUvarint(out, v)
}

func pbytes(num int, data []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(num)<<3|2)
	out = binary.AppendUvarint(out, uint64(len(data)))
	return append(out, data...)
}

func pstr(num int, s string) []byte { return pbytes(num, []byte(s)) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// stepMeta builds a steps.metadata blob: field 1 = Timestamp{1:secs},
// field 9 = usage{1:modelID, 2:input, 3:output, 5:cacheRead, 11:genID}.
func stepMeta(secs int64, modelID, input, output, cacheRead uint64, genID string) []byte {
	usage := cat(
		pvarint(1, modelID),
		pvarint(2, input),
		pvarint(3, output),
	)
	if cacheRead > 0 {
		usage = append(usage, pvarint(5, cacheRead)...)
	}
	if genID != "" {
		usage = append(usage, pstr(11, genID)...)
	}
	return cat(
		pbytes(1, pvarint(1, uint64(secs))),
		pbytes(9, usage),
	)
}

// genMeta builds a gen_metadata.data blob: field 3 = {15:{1:modelID}, 28:name}.
func genMeta(modelID uint64, name string) []byte {
	return pbytes(3, cat(
		pbytes(15, pvarint(1, modelID)),
		pstr(28, name),
	))
}

// trajMeta builds a trajectory_metadata_blob.data blob: field 1 = {1: uri}.
func trajMeta(uri string) []byte {
	return pbytes(1, pstr(1, uri))
}

const testSchema = `
CREATE TABLE steps (idx integer, step_type integer NOT NULL DEFAULT 0,
  status integer NOT NULL DEFAULT 0, metadata blob, step_payload blob,
  PRIMARY KEY (idx));
CREATE TABLE gen_metadata (idx integer, data blob, size integer NOT NULL DEFAULT 0,
  PRIMARY KEY (idx));
CREATE TABLE trajectory_metadata_blob (id text DEFAULT "main", data blob,
  PRIMARY KEY (id));
`

func writeTestDB(t *testing.T, dir, session string) string {
	t.Helper()
	path := filepath.Join(dir, session+".db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	exec(`INSERT INTO trajectory_metadata_blob (id, data) VALUES ('main', ?)`,
		trajMeta("file:///tmp/myproj"))
	exec(`INSERT INTO gen_metadata (idx, data) VALUES (0, ?)`, genMeta(1071, "gemini-3.6-flash-high"))
	exec(`INSERT INTO gen_metadata (idx, data) VALUES (1, ?)`, genMeta(1035, "claude-sonnet-4-6"))
	// idx 0: no usage submessage (plan step) — must be skipped.
	exec(`INSERT INTO steps (idx, metadata) VALUES (0, ?)`,
		pbytes(1, pvarint(1, 1785000000)))
	exec(`INSERT INTO steps (idx, metadata) VALUES (1, ?)`,
		stepMeta(1785000100, 1071, 3755, 372, 93413, "gen-aaa"))
	exec(`INSERT INTO steps (idx, metadata) VALUES (2, ?)`,
		stepMeta(1785000200, 1035, 1380, 150, 0, "req_vrtx_bbb"))
	// idx 3: unknown model id, no gen id — falls back to session:idx key.
	exec(`INSERT INTO steps (idx, metadata) VALUES (3, ?)`,
		stepMeta(1785000300, 9999, 10, 5, 0, ""))
	// idx 4: zero tokens — must be skipped.
	exec(`INSERT INTO steps (idx, metadata) VALUES (4, ?)`,
		stepMeta(1785000400, 1071, 0, 0, 0, "gen-zero"))
	// idx 5: garbage metadata — must be skipped, not fatal.
	exec(`INSERT INTO steps (idx, metadata) VALUES (5, ?)`, []byte{0xff, 0xff, 0xff})
	return path
}

func parseAll(t *testing.T, s *Src, path string, st *source.FileState) ([]source.UsageEvent, source.FileState) {
	t.Helper()
	var evs []source.UsageEvent
	ns, err := s.Parse(path, st, func(e source.UsageEvent) { evs = append(evs, e) })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return evs, ns
}

func TestParse(t *testing.T) {
	dir := t.TempDir()
	sess := "781d021a-8c5c-4a56-a8c5-ee7f9fd16904"
	path := writeTestDB(t, dir, sess)
	s := NewWithRoot(dir)

	if found, root := s.Detect(); !found || root != dir {
		t.Fatalf("Detect = (%v, %q)", found, root)
	}
	targets, err := s.ScanTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("ScanTargets = (%v, %v)", targets, err)
	}

	evs, ns := parseAll(t, s, path, &source.FileState{Path: path, Source: name})
	if len(evs) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(evs), evs)
	}
	e := evs[0]
	if e.Source != name || e.DedupKey != "gen-aaa" || e.Model != "gemini-3.6-flash-high" ||
		e.SessionID != sess || e.Project != "/tmp/myproj" ||
		e.Input != 3755 || e.Output != 372 || e.CacheRead != 93413 ||
		!e.TS.Equal(time.Unix(1785000100, 0)) {
		t.Fatalf("event[0] = %+v", e)
	}
	if evs[1].Model != "claude-sonnet-4-6" || evs[1].DedupKey != "req_vrtx_bbb" {
		t.Fatalf("event[1] = %+v", evs[1])
	}
	if evs[2].Model != "antigravity-9999" || evs[2].DedupKey != sess+":3" {
		t.Fatalf("event[2] = %+v", evs[2])
	}

	// Unchanged db: stat gate returns without emitting.
	evs2, ns2 := parseAll(t, s, path, &ns)
	if len(evs2) != 0 {
		t.Fatalf("unchanged reparse emitted %d events", len(evs2))
	}
	if ns2.State != ns.State {
		t.Fatalf("state changed on unchanged db: %q -> %q", ns.State, ns2.State)
	}
}

func TestParseIncremental(t *testing.T) {
	dir := t.TempDir()
	sess := "31bab8b9-2c88-404f-8212-86a8e7f4418a"
	path := writeTestDB(t, dir, sess)
	s := NewWithRoot(dir)

	evs, ns := parseAll(t, s, path, &source.FileState{Path: path, Source: name})
	if len(evs) != 3 {
		t.Fatalf("initial events = %d, want 3", len(evs))
	}

	// Append a new step and bump mtime so the stat gate reopens the db.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO steps (idx, metadata) VALUES (6, ?)`,
		stepMeta(1785000600, 1071, 200, 20, 0, "gen-ccc")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	db.Close()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	evs2, ns2 := parseAll(t, s, path, &ns)
	if len(evs2) != 1 || evs2[0].DedupKey != "gen-ccc" {
		t.Fatalf("incremental events = %+v, want just gen-ccc", evs2)
	}

	// Cold re-parse (reset state) must reproduce identical dedup keys.
	evs3, _ := parseAll(t, s, path, &source.FileState{Path: path, Source: name})
	keys := map[string]bool{}
	for _, e := range evs3 {
		keys[e.DedupKey] = true
	}
	for _, e := range append(evs, evs2...) {
		if !keys[e.DedupKey] {
			t.Fatalf("cold re-parse missing dedup key %q", e.DedupKey)
		}
	}
	_ = ns2
}
