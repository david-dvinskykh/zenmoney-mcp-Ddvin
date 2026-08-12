import type { Transaction } from "../api.js";
import type { ZenState } from "../state.js";

export type TransactionKind =
  | "expense"
  | "income"
  | "transfer"
  | "debt"
  | "other";

export interface TransactionSummary {
  kind: TransactionKind;
  /** Amount with currency, plus account names for two-sided operations. */
  amount: string;
  categories: string;
  payee: string;
  comment: string;
}

/**
 * Classify a transaction and render its amount the way ZenMoney shows it.
 *
 * A debt (loan given or taken) is an ordinary transaction with the user's
 * "debt" account on one side, so it is detected from the account types rather
 * than from a field of its own.
 */
export function summarizeTransaction(
  state: ZenState,
  t: Transaction
): TransactionSummary {
  const from = state.accounts.find((a) => a.id === t.outcomeAccount);
  const to = state.accounts.find((a) => a.id === t.incomeAccount);

  const isTwoSided = t.incomeAccount !== t.outcomeAccount;
  const touchesDebt = from?.type === "debt" || to?.type === "debt";

  let kind: TransactionKind = "other";
  let amount = "";

  if (isTwoSided) {
    kind = touchesDebt ? "debt" : "transfer";
    const route = `${from?.title ?? "?"} → ${to?.title ?? "?"}`;
    if (t.outcomeInstrument !== t.incomeInstrument) {
      const fromInstr = state.getInstrument(t.outcomeInstrument);
      const toInstr = state.getInstrument(t.incomeInstrument);
      amount = `${t.outcome} ${fromInstr?.shortTitle ?? ""} → ${t.income} ${toInstr?.shortTitle ?? ""} (${route})`;
    } else {
      amount = `${t.outcome} (${route})`;
    }
  } else if (t.outcome > 0 && t.income === 0) {
    const instr = state.getInstrument(t.outcomeInstrument);
    kind = "expense";
    amount = `-${t.outcome} ${instr?.shortTitle ?? ""}`;
  } else if (t.income > 0 && t.outcome === 0) {
    const instr = state.getInstrument(t.incomeInstrument);
    kind = "income";
    amount = `+${t.income} ${instr?.shortTitle ?? ""}`;
  }

  const categories = t.tag
    ? t.tag
        .map((id) => state.tags.find((tag) => tag.id === id)?.title ?? id)
        .join(", ")
    : "";

  return {
    kind,
    amount,
    categories,
    payee: t.payee ?? "",
    comment: t.comment ?? "",
  };
}

/** One-line rendering used by list_transactions and the delete preview. */
export function formatTransactionLine(
  state: ZenState,
  t: Transaction
): string {
  const { kind, amount, categories, payee, comment } = summarizeTransaction(
    state,
    t
  );
  const commentStr = comment ? ` — "${comment}"` : "";
  return `${t.date} | ${kind.padEnd(8)} | ${amount.padEnd(20)} | ${categories.padEnd(15)} | ${payee}${commentStr} | id: \`${t.id}\``;
}
