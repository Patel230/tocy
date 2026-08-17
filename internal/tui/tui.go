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
	streakWeeks = 17
)

var tabNames = []string{"Overview", "By Model", "Daily", "Projects", "Streak"}

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
	cards        [4]cardData
	byTool       []report.Line
	byModel      []report.Line
	byDay        []report.Line
	byProject    []report.Line
	insights     string
	trend        []int64
	streakData   []int64
	streakStart  time.Time
	unpriced     []string
	tools        []string
	loadingCost  float64
	pricingLabel string
	loadedAt     time.Time
	since        time.Time
	err          error
}

type (
	tickMsg time.Time
	spinMsg int
	scanMsg struct{ note string }
	dataMsg struct {
		seq int
		vd  *viewData
	}
)

type Model struct {
	st     *store.Store
	prices *pricing.Table

	width, height int
	tab           int
	rangeIdx      int
	toolIdx       int
	sortByCost    bool
	scroll        int
	showHelp      bool

	scanning  bool
	spinFrame int
	spinToken int
	scanNote  string
	lastErr   string
	// loadSeq is incremented in the Bubble Tea message handler to give every
	// async load a monotonically increasing version.  The seq field on
	// loadDataMsg is compared against the current loadSeq so stale data
	// from a previous load never overwrites fresh data.  It is safe to read
	// and increment without a mutex because all mutations happen through the
	// single Bubble Tea update loop.
	loadSeq   int
	data      *viewData
}

func Run(st *store.Store, prices *pricing.Table) error {
	m := Model{st: st, prices: prices, rangeIdx: 1, scanning: true}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), tickCmd(), spinCmd(m.spinToken))
}

func tickCmd() tea.Cmd {
	return tea.Tick(rescanEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// spinCmd carries a token so stale spinner chains die instead of stacking
// up and accelerating the animation.
func spinCmd(token int) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return spinMsg(token) })
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
	st, prices, tool, seq := m.st, m.prices, m.tool(), m.loadSeq
	since := ranges[m.rangeIdx].since(time.Now())
	return func() tea.Msg {
		vd, err := load(st, prices, since, tool)
		if err != nil {
			vd = &viewData{err: err}
		}
		return dataMsg{seq: seq, vd: vd}
	}
}

