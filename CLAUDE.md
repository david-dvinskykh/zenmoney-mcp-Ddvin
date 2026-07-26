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
- `src/tools/` — MCP tool registrations (sync, accounts, categories, transactions, suggest)

## Conventions

Tools must not require a prior `sync_data` call — gate them with
`ensureSynced(state)` from `src/tools/ensure-synced.ts`, which syncs on demand and
returns a tool error result if that fails.
