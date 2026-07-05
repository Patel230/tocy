# Cursor CLI/IDE — format-discovery spike (2026-07-05)

**Conclusion: no parser shipped.** Cursor does not store usable per-request
token counts or cost locally. Per the plan's "ship only if token fields
exist" rule, this is documented as a non-goal, same as aider.

## Where usage-like data lives

Main store: `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb`
(SQLite, ~268MB on this machine). Two relevant tables:

- `ItemTable` (plain key/value) — settings, auth tokens
  (`cursorAuth/accessToken`, `cursorAuth/refreshToken`), billing UI dismissal
  flags (`cursor.billingBanner...`, `cursor.dismissedCreditGrantIds`) but no
  usage numbers. `composer.composerHeaders` lists every composer/chat
  (`composerId`, `name`, `createdAt`, `totalLinesAdded/Removed`,
  `filesChangedCount`) — rich metadata, zero tokens or cost.
- `cursorDiskKV` (28,946 rows) — the actual conversation data, keyed by
  prefix:
  - `composerData:<id>` (156 rows, largest ~730KB) — full composer state.
    Has `contextTokensUsed` / `contextTokenLimit` / `promptTokenBreakdown`,
    but these describe **context-window usage of the current prompt**, not
    cumulative input/output tokens consumed or cost. Also has
    `usageData: {}` — always empty in every composer sampled.
  - `bubbleId:<composerId>:<bubbleId>` (9,758 rows) — one per chat message.
    Has a `tokenCount: {inputTokens, outputTokens}` field — the field exists,
    but sampling 400 random bubbles found **zero** with a nonzero value.
    Not populated for local/CLI-attributed usage on this install.
  - `agentKv:blob:<hash>` (18,387 rows) — mixed: ~62% are plain protobuf-ish
    binary (not JSON — raw text/varint encoded conversation turns), ~38% are
    JSON messages in a Vercel-AI-SDK-like shape (`role`, `content`, `id`,
    `providerOptions`). Searched all JSON-parseable blobs for any of
    `usage`, `promptTokens`, `completionTokens`, `totalTokens`,
    `inputTokens`, `outputTokens`, `tokenCount`, `model`, `modelName` as
    actual object keys (not substring matches in body text, which produces
    false positives from tool results mentioning "token"/"cost" in prose).
    Result: 36/18,387 blobs carry a `providerOptions.cursor.modelName`
    (only on `redacted-reasoning` content blocks) and **none** carry any
    usage/cost key.

## Why

Cursor's usage-based pricing (see `cursor.creditGrantPrimaryDismissedPromos`
etc.) is almost certainly tracked and billed server-side against the
account in `cursorAuth/*`, not persisted locally per-request. Nothing in
`state.vscdb` gives a trustworthy per-message or per-session token/cost
figure to ingest.

## Revisit if

- Cursor ships a local usage export or a documented API endpoint for
  historical usage (would need `cursorAuth/accessToken` to call it —
  out of scope for a local-file scanner).
- A future Cursor version starts populating `bubbleId.tokenCount` or adds a
  real `usageData` payload to `composerData`.