func load(st *store.Store, prices *pricing.Table, since time.Time, tool string) (*viewData, error) {
	now := time.Now()
	vd := &viewData{loadedAt: now, pricingLabel: pricingSourceLabel(prices), since: since}
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
	if vd.byProject, _, err = build("project", since); err != nil {
		return nil, err
	}

	// A single byDay query from the earliest date needed serves the daily
	// tab, the 30-day trend, the streak heatmap, and the four summary cards
	// (each just sums the days falling in its window). Without this, load()
	// fires off eight full-table scans where two will do.
	dayFrom := since
	if dayFrom.IsZero() {
		dayFrom = now.AddDate(0, 0, -(streakWeeks*7 - 1))
	}
	earliest, _ := st.EarliestEvent()
	if !earliest.IsZero() && !earliest.After(now) && earliest.Before(dayFrom) {
		dayFrom = earliest
	}

	dl, _, err := build("day", dayFrom)
	if err != nil {
		return nil, err
	}
	if since.IsZero() {
		vd.byDay = dl
	} else {
		cutoff := since.Format("2006-01-02")
		for _, l := range dl {
			if l.Key >= cutoff {
				vd.byDay = append(vd.byDay, l)
			}
		}
	}

	byDate := map[string]int64{}
	for _, l := range dl {
		byDate[l.Key] = l.Total
	}
	vd.trend = make([]int64, trendDays)
	for i := 0; i < trendDays; i++ {
		d := now.AddDate(0, 0, -(trendDays - 1 - i)).Format("2006-01-02")
		vd.trend[i] = byDate[d]
	}

	windowStart := now.AddDate(0, 0, -(streakWeeks*7 - 1))
	if dayFrom.After(windowStart) {
		vd.streakStart = dayFrom
	} else {
		vd.streakStart = windowStart
	}
	streakDays := int(now.Sub(vd.streakStart).Hours()/24) + 1
	vd.streakData = make([]int64, streakDays)
	for i := 0; i < streakDays; i++ {
		d := vd.streakStart.AddDate(0, 0, i).Format("2006-01-02")
		vd.streakData[i] = byDate[d]
	}

	for i, rg := range ranges {
		cStart := rg.since(now)
		var c cardData
		startKey := cStart.Format("2006-01-02")
		for _, l := range dl {
			if l.Key >= startKey {
				c.total += l.Total
				c.cost += l.Cost
				c.unpriced = c.unpriced || l.UnpricedEvents > 0
			}
		}
		vd.cards[i] = c
	}
	vd.loadingCost = vd.cards[1].cost

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

func pricingSourceLabel(p *pricing.Table) string {
	if p == nil {
		return "no pricing"
	}
	return p.Source
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			if m.showHelp {
				m.showHelp = false
				return m, nil
			}
			return m, tea.Quit
		case "tab", "right", "l":
			m.tab = (m.tab + 1) % len(tabNames)
			m.scroll = 0
		case "shift+tab", "left", "h":
			m.tab = (m.tab + len(tabNames) - 1) % len(tabNames)
			m.scroll = 0
		case "1", "2", "3", "4", "5":
			m.tab = int(msg.String()[0] - '1')
			m.scroll = 0
		case "?":
			m.showHelp = !m.showHelp
		case "t":
			m.rangeIdx = (m.rangeIdx + 1) % len(ranges)
			m.loadSeq++
			m.scroll = 0
			return m, m.loadCmd()
		case "f":
			n := 1
			if m.data != nil {
				n = len(m.data.tools) + 1
			}
			m.toolIdx = (m.toolIdx + 1) % n
			m.loadSeq++
			m.scroll = 0
			return m, m.loadCmd()
		case "s":
			m.sortByCost = !m.sortByCost
		case "r":
			if !m.scanning {
				m.scanning = true
				m.spinToken++
				return m, tea.Batch(m.scanCmd(), spinCmd(m.spinToken))
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
	case spinMsg:
		if int(msg) != m.spinToken {
			return m, nil // stale chain from a previous scan
		}
		m.spinFrame++
		if m.scanning || m.data == nil {
			return m, spinCmd(m.spinToken)
		}
		return m, nil
	case tickMsg:
		cmds := []tea.Cmd{tickCmd()}
		if !m.scanning {
			m.scanning = true
			m.spinToken++
			cmds = append(cmds, m.scanCmd(), spinCmd(m.spinToken))
		}
		return m, tea.Batch(cmds...)
	case scanMsg:
		m.scanning = false
		m.scanNote = msg.note
		m.loadSeq++
		return m, m.loadCmd()
	case dataMsg:
		if msg.seq != m.loadSeq {
			return m, nil
		}
		if msg.vd.err != nil {
			m.lastErr = msg.vd.err.Error()
			return m, nil
		}
		m.data = msg.vd
		m.lastErr = ""
		if m.data != nil {
			m.data.insights = buildInsights(m.data)
		}
	}
	return m, nil
}

func buildInsights(vd *viewData) string {
	if len(vd.byTool) == 0 {
		return ""
	}
	parts := []string{}

	var topTool string
	var topTotal, totalAll int64
	var totalCost float64
	for _, l := range vd.byTool {
		totalAll += l.Total
		totalCost += l.Cost
		if l.Total > topTotal {
			topTotal = l.Total
			topTool = l.Key
		}
	}
	if topTotal > 0 {
		parts = append(parts, fmt.Sprintf("top %s (%s)", topTool, report.Humanize(topTotal)))
	}
	if len(vd.byTool) > 1 && topTotal > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%% from %s", float64(topTotal)/float64(totalAll)*100, topTool))
	}
	if vd.cards[1].cost > 0 {
		proj, _ := report.Projection(vd.cards[1].cost, vd.since)
		parts = append(parts, fmt.Sprintf("7d %s ~%s/mo", report.Money(vd.cards[1].cost), proj))
	}
	if _, exceeded := report.BudgetWarn(totalCost, vd.since); exceeded {
		parts = append(parts, fmt.Sprintf("%s ⚠ over budget", report.Money(totalCost)))
	}
	if len(vd.unpriced) > 0 {
		parts = append(parts, fmt.Sprintf("%d unpriced model(s)", len(vd.unpriced)))
	}
	em := report.ComputeEfficiency(vd.byTool, nil)
	if em.TotalTokens > 0 && em.CacheHitRate > 0.01 {
		parts = append(parts, fmt.Sprintf("cache %.0f%%", em.CacheHitRate*100))
	}
	if tip := report.OptimizationTip(vd.byTool); tip != "" {
		parts = append(parts, tip)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, "  ·  ")
}

