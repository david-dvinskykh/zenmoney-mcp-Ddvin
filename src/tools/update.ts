import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { Transaction, ZenMoneyAPI } from "../api.js";
import type { ZenState } from "../state.js";
import { ensureSynced } from "./ensure-synced.js";
import { formatTransactionLine } from "./format.js";
import {
  resolveAccount,
  resolveMerchantByTitle,
  resolveTag,
  tagTitles,
  textResult,
} from "./resolve.js";

/** Fields whose presence means the caller actually asked for a change. */
const EDITABLE = [
  "date",
  "amount",
  "outcome_amount",
  "income_amount",
  "account",
  "from_account",
  "to_account",
  "category",
  "payee",
  "comment",
] as const;

export function registerUpdateTools(
  server: McpServer,
  api: ZenMoneyAPI,
  state: ZenState
) {
  server.tool(
    "update_transaction",
    "Edit an existing transaction in ZenMoney — its date, amount, account, category, payee, or " +
      "comment. Works for every kind of transaction: expenses, income, transfers between accounts, " +
      "and debts. Get the id from list_transactions, which prints one at the end of each row. Only " +
      "the fields you pass are changed; pass an empty string to category, payee, or comment to clear " +
      "it. The first call previews the before/after and changes nothing; repeat it with confirm=true " +
      "to save. Editing overwrites the old values and cannot be undone.",
    {
      id: z.string().describe("UUID of the transaction to edit"),
      date: z
        .string()
        .regex(/^\d{4}-\d{2}-\d{2}$/, "Date must be in YYYY-MM-DD format")
        .optional()
        .describe("New date in YYYY-MM-DD format"),
      amount: z
        .number()
        .positive()
        .optional()
        .describe(
          "New amount. For an expense or income it replaces the single amount; for a same-currency " +
            "transfer or debt it replaces both sides. Cross-currency transfers need outcome_amount/income_amount."
        ),
      outcome_amount: z
        .number()
        .positive()
        .optional()
        .describe("New amount leaving the source account (in the source account's currency)"),
      income_amount: z
        .number()
        .positive()
        .optional()
        .describe("New amount arriving on the destination account (in the destination account's currency)"),
      account: z
        .string()
        .optional()
        .describe("Move an expense or income to a different account (name or UUID)"),
      from_account: z
        .string()
        .optional()
        .describe("New source account of a transfer or debt (name or UUID)"),
      to_account: z
        .string()
        .optional()
        .describe("New destination account of a transfer or debt (name or UUID)"),
      category: z
        .string()
        .optional()
        .describe("New category name or UUID. Empty string removes the category."),
      payee: z
        .string()
        .optional()
        .describe("New payee/counterparty name. Empty string removes it."),
      comment: z
        .string()
        .optional()
        .describe("New comment. Empty string removes it."),
      confirm: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Set to true to actually save the edit. When false (the default) the tool only reports what would change."
        ),
    },
    async (args) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const touched = EDITABLE.filter(
        (field) => (args as Record<string, unknown>)[field] !== undefined
      );
      if (touched.length === 0) {
        return textResult(
          `Nothing to update. Pass at least one of: ${EDITABLE.join(", ")}.`,
          true
        );
      }

      const original = state.transactions.find(
        (t) => t.id === args.id && !t.deleted
      );
      if (!original) {
        return textResult(
          `Transaction ${args.id} not found. Use list_transactions to find the right id ` +
            "(each row ends with one), or sync_data if it was added from another client just now.",
          true
        );
      }

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }

      const updated: Transaction = { ...original };
      const wasTwoSided = original.incomeAccount !== original.outcomeAccount;

      if (args.date !== undefined) updated.date = args.date;

      // --- accounts -------------------------------------------------------
      if (args.account !== undefined) {
        if (wasTwoSided) {
          return textResult(
            "This transaction moves money between two accounts, so 'account' is ambiguous. " +
              "Use from_account and/or to_account instead.",
            true
          );
        }
        const acc = resolveAccount(state, args.account);
        if (!acc) {
          return textResult(
            `Account "${args.account}" not found. Use list_accounts to see available accounts.`,
            true
          );
        }
        updated.outcomeAccount = acc.id;
        updated.incomeAccount = acc.id;
        updated.outcomeInstrument = acc.instrument ?? user.currency ?? 1;
        updated.incomeInstrument = updated.outcomeInstrument;
      }

      if (args.from_account !== undefined) {
        const acc = resolveAccount(state, args.from_account);
        if (!acc) {
          return textResult(
            `Source account "${args.from_account}" not found. Use list_accounts to see available accounts.`,
            true
          );
        }
        updated.outcomeAccount = acc.id;
        updated.outcomeInstrument = acc.instrument ?? user.currency ?? 1;
      }

      if (args.to_account !== undefined) {
        const acc = resolveAccount(state, args.to_account);
        if (!acc) {
          return textResult(
            `Destination account "${args.to_account}" not found. Use list_accounts to see available accounts.`,
            true
          );
        }
        updated.incomeAccount = acc.id;
        updated.incomeInstrument = acc.instrument ?? user.currency ?? 1;
      }

      normalizeDebtInstruments(state, updated, user.currency);

      // --- amounts --------------------------------------------------------
      const twoSided = updated.incomeAccount !== updated.outcomeAccount;

      if (args.amount !== undefined) {
        if (twoSided) {
          if (updated.incomeInstrument !== updated.outcomeInstrument) {
            const from = state.getInstrument(updated.outcomeInstrument);
            const to = state.getInstrument(updated.incomeInstrument);
            return textResult(
              `This is a cross-currency transaction (${from?.shortTitle ?? "?"} → ${to?.shortTitle ?? "?"}), ` +
                "so a single 'amount' is ambiguous. Pass outcome_amount and income_amount instead.",
              true
            );
          }
          updated.outcome = args.amount;
          updated.income = args.amount;
        } else if (original.income > 0 && original.outcome === 0) {
          updated.income = args.amount;
          updated.outcome = 0;
        } else {
          updated.outcome = args.amount;
          updated.income = 0;
        }
      }

      if (args.outcome_amount !== undefined) updated.outcome = args.outcome_amount;
      if (args.income_amount !== undefined) updated.income = args.income_amount;

      const amountError = validateAmounts(updated, twoSided);
      if (amountError) return textResult(amountError, true);

      // --- category, payee, comment ---------------------------------------
      if (args.category !== undefined) {
        if (args.category.trim() === "") {
          updated.tag = null;
        } else {
          const tagIds = resolveTag(state, args.category);
          if (!tagIds) {
            return textResult(
              `Category "${args.category}" not found. Use list_categories to see available categories.`,
              true
            );
          }
          updated.tag = tagIds;
        }
      }

      if (args.payee !== undefined) {
        const payee = args.payee.trim() === "" ? null : args.payee;
        updated.payee = payee;
        // The merchant is the normalized counterparty and wins over `payee` in
        // ZenMoney's UI, so it has to follow the new name or be dropped.
        updated.merchant = payee
          ? (resolveMerchantByTitle(state, payee)?.id ?? null)
          : null;
      }

      if (args.comment !== undefined) {
        updated.comment = args.comment.trim() === "" ? null : args.comment;
      }

      const changes = describeChanges(state, original, updated);
      if (changes.length === 0) {
        return textResult(
          `Transaction \`${original.id}\` already has these values — nothing to change.\n\n` +
            formatTransactionLine(state, original)
        );
      }

      const diff =
        `Before: ${formatTransactionLine(state, original)}\n` +
        `After:  ${formatTransactionLine(state, updated)}\n\n` +
        `Changes:\n${changes.map((c) => `- ${c}`).join("\n")}`;

      if (!args.confirm) {
        return textResult(
          `About to update transaction \`${original.id}\`:\n\n${diff}\n\n` +
            "Nothing has been saved yet. Call update_transaction again with confirm=true to apply."
        );
      }

      const now = Math.floor(Date.now() / 1000);
      updated.changed = now;

      try {
        const resp = await api.diff({
          currentClientTimestamp: now,
          serverTimestamp: state.serverTimestamp,
          transaction: [updated],
        });

        await state.applyLocalTransaction(updated, resp);

        return textResult(`Transaction updated:\n\n${diff}`);
      } catch (error) {
        return textResult(
          `Failed to update transaction: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );
}

/**
 * Keep the ZenMoney invariant that a debt transaction carries the *non-debt*
 * account's currency on both legs — the debt account's own instrument is always
 * the user's currency and never appears on the transaction.
 */
function normalizeDebtInstruments(
  state: ZenState,
  t: Transaction,
  userCurrency: number
): void {
  const from = state.accounts.find((a) => a.id === t.outcomeAccount);
  const to = state.accounts.find((a) => a.id === t.incomeAccount);
  const debtSides = [from, to].filter((a) => a?.type === "debt").length;
  if (debtSides !== 1) return;

  const nonDebt = from?.type === "debt" ? to : from;
  if (!nonDebt) return;

  const instrument = nonDebt.instrument ?? userCurrency ?? 1;
  t.outcomeInstrument = instrument;
  t.incomeInstrument = instrument;
}

function validateAmounts(t: Transaction, twoSided: boolean): string | null {
  if (twoSided) {
    if (t.outcome <= 0 || t.income <= 0) {
      return (
        "A transfer or debt needs a positive amount on both sides. " +
        "Pass outcome_amount and income_amount (or 'amount' when both use the same currency)."
      );
    }
    return null;
  }
  if (t.outcome > 0 && t.income > 0) {
    return (
      "A single-account transaction is either an expense or an income, not both. " +
      "Pass 'amount', or set from_account/to_account to turn it into a transfer."
    );
  }
  if (t.outcome === 0 && t.income === 0) {
    return "The transaction would end up with no amount. Pass a positive 'amount'.";
  }
  return null;
}

/** Human-readable list of what actually differs, used by preview and result. */
function describeChanges(
  state: ZenState,
  before: Transaction,
  after: Transaction
): string[] {
  const changes: string[] = [];
  const accountName = (id: string) =>
    state.accounts.find((a) => a.id === id)?.title ?? id;
  const currency = (id: number) => state.getInstrument(id)?.shortTitle ?? "";

  if (before.date !== after.date) {
    changes.push(`date: ${before.date} → ${after.date}`);
  }
  if (before.outcomeAccount !== after.outcomeAccount) {
    const label = before.incomeAccount === before.outcomeAccount ? "account" : "from account";
    changes.push(
      `${label}: ${accountName(before.outcomeAccount)} → ${accountName(after.outcomeAccount)}`
    );
  }
  if (before.incomeAccount !== after.incomeAccount) {
    // A one-sided edit moves both legs at once; report that as a single change.
    const alreadyReported =
      before.incomeAccount === before.outcomeAccount &&
      after.incomeAccount === after.outcomeAccount;
    if (!alreadyReported) {
      changes.push(
        `to account: ${accountName(before.incomeAccount)} → ${accountName(after.incomeAccount)}`
      );
    }
  }
  if (
    before.outcome !== after.outcome ||
    before.outcomeInstrument !== after.outcomeInstrument
  ) {
    changes.push(
      `outcome: ${before.outcome} ${currency(before.outcomeInstrument)} → ${after.outcome} ${currency(after.outcomeInstrument)}`
    );
  }
  if (
    before.income !== after.income ||
    before.incomeInstrument !== after.incomeInstrument
  ) {
    changes.push(
      `income: ${before.income} ${currency(before.incomeInstrument)} → ${after.income} ${currency(after.incomeInstrument)}`
    );
  }
  const beforeTags = tagTitles(state, before.tag);
  const afterTags = tagTitles(state, after.tag);
  if (beforeTags !== afterTags) {
    changes.push(`category: ${beforeTags} → ${afterTags}`);
  }
  if (before.payee !== after.payee) {
    changes.push(`payee: ${before.payee ?? "(none)"} → ${after.payee ?? "(none)"}`);
  }
  if (before.merchant !== after.merchant) {
    const name = (id: string | null) =>
      id ? (state.merchants.find((m) => m.id === id)?.title ?? id) : "(none)";
    changes.push(`merchant: ${name(before.merchant)} → ${name(after.merchant)}`);
  }
  if (before.comment !== after.comment) {
    changes.push(`comment: ${before.comment ?? "(none)"} → ${after.comment ?? "(none)"}`);
  }
  return changes;
}
