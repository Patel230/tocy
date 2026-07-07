# tocy

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/Patel230/tocy?style=flat-square&logo=go)](https://go.dev)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg?style=flat-square)](http://makeapullrequest.com)

Token usage and cost across every AI coding CLI on your machine, in one
terminal dashboard. Single Go binary, no server, no telemetry — it only
reads log files and databases that your existing tools already write to
disk, and pulls live model pricing to price them.

```
tocy                  # interactive dashboard (scans, then live TUI)
tocy scan             # one-shot ingest
tocy watch            # keep ingesting in the foreground (or as a launchd agent)
tocy report --since 7d --by model
tocy tools            # what's detected, what's been ingested
```

## Supported tools

| Tool | Source read | Status |
|---|---|---|
| Claude Code | `~/.claude/projects/**/*.jsonl` | ✅ |
| Codex CLI | `~/.codex/sessions/**/*.jsonl` | ✅ |
| opencode | `~/.local/share/opencode/opencode.db` | ✅ |
| Cursor CLI/IDE | `~/Library/Application Support/Cursor/.../state.vscdb` | ❌ investigated — [no local token/cost data exists](internal/source/cursor/NOTES.md) |
| Gemini CLI | `~/.gemini/` | ❌ investigated — [no local usage data exists](internal/source/gemini/NOTES.md) |
| aider | `~/.aider/` | ❌ no local per-request usage log |

tocy only ever reads these files — it never talks to the tools themselves
and never modifies your `~/.claude`, `~/.codex`, etc. directories.

## Install

```
go build -o tocy ./cmd/tocy
```

Requires Go 1.26+. Pure Go (no CGO), so `go build` alone produces a
working binary — no sqlite3 headers or other system deps needed.

## How it works

- **Ingest**: each tool has a small parser in `internal/source/<tool>/`
  implementing a common `Source` interface (detect, list files to scan,
  parse). JSONL sources are tailed by byte offset so re-scanning only reads
  new bytes; the opencode SQLite source tracks an incremental cursor
  instead. Every ingested row is deduped on `(source, dedup_key)`, so
  `tocy scan` is safe to run as often as you like — nothing is
  double-counted, and nothing is lost if a scan is interrupted mid-file.
- **Store**: everything lands in a local SQLite DB at `~/.tocy/tocy.db`.
  No rollup tables — the dashboard and `report` just `SUM(...) GROUP BY`
  at query time, which is fast enough at this scale and means historical
  numbers automatically reflect current pricing.
- **Pricing**: model prices come from the community-maintained
  [LiteLLM pricing dataset](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json),
  fetched live and cached at `~/.tocy/pricing.json` for 24h. If the network
  is unavailable, tocy falls back to the last cached copy, then to a
  snapshot embedded in the binary — it never fails to start because of a
  pricing fetch. Models tocy can't match are shown as **unpriced** in
  reports rather than silently costed at $0.
- **Dashboard**: a [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  TUI with four tabs (Overview, By Model, Daily, Projects), hand-rolled bar
  and column charts, and a background rescan every 30s.

## `tocy watch`

Runs the same incremental scan in a loop (default every 30s) so the
dashboard and `report` stay current without you remembering to run
`tocy scan`. On macOS you can install it as a per-user background service:

```
tocy watch --install              # writes + loads a launchd agent
tocy watch --install --interval 1m
tocy watch --uninstall            # removes it
```

This writes `~/Library/LaunchAgents/com.tocy.watch.plist` (runs at login,
restarts if killed) and logs to `~/.tocy/watch.log`. It's just a plist
pointing at your `tocy` binary — inspect or remove it with `launchctl`
directly if you'd rather manage it by hand.

## `tocy report`

```
tocy report --since 7d --by model
tocy report --since today --by tool
tocy report --by project --json
```

- `--since`: `all` (default), `today`, `7d`, `24h`, `2w`, `1m`, or an exact
  `YYYY-MM-DD`.
- `--by`: `tool`, `model`, `day`, or `project`.
- `--tool <name>`: filter to one tool (e.g. `--tool claude-code`).
- `--json`: machine-readable output, includes both tocy's recomputed cost
  and (for opencode) the tool's own reported cost.

## Development

```
go build ./...
go test ./...
"$(go env GOROOT)/bin/gofmt" -l .   # should print nothing
```

Fixtures for each parser live under `internal/source/<tool>/testdata/` (or
inline in the `_test.go` file for the SQLite-backed sources) and cover
duplicate lines, cumulative-counter resets, mid-session model switches, and
resuming from a saved offset/cursor.

## License

MIT
