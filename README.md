# zenmoney-mcp-ddvin

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

No cloning or building needed — the server is published to npm:

```bash
npx -y zenmoney-mcp-ddvin
```

Use it in any MCP client config:

```json
{
  "mcpServers": {
    "zenmoney": {
      "command": "npx",
      "args": ["-y", "zenmoney-mcp-ddvin"],
      "env": {
        "ZENMONEY_TOKEN": "your_token_here"
      }
    }
  }
}
```

### Running unreleased changes from Git

`npx` can also install straight from the repository, which is useful for testing
a branch before it is released:

```bash
npx -y github:david-dvinskykh/zenmoney-mcp-Ddvin          # default branch
npx -y github:david-dvinskykh/zenmoney-mcp-Ddvin#main     # pin a branch/tag/commit
npx -y git+ssh://git@github.com/david-dvinskykh/zenmoney-mcp-Ddvin.git  # SSH auth
```

npm clones the repo, installs dependencies, runs the `prepare` script (which
compiles TypeScript to `build/`), and starts the server — all from the checkout.
Expect the first run to take a while; the published package starts far faster.

> npm caches the resolved commit for a git dependency. After pushing changes,
> re-run with an explicit `#<ref>` (or `npm cache clean --force`) to pick them up.

### MetaMCP

In MetaMCP, add a new **STDIO** server:

| Field | Value |
|-------|-------|
| Name | `zenmoney` |
| Type | `STDIO` |
| Command | `npx` — just the binary, no flags |
| Args | `-y` and `zenmoney-mcp-ddvin` as two separate entries |
| Env | `ZENMONEY_TOKEN=your_token_here` |

Keep the flags out of the Command field. If MetaMCP spawns a bare shell instead
of the server, the JSON-RPC handshake ends up on the shell's stdin and you get
`sh: 1: {method:initialize,...}: not found`.

Equivalent JSON, if you configure MetaMCP by importing a config:

```json
{
  "mcpServers": {
    "zenmoney": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "zenmoney-mcp-ddvin"],
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
      "args": ["-y", "zenmoney-mcp-ddvin"],
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
      "args": ["-y", "zenmoney-mcp-ddvin"],
      "env": {
        "ZENMONEY_TOKEN": "your_token_here"
      }
    }
  }
}
```

### Claude Code

```bash
claude mcp add zenmoney -- npx -y zenmoney-mcp-ddvin
```

Replace `your_token_here` with your token from [zerro.app/token](https://zerro.app/token).
In any of the examples above, `zenmoney-mcp-ddvin` can be swapped for
`github:david-dvinskykh/zenmoney-mcp-Ddvin` to run unreleased code from the repo.

### Claude Desktop (MCPB bundle)

If you prefer a one-click install without editing JSON, build a `.mcpb` bundle and drag it into Claude Desktop → Settings → Extensions:

```bash
npm install
npm run pack:mcpb
# → dist/zenmoney-mcp-ddvin-<version>.mcpb
```

On install, Claude Desktop will prompt for your ZenMoney token (stored in the OS keychain).

### From source

```bash
git clone https://github.com/david-dvinskykh/zenmoney-mcp-Ddvin.git
cd zenmoney-mcp-Ddvin
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

## Releasing

Publishing is handled by [`.github/workflows/publish.yml`](.github/workflows/publish.yml),
which runs the test suite and then publishes to npm with
[provenance](https://docs.npmjs.com/generating-provenance-statements).

One-time setup: create an **Automation** access token on npmjs.com and add it to
the repository as the `NPM_TOKEN` secret (Settings → Secrets and variables →
Actions).

To cut a release, bump the version in **`package.json`, `manifest.json`,
`server.json`, and the `McpServer` block in `src/index.ts`** (they are kept in
sync by hand), then:

```bash
git commit -am "Release v0.4.0"
git tag v0.4.0
git push origin main --tags
```

Pushing the tag triggers the workflow. It refuses to publish if the tag doesn't
match `package.json`, or if that version is already on npm. There is also a
manual **Run workflow** button with a `dry_run` option that packs and validates
without publishing.

## Contributing

PRs welcome! Feel free to open issues for bugs or feature requests.

## Credits

A fork of [zenmoney-mcp](https://github.com/artarasov/zenmoney-mcp) by Artem
Tarasov, published to npm as `zenmoney-mcp-ddvin`.

## License

[MIT](LICENSE)
