import { describe, it, expect, vi, beforeEach } from "vitest";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type { Merchant, Transaction, ZenMoneyAPI } from "../src/api.js";
import { ZenState } from "../src/state.js";
import { registerUpdateTools } from "../src/tools/update.js";
import {
  makeDiffResponse,
  makeTransaction,
  ARCHIVED_ACCOUNT,
  CHECKING,
  DEBT_ACCOUNT,
  EURO_CARD,
  MERCHANT_CAFE,
  MERCHANT_MASHA,
  SAVINGS,
} from "./fixtures.js";
import { getTextContent } from "./helpers.js";

let server: McpServer;
let client: Client;
let api: ZenMoneyAPI;
let state: ZenState;

const EXPENSE = makeTransaction({
  id: "tx-expense",
  outcome: 50,
  income: 0,
  outcomeAccount: "acc-checking",
  incomeAccount: "acc-checking",
  tag: ["tag-food"],
  payee: "Corner Cafe",
  merchant: "merchant-cafe",
  comment: "lunch",
  date: "2026-03-20",
});

const INCOME = makeTransaction({
  id: "tx-income",
  outcome: 0,
  income: 1000,
  outcomeAccount: "acc-checking",
  incomeAccount: "acc-checking",
  tag: ["tag-salary"],
  date: "2026-03-01",
});

const TRANSFER = makeTransaction({
  id: "tx-transfer",
  outcome: 200,
  income: 200,
  outcomeAccount: "acc-checking",
  incomeAccount: "acc-savings",
  date: "2026-03-10",
});

const FX_TRANSFER = makeTransaction({
  id: "tx-fx",
  outcome: 100,
  outcomeAccount: "acc-checking",
  outcomeInstrument: 1,
  income: 92,
  incomeAccount: "acc-euro",
  incomeInstrument: 2,
  date: "2026-03-11",
});

const DEBT = makeTransaction({
  id: "tx-debt",
  outcome: 500,
  income: 500,
  outcomeAccount: "acc-checking",
  incomeAccount: "acc-debt",
  outcomeInstrument: 1,
  incomeInstrument: 1,
  payee: "Masha",
  date: "2026-03-12",
});

async function setup(opts?: {
  synced?: boolean;
  syncError?: Error;
  transactions?: Transaction[];
  merchants?: Merchant[];
}) {
  const diffResp = makeDiffResponse({
    transaction: opts?.transactions ?? [
      EXPENSE,
      INCOME,
      TRANSFER,
      FX_TRANSFER,
      DEBT,
    ],
    account: [CHECKING, SAVINGS, EURO_CARD, ARCHIVED_ACCOUNT, DEBT_ACCOUNT],
    merchant: opts?.merchants ?? [MERCHANT_CAFE],
  });

  api = {
    diff: opts?.syncError
      ? vi.fn().mockRejectedValue(opts.syncError)
      : vi.fn().mockResolvedValue(diffResp),
    suggest: vi.fn(),
  } as unknown as ZenMoneyAPI;

  state = new ZenState(api);
  if (opts?.synced !== false) {
    await state.sync();
    // Forget the sync call so assertions only see what the tool itself did.
    vi.mocked(api.diff).mockClear();
    vi.mocked(api.diff).mockResolvedValue(
      makeDiffResponse({
        serverTimestamp: diffResp.serverTimestamp + 1,
        account: [],
        tag: [],
        instrument: [],
        merchant: [],
        company: [],
        user: [],
        transaction: [],
        deletion: [],
      })
    );
  }

  server = new McpServer({ name: "test", version: "1.0.0" });
  registerUpdateTools(server, api, state);

  client = new Client({ name: "test-client", version: "1.0.0" });
  const [ct, st] = InMemoryTransport.createLinkedPair();
  await server.connect(st);
  await client.connect(ct);
}

async function callTool(args: Record<string, unknown>) {
  return client.callTool({ name: "update_transaction", arguments: args });
}

function sentTransaction() {
  const call = vi.mocked(api.diff).mock.calls.at(-1)?.[0] as any;
  return call.transaction[0];
}

