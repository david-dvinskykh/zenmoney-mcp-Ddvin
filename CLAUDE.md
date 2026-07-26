# zenmoney-mcp

MCP server for ZenMoney personal finance. TypeScript, built with `@modelcontextprotocol/sdk`.

## Build & Run

```bash
npm install
npm run build   # tsc → build/
npm start       # node build/index.js
```

Requires `ZENMONEY_TOKEN` env var (from https://zerro.app/token). Set in `.env`.
Optional: `ZENMONEY_CACHE_DIR`, `ZENMONEY_CACHE_TTL`, `ZENMONEY_NO_CACHE`.

Also runnable straight from the repo without publishing:
`npx -y github:david-dvinskykh/zenmoney-mcp-Ddvin` (the `prepare` script builds on install).

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
