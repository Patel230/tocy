// Package report turns store aggregates into text/JSON reports.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lakshmanpatel/tocy/internal/store"
)

// Line is one output row (all models within a group key collapsed).
type Line struct {
	Key        string  `json:"key"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	Reasoning  int64   `json:"reasoning"`
	Total      int64   `json:"total"`
	Events     int64   `json:"events"`
	RawCost    float64 `json:"raw_cost,omitempty"`
}

type Options struct {
	Since   time.Time
	GroupBy string // tool | model | day | project
	Source  string
	JSON    bool
}

var sinceRe = regexp.MustCompile(`^(\d+)([hdwm])$`)

// ParseSince accepts "all", "today", durations like "7d"/"24h"/"2w"/"1m",
// or a date "2026-07-01". Returned time is zero for "all".
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

// Build aggregates the store into collapsed, sorted lines.
func Build(st *store.Store, o Options) ([]Line, error) {
	rows, err := st.Aggregate(store.AggOpts{Since: o.Since, GroupBy: o.GroupBy, Source: o.Source})
	if err != nil {
		return nil, err
	}
	byKey := map[string]*Line{}
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
	}
	out := make([]Line, 0, len(order))
	for _, k := range order {
		l := byKey[k]
		l.Total = l.Input + l.Output + l.CacheRead + l.CacheWrite + l.Reasoning
		out = append(out, *l)
	}
	if o.GroupBy == "day" {
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key }) // chronological
	} else {
		sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total }) // biggest first
	}
	return out, nil
}

// Render writes lines as a table (or JSON) to w.
func Render(w io.Writer, lines []Line, o Options) error {
	if o.JSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(lines)
	}
	if len(lines) == 0 {
		fmt.Fprintln(w, "no usage data — run `tocy scan` first")
		return nil
	}
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	head := strings.ToUpper(orDefault(o.GroupBy, "tool"))
	fmt.Fprintf(tw, "%s\tINPUT\tOUTPUT\tCACHE R\tCACHE W\tREASON\tTOTAL\tEVENTS\n", head)
	var tot Line
	for _, l := range lines {
		key := l.Key
		if o.GroupBy == "project" {
			key = shortProject(key)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n", key,
			Humanize(l.Input), Humanize(l.Output), Humanize(l.CacheRead),
			Humanize(l.CacheWrite), Humanize(l.Reasoning), Humanize(l.Total), l.Events)
		tot.Input += l.Input
		tot.Output += l.Output
		tot.CacheRead += l.CacheRead
		tot.CacheWrite += l.CacheWrite
		tot.Reasoning += l.Reasoning
		tot.Total += l.Total
		tot.Events += l.Events
	}
	fmt.Fprintf(tw, "TOTAL\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
		Humanize(tot.Input), Humanize(tot.Output), Humanize(tot.CacheRead),
		Humanize(tot.CacheWrite), Humanize(tot.Reasoning), Humanize(tot.Total), tot.Events)
	return tw.Flush()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// shortProject renders "/Users/x/Desktop/ProjectAlpha/tocy" as "…/ProjectAlpha/tocy".
func shortProject(p string) string {
	if p == "" {
		return "(unknown)"
	}
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	if len(parts) <= 3 {
		return p
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}

// Humanize renders 1234 as "1.2K", 5600000 as "5.6M", etc.
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

func trimZero(s string) string {
	return strings.Replace(s, ".0", "", 1)
}
