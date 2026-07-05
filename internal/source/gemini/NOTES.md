# Gemini CLI — format-discovery spike (2026-07-05)

**Conclusion: no parser shipped.** `~/.gemini/` holds no per-request token
or cost data, so — same as aider and Cursor — this is documented as a
non-goal rather than a stub parser.

## What's actually in `~/.gemini/`

- `projects.json` — one line mapping project path -> label. No usage.
- `history/<project>/` — empty in practice on this machine (0 files).
- `state.json` — UI-only state (`defaultBannerShownCount`, `tipsShown`).
- `oauth_creds.json` / `google_accounts.json` — auth only.
- `settings.json` — CLI preferences.

Grepped every file under `~/.gemini/` for `token|usage|cost`; the only hits
are `oauth_creds.json` (OAuth *access* token, unrelated) and doc/example
files under `~/.gemini/config/plugins/**` (third-party skill docs that
happen to mention "token" in prose, e.g. API tutorials) — no numeric
usage records anywhere.

## Why

The installed Gemini CLI on this machine doesn't persist a local
session/usage log the way Claude Code or Codex do — usage is presumably
only visible through Google's own billing/quota UI, not a local file.

## Note: a second, more capable tool is present

`~/.gemini/antigravity-cli/` exists alongside the plain Gemini CLI dirs —
this looks like Google's "Antigravity" CLI, a separate and more actively
used tool on this machine. It was **not** investigated in this spike (out
of the original tool list this project targeted: Claude Code, Codex,
opencode, Cursor, Gemini CLI, aider). Worth a follow-up spike if the user
wants tocy to cover it — same "ship only if token fields exist" rule
should apply.

## Revisit if

- Gemini CLI starts writing a local transcript/usage log (some CLI tools
  added this after initial release).
- The user wants Antigravity CLI investigated as a new source.
