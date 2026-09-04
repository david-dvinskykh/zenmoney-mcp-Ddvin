# zenmoney-mcp-ddvin — Go port

A line-for-line port of the TypeScript server in the repository root, plus one
thing the original cannot do: it can run as a **long-lived HTTP service** instead
of a process per client session.

That is the whole point. On the Raspberry Pi this server runs on, MetaMCP spawns
a fresh copy of every stdio MCP server for every client session — so the same
ZenMoney snapshot was being downloaded and held in memory up to five times over.
An HTTP service is started once, syncs once, and serves every session from one
warm snapshot.

## Measured against the TypeScript server

Both servers were driven through the same MCP handshake against a local stub
serving a 20 593-transaction snapshot (13.9 MB of JSON — the size of the real
account), then asked for `list_transactions`. Peak RSS is `VmHWM`.

|                    | TypeScript | Go       |
| ------------------ | ---------- | -------- |
| peak RSS           | 158.5 MB   | 11.3 MB  |
| ready (tools/list) | 0.37 s     | 0.02 s   |
| first query        | 0.55 s     | 0.33 s   |

The timings come from a local stub, so they leave out the network round trip that
dominates a real first sync; the memory figure is the one that carries over. On
the Pi the same TypeScript server sits at ~186 MB RSS per copy.

## Build and run

```bash
cd go
go test ./...
go build -o zenmoney-mcp ./cmd/zenmoney-mcp

ZENMONEY_TOKEN=... ./zenmoney-mcp              # stdio, drop-in for the Node server
ZENMONEY_TOKEN=... ./zenmoney-mcp -http :8081  # one shared service, POST /mcp
```

`-http` also serves `GET /health`, which returns `{"status":"ok"}` — enough for a
Docker health check.

A container image is in `Dockerfile`; it produces a static binary on a `scratch`
base (about 9 MB).

## Environment

Identical to the TypeScript server:

- `ZENMONEY_TOKEN` — required, from https://zerro.app/token
- `ZENMONEY_CACHE_DIR` — where snapshots live (same default and layout, so the
  two implementations share one cache directory)
- `ZENMONEY_CACHE_TTL` — seconds a snapshot is served without revalidating
- `ZENMONEY_NO_CACHE=1` — disable on-disk caching

`ZENMONEY_API_BASE` is added on top, and only so tests can point the client at a
local stub.

## Parity

All 15 tools are ported with the same names, arguments and output text:
`sync_data`, `list_accounts`, `list_categories`, `list_merchants`,
`list_transactions`, `add_expense`, `add_income`, `add_transfer`, `add_debt`,
`update_transaction`, `delete_transaction`, `delete_object`, `list_reminders`,
`delete_reminder`, `suggest_category`.

The behaviours the TypeScript tests pin down are pinned here too (39 Go tests):
incremental sync from the cached timestamp, the additive merge that keeps entity
order, deletions cascading from accounts, tags and reminders, the two-step
`confirm` flow on every destructive tool, the debt currency invariant, and
serving a stale snapshot when the live sync fails.

Two differences worth knowing:

- **Argument validation.** The TypeScript server uses zod, so `amount` being
  positive and `date` matching `YYYY-MM-DD` are rejected by the schema. The Go
  SDK validates types, not string formats, so the tools check the date format
  themselves and report it as a tool error instead of a protocol error.
- **Concurrency.** One `zen.State` is now shared by concurrent sessions, so it is
  mutex-guarded. Writers replace slices rather than mutate them, which lets a
  reader keep using the slice it was handed. `go test -race ./...` is clean.
