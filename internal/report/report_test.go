package report

import "testing"

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
