package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lakshmanpatel/tocy/internal/ingest"
	"github.com/lakshmanpatel/tocy/internal/pricing"
	"github.com/lakshmanpatel/tocy/internal/report"
	"github.com/lakshmanpatel/tocy/internal/store"
	"github.com/lakshmanpatel/tocy/internal/tui"
)

var version = "dev"

// Aliased from internal/report so the whole CLI shares one set of ANSI codes.
const (
	ansiReset  = report.Reset
	ansiBold   = report.Bold
	ansiDim    = report.Dim
	ansiRed    = report.Red
	ansiGreen  = report.Green
	ansiYellow = report.Yellow
	ansiBlue   = report.Blue
	ansiPurple = report.Purple
	ansiCyan   = report.Cyan
)

func color(c, s string) string { return report.C(c, s) }

const usage = `tocy — token usage & cost across your AI CLIs

Usage:
  tocy                           interactive dashboard
  tocy scan                      ingest new usage
      --verbose                  show per-file scan details
  tocy report                    print usage table
      --since                    all|today|7d|24h|2w|1m|YYYY-MM-DD (default all)
      --until                    end of window (exclusive): 7d|2w|1m|YYYY-MM-DD
      --by                       tool|model|day|project|session (default tool)
      --json                     machine-readable output
      --tool <name>              filter to one tool
  tocy sessions                  list recent sessions with cost
      --since                    time window (default today)
      --until                    end of window (exclusive): 7d|2w|1m|YYYY-MM-DD
      --json                     machine-readable output
  tocy tools                     list detected tools and ingest status
  tocy statusline                compact one-line cost summary for today
  tocy watch                     keep ingesting (fsnotify + periodic rescan)
      --interval <dur>           fallback rescan interval (default 30s)
      --install                  install launchd agent (macOS)
      --uninstall                remove launchd agent
  tocy prune --keep <days> --yes delete events older than N days
  tocy pricing refresh           force-refresh pricing cache
  tocy version                   print version
  tocy help [cmd]                this help, or help for a specific command
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
		err = cmdScan(args)
	case "report":
		err = cmdReport(args)
	case "sessions":
		err = cmdSessions(args)
	case "tools":
		err = cmdTools()
	case "statusline":
		err = cmdStatusline()
	case "watch":
		err = cmdWatch(args)
	case "prune":
		err = cmdPrune(args)
	case "pricing":
		err = cmdPricing(args)
	case "help", "-h", "--help":
		err = cmdHelp(args)
	case "version", "-v", "--version":
		fmt.Println(color(ansiBold+ansiPurple, "tocy"), version)
	default:
		fmt.Fprintf(os.Stderr, "%s unknown command %q\n\n%s", color(ansiRed, "tocy:"), cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, color(ansiRed, "tocy:"), err)
		os.Exit(1)
	}
}

func openStore() (*store.Store, error) {
	return store.Open(store.DefaultPath())
}

func cmdScan(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	verbose := fs.Bool("verbose", false, "show per-file scan details")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	results := ingest.ScanAll(st, ingest.Sources())
	had := false
	for _, r := range results {
		had = true
		switch {
		case r.Err != nil && r.Files == 0:
			fmt.Printf("  %s %s\n", color(ansiRed, "✖"), color(ansiBold+ansiRed, r.Source)+"  "+color(ansiRed, r.Err.Error()))
		case !r.Found:
			fmt.Printf("  %s %s\n", color(ansiYellow, "•"), color(ansiBold, r.Source)+"  "+color(ansiDim, "not detected"))
		default:
			ev := fmt.Sprintf("+%d event(s)", r.NewEvents)
			fi := fmt.Sprintf("%d file(s)", r.Files)
			du := r.Duration.Round(time.Millisecond).String()
			stat := fmt.Sprintf("%s  %s %s", color(ansiGreen, ev), color(ansiDim, fi), color(ansiDim, du))
			fmt.Printf("  %s %s  %s\n", color(ansiGreen, "✓"), color(ansiBold, r.Source), stat)
			if r.Err != nil {
				fmt.Printf("    %s %s\n", color(ansiYellow, "⚠"), color(ansiYellow, "some files skipped: "+r.Err.Error()))
			}
		}
		if *verbose {
			for _, d := range r.Details {
				if d.Err != nil {
					fmt.Printf("    %s %s  %s\n", color(ansiDim, "└─"), color(ansiDim, d.Path), color(ansiRed, d.Err.Error()))
				} else {
					fmt.Printf("    %s %s  %s\n", color(ansiDim, "└─"), color(ansiDim, d.Path), color(ansiGreen, fmt.Sprintf("+%d", d.NewEvents)))
				}
			}
		}
	}
	if !had {
		fmt.Println("  " + color(ansiDim, "no sources scanned"))
	}
	return nil
}

func cmdWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	interval := fs.Duration("interval", 30*time.Second, "rescan interval")
	install := fs.Bool("install", false, "install a launchd agent")
	uninstall := fs.Bool("uninstall", false, "remove the launchd agent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *interval < time.Second {
		return fmt.Errorf("--interval must be at least 1s")
	}
	if *uninstall {
		return uninstallLaunchAgent()
	}
	if *install {
		return installLaunchAgent(interval.String())
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// Exit cleanly on SIGINT/SIGTERM so the deferred st.Close() runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWatch(ctx, st, ingest.Sources(), *interval)
}

func cmdSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	since := fs.String("since", "today", "time window")
	until := fs.String("until", "", "end of time window (exclusive)")
	jsonOut := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now()
	sinceT, err := report.ParseSince(*since, now)
	if err != nil {
		return err
	}
	untilT, err := report.ParseUntil(*until, now)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	sessions, unpriced, err := report.BuildSessions(st, report.Options{Since: sinceT, Until: untilT}, pricing.Load(false))
	if err != nil {
		return err
	}
	return report.RenderSessions(os.Stdout, sessions, report.Options{JSON: *jsonOut}, unpriced)
}

func cmdStatusline() error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	line, err := report.Statusline(st, pricing.Load(false))
	if err != nil {
		return err
	}
	fmt.Println(line)
	return nil
}

func cmdPrune(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	keep := fs.Int("keep", 0, "keep events newer than N days")
	yes := fs.Bool("yes", false, "confirm deletion without prompting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keepSet := false
	for _, arg := range args {
		if arg == "--keep" || strings.HasPrefix(arg, "--keep=") {
			keepSet = true
		}
	}
	if !keepSet || *keep < 1 {
		return fmt.Errorf("--keep is required and must be >= 1")
	}
	if !*yes {
		return fmt.Errorf("prune deletes data permanently; re-run with --yes to confirm")
	}
	before := time.Now().AddDate(0, 0, -*keep)
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	n, err := st.Prune(before)
	if err != nil {
		return err
	}
	fmt.Printf("  %s %s %s\n", color(ansiGreen, "✓"), color(ansiBold, fmt.Sprintf("%d event(s) pruned", n)),
		color(ansiDim, fmt.Sprintf("(kept %s → today)", before.Format("2006-01-02"))))
	if n > 0 {
		// incremental_vacuum reclaims space without rewriting the whole file,
		// which can block for seconds on a large database.
		if _, err := st.DB.Exec("PRAGMA incremental_vacuum(100)"); err != nil {
			return fmt.Errorf("prune succeeded but vacuum failed: %w", err)
		}
	}
	return nil
}

func cmdHelp(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "scan":
		fmt.Println(color(ansiBold+ansiPurple, "tocy scan") + " — " + color(ansiDim, "ingest new usage"))
		fmt.Println()
		fmt.Println("  " + color(ansiBold, "tocy scan"))
		fmt.Println("      " + color(ansiDim, "--verbose    show per-file scan details"))
	case "report":
		fmt.Println(color(ansiBold+ansiPurple, "tocy report") + " — " + color(ansiDim, "print usage table"))
		fmt.Println()
		fmt.Println("  " + color(ansiBold, "tocy report") + " " + color(ansiDim, "[flags]"))
		fmt.Println("      " + color(ansiDim, "--since all|today|7d|24h|2w|1m|YYYY-MM-DD  (default all)"))
		fmt.Println("      " + color(ansiDim, "--until 7d|2w|1m|YYYY-MM-DD                end of window (exclusive)"))
		fmt.Println("      " + color(ansiDim, "--by tool|model|day|project|session      (default tool)"))
		fmt.Println("      " + color(ansiDim, "--json                                   machine-readable output"))
		fmt.Println("      " + color(ansiDim, "--tool <name>                            filter to one tool"))
	case "sessions":
		fmt.Println(color(ansiBold+ansiPurple, "tocy sessions") + " — " + color(ansiDim, "list recent sessions with cost"))
		fmt.Println()
		fmt.Println("  " + color(ansiBold, "tocy sessions") + " " + color(ansiDim, "[flags]"))
		fmt.Println("      " + color(ansiDim, "--since all|today|7d|24h|2w|1m|YYYY-MM-DD  (default today)"))
		fmt.Println("      " + color(ansiDim, "--until 7d|2w|1m|YYYY-MM-DD                end of window (exclusive)"))
		fmt.Println("      " + color(ansiDim, "--json                                   machine-readable output"))
	case "tools":
		fmt.Println(color(ansiBold+ansiPurple, "tocy tools") + " — " + color(ansiDim, "list detected tools and ingest status"))
	case "statusline":
		fmt.Println(color(ansiBold+ansiPurple, "tocy statusline") + " — " + color(ansiDim, "compact one-line cost summary for today"))
		fmt.Println()
		fmt.Println("  " + color(ansiBold, "tocy statusline"))
		fmt.Println("      " + color(ansiDim, "outputs: $4.25*  ·  3 tools  ·  12.4K tok  ·  31 events"))
	case "watch":
		fmt.Println(color(ansiBold+ansiPurple, "tocy watch") + " — " + color(ansiDim, "keep ingesting (fsnotify + periodic rescan)"))
		fmt.Println()
		fmt.Println("  " + color(ansiBold, "tocy watch") + " " + color(ansiDim, "[flags]"))
		fmt.Println("      " + color(ansiDim, "--interval <dur>    fallback rescan interval (default 30s)"))
		fmt.Println("      " + color(ansiDim, "--install           install launchd agent (macOS)"))
		fmt.Println("      " + color(ansiDim, "--uninstall         remove launchd agent"))
	case "prune":
		fmt.Println(color(ansiBold+ansiPurple, "tocy prune") + " — " + color(ansiDim, "delete old events"))
		fmt.Println()
		fmt.Println("  " + color(ansiBold, "tocy prune --keep <days>"))
		fmt.Println("      " + color(ansiDim, "--keep <days>  keep events newer than N days (required)"))
		fmt.Println("      " + color(ansiDim, "--yes         confirm permanent deletion"))
	case "pricing":
		fmt.Println(color(ansiBold+ansiPurple, "tocy pricing refresh") + " — " + color(ansiDim, "force-refresh pricing cache"))
	case "version":
		fmt.Println(color(ansiBold+ansiPurple, "tocy version") + " — " + color(ansiDim, "print version"))
	default:
		return fmt.Errorf("unknown help topic %q", args[0])
	}
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	since := fs.String("since", "all", "time window")
	until := fs.String("until", "", "end of time window (exclusive)")
	by := fs.String("by", "tool", "group by: tool|model|day|project")
	jsonOut := fs.Bool("json", false, "JSON output")
	tool := fs.String("tool", "", "filter to one tool, e.g. claude-code")
	if err := fs.Parse(args); err != nil {
		return err
	}
	now := time.Now()
	sinceT, err := report.ParseSince(*since, now)
	if err != nil {
		return err
	}
	untilT, err := report.ParseUntil(*until, now)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	o := report.Options{Since: sinceT, Until: untilT, GroupBy: *by, Source: *tool, JSON: *jsonOut}
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
	models := color(ansiBold, fmt.Sprintf("%d models", t.Count))
	src := fmt.Sprintf("source: %s", t.Source)
	cache := fmt.Sprintf("cache: %s", pricing.CachePath())
	fmt.Printf("  %s  %s  %s  %s\n", color(ansiGreen, "✓"), color(ansiBold, models), color(ansiDim, src), color(ansiDim, cache))
	switch t.Source {
	case "stale-cache":
		fmt.Println("  " + color(ansiYellow, "⚠ network refresh failed; using stale cached data"))
	case "embedded":
		fmt.Println("  " + color(ansiYellow, "⚠ network refresh failed and no cache found; using built-in snapshot"))
	}
	return nil
}

func cmdTools() error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	stats, err := st.SourceStats()
	if err != nil {
		return err
	}
	for _, src := range ingest.Sources() {
		found, root := src.Detect()
		nm := color(ansiBold, src.Name())
		if found {
			fmt.Printf("  %s  %s  %s\n", color(ansiGreen, "✓"), nm, color(ansiDim, root))
		} else {
			fmt.Printf("  %s  %s  %s\n", color(ansiYellow, "•"), nm, color(ansiDim, "not detected"))
		}
		if s, ok := stats[src.Name()]; ok {
			ev := fmt.Sprintf("%d events", s.Events)
			rg := fmt.Sprintf("%s → %s", s.FirstTS.Format("2006-01-02"), s.LastTS.Format("2006-01-02 15:04"))
			fmt.Printf("  %s  %s  %s\n", color(ansiDim, "└─"), color(ansiCyan, ev), color(ansiDim, rg))
		}
	}
	return nil
}

func cmdDashboard() error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return tui.Run(st, pricing.Load(false))
}
