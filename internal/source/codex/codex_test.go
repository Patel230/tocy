package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lakshmanpatel/tocy/internal/source"
)

const fixtureHead = `{"timestamp":"2026-07-03T03:05:15.100Z","type":"session_meta","payload":{"id":"019f24c1-e6a6-77b3-906b-0e1599d3f7e0","cwd":"/Users/x/proj","originator":"codex-tui","cli_version":"0.142.5"}}
{"timestamp":"2026-07-03T03:05:15.200Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/Users/x/proj","model":"gpt-5.4","effort":"medium"}}
{"timestamp":"2026-07-03T03:05:16.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"text","text":"hi"}]}}
{"timestamp":"2026-07-03T03:05:17.000Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex"}}}
{"timestamp":"2026-07-03T03:05:18.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":15050,"cached_input_tokens":7040,"output_tokens":169,"reasoning_output_tokens":96,"total_tokens":15219},"last_token_usage":{"input_tokens":15050,"cached_input_tokens":7040,"output_tokens":169,"reasoning_output_tokens":96,"total_tokens":15219},"model_context_window":258400}}}
`

const fixtureTail = `{"timestamp":"2026-07-03T03:06:00.000Z","type":"turn_context","payload":{"turn_id":"t2","cwd":"/Users/x/proj","model":"codex-auto-review","effort":"low"}}
{"timestamp":"2026-07-03T03:06:05.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30000,"cached_input_tokens":20000,"output_tokens":400,"reasoning_output_tokens":150,"total_tokens":30400},"last_token_usage":{"input_tokens":14950,"cached_input_tokens":12960,"output_tokens":231,"reasoning_output_tokens":54,"total_tokens":15181}}}}
{"timestamp":"2026-07-03T03:06:06.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":30000,"cached_input_tokens":20000,"output_tokens":400,"reasoning_output_tokens":150,"total_tokens":30400}}}}
`

func parseAll(t *testing.T, s *Src, path string, st source.FileState) ([]source.UsageEvent, source.FileState) {
	t.Helper()
	var got []source.UsageEvent
	ns, err := s.Parse(path, &st, func(e source.UsageEvent) { got = append(got, e) })
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return got, ns
}

func TestParseAndResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-2026-07-03T03-05-15-019f24c1-e6a6-77b3-906b-0e1599d3f7e0.jsonl")
	if err := os.WriteFile(path, []byte(fixtureHead), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewWithRoot(dir)
	got, st1 := parseAll(t, s, path, source.FileState{})
	if len(got) != 1 {
		t.Fatalf("head: want 1 event, got %d", len(got))
	}
	e := got[0]
	if e.Model != "gpt-5.4" || e.SessionID != "019f24c1-e6a6-77b3-906b-0e1599d3f7e0" || e.Project != "/Users/x/proj" {
		t.Errorf("attribution wrong: %+v", e)
	}
	// input excludes cache; output excludes reasoning; total preserved.
	if e.Input != 15050-7040 || e.CacheRead != 7040 || e.Output != 169-96 || e.Reasoning != 96 {
		t.Errorf("token classes wrong: %+v", e)
	}
	if e.Total() != 15219 {
		t.Errorf("Total() = %d, want 15219", e.Total())
	}

	// Append the tail and resume from saved state: model switches mid-file,
	// cumulative totals get diffed, and the zero-delta event is dropped.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(fixtureTail); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got2, st2 := parseAll(t, s, path, st1)
	if len(got2) != 1 {
		t.Fatalf("tail: want 1 event (zero-delta dropped), got %d", len(got2))
	}
	e2 := got2[0]
	if e2.Model != "codex-auto-review" {
		t.Errorf("model not updated from second turn_context: %q", e2.Model)
	}
	if e2.Input != (30000-15050)-(20000-7040) || e2.CacheRead != 20000-7040 ||
		e2.Output != (400-169)-(150-96) || e2.Reasoning != 150-96 {
		t.Errorf("delta classes wrong: %+v", e2)
	}
	if e2.Total() != 30400-15219 {
		t.Errorf("delta Total() = %d, want %d", e2.Total(), 30400-15219)
	}
	if e2.DedupKey == e.DedupKey {
		t.Error("dedup keys must differ across events")
	}
	if st2.Offset <= st1.Offset {
		t.Errorf("offset did not advance: %d -> %d", st1.Offset, st2.Offset)
	}

	// Determinism: a cold re-parse of the full file yields the same dedup keys,
	// so INSERT OR IGNORE keeps ingestion idempotent after a state reset.
	all, _ := parseAll(t, s, path, source.FileState{})
	if len(all) != 2 || all[0].DedupKey != e.DedupKey || all[1].DedupKey != e2.DedupKey {
		t.Errorf("cold re-parse not deterministic: %+v", all)
	}
}

func TestScanTargets(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "2026", "07", "03")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(deep, "rollout-2026-07-03T03-05-15-019f24c1-e6a6-77b3-906b-0e1599d3f7e0.jsonl")
	if err := os.WriteFile(path, []byte(fixtureHead), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewWithRoot(dir)
	targets, err := s.ScanTargets()
	if err != nil || len(targets) != 1 || targets[0] != path {
		t.Fatalf("ScanTargets = %v, %v", targets, err)
	}
}
