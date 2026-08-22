import type { Account, Merchant } from "../api.js";
import type { ZenState } from "../state.js";

/** Wrap a plain string in the tool-result shape every tool returns. */
export function textResult(text: string, isError = false) {
  return {
    content: [{ type: "text" as const, text }],
    ...(isError ? { isError: true } : {}),
  };
}

/** Look up an account by exact UUID first, then by a loose name match. */
export function resolveAccount(
  state: ZenState,
  nameOrId: string
): Account | undefined {
  const direct = state.accounts.find((a) => a.id === nameOrId);
  if (direct) return direct;
  return state.findAccountByName(nameOrId);
}

/** Look up a category by UUID or name and return it as a tag list. */
export function resolveTag(
  state: ZenState,
  nameOrId: string
): string[] | null {
  const direct = state.tags.find((t) => t.id === nameOrId);
  if (direct) return [direct.id];
  const byName = state.findTagByName(nameOrId);
  if (byName) return [byName.id];
  return null;
}

/** Render tag ids as their titles, the way the add/update tools report them. */
export function tagTitles(state: ZenState, ids: string[] | null): string {
  if (!ids || ids.length === 0) return "uncategorized";
  return ids
    .map((id) => state.tags.find((t) => t.id === id)?.title ?? id)
    .join(", ");
}

/**
 * Find the merchant ZenMoney would attach to this payee.
 *
 * `merchant` is the normalized counterparty and takes precedence over the raw
 * `payee` string in the app's UI, so a payee change that left a stale merchant
 * behind would look like nothing happened. The match is exact (case- and
 * space-insensitive) on purpose — the loose matching used for accounts and
 * categories would happily pick "Masha" for "Mashal".
 */
export function resolveMerchantByTitle(
  state: ZenState,
  title: string
): Merchant | undefined {
  const normalized = title.trim().toLowerCase();
  if (!normalized) return undefined;
  return state.merchants.find(
    (m) => m.title.trim().toLowerCase() === normalized
  );
}

/** The single system account ZenMoney books every debt against. */
export function findDebtAccount(state: ZenState): Account | undefined {
  return state.accounts.find((a) => a.type === "debt");
}

