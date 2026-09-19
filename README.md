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
| `add_debt` | Record a debt — money lent, borrowed, or repaid |
| `update_transaction` | Edit an existing transaction (date, amount, account, category, payee, comment) |
| `delete_transaction` | Delete transactions — expenses, income, transfers, debts |
| `delete_object` | Delete an account, a category, or a merchant |
| `list_reminders` | List planned transactions — recurring and one-off |
| `add_reminder` | Plan a transaction ahead — one-off or a repeating series |
| `add_reminder_marker` | Add one more planned occurrence to an existing series |
| `delete_reminder` | Delete a planned transaction and its future occurrences |
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
5. **Lend and borrow** — "I lent Masha 500 RUB in cash today", "Masha paid me back 200"
6. **Edit** — "That coffee was 350, not 500", "Move yesterday's lunch to the Restaurants category"
7. **Delete** — "Delete yesterday's duplicate coffee expense", "Remove that transfer to Savings"
8. **Plan** — "What payments are coming up?", "Cancel the gym reminder"
9. **Refresh** — "Sync my ZenMoney data" (only needed to pull changes mid-conversation)

## Debts

A debt in ZenMoney is an ordinary transaction with the system **debt account** on
one side, so `add_debt` books both legs for you. `direction` says which way the
money moved and what it means:

| `direction` | Money | Meaning |
|-------------|-------|---------|
| `lend` | leaves your account | they owe you |
| `borrow` | arrives on your account | you owe them |
| `repay_received` | arrives on your account | their debt to you goes down |
| `repay_sent` | leaves your account | your debt to them goes down |

```
"I lent Masha 500 RUB from my Cash account on 2026-03-20"
→ add_debt(direction="lend", account="Cash", amount=500, payee="Masha", date="2026-03-20")
```

`payee` is the counterparty. Reuse the exact same name for a loan and its
repayments — that is how ZenMoney keeps one running balance per person. If a
merchant with that exact title already exists, it is linked automatically.

The amount is always in the currency of *your* account: the debt account's own
currency is your main currency and never appears on the transaction, which is
what the ZenMoney diff protocol expects.

ZenMoney creates the debt account itself the first time a debt is recorded. If
you have never had one, `add_debt` says so — add a single debt in the ZenMoney
app (or [Zerro](https://zerro.app)), run `sync_data`, and it will work from then on.

## Editing transactions

`update_transaction` edits any existing transaction — an expense, an income, a
transfer, or a debt. It takes the id `list_transactions` prints at the end of
each row, and only the fields you pass are changed:

| Field | Applies to |
|-------|-----------|
| `date` | everything |
| `amount` | expenses, income, and same-currency transfers/debts (replaces both legs) |
| `outcome_amount` / `income_amount` | transfers and debts, including cross-currency |
| `account` | expenses and income |
| `from_account` / `to_account` | transfers and debts |
| `category`, `payee`, `comment` | everything — pass an empty string to clear |

Like the delete tools it is two-step: the first call previews the before/after
and changes nothing.

```
About to update transaction `a1b2…`:

Before: 2026-03-20 | expense  | -50 USD    | Food        | Corner Cafe — "lunch" | id: `a1b2…`
After:  2026-03-22 | expense  | -60 USD    | Restaurants | Corner Cafe — "lunch" | id: `a1b2…`

Changes:
- date: 2026-03-20 → 2026-03-22
- outcome: 50 USD → 60 USD
- category: Food → Restaurants

Nothing has been saved yet. Call update_transaction again with confirm=true to apply.
```

Repeat with `confirm: true` to save. Changing the payee also updates the linked
merchant (ZenMoney shows the merchant in preference to the raw payee), so it is
re-matched by name or cleared. Editing overwrites the old values — there is no
undo.

## Planning ahead

`add_reminder` creates a planned transaction. Leave `interval` out and it is a
one-off dated entry; pass `interval` (`day`, `week`, `month`, `year`) and it
repeats. `step` says how many intervals apart the repeats are, so
`interval: "week", step: 2` is fortnightly:

```
add_reminder({
  type: "expense", account: "Checking", amount: 1200,
  start_date: "2026-04-01", interval: "month", comment: "Rent"
})

Reminder added:

- 2026-04-01 | expense  | -1200 PLN | Home | Landlord — "Rent" | every month | id: `f1e2…`

Planned: 2026-04-01, 2026-05-01, 2026-06-01 (+9 more).
```

`type` picks the shape of the operation: `expense` and `income` take `account`,
`transfer` takes `from_account` and `to_account` (and both amounts when the two
accounts hold different currencies, as `add_transfer` does).

ZenMoney expands a series into dated occurrences — reminder *markers* — on its
own side, and returns them with the write, which is where the "Planned:" line
comes from. `points` is the advanced knob for a series that fires more than once
per window: positions inside the step window, counted in `interval` units from
`start_date` and zero-based, so `interval: "day", step: 7, points: [0, 2, 4]`
repeats weekly on the start weekday plus two and four days later. It defaults to
`[0]` — once per window.

`add_reminder_marker` adds a single occurrence to a series that already exists:
an extra rent month, a one-off top-up. It copies the reminder's accounts,
amount, category, payee and comment unless you override them, and refuses to
plan a day the series is already planned for.

## Deleting data

`delete_transaction` removes transactions of any kind — expenses, income,
transfers between accounts, and debts (a debt in ZenMoney is an ordinary
transaction with the debt account on one side). It takes the ids that
`list_transactions` prints at the end of each row, one via `id` or several via
`ids`.

`delete_object` removes an account, a category, or a merchant. Deleting an
account also deletes every transaction booked on it; deleting a category keeps
the transactions and leaves them uncategorized.

`delete_reminder` removes a planned transaction. For a recurring series that
means the series itself and every occurrence still planned; transactions
already created from past occurrences stay. It takes an id from
`list_reminders`, or text matched against the reminder's payee, comment,
merchant and category — when that text matches more than one reminder the tool
lists the candidates and deletes nothing.

All three are two-step. The first call reports exactly what would go — including the
knock-on effects — and changes nothing:

```
About to delete 1 transaction:

- 2026-03-20 | expense  | -50 USD  | Food | Grocery Store | id: `a1b2…`

Nothing has been deleted yet. Call delete_transaction again with confirm=true
to delete permanently.
```

Repeat the call with `confirm: true` to go through with it. ZenMoney deletions
are permanent — there is no undo, so the preview is the last checkpoint.

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
git commit -am "Release v0.5.0"
git tag v0.5.0
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
