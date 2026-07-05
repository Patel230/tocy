// Package tui is the live tocy dashboard: 4 tabs (Overview, By Model,
// Daily, Projects), background rescans every 30s, pricing-aware bars.
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lakshmanpatel/tocy/internal/ingest"
	"github.com/lakshmanpatel/tocy/internal/pricing"
	"github.com/lakshmanpatel/tocy/internal/report"
	"github.com/lakshmanpatel/tocy/internal/store"
)

const (
	rescanEvery = 30 * time.Second
	trendDays   = 30
)

var tabNames = []string{"Overview", "By Model", "Daily", "Projects"}

var ranges = []struct {
	name  string
	since func(now time.Time) time.Time
}{
	{"today", func(n time.Time) time.Time {
		y, mo, d := n.Date()
		return time.Date(y, mo, d, 0, 0, 0, 0, n.Location())
	}},
	{"7d", func(n time.Time) time.Time { return n.AddDate(0, 0, -7) }},
	{"30d", func(n time.Time) time.Time { return n.AddDate(0, 0, -30) }},
	{"all", func(time.Time) time.Time { return time.Time{} }},
}

type cardData struct {
	total    int64
	cost     float64
	unpriced bool
}

type viewData struct {
	cards     [4]cardData // today / 7d / 30d / all
	byTool    []report.Line
	byModel   []report.Line
	byDay     []report.Line
	byProject []report.Line
	trend     []int64 // tokens per day, oldest → newest, exactly trendDays long
	unpriced  []string
	tools     []string
	loadedAt  time.Time
}

type (
	tickMsg time.Time
	scanMsg struct{ note string }
	dataMsg struct{ vd *viewData }
	errMsg  struct{ err error }
)

// Model is the bubbletea model.
type Model struct {
	st     *store.Store
	prices *pricing.Table

	width, height int
	tab           int
	rangeIdx      int // index into ranges; default 7d
	toolIdx       int // 0 = all, else tools[toolIdx-1]
	sortByCost    bool
	scroll        int

	scanning bool
	scanNote string
	err      error
	data     *viewData
}

