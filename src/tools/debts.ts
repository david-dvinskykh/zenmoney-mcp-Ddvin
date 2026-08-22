import { z } from "zod";
import { randomUUID } from "node:crypto";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { ZenMoneyAPI } from "../api.js";
import type { ZenState } from "../state.js";
import { ensureSynced } from "./ensure-synced.js";
import {
  findDebtAccount,
  resolveAccount,
  resolveMerchantByTitle,
  resolveTag,
  tagTitles,
  textResult,
} from "./resolve.js";

/**
 * A debt in ZenMoney is an ordinary transaction with the system "debt" account
 * on one side, so there are only two mechanical shapes:
 *
 *   money leaves my account  → I am owed more / I owe less   (lend, repay_sent)
 *   money enters my account  → I am owed less / I owe more   (borrow, repay_received)
 *
 * The four directions map onto those two, and differ only in what the user
 * means by them — ZenMoney tracks the running balance per counterparty, not the
 * intent of each leg.
 */
const DIRECTIONS = {
  lend: {
    outgoing: true,
    label: "Lent",
    summary: (payee: string) => `${payee} now owes you this amount`,
  },
  borrow: {
    outgoing: false,
    label: "Borrowed",
    summary: (payee: string) => `you now owe ${payee} this amount`,
  },
  repay_received: {
    outgoing: false,
    label: "Repayment received",
    summary: (payee: string) => `${payee}'s debt to you goes down by this amount`,
  },
  repay_sent: {
    outgoing: true,
    label: "Repayment sent",
    summary: (payee: string) => `your debt to ${payee} goes down by this amount`,
  },
} as const;

type Direction = keyof typeof DIRECTIONS;

export function registerDebtTools(
  server: McpServer,
  api: ZenMoneyAPI,
  state: ZenState
) {
  server.tool(
    "add_debt",
    "Record a debt in ZenMoney — money you lent to someone, money you borrowed, " +
      "or a repayment in either direction. A debt is booked between one of your " +
      "accounts and ZenMoney's system debt account; the counterparty is the payee, " +
      "so use the same payee name for a loan and its repayments to keep one running " +
      "balance per person. The amount is always in the currency of your own account.",
    {
      direction: z
        .enum(["lend", "borrow", "repay_received", "repay_sent"])
        .describe(
          "lend = you gave money and are now owed it; borrow = you took money and now owe it; " +
            "repay_received = someone paid you back; repay_sent = you paid someone back"
        ),
      account: z
        .string()
        .describe(
          "Your own account (name or UUID) the money leaves from or arrives on — not the debt account"
        ),
      amount: z
        .number()
        .positive()
        .describe("Amount, in the currency of your own account"),
      payee: z
        .string()
        .min(1)
        .describe("Who the debt is with. Reuse the exact name across a loan and its repayments."),
      date: z
        .string()
        .regex(/^\d{4}-\d{2}-\d{2}$/, "Date must be in YYYY-MM-DD format")
        .describe("Transaction date in YYYY-MM-DD format"),
      category: z.string().optional().describe("Category name or UUID (debts are usually uncategorized)"),
      comment: z.string().optional().describe("Transaction comment"),
      debt_account: z
        .string()
        .optional()
        .describe(
          "The debt account to book against (name or UUID). Only needed if auto-detection picks the wrong one."
        ),
    },
    async ({ direction, account, amount, payee, date, category, comment, debt_account }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }

      const acc = resolveAccount(state, account);
      if (!acc) {
        return textResult(
          `Account "${account}" not found. Use list_accounts to see available accounts.`,
          true
        );
      }
      if (acc.type === "debt") {
        return textResult(
          `"${acc.title}" is the debt account itself. Pass the account of yours the money moves to or from — ` +
            "add_debt books the debt side for you.",
          true
        );
      }

      const debtAcc = debt_account
        ? resolveAccount(state, debt_account)
        : findDebtAccount(state);
      if (!debtAcc) {
        return textResult(
          "No debt account found. ZenMoney creates its system debt account the first time a debt is " +
            "recorded — add one debt in the ZenMoney app (or Zerro), then run sync_data and try again.",
          true
        );
      }
      if (debtAcc.type !== "debt") {
        return textResult(
          `"${debtAcc.title}" is not a debt account (its type is "${debtAcc.type}"). ` +
            "Use add_transfer to move money between two ordinary accounts.",
          true
        );
      }

      let tagIds: string[] | null = null;
      if (category) {
        tagIds = resolveTag(state, category);
        if (!tagIds) {
          return textResult(
            `Category "${category}" not found. Use list_categories to see available categories.`,
            true
          );
        }
      }

      // Both legs carry the non-debt account's currency: the debt account's own
      // instrument is user.currency and is overridden here, per the ZenMoney
      // diff protocol.
      const instrument = acc.instrument ?? user.currency ?? 1;
      const spec = DIRECTIONS[direction as Direction];
      const now = Math.floor(Date.now() / 1000);

      const transaction = {
        id: randomUUID(),
        changed: now,
        created: now,
        user: user.id,
        deleted: false,
        hold: null,
        viewed: false,
        incomeInstrument: instrument,
        incomeAccount: spec.outgoing ? debtAcc.id : acc.id,
        income: amount,
        incomeBankID: null as string | null,
        outcomeInstrument: instrument,
        outcomeAccount: spec.outgoing ? acc.id : debtAcc.id,
        outcome: amount,
        outcomeBankID: null as string | null,
        opIncome: null as number | null,
        opIncomeInstrument: null as number | null,
        opOutcome: null as number | null,
        opOutcomeInstrument: null as number | null,
        tag: tagIds,
        merchant: resolveMerchantByTitle(state, payee)?.id ?? null,
        payee,
        originalPayee: null,
        comment: comment ?? null,
        date,
        mcc: null,
        latitude: null,
        longitude: null,
        reminderMarker: null,
        qrCode: null,
      };

      try {
        const resp = await api.diff({
          currentClientTimestamp: now,
          serverTimestamp: state.serverTimestamp,
          transaction: [transaction],
        });

        await state.applyLocalTransaction(transaction, resp);

        const currency = state.getInstrument(instrument)?.shortTitle ?? "";
        const route = spec.outgoing
          ? `${acc.title} → ${debtAcc.title}`
          : `${debtAcc.title} → ${acc.title}`;

        return textResult(
          `${spec.label}:\n` +
            `- Amount: ${amount} ${currency} — ${spec.summary(payee)}\n` +
            `- Counterparty: ${payee}\n` +
            `- Booked: ${route}\n` +
            `- Date: ${date}\n` +
            `- Category: ${tagTitles(state, tagIds)}` +
            (comment ? `\n- Comment: ${comment}` : "") +
            `\n- ID: ${transaction.id}`
        );
      } catch (error) {
        return textResult(
          `Failed to add debt: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );
}
