# zenmoney-mcp-ddvin

MCP server for ZenMoney personal finance. TypeScript, built with `@modelcontextprotocol/sdk`.
Published to npm as `zenmoney-mcp-ddvin` (a fork of `zenmoney-mcp`).

## Build & Run

```bash
npm install
npm run build   # tsc → build/
npm start       # node build/index.js
```

Requires `ZENMONEY_TOKEN` env var (from https://zerro.app/token). Set in `.env`.
Optional: `ZENMONEY_CACHE_DIR`, `ZENMONEY_CACHE_TTL`, `ZENMONEY_NO_CACHE`.

Run the release: `npx -y zenmoney-mcp-ddvin`. Run unreleased code straight from the
repo: `npx -y github:david-dvinskykh/zenmoney-mcp-Ddvin` (the `prepare` script builds
on install).

## Releasing

`.github/workflows/publish.yml` publishes to npm on a `v*` tag push (needs the
`NPM_TOKEN` secret). The version lives in four places that must be bumped
together: `package.json`, `manifest.json`, `server.json`, and the `McpServer`
block in `src/index.ts`. The workflow fails if the tag and `package.json`
disagree.

## Structure

- `src/index.ts` — entry point, server setup
- `src/api.ts` — ZenMoney API client
- `src/state.ts` — in-memory state, auto-sync (`ensureSynced`), cache restore/persist
- `src/cache.ts` — on-disk snapshot keyed by `sha256(token)`, survives process restarts
- `src/tools/` — MCP tool registrations (sync, accounts, categories, transactions,
  debts, update, delete, reminders, suggest) plus `format.ts`, the shared
  transaction renderer, and `resolve.ts`, the shared account/category/merchant
  lookups

## Go port (`go/`)

`go/` holds a full port of this server: the same 15 tools, the same output text,
the same cache file format and directory, so both implementations can share one
cache. It adds a `-http` flag that serves the tools as a long-lived streamable
HTTP service, which is what makes it worth having — under MetaMCP a stdio server
is respawned per client session, and the Go service is started once instead.
Measured against a 20 593-transaction stub: 158 MB peak RSS down to 11 MB.

Changes to a tool's behaviour belong in both implementations, or in neither.
`go/README.md` records the two places they deliberately differ (date-format
validation, and the mutex the shared state needs).

## Conventions

Tools must not require a prior `sync_data` call — gate them with
`ensureSynced(state)` from `src/tools/ensure-synced.ts`, which syncs on demand and
returns a tool error result if that fails.

Reminders are ZenMoney's planned transactions: a `reminder` holds the schedule
and `reminderMarker`s are its dated occurrences. Deleting a reminder deletes its
markers too — server-side and in `applyDeletion`. Nothing interprets a
reminder's `points` field; concrete dates come from the markers.

Destructive tools are two-step: without `confirm: true` they only report what
would happen. That covers `update_transaction` as well — an edit overwrites the
old values — so its preview renders a before/after pair. Deletions go out as the
diff request's `deletion` array and are mirrored locally with
`state.applyLocalDeletions()` — like any write, the response is a diff since the
last `serverTimestamp` and must be applied in full.

A debt is not a separate entity: it is a transaction with the system `debt`
account (`account.type === "debt"`, one per user) on one side. Both legs carry
the **non-debt** account's `instrument` and an equal amount — the debt account's
own currency is `user.currency` and never appears on the transaction. Editing a
debt goes through the same normalization in `src/tools/update.ts`.