describe("update_transaction preview", () => {
  beforeEach(() => setup());

  it("previews the change without touching the server", async () => {
    const result = await callTool({ id: "tx-expense", amount: 75 });

    const text = getTextContent(result);
    expect(text).toContain("About to update");
    expect(text).toContain("Before:");
    expect(text).toContain("After:");
    expect(text).toContain("outcome: 50 USD → 75 USD");
    expect(text).toContain("confirm=true");
    expect(api.diff).not.toHaveBeenCalled();
    expect(state.transactions.find((t) => t.id === "tx-expense")?.outcome).toBe(50);
  });

  it("says so when the requested values are already in place", async () => {
    const result = await callTool({ id: "tx-expense", amount: 50, confirm: true });

    expect(result.isError).toBeFalsy();
    expect(getTextContent(result)).toContain("nothing to change");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("requires at least one editable field", async () => {
    const result = await callTool({ id: "tx-expense", confirm: true });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Nothing to update");
  });

  it("errors on an unknown id", async () => {
    const result = await callTool({ id: "tx-missing", amount: 5, confirm: true });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
  });
});

describe("update_transaction amounts and dates", () => {
  beforeEach(() => setup());

  it("changes the amount of an expense", async () => {
    const result = await callTool({ id: "tx-expense", amount: 75, confirm: true });

    expect(result.isError).toBeFalsy();
    expect(getTextContent(result)).toContain("Transaction updated");
    expect(sentTransaction()).toMatchObject({
      id: "tx-expense",
      outcome: 75,
      income: 0,
    });
    expect(state.transactions.find((t) => t.id === "tx-expense")?.outcome).toBe(75);
  });

  it("changes the amount of an income on the income leg", async () => {
    await callTool({ id: "tx-income", amount: 1200, confirm: true });

    expect(sentTransaction()).toMatchObject({ income: 1200, outcome: 0 });
  });

  it("changes both legs of a same-currency transfer", async () => {
    await callTool({ id: "tx-transfer", amount: 250, confirm: true });

    expect(sentTransaction()).toMatchObject({ outcome: 250, income: 250 });
  });

  it("refuses a single amount on a cross-currency transfer", async () => {
    const result = await callTool({ id: "tx-fx", amount: 250, confirm: true });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("cross-currency");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("accepts both legs of a cross-currency transfer separately", async () => {
    await callTool({
      id: "tx-fx",
      outcome_amount: 120,
      income_amount: 110,
      confirm: true,
    });

    expect(sentTransaction()).toMatchObject({ outcome: 120, income: 110 });
  });

  it("changes the date", async () => {
    const result = await callTool({
      id: "tx-expense",
      date: "2026-03-25",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("date: 2026-03-20 → 2026-03-25");
    expect(sentTransaction().date).toBe("2026-03-25");
  });

  it("rejects a malformed date", async () => {
    const result = await callTool({ id: "tx-expense", date: "25.03.2026" });

    expect(result.isError).toBe(true);
  });

  it("bumps `changed` so the server accepts the edit", async () => {
    const before = Math.floor(Date.now() / 1000);
    await callTool({ id: "tx-expense", amount: 75, confirm: true });

    expect(sentTransaction().changed).toBeGreaterThanOrEqual(before);
    expect(sentTransaction().changed).toBeGreaterThan(EXPENSE.changed);
  });

  it("rejects an edit that would leave a transfer leg empty", async () => {
    const result = await callTool({
      id: "tx-transfer",
      to_account: "Checking",
      confirm: true,
    });

    // Both legs now point at Checking, which makes it single-account with two
    // positive amounts.
    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("either an expense or an income");
  });
});

describe("update_transaction accounts", () => {
  beforeEach(() => setup());

  it("moves an expense to another account and follows its currency", async () => {
    const result = await callTool({
      id: "tx-expense",
      account: "Euro Card",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("account: Checking → Euro Card");
    expect(sentTransaction()).toMatchObject({
      outcomeAccount: "acc-euro",
      incomeAccount: "acc-euro",
      outcomeInstrument: 2,
      incomeInstrument: 2,
    });
  });

  it("refuses 'account' on a two-sided transaction", async () => {
    const result = await callTool({
      id: "tx-transfer",
      account: "Savings",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("from_account");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("repoints the source account of a transfer", async () => {
    const result = await callTool({
      id: "tx-transfer",
      from_account: "Savings",
      to_account: "Checking",
      confirm: true,
    });

    expect(result.isError).toBeFalsy();
    expect(getTextContent(result)).toContain("from account: Checking → Savings");
    expect(sentTransaction()).toMatchObject({
      outcomeAccount: "acc-savings",
      incomeAccount: "acc-checking",
    });
  });

  it("errors on an unknown account", async () => {
    const result = await callTool({
      id: "tx-expense",
      account: "Nonexistent",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
  });

  it("keeps a debt on the non-debt account's currency", async () => {
    const result = await callTool({
      id: "tx-debt",
      from_account: "Euro Card",
      confirm: true,
    });

    expect(result.isError).toBeFalsy();
    // Both legs move to EUR even though only the non-debt side was repointed.
    expect(sentTransaction()).toMatchObject({
      outcomeAccount: "acc-euro",
      incomeAccount: "acc-debt",
      outcomeInstrument: 2,
      incomeInstrument: 2,
    });
  });

  it("edits the amount of a debt like any other transaction", async () => {
    await callTool({ id: "tx-debt", amount: 450, confirm: true });

    expect(sentTransaction()).toMatchObject({ outcome: 450, income: 450 });
  });
});

describe("update_transaction category, payee and comment", () => {
  beforeEach(() => setup());

  it("recategorizes a transaction", async () => {
    const result = await callTool({
      id: "tx-expense",
      category: "Restaurants",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("category: Food → Restaurants");
    expect(sentTransaction().tag).toEqual(["tag-restaurants"]);
  });

  it("clears the category on an empty string", async () => {
    const result = await callTool({
      id: "tx-expense",
      category: "",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("category: Food → uncategorized");
    expect(sentTransaction().tag).toBeNull();
  });

  it("errors on an unknown category instead of clearing it", async () => {
    const result = await callTool({
      id: "tx-expense",
      category: "Nonexistent",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("drops a stale merchant when the payee changes", async () => {
    const result = await callTool({
      id: "tx-expense",
      payee: "Other Shop",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("payee: Corner Cafe → Other Shop");
    expect(sentTransaction()).toMatchObject({
      payee: "Other Shop",
      merchant: null,
    });
  });

  it("links a merchant whose title matches the new payee", async () => {
    await setup({ merchants: [MERCHANT_CAFE, MERCHANT_MASHA] });

    await callTool({ id: "tx-expense", payee: "Masha", confirm: true });

    expect(sentTransaction()).toMatchObject({
      payee: "Masha",
      merchant: "merchant-masha",
    });
  });

  it("clears the payee and its merchant on an empty string", async () => {
    await callTool({ id: "tx-expense", payee: "", confirm: true });

    expect(sentTransaction()).toMatchObject({ payee: null, merchant: null });
  });

  it("replaces and clears the comment", async () => {
    await callTool({ id: "tx-expense", comment: "dinner", confirm: true });
    expect(sentTransaction().comment).toBe("dinner");

    await callTool({ id: "tx-expense", comment: "", confirm: true });
    expect(sentTransaction().comment).toBeNull();
  });

  it("applies several fields at once", async () => {
    const result = await callTool({
      id: "tx-expense",
      amount: 60,
      date: "2026-03-22",
      category: "Restaurants",
      comment: "team lunch",
      confirm: true,
    });

    const text = getTextContent(result);
    expect(text).toContain("outcome: 50 USD → 60 USD");
    expect(text).toContain("date: 2026-03-20 → 2026-03-22");
    expect(text).toContain("category: Food → Restaurants");
    expect(text).toContain("comment: lunch → team lunch");
  });
});

describe("update_transaction sync and errors", () => {
  it("syncs automatically when not synced yet", async () => {
    await setup({ synced: false });

    const result = await callTool({ id: "tx-expense", amount: 75 });

    expect(result.isError).toBeFalsy();
    expect(state.isSynced).toBe(true);
  });

  it("reports a failed automatic sync", async () => {
    await setup({ synced: false, syncError: new Error("Auth failed") });

    const result = await callTool({ id: "tx-expense", amount: 75 });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Automatic sync failed");
  });

  it("reports API errors and leaves local state alone", async () => {
    await setup();
    vi.mocked(api.diff).mockRejectedValue(new Error("Network error"));

    const result = await callTool({ id: "tx-expense", amount: 75, confirm: true });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Network error");
    expect(state.transactions.find((t) => t.id === "tx-expense")?.outcome).toBe(50);
  });
});