// Run starts the dashboard and blocks until quit.
func Run(st *store.Store, prices *pricing.Table) error {
	m := Model{st: st, prices: prices, rangeIdx: 1, scanning: true}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(rescanEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) tool() string {
	if m.toolIdx == 0 || m.data == nil || m.toolIdx > len(m.data.tools) {
		return ""
	}
	return m.data.tools[m.toolIdx-1]
}

func (m Model) scanCmd() tea.Cmd {
	st := m.st
	return func() tea.Msg {
		var files, events int
		var errs int
		for _, r := range ingest.ScanAll(st, ingest.Sources()) {
			if r.Err != nil {
				errs++
				continue
			}
			files += r.Files
			events += r.NewEvents
		}
		note := fmt.Sprintf("scanned %d file(s), +%d event(s)", files, events)
		if errs > 0 {
			note += fmt.Sprintf(", %d source error(s)", errs)
		}
		return scanMsg{note}
	}
}

func (m Model) loadCmd() tea.Cmd {
	st, prices, tool := m.st, m.prices, m.tool()
	since := ranges[m.rangeIdx].since(time.Now())
	return func() tea.Msg {
		vd, err := load(st, prices, since, tool)
		if err != nil {
			return errMsg{err}
		}
		return dataMsg{vd}
	}
}

func load(st *store.Store, prices *pricing.Table, since time.Time, tool string) (*viewData, error) {
	now := time.Now()
	vd := &viewData{loadedAt: now}
	build := func(by string, s time.Time) ([]report.Line, []string, error) {
		return report.Build(st, report.Options{Since: s, GroupBy: by, Source: tool}, prices)
	}
	var err error
	if vd.byTool, _, err = build("tool", since); err != nil {
		return nil, err
	}
	if vd.byModel, vd.unpriced, err = build("model", since); err != nil {
		return nil, err
	}
	if vd.byDay, _, err = build("day", since); err != nil {
		return nil, err
	}
	if vd.byProject, _, err = build("project", since); err != nil {
		return nil, err
	}

	// 30-day trend is independent of the selected range.
	tl, _, err := build("day", now.AddDate(0, 0, -(trendDays-1)))
	if err != nil {
		return nil, err
	}
	byDate := map[string]int64{}
	for _, l := range tl {
		byDate[l.Key] = l.Total
	}
	vd.trend = make([]int64, trendDays)
	for i := 0; i < trendDays; i++ {
		d := now.AddDate(0, 0, -(trendDays - 1 - i)).Format("2006-01-02")
		vd.trend[i] = byDate[d]
	}

	// Summary cards: fixed ranges, respecting the tool filter.
	for i, rg := range ranges {
		lines, _, err := build("tool", rg.since(now))
		if err != nil {
			return nil, err
		}
		var c cardData
		for _, l := range lines {
			c.total += l.Total
			c.cost += l.Cost
			c.unpriced = c.unpriced || l.UnpricedEvents > 0
		}
		vd.cards[i] = c
	}

	stats, err := st.SourceStats()
	if err != nil {
		return nil, err
	}
	for s := range stats {
		vd.tools = append(vd.tools, s)
	}
	sort.Strings(vd.tools)
	return vd, nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "tab", "right", "l":
			m.tab = (m.tab + 1) % len(tabNames)
			m.scroll = 0
		case "shift+tab", "left", "h":
			m.tab = (m.tab + len(tabNames) - 1) % len(tabNames)
			m.scroll = 0
		case "1", "2", "3", "4":
			m.tab = int(msg.String()[0] - '1')
			m.scroll = 0
		case "t":
			m.rangeIdx = (m.rangeIdx + 1) % len(ranges)
			m.scroll = 0
			return m, m.loadCmd()
		case "f":
			n := 1
			if m.data != nil {
				n = len(m.data.tools) + 1
			}
			m.toolIdx = (m.toolIdx + 1) % n
			m.scroll = 0
			return m, m.loadCmd()
		case "s":
			m.sortByCost = !m.sortByCost
		case "r":
			if !m.scanning {
				m.scanning = true
				return m, m.scanCmd()
			}
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down", "j":
			m.scroll++
		case "g":
			m.scroll = 0
		}
	case tickMsg:
		cmds := []tea.Cmd{tickCmd()}
		if !m.scanning {
			m.scanning = true
			cmds = append(cmds, m.scanCmd())
		}
		return m, tea.Batch(cmds...)
	case scanMsg:
		m.scanning = false
		m.scanNote = msg.note
		return m, m.loadCmd()
	case dataMsg:
		m.data = msg.vd
		m.err = nil
	case errMsg:
		m.err = msg.err
	}
	return m, nil
}

