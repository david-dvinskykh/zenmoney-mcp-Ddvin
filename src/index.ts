#!/usr/bin/env node
import "dotenv/config";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { ZenMoneyAPI } from "./api.js";
import { StateCache } from "./cache.js";
import { ZenState } from "./state.js";
import { registerSyncTools } from "./tools/sync.js";
import { registerAccountTools } from "./tools/accounts.js";
import { registerCategoryTools } from "./tools/categories.js";
import { registerTransactionTools } from "./tools/transactions.js";
import { registerSuggestTools } from "./tools/suggest.js";

const token = process.env.ZENMONEY_TOKEN;
if (!token) {
  console.error(
    "ZENMONEY_TOKEN environment variable is required.\n" +
      "Get your token from https://zerro.app/token and set it in .env"
  );
  process.exit(1);
}

const api = new ZenMoneyAPI(token);
// Snapshots are stored per token hash so data survives between stdio
// processes (and separate accounts never share a file).
const cache =
  process.env.ZENMONEY_NO_CACHE === "1" ? null : new StateCache(token);
const state = new ZenState(api, cache);

const server = new McpServer({
  name: "zenmoney-mcp",
  version: "0.3.1",
});

registerSyncTools(server, state);
registerAccountTools(server, state);
registerCategoryTools(server, state);
registerTransactionTools(server, api, state);
registerSuggestTools(server, api, state);

async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("ZenMoney MCP server running on stdio");
  if (cache) console.error(`Cache file: ${cache.path}`);

  // Warm up in the background so the first tool call doesn't pay for the
  // sync. Tool calls await this same attempt via ensureSynced().
  state
    .ensureSynced()
    .then(() => {
      console.error(
        `Initial sync done (${state.transactions.length} transactions${state.isFromCache ? ", restored from cache" : ""})`
      );
    })
    .catch((error) => {
      console.error(
        `Initial sync failed, will retry on first tool call: ${error instanceof Error ? error.message : String(error)}`
      );
    });
}

main().catch((error) => {
  console.error("Fatal error:", error);
  process.exit(1);
});
