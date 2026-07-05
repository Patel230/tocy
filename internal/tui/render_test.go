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

func TestColumns(t *testing.T) {
	rows := columns([]int64{0, 5, 10}, 2)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	for i, r := range rows {
		if n := len([]rune(r)); n != 3 {
			t.Errorf("row %d width = %d, want 3", i, n)
		}
	}
	// Max value fills the full column: top row's last rune is '█'.
	top := []rune(rows[0])
	if top[2] != '█' {
		t.Errorf("max column top rune = %q, want █", top[2])
	}
	// Zero value renders blank in every row.
	bot := []rune(rows[1])
	if top[0] != ' ' || bot[0] != ' ' {
		t.Error("zero column should be blank")
	}
	// Tiny-but-nonzero values must remain visible in the bottom row.
	rows = columns([]int64{1, 1000000}, 4)
	last := []rune(rows[3])
	if last[0] == ' ' {
		t.Error("nonzero value rendered as empty column")
	}
	// All-zero input: no divide-by-zero, blank chart.
	rows = columns([]int64{0, 0}, 2)
	if rows[0] != "  " {
		t.Errorf("all-zero top row = %q", rows[0])
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
	if !strings.Contains(out[1], "—*") {
		t.Errorf("unpriced row missing —*: %q", out[1])
	}
	if !strings.Contains(out[2], "$1.50*") {
		t.Errorf("partially priced row missing *: %q", out[2])
	}
	if empty := barList(nil, 80, func(l report.Line) string { return l.Key }); len(empty) != 1 {
		t.Error("empty list should render a single placeholder row")
	}
}

func TestTruncateAndShortProj(t *testing.T) {
	if got := truncate("abcdef", 4); got != "abc…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("ab", 4); got != "ab" {
		t.Errorf("truncate short = %q", got)
	}
	if got := shortProj("/Users/x/Desktop/ProjectAlpha/tocy"); got != "…/ProjectAlpha/tocy" {
		t.Errorf("shortProj = %q", got)
	}
	if got := shortProj(""); got != "(unknown)" {
		t.Errorf("shortProj empty = %q", got)
	}
}
