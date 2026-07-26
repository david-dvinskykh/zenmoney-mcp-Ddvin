import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { ZenState } from "../state.js";
import { ensureSynced } from "./ensure-synced.js";

export function registerCategoryTools(server: McpServer, state: ZenState) {
  server.tool(
    "list_categories",
    "List all expense/income categories (tags) with their hierarchy. Syncs automatically if needed.",
    {},
    async () => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const hierarchy = state.getTagHierarchy();
      const lines: string[] = [];

      for (const { parent, children } of hierarchy) {
        const flags: string[] = [];
        if (parent.showOutcome) flags.push("expense");
        if (parent.showIncome) flags.push("income");
        lines.push(
          `- **${parent.title}** (${flags.join(", ")}) — id: \`${parent.id}\``
        );
        for (const child of children) {
          lines.push(`  - ${child.title} — id: \`${child.id}\``);
        }
      }

      return {
        content: [
          {
            type: "text" as const,
            text:
              lines.length > 0
                ? `Categories (${state.tags.length}):\n\n${lines.join("\n")}`
                : "No categories found.",
          },
        ],
      };
    }
  );

  server.tool(
    "list_merchants",
    "List known merchants/payees. Syncs automatically if needed.",
    {},
    async () => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const lines = state.merchants.map(
        (m) => `- **${m.title}** — id: \`${m.id}\``
      );

      return {
        content: [
          {
            type: "text" as const,
            text:
              lines.length > 0
                ? `Merchants (${lines.length}):\n\n${lines.join("\n")}`
                : "No merchants found.",
          },
        ],
      };
    }
  );
}
