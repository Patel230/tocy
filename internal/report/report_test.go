package report

import (
	"strings"
	"testing"
)

func TestShortProj(t *testing.T) {
	if got := ShortProj("/Users/x/Desktop/ProjectAlpha/tocy"); got != ".../ProjectAlpha/tocy" {
		t.Errorf("ShortProj = %q", got)
	}
	if got := ShortProj(""); got != "(unknown)" {
		t.Errorf("ShortProj empty = %q", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("ab", 4); got != "ab" {
		t.Errorf("Truncate short = %q", got)
	}
}

func TestCostCell(t *testing.T) {
	if got := CostCell(Line{Cost: 4.31}); got != "$4.31" {
		t.Errorf("CostCell = %q", got)
	}
	if got := CostCell(Line{Cost: 0, UnpricedEvents: 3}); got != "-*" {
		t.Errorf("CostCell unpriced = %q", got)
	}
	if got := CostCell(Line{Cost: 1.5, UnpricedEvents: 1}); got != "$1.50*" {
		t.Errorf("CostCell partial = %q", got)
	}
}

func TestHumanize(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{999, "999"},
		{1_000, "1K"},
		{10_000, "10K"},
		{1_500_000, "1.5M"},
		{1_000_000_000, "1B"},
	}
	for _, c := range cases {
		if got := Humanize(c.in); got != c.want {
			t.Errorf("Humanize(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOptimizationTip(t *testing.T) {
	// Empty input
	if got := OptimizationTip(nil); got != "" {
		t.Errorf("OptimizationTip(nil) = %q, want empty", got)
	}

	// No suggestions for low-cost models with low input
	lines := []Line{{Key: "gpt-4o-mini", Cost: 0.01, Input: 100}}
	if got := OptimizationTip(lines); got != "" {
		t.Errorf("OptimizationTip(low usage) = %q, want empty", got)
	}

	// Suggestion when Pro model is used
	lines = []Line{{Key: "claude-sonnet-4-20250514-Pro", Cost: 5.0, Input: 5000}}
	got := OptimizationTip(lines)
	if got == "" {
		t.Error("OptimizationTip(Pro model) = empty, want suggestion")
	}
	if !strings.Contains(got, "Haiku") {
		t.Errorf("OptimizationTip(Pro model) = %q, want Haiku suggestion", got)
	}

	// High input volume warning
	lines = []Line{{Key: "claude-sonnet-4-20250514", Cost: 10.0, Input: 10_000_001}}
	got = OptimizationTip(lines)
	if got == "" {
		t.Error("OptimizationTip(high input) = empty, want batching suggestion")
	}
	if !strings.Contains(got, "batch") {
		t.Errorf("OptimizationTip(high input) = %q, want batch suggestion", got)
	}
}
