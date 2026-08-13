package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/lakshmanpatel/tocy/internal/report"
)

var palette = []lipgloss.Color{
	"#A78BFA", // 0  violet
	"#2DD4BF", // 1  teal
	"#FBBF24", // 2  amber
	"#FB923C", // 3  orange
	"#4ADE80", // 4  emerald
	"#F472B6", // 5  pink
	"#60A5FA", // 6  blue
	"#F87171", // 7  red
	"#818CF8", // 8  indigo
	"#34D399", // 9  mint
}

var (
	titleSty  = lipgloss.NewStyle().Bold(true).Foreground(palette[0])
	dimSty    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	barSty    = lipgloss.NewStyle().Foreground(palette[2])
	costSty   = lipgloss.NewStyle().Foreground(palette[7])
	warnSty   = lipgloss.NewStyle().Foreground(palette[5])
	secSty    = lipgloss.NewStyle().Bold(true).Foreground(palette[4])
	tabOnSty  = lipgloss.NewStyle().Bold(true).Foreground(palette[0]).Reverse(true).Padding(0, 2)
	tabOffSty = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Padding(0, 2)
	infoSty   = lipgloss.NewStyle().Foreground(palette[1])
	keySty    = lipgloss.NewStyle().Bold(true).Foreground(palette[6])
)

func colorIdx(s string, m int) int {
	h := uint64(0)
	for i := 0; i < len(s); i++ {
		h = h*31 + uint64(s[i])
	}
	return int(h % uint64(m))
}

func colorFor(s string) lipgloss.Color {
	return palette[colorIdx(s, len(palette))]
}

func hbar(frac float64, width int) string {
	if width <= 0 {
		return ""
	}
	frac = max(0, min(1, frac))
	e := int(frac*float64(width*8) + 0.5)
	full, rem := e/8, e%8
	eighths := []rune("▏▎▍▌▋▊▉")
	var b strings.Builder
	b.WriteString(strings.Repeat("█", full))
	if rem > 0 && full < width {
		b.WriteRune(eighths[rem-1])
	}
	return b.String()
}

func trendChart(vals []int64, width int) []string {
	if len(vals) == 0 {
		return []string{dimSty.Render("  (no data)")}
	}
	var maxV int64
	for _, v := range vals {
		if v > maxV {
			maxV = v
		}
	}
	if maxV == 0 {
		return []string{dimSty.Render("  no activity")}
	}

	chartH := 7
	labelW := 6
	chartW := width - labelW - 2
	if chartW < 10 {
		chartW = 10
	}
	n := len(vals)
	if n > chartW {
		n = chartW
	}

	var out []string
	vblocks := []rune("▁▂▃▄▅▆▇█")

	for r := 0; r < chartH; r++ {
		row := chartH - 1 - r
		label := ""
		if row == chartH-1 {
			label = report.Humanize(maxV)
		}
		bar := strings.Builder{}
		bar.WriteString(dimSty.Render(fmt.Sprintf("%-*s", labelW, label)))
		bar.WriteString(" ")
		for i := 0; i < n; i++ {
			v := vals[len(vals)-n+i]
			e := int(float64(v) / float64(maxV) * float64(chartH*8))
			if v > 0 && e == 0 {
				e = 1
			}
			level := e - row*8
			switch {
			case level >= 8:
				bar.WriteString(barSty.Render("█"))
			case level > 0:
				bar.WriteString(barSty.Render(string(vblocks[level-1])))
			default:
				bar.WriteString(dimSty.Render("·"))
			}
		}
		out = append(out, bar.String())
	}

	axis := strings.Builder{}
	axis.WriteString(strings.Repeat(" ", labelW+1))
	for i := 0; i < n; i++ {
		axis.WriteString(dimSty.Render("─"))
	}
	out = append(out, axis.String())

	lbl := strings.Builder{}
	lbl.WriteString(strings.Repeat(" ", labelW+1))
	agoLbl := fmt.Sprintf("%dd ago", n)
	lbl.WriteString(dimSty.Render(agoLbl))
	gap := n - len(agoLbl) - len("today")
	if gap > 0 {
		lbl.WriteString(strings.Repeat(" ", gap))
	}
	lbl.WriteString(dimSty.Render("today"))
	out = append(out, lbl.String())

	return out
}

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
	const valW = 8 + 10
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
		lbl := label(l)
		clr := colorFor(lbl)
		lc := lipgloss.NewStyle().Foreground(clr)
		bar := hbar(frac, barW)
		cSty := costSty
		if l.UnpricedEvents > 0 {
			cSty = warnSty
		}
		out = append(out, fmt.Sprintf("%s %s%-*s %7s %9s",
			lc.Render(fmt.Sprintf("%-*s", labelW, report.Truncate(lbl, labelW))),
			lc.Render(bar), barW-len([]rune(bar)), "",
			report.Humanize(l.Total), cSty.Render(report.CostCell(l))))
	}
	return out
}