func (m Model) View() string {
	if m.showHelp {
		return m.helpView()
	}
	if m.width == 0 {
		return "starting..."
	}
	w := m.width

	filter := "all tools"
	if t := m.tool(); t != "" {
		filter = t
	}

	pricingLabel := ""
	if m.data != nil {
		pricingLabel = m.data.pricingLabel
	}
	left := titleSty.Render(" tocy ") +
		dimSty.Render(" ◆ ") +
		dimSty.Render("range:") + " " + ranges[m.rangeIdx].name + " " +
		dimSty.Render("tool:") + " " + filter

	if pricingLabel != "" {
		left += " " + dimSty.Render("pricing:") + " " + infoSty.Render(pricingLabel)
	}

	status := ""
	switch {
	case m.scanning:
		status = infoSty.Render(spinnerFrame(m.spinFrame) + " scanning...")
	case m.data != nil:
		status = dimSty.Render(m.scanNote + "  " + m.data.loadedAt.Format("15:04:05"))
	}
	if m.lastErr != "" {
		status = warnSty.Render("! " + m.lastErr)
	}
	pad := w - lipgloss.Width(left) - lipgloss.Width(status) - 3
	if pad < 0 {
		pad = 0
	}
	header := left + strings.Repeat(" ", pad) + status

	var tabs []string
	for i, n := range tabNames {
		lbl := fmt.Sprintf("%d %s", i+1, n)
		if i == m.tab {
			tabs = append(tabs, tabOnSty.Foreground(palette[i%len(palette)]).Render(lbl))
		} else {
			c := palette[i%len(palette)]
			off := lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Faint(true).Padding(0, 2)
			tabs = append(tabs, off.Render(lbl))
		}
	}
	tabBar := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	tabLineSty := lipgloss.NewStyle().Foreground(palette[m.tab%len(palette)])
	tabLine := tabLineSty.Render(strings.Repeat("─", max(10, w-lipgloss.Width(tabBar)-2)))

	chrome := 2 + lipgloss.Height(tabBar) + 2
	avail := m.height - chrome
	if avail < 3 {
		avail = 3
	}

	var lines []string
	switch {
	case m.lastErr != "":
		lines = []string{warnSty.Render("error: " + m.lastErr)}
		if m.data != nil && m.data.insights != "" {
			lines = append(lines, "", m.tabHeaderStyle().Render(m.data.insights))
		}
	case m.data == nil:
		lines = []string{"  " + infoSty.Render(spinnerFrame(m.spinFrame)+" loading data...")}
	default:
		lines = m.body(w - 2)
	}
	scroll := m.scroll
	if mx := len(lines) - avail; scroll > mx {
		scroll = mx
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
		body += "\n " + dimSty.Render(fmt.Sprintf("... %d more (j/↓ to scroll)", len(lines)-end))
	}

	footer := " " + strings.Join([]string{
		keySty.Render("1-5") + dimSty.Render("/tab"),
		keySty.Render("t") + dimSty.Render("/range"),
		keySty.Render("f") + dimSty.Render("/tool"),
		keySty.Render("s") + dimSty.Render("/sort"),
		keySty.Render("r") + dimSty.Render("/rescan"),
		keySty.Render("?") + dimSty.Render("/help"),
		keySty.Render("q") + dimSty.Render("/quit"),
	}, dimSty.Render("  "))
	if m.data != nil && len(m.data.unpriced) > 0 {
		footer += "\n" + warnSty.Render(" * no pricing: "+report.Truncate(strings.Join(m.data.unpriced, ", "), w-22))
	}

	return header + "\n" + tabBar + " " + tabLine + "\n" + body + "\n" + footer
}

