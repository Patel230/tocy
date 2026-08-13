package tui

import (
	"strings"
	"testing"

	"github.com/lakshmanpatel/tocy/internal/report"
)

func TestHBar(t *testing.T) {
	if got := hbar(1, 4); got != "████" {
		t.Errorf("hbar(1,4) = %q", got)
	}
	if got := hbar(0, 4); got != "" {
		t.Errorf("hbar(0,4) = %q", got)
	}
	if got := hbar(0.5, 4); got != "██" {
		t.Errorf("hbar(0.5,4) = %q", got)
	}
	// 1/16 of width 2 = one eighth-block partial.
	if got := hbar(0.0625, 2); got != "▏" {
		t.Errorf("hbar(0.0625,2) = %q", got)
	}
	// Out-of-range input must clamp, never panic or overflow width.
	for _, f := range []float64{-1, 2, 0.999999} {
		if n := len([]rune(hbar(f, 5))); n > 5 {
			t.Errorf("hbar(%v,5) width = %d > 5", f, n)
		}
	}
	if got := hbar(0.5, 0); got != "" {
		t.Errorf("hbar with zero width = %q", got)
	}
}

func TestTrendChart(t *testing.T) {
	// Empty input = placeholder line.
	out := trendChart(nil, 80)
	if len(out) < 1 {
		t.Fatal("empty chart returned no lines")
	}
	// All-zero input = single "no activity" line.
	out = trendChart([]int64{0, 0, 0}, 80)
	if len(out) < 1 {
		t.Fatal("zero chart returned no lines")
	}
	// Normal data returns chart rows + axis + label line.
	out = trendChart([]int64{1, 5, 10, 7, 3}, 80)
	if len(out) < 7 {
		t.Errorf("got %d lines, want >= 7 chart rows + axis + label", len(out))
	}
	// Single spike renders at least one non-empty row.
	out = trendChart([]int64{1000}, 80)
	for i, line := range out {
		if len(line) == 0 {
			t.Errorf("line %d is empty", i)
		}
	}
}

func TestBarListAndCostCell(t *testing.T) {
	lines := []report.Line{
		{Key: "claude-code", Total: 100, Cost: 4.31},
		{Key: "codex", Total: 50, Cost: 0, UnpricedEvents: 3},
		{Key: "opencode", Total: 25, Cost: 1.5, UnpricedEvents: 1},
	}
	out := barList(lines, 80, func(l report.Line) string { return l.Key })
	if len(out) != 3 {
		t.Fatalf("got %d rows, want 3", len(out))
	}
	if !strings.Contains(out[0], "$4.31") {
		t.Errorf("priced row missing cost: %q", out[0])
	}
	if !strings.Contains(out[1], "-*") {
		t.Errorf("unpriced row missing -*: %q", out[1])
	}
	if !strings.Contains(out[2], "$1.50*") {
		t.Errorf("partially priced row missing *: %q", out[2])
	}
	if empty := barList(nil, 80, func(l report.Line) string { return l.Key }); len(empty) != 1 {
		t.Error("empty list should render a single placeholder row")
	}
}

func TestTruncate(t *testing.T) {
	if got := report.Truncate("abcdef", 4); got != "abc…" {
		t.Errorf("truncate = %q", got)
	}
	if got := report.Truncate("ab", 4); got != "ab" {
		t.Errorf("truncate short = %q", got)
	}
}
