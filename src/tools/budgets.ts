import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { Budget, ZenMoneyAPI } from "../api.js";
import { budgetKey, type ZenState } from "../state.js";
import { ensureSynced } from "./ensure-synced.js";
import { resolveTag, textResult } from "./resolve.js";

const MONTH_RE = /^\d{4}-(0[1-9]|1[0-2])$/;

/** The current month as YYYY-MM, in UTC like the tools' default dates. */
function currentMonth(): string {
  return new Date().toISOString().slice(0, 7);
}

/** A budget with nothing in it — what ZenMoney leaves after a removal. */
function isEmpty(b: Budget): boolean {
  return b.income === 0 && b.outcome === 0 && !b.incomeLock && !b.outcomeLock;
}

/** One side of a budget: "none", or the amount with a note when unlocked. */
function sideText(amount: number, locked: boolean, currency: string): string {
  if (amount === 0 && !locked) return "none";
  return `${amount} ${currency}${locked ? "" : " (suggested)"}`;
}

export function registerBudgetTools(
  server: McpServer,
  api: ZenMoneyAPI,
  state: ZenState
) {
  server.tool(
    "list_budgets",
    "List the monthly category budgets in ZenMoney — the planned expense and income per category " +
      "for one month, in the user's main currency. An amount marked (suggested) is the app's own " +
      "forecast rather than one set by hand. Syncs automatically if needed.",
    {
      month: z
        .string()
        .regex(MONTH_RE, "Month must be in YYYY-MM format")
        .optional()
        .describe("Month in YYYY-MM format (default: current month)"),
    },
    async ({ month }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const target = month ?? currentMonth();
      const date = `${target}-01`;
      const currency =
        state.getInstrument(state.getUser()?.currency ?? 0)?.shortTitle ?? "";

      const rows = state.budgets
        .filter((b) => b.date === date && !isEmpty(b))
        .map((b) => ({
          budget: b,
          title: b.tag
            ? (state.tags.find((t) => t.id === b.tag)?.title ?? b.tag)
            : "(whole month)",
        }))
        .sort((a, b) => {
          // The month-wide row leads, categories follow by title. A plain
          // code-point order, not localeCompare, so the Go port sorts the same.
          if (!a.budget.tag !== !b.budget.tag) return a.budget.tag ? 1 : -1;
          return a.title < b.title ? -1 : a.title > b.title ? 1 : 0;
        });

      if (rows.length === 0) {
        return textResult(`No budgets for ${target}.`);
      }

      const lines = rows.map(({ budget: b, title }) => {
        const id = b.tag ? ` — id: \`${b.tag}\`` : "";
        return (
          `- **${title}**: expense ${sideText(b.outcome, b.outcomeLock, currency)}, ` +
          `income ${sideText(b.income, b.incomeLock, currency)}${id}`
        );
      });

      return textResult(
        `Budgets for ${target} (${rows.length}):\n\n${lines.join("\n")}`
      );
    }
  );

  server.tool(
    "set_budget",
    "Set or change the monthly budget of a category in ZenMoney — the planned expense and/or income " +
      "for one month, in the user's main currency. Only the side you pass is changed; pass 0 to " +
      "remove it. The first call previews the before/after and changes nothing; repeat it with " +
      "confirm=true to save. Saving overwrites the old amount.",
    {
      month: z
        .string()
        .regex(MONTH_RE, "Month must be in YYYY-MM format")
        .describe("Month in YYYY-MM format"),
      category: z.string().describe("Category name or UUID"),
      outcome: z
        .number()
        .nonnegative()
        .optional()
        .describe("Planned expense for the month. 0 removes it."),
      income: z
        .number()
        .nonnegative()
        .optional()
        .describe("Planned income for the month. 0 removes it."),
      confirm: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Set to true to actually save the budget. When false (the default) the tool only reports what would change."
        ),
    },
    async ({ month, category, outcome, income, confirm }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      if (outcome === undefined && income === undefined) {
        return textResult(
          "Nothing to set. Pass outcome (planned expense) and/or income (planned income).",
          true
        );
      }

      const tagIds = resolveTag(state, category);
      if (!tagIds) {
        return textResult(
          `Category "${category}" not found. Use list_categories to see available categories.`,
          true
        );
      }
      const tag = state.tags.find((t) => t.id === tagIds[0])!;

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }
      const currency = state.getInstrument(user.currency)?.shortTitle ?? "";

      const date = `${month}-01`;
      const key = budgetKey({ tag: tag.id, date });
      const original: Budget = state.budgets.find(
        (b) => budgetKey(b) === key
      ) ?? {
        user: user.id,
        changed: 0,
        tag: tag.id,
        date,
        income: 0,
        outcome: 0,
        incomeLock: false,
        outcomeLock: false,
      };

      const now = Math.floor(Date.now() / 1000);
      const updated: Budget = { ...original, user: user.id, changed: now };
      // A hand-set amount is locked so the app's forecast does not replace
      // it; a removal unlocks, which is how ZenMoney itself clears a budget.
      if (outcome !== undefined) {
        updated.outcome = outcome;
        updated.outcomeLock = outcome !== 0;
      }
      if (income !== undefined) {
        updated.income = income;
        updated.incomeLock = income !== 0;
      }

      const changes: string[] = [];
      if (outcome !== undefined) {
        changes.push(
          `- expense: ${sideText(original.outcome, original.outcomeLock, currency)} → ` +
            sideText(updated.outcome, updated.outcomeLock, currency)
        );
      }
      if (income !== undefined) {
        changes.push(
          `- income: ${sideText(original.income, original.incomeLock, currency)} → ` +
            sideText(updated.income, updated.incomeLock, currency)
        );
      }
      const summary = `Budget for **${tag.title}**, ${month}:\n${changes.join("\n")}`;

      if (!confirm) {
        return textResult(
          `${summary}\n\nNothing has been saved yet. Call set_budget again with confirm=true to save.`
        );
      }

      try {
        const resp = await api.diff({
          currentClientTimestamp: now,
          serverTimestamp: state.serverTimestamp,
          budget: [updated],
        });

        await state.applyLocalBudget(updated, resp);

        return textResult(`Saved. ${summary}`);
      } catch (error) {
        return textResult(
          `Failed to save budget: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );
}