func (m Model) helpView() string {
	w := m.width
	if w == 0 {
		w = 80
	}
	h := m.height
	if h == 0 {
		h = 24
	}

	helpText := `tocy — token usage & cost dashboard

KEYBINDINGS
 1-5          Switch tabs (Overview / Model / Daily / Projects / Streak)
 tab / →      Next tab
 ⇧tab / ←     Previous tab
 t            Cycle time range (today → 7d → 30d → all)
 f            Filter by tool
 s            Toggle sort by cost / tokens
 r            Force rescan
 j / ↓        Scroll down
 k / ↑        Scroll up
 g            Jump to top
 ?            Toggle this help
 q / esc      Quit

TABS
 Overview     Summary cards, tool breakdown, 30-day trend, insights
 By Model     Usage grouped by AI model
 Daily        Day-by-day usage history
 Projects     Usage grouped by project directory
 Streak       GitHub-style contribution heatmap

INSIGHTS
 Smart suggestions appear in the Overview tab based on
 your usage patterns — top tools, cost projections, budget
 warnings, cache efficiency, and optimization tips.

A dollar (*) suffix means some events in that row have no
pricing data and were not counted toward the cost column.
A dash (-) means the entire row is unpriced.

CLI COMMANDS
 tocy report --csv              export usage as CSV
 tocy compare                   compare two time periods
 tocy efficiency                show cost/token and cache metrics
 tocy sessions                  list sessions with cost
 tocy statusline                compact one-line cost summary
`

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(palette[0]).
		Padding(1, 2).
		Width(min(w-4, 60)).
		Render(helpText)

	lines := strings.Split(box, "\n")
	if len(lines) > h-2 {
		lines = lines[:h-2]
	}
	return strings.Join(lines, "\n") + "\n" + dimSty.Render(" press ? or esc to close ")
}

func (m Model) body(w int) []string {
	d := m.data
	switch m.tab {
	case 0:
		return m.overview(w)
	case 1:
		return m.list(d.byModel, w, func(l report.Line) string { return l.Key }, true)
	case 2:
		return m.listRev(d.byDay, w, func(l report.Line) string { return l.Key })
	case 3:
		return m.list(d.byProject, w, func(l report.Line) string { return report.ShortProj(l.Key) }, true)
	default:
		return m.streak(w)
	}
}

func (m Model) streak(w int) []string {
	d := m.data
	return heatmap(d.streakData, d.streakStart, w)
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

// listRev renders src in reverse chronological order without copying the
// slice.  The daily tab shows newest-first; sorting by cost is not offered
// because the x-axis is time.
func (m Model) listRev(src []report.Line, w int, label func(report.Line) string) []string {
	lines := make([]report.Line, len(src))
	for i := range src {
		lines[i] = src[len(src)-1-i]
	}
	return barList(lines, w, label)
}

func (m Model) tabColor() lipgloss.Color {
	return palette[m.tab%len(palette)]
}

func (m Model) tabHeaderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(m.tabColor())
}

func (m Model) overview(w int) []string {
	d := m.data
	cardTitles := []string{"Today", "7 days", "30 days", "All time"}

	cardW := min((w-8)/4, 22)
	if cardW < 14 {
		cardW = 14
	}

	var cards []string
	for i, t := range cardTitles {
		// Skip the muted sequential palette entries and use vivid,
		// visually-distinct colors for the summary cards.
		pi := map[int]int{0: 7, 1: 6, 2: 9}[i]
		cards = append(cards, cardAtProjected(t, d.cards[i].total, d.cards[i].cost, d.cards[i].unpriced, pi, cardW, d.since))
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
	out := strings.Split(row, "\n")

	if d.insights != "" {
		out = append(out, "", secSty.Render("insights")+infoSty.Render(d.insights))
	}

	sep := strings.Repeat("─", min(w, 60))
	out = append(out, "", dimSty.Render(sep))
	out = append(out, secSty.Render("BY TOOL  ")+dimSty.Render(ranges[m.rangeIdx].name))
	out = append(out, barList(d.byTool, w, func(l report.Line) string { return l.Key })...)

	out = append(out, "", dimSty.Render(sep))
	out = append(out, secSty.Render("30-DAY TREND"))
	chart := trendChart(d.trend, w-2)
	out = append(out, chart...)

	return out
}
