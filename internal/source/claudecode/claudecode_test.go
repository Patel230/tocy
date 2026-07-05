package claudecode

import (
	"path/filepath"
	"testing"

	"github.com/lakshmanpatel/tocy/internal/source"
)

const fixtureRoot = "../../../testdata/claudecode/projects"

func parseFixture(t *testing.T) []source.UsageEvent {
	t.Helper()
	src := NewWithRoot(fixtureRoot)
	targets, err := src.ScanTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("ScanTargets = %v, %v; want 1 file", targets, err)
	}
	var events []source.UsageEvent
	st := &source.FileState{Path: targets[0], Source: src.Name()}
	ns, err := src.Parse(targets[0], st, func(e source.UsageEvent) {
		events = append(events, e)
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ns.Offset == 0 {
		t.Fatal("Parse did not advance offset")
	}
	return events
}

func TestParseFixture(t *testing.T) {
	events := parseFixture(t)
	// a1, a2 (dup key), a4 — synthetic and non-assistant lines skipped.
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}

	e := events[0]
	if e.DedupKey != "msg_01:req_1" {
		t.Errorf("dedup key = %q, want msg_01:req_1", e.DedupKey)
	}
	if e.Model != "claude-opus-4-7" || e.Input != 10 || e.Output != 200 ||
		e.CacheWrite != 1500 || e.CacheRead != 9000 {
		t.Errorf("bad event: %+v", e)
	}
	if e.Project != "/tmp/proj" || e.SessionID != "s1" {
		t.Errorf("bad project/session: %+v", e)
	}
	if got := e.TS.Format("2006-01-02T15:04:05"); got != "2026-07-01T10:00:05" {
		t.Errorf("ts = %s", got)
	}

	// Duplicate carries the same dedup key (DB layer collapses it).
	if events[1].DedupKey != events[0].DedupKey {
		t.Errorf("dup line should share dedup key: %q vs %q", events[1].DedupKey, events[0].DedupKey)
	}
	if events[2].DedupKey != "msg_03:req_3" || events[2].Model != "claude-sonnet-5" {
		t.Errorf("bad third event: %+v", events[2])
	}
}

func TestParseResumeFromOffset(t *testing.T) {
	src := NewWithRoot(fixtureRoot)
	path := filepath.Join(fixtureRoot, "-tmp-proj", "session1.jsonl")

	// First pass to learn the end offset.
	st := &source.FileState{Path: path}
	ns, err := src.Parse(path, st, func(source.UsageEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	// Resume at end: no new events.
	n := 0
	ns2, err := src.Parse(path, &ns, func(source.UsageEvent) { n++ })
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("resume at EOF emitted %d events, want 0", n)
	}
	if ns2.Offset != ns.Offset {
		t.Errorf("offset moved on empty resume: %d -> %d", ns.Offset, ns2.Offset)
	}
}
