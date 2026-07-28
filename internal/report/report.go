package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lakshmanpatel/tocy/internal/pricing"
	"github.com/lakshmanpatel/tocy/internal/store"
)

type Line struct {
	Key            string  `json:"key"`
	Input          int64   `json:"input"`
	Output         int64   `json:"output"`
	CacheRead      int64   `json:"cache_read"`
	CacheWrite     int64   `json:"cache_write"`
	Reasoning      int64   `json:"reasoning"`
	Total          int64   `json:"total"`
	Events         int64   `json:"events"`
	RawCost        float64 `json:"raw_cost,omitempty"`
	Cost           float64 `json:"cost"`
	UnpricedEvents int64   `json:"unpriced_events,omitempty"`
}

type Options struct {
	Since   time.Time
	GroupBy string
	Source  string
	JSON    bool
}

type SessionLine struct {
	SessionID      string        `json:"session_id"`
	Source         string        `json:"source"`
	Project        string        `json:"project"`
	Models         int64         `json:"models"`
	Input          int64         `json:"input"`
	Output         int64         `json:"output"`
	CacheRead      int64         `json:"cache_read"`
	CacheWrite     int64         `json:"cache_write"`
	Reasoning      int64         `json:"reasoning"`
	Total          int64         `json:"total"`
	Events         int64         `json:"events"`
	Cost           float64       `json:"cost"`
	UnpricedEvents int64         `json:"unpriced_events,omitempty"`
	FirstTS        time.Time     `json:"first_ts"`
	LastTS         time.Time     `json:"last_ts"`
	Duration       time.Duration `json:"duration"`
	TruncID        string        `json:"-"`
}

var sinceRe = regexp.MustCompile(`^(\d+)([hdwm])$`)

func ParseSince(s string, now time.Time) (time.Time, error) {
	switch s {
	case "", "all":
		return time.Time{}, nil
	case "today":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	}
	if m := sinceRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "h":
			return now.Add(-time.Duration(n) * time.Hour), nil
		case "d":
			return now.AddDate(0, 0, -n), nil
		case "w":
			return now.AddDate(0, 0, -7*n), nil
		case "m":
			return now.AddDate(0, -n, 0), nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("bad --since %q (want all|today|7d|24h|2w|1m|YYYY-MM-DD)", s)
}

func Build(st *store.Store, o Options, prices *pricing.Table) ([]Line, []string, error) {
	rows, err := st.Aggregate(store.AggOpts{Since: o.Since, GroupBy: o.GroupBy, Source: o.Source})
	if err != nil {
		return nil, nil, err
	}
	byKey := map[string]*Line{}
	unpriced := map[string]bool{}
	var order []string
	for _, r := range rows {
		l, ok := byKey[r.Key]
		if !ok {
			l = &Line{Key: r.Key}
			byKey[r.Key] = l
			order = append(order, r.Key)
		}
		l.Input += r.Input
		l.Output += r.Output
		l.CacheRead += r.CacheRead
		l.CacheWrite += r.CacheWrite
		l.Reasoning += r.Reasoning
		l.Events += r.Events
		l.RawCost += r.RawCost

		// Source-reported cost is ground truth (opencode records the actual
		// charge); fall back to a pricing-table estimate otherwise.
		var priced bool
		switch {
		case r.HasRawCost:
			l.Cost += r.RawCost
			priced = true
		case prices != nil:
			if usd, ok := prices.Cost(r.Model, r.Input, r.Output, r.CacheRead, r.CacheWrite); ok {
				l.Cost += usd
				priced = true
			}
		}
		if !priced {
			l.UnpricedEvents += r.Events
			unpriced[r.Model] = true
		}
	}
	out := make([]Line, 0, len(order))
	for _, k := range order {
		l := byKey[k]
		l.Total = l.Input + l.Output + l.CacheRead + l.CacheWrite + l.Reasoning
		out = append(out, *l)
	}
	if o.GroupBy == "day" {
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	}
	var up []string
	for m := range unpriced {
		up = append(up, m)
	}
	sort.Strings(up)
	return out, up, nil
}

// Exported so cmd/tocy and internal/tui can share one set of ANSI codes
// instead of each keeping its own copy.
const (
	Reset  = "\033[0m"
	Bold   = "\033[1m"
	Dim    = "\033[2m"
	Red    = "\033[31m"
	Green  = "\033[32m"
	Yellow = "\033[33m"
	Blue   = "\033[34m"
	Purple = "\033[35m"
	Cyan   = "\033[36m"
)

