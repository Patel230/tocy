package pricing

import (
	"sync"
	"testing"
)

// table parses the embedded snapshot once for all matcher tests.
func table(t *testing.T) *Table {
	t.Helper()
	tb := parse(snapshot)
	if tb == nil {
		t.Fatal("embedded snapshot failed to parse")
	}
	if tb.Count < 500 {
		t.Fatalf("suspiciously small snapshot: %d entries", tb.Count)
	}
	return tb
}

func TestMatchRealModelNames(t *testing.T) {
	tb := table(t)
	// Every model name observed in real logs on this machine must resolve.
	for _, m := range []string{
		"claude-sonnet-5",
		"claude-opus-4-7",
		"claude-opus-4-8",
		"claude-opus-4-6",
		"claude-sonnet-4-6",
		"claude-fable-5",
		"claude-haiku-4-5-20251001", // date suffix
		"gpt-5.5",
		"anthropic/claude-sonnet-5", // provider prefix
	} {
		if _, ok := tb.Match(m); !ok {
			t.Errorf("Match(%q) = miss, want hit", m)
		}
	}
}

func TestMatchVariants(t *testing.T) {
	tb := table(t)
	cases := []struct{ a, b string }{
		{"claude-opus-4.7", "claude-opus-4-7"},        // dot/dash version swap
		{"claude-sonnet-5-latest", "claude-sonnet-5"}, // -latest strip
	}
	for _, c := range cases {
		pa, oka := tb.Match(c.a)
		pb, okb := tb.Match(c.b)
		if !oka || !okb {
			t.Errorf("Match(%q)=%v Match(%q)=%v, want both hits", c.a, oka, c.b, okb)
			continue
		}
		if pa != pb {
			t.Errorf("Match(%q) and Match(%q) resolved to different prices", c.a, c.b)
		}
	}
}

func TestMissNeverZeroDollar(t *testing.T) {
	tb := table(t)
	if _, ok := tb.Match("totally-made-up-model-xyz"); ok {
		t.Error("nonsense model matched; matcher too loose")
	}
	if usd, ok := tb.Cost("totally-made-up-model-xyz", 1000, 1000, 0, 0); ok || usd != 0 {
		t.Errorf("Cost on miss = (%v, %v), want (0, false)", usd, ok)
	}
	if _, ok := tb.Match(""); ok {
		t.Error("empty model name matched")
	}
}

func TestCostArithmetic(t *testing.T) {
	tb := &Table{
		prices: map[string]ModelPrice{"m": {
			InputCostPerToken:           1e-6,
			OutputCostPerToken:          2e-6,
			CacheReadInputTokenCost:     1e-7,
			CacheCreationInputTokenCost: 5e-7,
		}},
		norm: map[string]string{"m": "m"},
		memo: map[string]string{},
	}
	usd, ok := tb.Cost("m", 1_000_000, 500_000, 2_000_000, 100_000)
	if !ok {
		t.Fatal("expected hit")
	}
	want := 1.0 + 1.0 + 0.2 + 0.05
	if diff := usd - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("Cost = %v, want %v", usd, want)
	}
}

func TestMemoization(t *testing.T) {
	tb := table(t)
	tb.Match("claude-sonnet-5")
	if _, seen := tb.memo["claude-sonnet-5"]; !seen {
		t.Error("hit not memoized")
	}
	tb.Match("nope-nope")
	if key, seen := tb.memo["nope-nope"]; !seen || key != "" {
		t.Error("miss not memoized as empty key")
	}
}

// TestMatchConcurrent guards the memo mutex: the TUI calls Match from
// overlapping load goroutines, so this must stay clean under -race.
func TestMatchConcurrent(t *testing.T) {
	tb := table(t)
	models := []string{
		"claude-sonnet-5", "claude-opus-4-7", "gpt-5.5",
		"nope-1", "nope-2", "anthropic/claude-sonnet-5",
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				tb.Match(models[i%len(models)])
				tb.Cost(models[i%len(models)], 100, 100, 0, 0)
			}
		}()
	}
	wg.Wait()
}
