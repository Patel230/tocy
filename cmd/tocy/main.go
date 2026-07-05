// tocy — track token usage & cost across every AI CLI on this machine.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lakshmanpatel/tocy/internal/ingest"
	"github.com/lakshmanpatel/tocy/internal/pricing"
	"github.com/lakshmanpatel/tocy/internal/report"
	"github.com/lakshmanpatel/tocy/internal/store"
	"github.com/lakshmanpatel/tocy/internal/tui"
)

const usageText = `tocy — token usage & cost across your AI CLIs

Usage:
  tocy                 interactive dashboard (scans, then live TUI)
  tocy scan            ingest new usage from all detected tools
  tocy report          print usage table
      --since <all|today|7d|24h|2w|1m|YYYY-MM-DD>   (default all)
      --by <tool|model|day|project>                 (default tool)
      --json
  tocy tools           list detected tools and their ingest status
  tocy pricing refresh force-refresh the LiteLLM pricing cache
  tocy help            this help
`

func main() {
	args := os.Args[1:]
	cmd := "dashboard"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	var err error
	switch cmd {
	case "dashboard":
		err = cmdDashboard()
	case "scan":
		err = cmdScan()
	case "report":
		err = cmdReport(args)
	case "tools":
		err = cmdTools()
	case "pricing":
		err = cmdPricing(args)
	case "help", "-h", "--help":
		fmt.Print(usageText)
	default:
		fmt.Fprintf(os.Stderr, "tocy: unknown command %q\n\n%s", cmd, usageText)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tocy:", err)
		os.Exit(1)
	}
}

func openStore() (*store.Store, error) {
	return store.Open(store.DefaultPath())
}

func cmdScan() error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	results := ingest.ScanAll(st, ingest.Sources())
	for _, r := range results {
		switch {
		case r.Err != nil:
			fmt.Printf("  %-12s ERROR: %v\n", r.Source, r.Err)
		case !r.Found:
			fmt.Printf("  %-12s not detected\n", r.Source)
		default:
			fmt.Printf("  %-12s %d file(s) parsed, %d new event(s) in %s\n",
				r.Source, r.Files, r.NewEvents, r.Duration.Round(time.Millisecond))
		}
	}
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	since := fs.String("since", "all", "time window")
	by := fs.String("by", "tool", "group by: tool|model|day|project")
	jsonOut := fs.Bool("json", false, "JSON output")
	tool := fs.String("tool", "", "filter to one tool, e.g. claude-code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sinceT, err := report.ParseSince(*since, time.Now())
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	o := report.Options{Since: sinceT, GroupBy: *by, Source: *tool, JSON: *jsonOut}
	lines, unpriced, err := report.Build(st, o, pricing.Load(false))
	if err != nil {
		return err
	}
	return report.Render(os.Stdout, lines, o, unpriced)
}

func cmdPricing(args []string) error {
	if len(args) == 0 || args[0] != "refresh" {
		return fmt.Errorf("usage: tocy pricing refresh")
	}
	t := pricing.Load(true)
	fmt.Printf("pricing: %d models loaded (source: %s, cache: %s)\n",
		t.Count, t.Source, pricing.CachePath())
	if t.Source != "network" {
		fmt.Println("note: network refresh failed; using best local data")
	}
	return nil
}

func cmdTools() error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	stats, err := st.SourceStats()
	if err != nil {
		return err
	}
	for _, src := range ingest.Sources() {
		found, root := src.Detect()
		status := "not detected"
		if found {
			status = "detected at " + root
		}
		fmt.Printf("  %-12s %s\n", src.Name(), status)
		if s, ok := stats[src.Name()]; ok {
			fmt.Printf("  %-12s %d events ingested, %s → %s\n", "",
				s.Events, s.FirstTS.Format("2006-01-02"), s.LastTS.Format("2006-01-02 15:04"))
		}
	}
	return nil
}

// cmdDashboard is the default command: the live TUI (scans on startup and
// every 30s in the background).
func cmdDashboard() error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return tui.Run(st, pricing.Load(false))
}