const (
	dim    = Dim
	yellow = Yellow
)

// colorEnabled disables ANSI codes when stdout is not a terminal (pipes,
// redirects) or the user opted out via NO_COLOR (https://no-color.org).
var colorEnabled = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

// C wraps s in the given ANSI color code, resetting after.
func C(code, s string) string {
	if !colorEnabled {
		return s
	}
	return code + s + Reset
}

func c(code, s string) string { return C(code, s) }

func Render(w io.Writer, lines []Line, o Options, unpriced []string) error {
	if o.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(lines)
	}
	if len(lines) == 0 {
		fmt.Fprintln(w, "  "+c(dim, "no usage data — run `tocy scan` first"))
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	head := strings.ToUpper(orDefault(o.GroupBy, "tool"))
	fmt.Fprintf(tw, "%s\tINPUT\tOUTPUT\tCACHE R\tCACHE W\tREASON\tTOTAL\tEVENTS\tCOST\n", head)
	var tot Line
	for _, l := range lines {
		key := l.Key
		if o.GroupBy == "project" {
			key = shortProj(key)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", key,
			Humanize(l.Input), Humanize(l.Output), Humanize(l.CacheRead),
			Humanize(l.CacheWrite), Humanize(l.Reasoning), Humanize(l.Total), l.Events,
			costCell(l))
		tot.Input += l.Input
		tot.Output += l.Output
		tot.CacheRead += l.CacheRead
		tot.CacheWrite += l.CacheWrite
		tot.Reasoning += l.Reasoning
		tot.Total += l.Total
		tot.Events += l.Events
		tot.Cost += l.Cost
		tot.UnpricedEvents += l.UnpricedEvents
	}
	fmt.Fprintf(tw, "TOTAL\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
		Humanize(tot.Input), Humanize(tot.Output), Humanize(tot.CacheRead),
		Humanize(tot.CacheWrite), Humanize(tot.Reasoning), Humanize(tot.Total), tot.Events,
		costCell(tot))
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(unpriced) > 0 {
		fmt.Fprintf(w, "  %s\n", c(yellow, "* unpriced: "+strings.Join(unpriced, ", ")))
	}
	return nil
}

// CostCell formats a Line's cost, marking it with "*" (or "-*" when there's
// no priced cost at all) when some of its events couldn't be priced.
func CostCell(l Line) string {
	if l.Cost == 0 && l.UnpricedEvents > 0 {
		return "-*"
	}
	s := Money(l.Cost)
	if l.UnpricedEvents > 0 {
		s += "*"
	}
	return s
}

func costCell(l Line) string { return CostCell(l) }

