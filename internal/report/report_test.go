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