func cardAt(title string, total int64, cost float64, unpriced bool, paletteIdx int, width int) string {
	c := report.Money(cost)
	if unpriced {
		c += "*"
	}
	bc := palette[paletteIdx%len(palette)]
	sty := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(bc).
		Padding(0, 1).
		Width(width)
	valSty := lipgloss.NewStyle().Bold(true).Foreground(bc)
	body := fmt.Sprintf("%s\n%s\n%s",
		dimSty.Render(title), valSty.Render(report.Humanize(total)+" tok"), costSty.Render(c))
	return sty.Render(body)
}

func spinnerFrame(n int) string {
	chars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return chars[n%len(chars)]
}

var heatColors = []lipgloss.Color{
	"#0e4429",
	"#006d32",
	"#26a641",
	"#39d353",
}

func heatmap(data []int64, start time.Time, width int) []string {
	if len(data) == 0 {
		return []string{dimSty.Render("  (no data)")}
	}

	now := time.Now()
	// Weekday() is Sunday=0..Saturday=6; rows are laid out Monday=0..Sunday=6
	// (dayLbl below), so convert to a Monday-based index.
	padding := (int(start.Weekday()) + 6) % 7
	total := padding + len(data)
	cols := (total + 6) / 7

	grid := make([][]int64, 7)
	for r := 0; r < 7; r++ {
		grid[r] = make([]int64, cols)
	}
	for i, v := range data {
		idx := padding + i
		if c := idx / 7; c < cols {
			grid[idx%7][c] = v
		}
	}

	var maxVal int64
	for _, v := range data {
		if v > maxVal {
			maxVal = v
		}
	}

	cw := 2
	gap := 1
	minW := cols*(cw+gap) + 6
	if width < minW {
		cw = 1
	}
	minW2 := cols*(cw+gap) + 6
	if width < minW2 {
		gap = 0
	}

	dayLbl := []string{"Mon", "", "Wed", "", "Fri", "", ""}

	monthLabels := make([]string, cols)
	prev := -1
	for c := 0; c < cols; c++ {
		date := start.AddDate(0, 0, c*7-padding)
		if m := int(date.Month()); m != prev {
			monthLabels[c] = date.Format("Jan")
			prev = m
		}
	}

	colW := cw + gap
	leftW := 4

	var out []string

	out = append(out, "  "+dimSty.Render(
		start.Format("Jan 2, 2006")+" – "+now.Format("Jan 2, 2006")))

	for c := 0; c < cols; c++ {
		lbl := monthLabels[c]
		if lbl != "" {
			pad := c * colW
			row := strings.Builder{}
			row.WriteString(strings.Repeat(" ", leftW))
			for p := 0; p < pad; p++ {
				row.WriteString(" ")
			}
			row.WriteString(infoSty.Render(lbl))
			out = append(out, dimSty.Render(row.String()))
			break
		}
	}

	for r := 0; r < 7; r++ {
		row := strings.Builder{}
		if dayLbl[r] != "" {
			row.WriteString(infoSty.Render(dayLbl[r]))
			row.WriteString(" ")
		} else {
			row.WriteString(strings.Repeat(" ", leftW))
		}
		for c := 0; c < cols; c++ {
			v := grid[r][c]
			if v == 0 {
				row.WriteString(dimSty.Render(strings.Repeat(" ", cw)))
			} else {
				ci := 0
				if maxVal > 0 {
					ci = int(float64(v) / float64(maxVal) * float64(len(heatColors)-1))
					if ci >= len(heatColors) {
						ci = len(heatColors) - 1
					}
				}
				row.WriteString(lipgloss.NewStyle().Background(heatColors[ci]).Render(strings.Repeat(" ", cw)))
			}
			if gap > 0 {
				row.WriteString(" ")
			}
		}
		out = append(out, row.String())
	}

	active := 0
	var maxStreak, streak int
	for _, v := range data {
		if v > 0 {
			active++
			streak++
			if streak > maxStreak {
				maxStreak = streak
			}
		} else {
			streak = 0
		}
	}
	// Current streak counts back from the most recent day; it is zero as
	// soon as the latest day has no activity.
	curStreak := 0
	for i := len(data) - 1; i >= 0 && data[i] > 0; i-- {
		curStreak++
	}

	out = append(out, "")
	sep := strings.Repeat("─", min(width, 50))
	out = append(out, "  "+dimSty.Render(sep))

	statLine := fmt.Sprintf("%s  %s  %s  %s",
		infoSty.Render(fmt.Sprintf("%d/%d days", active, len(data))),
		dimSty.Render(fmt.Sprintf("%d%%", active*100/len(data))),
		infoSty.Render(fmt.Sprintf("↗ %d-day", maxStreak)),
		dimSty.Render(fmt.Sprintf("peak %s tok/day", report.Humanize(maxVal))))
	out = append(out, "  "+statLine)

	lg := "  " + dimSty.Render("Less")
	for _, c := range heatColors {
		lg += " " + lipgloss.NewStyle().Background(c).Render("  ")
	}
	lg += " " + dimSty.Render("More")
	if curStreak > 0 {
		lg += dimSty.Render("  ·  ")
		lg += infoSty.Render(fmt.Sprintf("%d-day", curStreak))
		lg += dimSty.Render(" streak")
	}
	out = append(out, lg)

	return out
}