func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	w := m.width

	// Header.
	filter := "all tools"
	if t := m.tool(); t != "" {
		filter = t
	}
	status := ""
	switch {
	case m.scanning:
		status = warnSty.Render("⟳ scanning…")
	case m.data != nil:
		status = dimSty.Render(m.scanNote + " · " + m.data.loadedAt.Format("15:04:05"))
	}
	left := titleSty.Render(" tocy ") +
		dimSty.Render("· range:") + " " + ranges[m.rangeIdx].name + " " +
		dimSty.Render("· tool:") + " " + filter
	pad := w - lipgloss.Width(left) - lipgloss.Width(status) - 1
	if pad < 1 {
		pad = 1
	}
	header := left + strings.Repeat(" ", pad) + status

	// Tab bar.
	var tabs []string
	for i, n := range tabNames {
		lbl := fmt.Sprintf("%d %s", i+1, n)
		if i == m.tab {
			tabs = append(tabs, tabOnSty.Render(lbl))
		} else {
			tabs = append(tabs, tabOffSty.Render(lbl))
		}
	}
	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)

	// Footer.
	help := dimSty.Render(" 1-4/tab views · t range · f tool · s sort · r rescan · j/k scroll · q quit")
	if m.data != nil && len(m.data.unpriced) > 0 {
		help += "\n" + warnSty.Render(" * no pricing for: "+truncate(strings.Join(m.data.unpriced, ", "), w-22))
	}

	chrome := 2 + lipgloss.Height(tabBar) + lipgloss.Height(help)
	avail := m.height - chrome
	if avail < 3 {
		avail = 3
	}

	// Body.
	var lines []string
	switch {
	case m.err != nil:
		lines = []string{warnSty.Render("error: " + m.err.Error())}
	case m.data == nil:
		lines = []string{dimSty.Render("loading…")}
	default:
		lines = m.body(w - 2)
	}
	scroll := m.scroll
	if max := len(lines) - avail; scroll > max {
		scroll = max
	}
	if scroll < 0 {
		scroll = 0
	}
	end := scroll + avail
	if end > len(lines) {
		end = len(lines)
	}
	shown := lines[scroll:end]
	body := " " + strings.Join(shown, "\n ")
	if end < len(lines) {
		body += "\n " + dimSty.Render(fmt.Sprintf("… %d more (j to scroll)", len(lines)-end))
	}

	return header + "\n" + tabBar + "\n" + body + "\n" + help
}

// body renders the active tab's content as raw lines (pre-scroll).
func (m Model) body(w int) []string {
	d := m.data
	switch m.tab {
	case 0:
		return m.overview(w)
	case 1:
		return m.list(d.byModel, w, func(l report.Line) string { return l.Key }, true)
	case 2:
		rev := make([]report.Line, len(d.byDay))
		for i, l := range d.byDay {
			rev[len(d.byDay)-1-i] = l // newest first
		}
		return m.list(rev, w, func(l report.Line) string { return l.Key }, false)
	default:
		return m.list(d.byProject, w, func(l report.Line) string { return shortProj(l.Key) }, true)
	}
}

func (m Model) list(src []report.Line, w int, label func(report.Line) string, sortable bool) []string {
	lines := append([]report.Line(nil), src...)
	if sortable && m.sortByCost {
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].Cost > lines[j].Cost })
	}
	out := barList(lines, w, label)
	if sortable {
		mode := "tokens"
		if m.sortByCost {
			mode = "cost"
		}
		out = append([]string{dimSty.Render("sorted by " + mode + " (s to toggle)"), ""}, out...)
	}
	return out
}

func (m Model) overview(w int) []string {
	d := m.data
	cardTitles := []string{"Today", "7 days", "30 days", "All time"}
	var cards []string
	for i, t := range cardTitles {
		cards = append(cards, card(t, d.cards[i].total, d.cards[i].cost, d.cards[i].unpriced))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
	out := strings.Split(row, "\n")

	out = append(out, "", secSty.Render("BY TOOL — "+ranges[m.rangeIdx].name))
	out = append(out, barList(d.byTool, w, func(l report.Line) string { return l.Key })...)

	var max int64
	for _, v := range d.trend {
		if v > max {
			max = v
		}
	}
	out = append(out, "", secSty.Render("LAST 30 DAYS")+dimSty.Render(
		fmt.Sprintf("  (peak %s tok/day)", report.Humanize(max))))
	// Double each column for readability when it fits.
	vals := d.trend
	cols := columns(vals, 4)
	for _, r := range cols {
		if w >= trendDays*2+4 {
			var b strings.Builder
			for _, ch := range r {
				b.WriteRune(ch)
				b.WriteRune(ch)
			}
			r = b.String()
		}
		out = append(out, barSty.Render(r))
	}
	span := trendDays * 2
	if w < trendDays*2+4 {
		span = trendDays
	}
	lbl := "30d ago"
	gap := span - len(lbl) - len("today")
	if gap > 0 {
		out = append(out, dimSty.Render(lbl+strings.Repeat(" ", gap)+"today"))
	}
	return out
}
