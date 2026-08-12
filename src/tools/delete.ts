import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { Deletion, ZenMoneyAPI } from "../api.js";
import type { ZenState } from "../state.js";
import { ensureSynced } from "./ensure-synced.js";
import { formatTransactionLine } from "./format.js";

/** Entity types delete_object accepts, mapped to their diff-protocol names. */
const OBJECT_TYPES = {
  account: "account",
  category: "tag",
  merchant: "merchant",
} as const;

type ObjectType = keyof typeof OBJECT_TYPES;

function textResult(text: string, isError = false) {
  return {
    content: [{ type: "text" as const, text }],
    ...(isError ? { isError: true } : {}),
  };
}

export function registerDeleteTools(
  server: McpServer,
  api: ZenMoneyAPI,
  state: ZenState
) {
  server.tool(
    "delete_transaction",
    "Permanently delete one or more transactions from ZenMoney. Works for every kind of transaction — expenses, income, transfers between accounts, and debts (loans given or taken). Get the ids from list_transactions, which prints one at the end of each row. The first call previews what would be deleted and changes nothing; repeat it with confirm=true to actually delete. Deletion cannot be undone.",
    {
      id: z.string().optional().describe("Transaction UUID to delete"),
      ids: z
        .array(z.string())
        .optional()
        .describe("Several transaction UUIDs to delete at once"),
      confirm: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Set to true to actually delete. When false (the default) the tool only reports what would be deleted."
        ),
    },
    async ({ id, ids, confirm }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const requested = Array.from(
        new Set([...(id ? [id] : []), ...(ids ?? [])])
      );

      if (requested.length === 0) {
        return textResult(
          "Provide 'id' (a single transaction UUID) or 'ids' (several). Use list_transactions to look them up.",
          true
        );
      }

      const targets = [];
      const missing = [];
      for (const txId of requested) {
        const tx = state.transactions.find((t) => t.id === txId && !t.deleted);
        if (tx) targets.push(tx);
        else missing.push(txId);
      }

      if (missing.length > 0) {
        return textResult(
          `Transaction${missing.length > 1 ? "s" : ""} not found: ${missing.join(", ")}.\n` +
            "Nothing was deleted. Use list_transactions to find the right ids (each row ends with one), " +
            "or sync_data if the transaction was added from another client just now.",
          true
        );
      }

      const preview = targets
        .map((t) => `- ${formatTransactionLine(state, t)}`)
        .join("\n");

      if (!confirm) {
        return textResult(
          `About to delete ${targets.length} transaction${targets.length > 1 ? "s" : ""}:\n\n${preview}\n\n` +
            "Nothing has been deleted yet. Call delete_transaction again with confirm=true to delete permanently."
        );
      }

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }

      const now = Math.floor(Date.now() / 1000);
      const deletions: Deletion[] = targets.map((t) => ({
        id: t.id,
        object: "transaction",
        stamp: now,
        user: user.id,
      }));

      try {
        const resp = await api.diff({
          currentClientTimestamp: now,
          serverTimestamp: state.serverTimestamp,
          deletion: deletions,
        });

        await state.applyLocalDeletions(deletions, resp);

        return textResult(
          `Deleted ${targets.length} transaction${targets.length > 1 ? "s" : ""}:\n\n${preview}`
        );
      } catch (error) {
        return textResult(
          `Failed to delete: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );

  server.tool(
    "delete_object",
    "Permanently delete an account, a category, or a merchant from ZenMoney. Deleting an account (including a debt account) also deletes every transaction on it. The first call previews the consequences and changes nothing; repeat it with confirm=true to actually delete. Deletion cannot be undone.",
    {
      type: z
        .enum(["account", "category", "merchant"])
        .describe("What to delete"),
      name_or_id: z
        .string()
        .describe("Name or UUID of the object. Names are matched loosely."),
      confirm: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Set to true to actually delete. When false (the default) the tool only reports what would be deleted."
        ),
    },
    async ({ type, name_or_id, confirm }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const target = resolveObject(state, type as ObjectType, name_or_id);
      if (!target) {
        return textResult(
          `No ${type} matching "${name_or_id}" found. Nothing was deleted. Use ${listToolFor(type as ObjectType)} to see what exists.`,
          true
        );
      }

      const impact = describeImpact(state, type as ObjectType, target.id);

      if (!confirm) {
        return textResult(
          `About to delete ${type} **${target.title}** (id: \`${target.id}\`).\n${impact}\n\n` +
            "Nothing has been deleted yet. Call delete_object again with confirm=true to delete permanently."
        );
      }

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }

      const now = Math.floor(Date.now() / 1000);
      const deletions: Deletion[] = [
        {
          id: target.id,
          object: OBJECT_TYPES[type as ObjectType],
          stamp: now,
          user: user.id,
        },
      ];

      try {
        const resp = await api.diff({
          currentClientTimestamp: now,
          serverTimestamp: state.serverTimestamp,
          deletion: deletions,
        });

        await state.applyLocalDeletions(deletions, resp);

        return textResult(
          `Deleted ${type} **${target.title}** (id: \`${target.id}\`).\n${impact}`
        );
      } catch (error) {
        return textResult(
          `Failed to delete: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );
}

function resolveObject(
  state: ZenState,
  type: ObjectType,
  nameOrId: string
): { id: string; title: string } | undefined {
  switch (type) {
    case "account":
      return (
        state.accounts.find((a) => a.id === nameOrId) ??
        state.findAccountByName(nameOrId)
      );
    case "category":
      return (
        state.tags.find((t) => t.id === nameOrId) ??
        state.findTagByName(nameOrId)
      );
    case "merchant":
      return (
        state.merchants.find((m) => m.id === nameOrId) ??
        state.findMerchantByName(nameOrId)
      );
  }
}

function listToolFor(type: ObjectType): string {
  return type === "account"
    ? "list_accounts"
    : type === "category"
      ? "list_categories"
      : "list_merchants";
}

/** Spell out what else disappears, so the confirmation is an informed one. */
function describeImpact(
  state: ZenState,
  type: ObjectType,
  id: string
): string {
  switch (type) {
    case "account": {
      const affected = state.transactions.filter(
        (t) => t.incomeAccount === id || t.outcomeAccount === id
      ).length;
      return affected > 0
        ? `Its ${affected} transaction${affected > 1 ? "s" : ""} will be deleted with it.`
        : "It has no transactions.";
    }
    case "category": {
      const children = state.tags.filter((t) => t.parent === id).length;
      const tagged = state.transactions.filter((t) =>
        t.tag?.includes(id)
      ).length;
      const parts: string[] = [];
      if (children > 0)
        parts.push(
          `${children} subcategor${children > 1 ? "ies" : "y"} will be deleted with it`
        );
      parts.push(
        tagged > 0
          ? `${tagged} transaction${tagged > 1 ? "s" : ""} will become uncategorized (the transactions themselves stay)`
          : "no transactions use it"
      );
      return `${parts.join("; ")}.`;
    }
    case "merchant": {
      const used = state.transactions.filter((t) => t.merchant === id).length;
      return used > 0
        ? `${used} transaction${used > 1 ? "s" : ""} will lose this merchant (the transactions themselves stay).`
        : "No transactions use it.";
    }
  }
}
