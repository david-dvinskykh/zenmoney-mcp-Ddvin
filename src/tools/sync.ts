import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { ZenState } from "../state.js";

export function registerSyncTools(server: McpServer, state: ZenState) {
  server.tool(
    "sync_data",
    "Sync data with ZenMoney. Other tools sync automatically, so this is only needed to refresh on demand. Use force_full=true to discard the cache and re-download everything.",
    {
      force_full: z
        .boolean()
        .optional()
        .default(false)
        .describe("Force a full sync instead of incremental"),
    },
    async ({ force_full }) => {
      try {
        if (force_full) await state.clearCache();
        await state.sync(force_full);
        const summary = {
          accounts: state.accounts.length,
          active_accounts: state.getActiveAccounts().length,
          categories: state.tags.length,
          merchants: state.merchants.length,
          transactions: state.transactions.length,
          currencies: state.instruments.length,
          serverTimestamp: state.serverTimestamp,
          cache_file: state.cachePath ?? "disabled",
        };

        return {
          content: [
            {
              type: "text" as const,
              text: `Sync complete. Summary:\n${JSON.stringify(summary, null, 2)}`,
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text" as const,
              text: `Sync failed: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }
  );
}
