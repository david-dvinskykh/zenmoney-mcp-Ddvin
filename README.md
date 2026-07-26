# zenmoney-mcp

MCP server for [ZenMoney](https://zenmoney.ru) — access your personal finance data from any MCP-compatible AI client (Claude Desktop, Cursor, etc.).

## Features

| Tool | Description |
|------|-------------|
| `sync_data` | Refresh data on demand (optional — every tool auto-syncs) |
| `list_accounts` | List wallets, cards, and cash accounts |
| `list_categories` | List expense/income categories with hierarchy |
| `list_merchants` | List known merchants/payees |
| `list_transactions` | List and filter recent transactions |
| `add_expense` | Add an expense transaction |
| `add_income` | Add an income transaction |
| `add_transfer` | Transfer money between accounts (including cross-currency) |
| `suggest_category` | Get auto-suggested category for a payee |

**No manual sync needed.** Any tool syncs on demand if the data isn't loaded yet,
and the synced snapshot is cached on disk so a restarted server picks up where the
previous one left off instead of re-downloading everything. See
[Auto-sync and caching](#auto-sync-and-caching).

## Prerequisites

- Node.js >= 18
- A [ZenMoney](https://zenmoney.ru) account
- API token from [zerro.app/token](https://zerro.app/token)

## Quick start

No cloning or building needed — just add to your MCP client config.

### Run straight from this repo with npx (no npm publish)

`npx` can install and run a package directly from a Git repository, so this
server can be used before (or without) publishing it to the npm registry:

```bash
npx -y github:david-dvinskykh/zenmoney-mcp-Ddvin
```

npm clones the repo, installs dependencies, runs the `prepare` script (which
compiles TypeScript to `build/`), and starts the server — all from the checkout.

Use it in any MCP client config:

```json
{
  "mcpServers": {
    "zenmoney": {
      "command": "npx",
      "args": ["-y", "github:david-dvinskykh/zenmoney-mcp-Ddvin"],
      "env": {
        "ZENMONEY_TOKEN": "your_token_here"
      }
    }
  }
}
```

Pin to a branch, tag, or commit by appending `#<ref>`:

```bash
npx -y github:david-dvinskykh/zenmoney-mcp-Ddvin#main
```

Private repo, or SSH auth preferred? Use the full Git URL form instead:

```bash
npx -y git+ssh://git@github.com/david-dvinskykh/zenmoney-mcp-Ddvin.git
```

> npm caches the resolved commit for a git dependency. After pushing changes,
> re-run with an explicit `#<ref>` (or `npm cache clean --force`) to pick them up.

### MetaMCP

In MetaMCP, add a new **STDIO** server:

| Field | Value |
|-------|-------|
| Name | `zenmoney` |
| Type | `STDIO` |
| Command | `npx` |
| Args | `-y github:david-dvinskykh/zenmoney-mcp-Ddvin` |
| Env | `ZENMONEY_TOKEN=your_token_here` |

Equivalent JSON, if you configure MetaMCP by importing a config:

```json
{
  "mcpServers": {
    "zenmoney": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "github:david-dvinskykh/zenmoney-mcp-Ddvin"],
      "env": {
        "ZENMONEY_TOKEN": "your_token_here"
      }
    }
  }
}
```

MetaMCP may start and stop the server process repeatedly. That's fine — the
on-disk cache (keyed by a hash of your token) means each new process restores
the previous snapshot and only fetches the delta.

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "zenmoney": {
      "command": "npx",
      "args": ["-y", "zenmoney-mcp"],
      "env": {
        "ZENMONEY_TOKEN": "your_token_here"
      }
    }
  }
}
```

### Cursor

Add to `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "zenmoney": {
      "command": "npx",
      "args": ["-y", "zenmoney-mcp"],
      "env": {
        "ZENMONEY_TOKEN": "your_token_here"
      }
    }
  }
}
```

### Claude Code

```bash
claude mcp add zenmoney -- npx -y zenmoney-mcp
```

Replace `your_token_here` with your token from [zerro.app/token](https://zerro.app/token).
In any of the examples above, `zenmoney-mcp` (the published package) can be swapped
for `github:david-dvinskykh/zenmoney-mcp-Ddvin` to run this repo directly.

### Claude Desktop (MCPB bundle)

If you prefer a one-click install without editing JSON, build a `.mcpb` bundle and drag it into Claude Desktop → Settings → Extensions:

```bash
npm install
npm run pack:mcpb
# → dist/zenmoney-mcp-<version>.mcpb
```

On install, Claude Desktop will prompt for your ZenMoney token (stored in the OS keychain).

### From source

```bash
git clone https://github.com/a-tarasoff/zenmoney-mcp.git
cd zenmoney-mcp
npm install
npm run build
cp .env.example .env  # add your token
```

## Usage

Once configured, start a conversation and ask your AI client to:

1. **Browse** — "Show me my accounts", "List my categories" (syncs automatically)
2. **Query** — "Show expenses for the last 7 days", "List transactions from January 1–31", "How much did I spend on groceries?"
3. **Add transactions** — "Add a 500 RUB expense for coffee today"
4. **Transfer** — "Transfer 1000 USD from Checking to Euro Card, received 920 EUR"
5. **Refresh** — "Sync my ZenMoney data" (only needed to pull changes mid-conversation)

## Auto-sync and caching

Tools no longer require `sync_data` to be called first:

- On the first tool call (and at server startup, in the background) the server
  restores the on-disk snapshot, then runs an **incremental** sync from the
  snapshot's `serverTimestamp`.
- With no snapshot, it runs a full sync.
- If the sync request fails but a snapshot exists, the cached data is served
  rather than erroring out.
- Concurrent tool calls share a single in-flight sync.

The snapshot lives in one JSON file per token:

```
~/.cache/zenmoney-mcp/<sha256(token)>.json      # Linux/macOS
%LOCALAPPDATA%\zenmoney-mcp\<sha256(token)>.json  # Windows
```

The file name is a hash of the API token, so different accounts never share a
snapshot and the token itself is never written to disk. It is written atomically
with `0600` permissions — but it does contain your financial data in plain text,
so treat it like any other local financial file.

`sync_data` with `force_full=true` deletes the snapshot and re-downloads
everything — use it if the cached data ever looks wrong.

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ZENMONEY_TOKEN` | — | **Required.** API token from [zerro.app/token](https://zerro.app/token) |
| `ZENMONEY_CACHE_DIR` | OS cache dir | Directory for snapshot files |
| `ZENMONEY_CACHE_TTL` | `0` | Seconds a snapshot is served without revalidating. `0` always runs an incremental sync (cheap, and always fresh) |
| `ZENMONEY_NO_CACHE` | — | Set to `1` to disable on-disk caching entirely |

## Contributing

PRs welcome! Feel free to open issues for bugs or feature requests.

## License

[MIT](LICENSE)
