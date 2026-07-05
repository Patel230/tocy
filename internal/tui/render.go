package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/lakshmanpatel/tocy/internal/report"
)

var (
	titleSty  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	dimSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	barSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	costSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	warnSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	secSty    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	tabOnSty  = lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	tabOffSty = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 1)
	cardSty   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("8")).Padding(0, 1)
)

var eighths = []rune("▏▎▍▌▋▊▉")
var vblocks = []rune("▁▂▃▄▅▆▇█")

// hbar renders a horizontal bar of frac∈[0,1] into width cells using
// eighth-block glyphs for the fractional tail.
func hbar(frac float64, width int) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	e := int(frac*float64(width*8) + 0.5)
	full, rem := e/8, e%8
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	if rem > 0 && full < width {
		b.WriteRune(eighths[rem-1])
	}
	return b.String()
}

// columns renders vals as a column chart `height` rows tall (top row first),
// one rune per value, using vertical eighth blocks.
func columns(vals []int64, height int) []string {
	var max int64
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	rows := make([]string, height)
	if max == 0 {
		for i := range rows {
			rows[i] = strings.Repeat(" ", len(vals))
		}
		return rows
	}
	for r := 0; r < height; r++ { // r=0 is the TOP row
		var b strings.Builder
		for _, v := range vals {
			e := int(float64(v) / float64(max) * float64(height*8))
			if v > 0 && e == 0 {
				e = 1 // never render activity as totally empty
			}
			level := e - (height-1-r)*8
			switch {
			case level >= 8:
				b.WriteRune('█')
			case level > 0:
				b.WriteRune(vblocks[level-1])
			default:
				b.WriteRune(' ')
			}
		}
		rows[r] = b.String()
	}
	return rows
}

// barList renders lines as `label ▕bar▏ tokens cost` rows.
func barList(lines []report.Line, width int, label func(report.Line) string) []string {
	if len(lines) == 0 {
		return []string{dimSty.Render("  (no data)")}
	}
	var max int64
	labelW := 0
	for _, l := range lines {
		if l.Total > max {
			max = l.Total
		}
		if n := len([]rune(label(l))); n > labelW {
			labelW = n
		}
	}
	if labelW > 28 {
		labelW = 28
	}
	const valW = 8 + 10 // " 999.9M" + " $9999.99*"
	barW := width - labelW - valW - 4
	if barW < 8 {
		barW = 8
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		frac := 0.0
		if max > 0 {
			frac = float64(l.Total) / float64(max)
		}
		bar := hbar(frac, barW)
		out = append(out, fmt.Sprintf("%-*s %s%-*s %7s %9s",
			labelW, truncate(label(l), labelW),
			barSty.Render(bar), barW-len([]rune(bar)), "",
			report.Humanize(l.Total), costSty.Render(costCell(l))))
	}
	return out
}

// costCell mirrors report's table cell: "—*" fully unpriced, "*" partial.
func costCell(l report.Line) string {
	if l.Cost == 0 && l.UnpricedEvents > 0 {
		return "—*"
	}
	s := report.Money(l.Cost)
	if l.UnpricedEvents > 0 {
		s += "*"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// shortProj renders "/Users/x/Desktop/ProjectAlpha/tocy" as "…/ProjectAlpha/tocy".
func shortProj(p string) string {
	if p == "" {
		return "(unknown)"
	}
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	if len(parts) <= 3 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}

// card renders one summary box.
func card(title string, total int64, cost float64, unpriced bool) string {
	c := report.Money(cost)
	if unpriced {
		c += "*"
	}
	body := fmt.Sprintf("%s\n%s tok\n%s",
		dimSty.Render(title), report.Humanize(total), costSty.Render(c))
	return cardSty.Render(body)
}