func Money(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("$%.0f", v)
	case v >= 1:
		return fmt.Sprintf("$%.2f", v)
	case v > 0:
		return fmt.Sprintf("$%.3f", v)
	default:
		return "$0"
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ShortProj abbreviates a project path to its last two segments.
func ShortProj(p string) string {
	if p == "" {
		return "(unknown)"
	}
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	if len(parts) <= 3 {
		return p
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}

func shortProj(p string) string { return ShortProj(p) }

// Truncate shortens s to at most n runes, replacing the tail with an
// ellipsis when it doesn't fit.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func Humanize(n int64) string {
	f := float64(n)
	switch {
	case n >= 1_000_000_000:
		return trimZero(fmt.Sprintf("%.1fB", f/1e9))
	case n >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1fM", f/1e6))
	case n >= 1_000:
		return trimZero(fmt.Sprintf("%.1fK", f/1e3))
	default:
		return strconv.FormatInt(n, 10)
	}
}

func BuildSessions(st *store.Store, o Options, prices *pricing.Table) ([]SessionLine, []string, error) {
	rows, err := st.Sessions(store.AggOpts{Since: o.Since, Source: o.Source})
	if err != nil {
		return nil, nil, err
	}
	unpriced := map[string]bool{}
	byKey := map[string]*SessionLine{}
	models := map[string]map[string]bool{}
	var order []string
	for _, r := range rows {
		key := r.SessionID + "\x00" + r.Source
		sl, ok := byKey[key]
		if !ok {
			sl = &SessionLine{
				SessionID: r.SessionID,
				Source:    r.Source,
				Project:   r.Project,
				FirstTS:   time.Unix(r.FirstTS, 0),
				LastTS:    time.Unix(r.LastTS, 0),
			}
			byKey[key] = sl
			models[key] = map[string]bool{}
			order = append(order, key)
		}
		if ts := time.Unix(r.FirstTS, 0); ts.Before(sl.FirstTS) {
			sl.FirstTS = ts
		}
		if ts := time.Unix(r.LastTS, 0); ts.After(sl.LastTS) {
			sl.LastTS = ts
		}
		models[key][r.Model] = true

		sl.Input += r.Input
		sl.Output += r.Output
		sl.CacheRead += r.CacheRead
		sl.CacheWrite += r.CacheWrite
		sl.Reasoning += r.Reasoning
		sl.Events += r.Events

		// Same priority as Build: actual source cost wins over an estimate.
		var priced bool
		switch {
		case r.HasRawCost:
			sl.Cost += r.RawCost
			priced = true
		case prices != nil:
			if usd, ok := prices.Cost(r.Model, r.Input, r.Output, r.CacheRead, r.CacheWrite); ok {
				sl.Cost += usd
				priced = true
			}
		}
		if !priced {
			sl.UnpricedEvents += r.Events
			unpriced[r.Source] = true
		}
	}

	out := make([]SessionLine, 0, len(order))
	for _, key := range order {
		sl := *byKey[key]
		sl.Models = int64(len(models[key]))
		sl.Duration = sl.LastTS.Sub(sl.FirstTS)
		sl.Total = sl.Input + sl.Output + sl.CacheRead + sl.CacheWrite + sl.Reasoning

		sl.TruncID = Truncate(sl.SessionID, 13)
		out = append(out, sl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTS.After(out[j].LastTS) })

	var up []string
	for m := range unpriced {
		up = append(up, m)
	}
	sort.Strings(up)
	return out, up, nil
}

func RenderSessions(w io.Writer, sessions []SessionLine, o Options, unpriced []string) error {
	if o.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(sessions)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(w, "  "+c(dim, "no sessions found"))
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "SESSION\tSOURCE\tTOKENS\tCOST\tEVENTS\tMODELS\tDURATION\tPROJECT\n")
	var totalCost float64
	var totalTokens int64
	var totalEvents int64
	var totalUnpriced int64
	for _, s := range sessions {
		dur := s.Duration.Truncate(time.Second).String()
		if s.Duration < time.Minute {
			dur = fmt.Sprintf("%ds", int(s.Duration.Seconds()))
		}
		proj := shortProj(s.Project)

		costStr := CostCell(Line{Cost: s.Cost, UnpricedEvents: s.UnpricedEvents})

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			s.TruncID, s.Source, Humanize(s.Total), costStr,
			s.Events, s.Models, dur, proj)
		totalCost += s.Cost
		totalTokens += s.Total
		totalEvents += s.Events
		totalUnpriced += s.UnpricedEvents
	}
	fmt.Fprintf(tw, "TOTAL\t\t%s\t%s\t%d\t\t\t\n",
		Humanize(totalTokens), CostCell(Line{Cost: totalCost, UnpricedEvents: totalUnpriced}), totalEvents)
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(unpriced) > 0 {
		fmt.Fprintf(w, "  %s\n", c(yellow, "* unpriced: "+strings.Join(unpriced, ", ")))
	}
	return nil
}

func Statusline(st *store.Store, prices *pricing.Table) (string, error) {
	now := time.Now()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())

	lines, _, err := Build(st, Options{Since: today, GroupBy: "tool"}, prices)
	if err != nil {
		return "", err
	}

	var totalTok int64
	var totalCost float64
	var totalEvents int64
	toolCount := 0
	for _, l := range lines {
		totalTok += l.Total
		totalCost += l.Cost
		totalEvents += l.Events
		toolCount++
	}

	if toolCount == 0 {
		return c(dim, "no data today"), nil
	}

	unpriced := ""
	for _, l := range lines {
		if l.UnpricedEvents > 0 {
			unpriced = "*"
			break
		}
	}

	return fmt.Sprintf("%s%s  ·  %d tools  ·  %s tok  ·  %d events",
		Money(totalCost), unpriced, toolCount, Humanize(totalTok), totalEvents), nil
}

// trimZero drops a redundant ".0" from a humanized value like "1.0M",
// leaving the unit suffix intact.
func trimZero(s string) string {
	if len(s) < 2 {
		return s
	}
	num, unit := s[:len(s)-1], s[len(s)-1:]
	return strings.TrimSuffix(num, ".0") + unit
}
