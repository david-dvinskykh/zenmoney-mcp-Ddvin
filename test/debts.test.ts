import { describe, it, expect, vi, beforeEach } from "vitest";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type { Account, Merchant, ZenMoneyAPI } from "../src/api.js";
import { ZenState } from "../src/state.js";
import { registerDebtTools } from "../src/tools/debts.js";
import {
  makeDiffResponse,
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

async function setup(opts?: {
  synced?: boolean;
  syncError?: Error;
  accounts?: Account[];
  merchants?: Merchant[];
}) {
  const diffResp = makeDiffResponse({
    account: opts?.accounts ?? [
      CHECKING,
      SAVINGS,
      EURO_CARD,
      ARCHIVED_ACCOUNT,
      DEBT_ACCOUNT,
    ],
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
  registerDebtTools(server, api, state);

  client = new Client({ name: "test-client", version: "1.0.0" });
  const [ct, st] = InMemoryTransport.createLinkedPair();
  await server.connect(st);
  await client.connect(ct);
}

async function callTool(name: string, args: Record<string, unknown>) {
  return client.callTool({ name, arguments: args });
}

function sentTransaction() {
  const call = vi.mocked(api.diff).mock.calls.at(-1)?.[0] as any;
  return call.transaction[0];
}

describe("add_debt", () => {
  beforeEach(() => setup());

  it("books a loan given as my account → debt account", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 500,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(result.isError).toBeFalsy();
    const text = getTextContent(result);
    expect(text).toContain("Lent");
    expect(text).toContain("500 USD");
    expect(text).toContain("Masha now owes you");
    expect(text).toContain("Checking → Debts");

    expect(sentTransaction()).toMatchObject({
      outcomeAccount: "acc-checking",
      incomeAccount: "acc-debt",
      outcome: 500,
      income: 500,
      outcomeInstrument: 1,
      incomeInstrument: 1,
      payee: "Masha",
      date: "2026-03-20",
    });
  });

  it("books a loan taken as debt account → my account", async () => {
    const result = await callTool("add_debt", {
      direction: "borrow",
      account: "Checking",
      amount: 30,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(getTextContent(result)).toContain("you now owe Masha");
    expect(sentTransaction()).toMatchObject({
      outcomeAccount: "acc-debt",
      incomeAccount: "acc-checking",
      outcome: 30,
      income: 30,
    });
  });

  it("books a repayment received like a loan taken", async () => {
    await callTool("add_debt", {
      direction: "repay_received",
      account: "Checking",
      amount: 100,
      payee: "Masha",
      date: "2026-03-21",
    });

    expect(sentTransaction()).toMatchObject({
      outcomeAccount: "acc-debt",
      incomeAccount: "acc-checking",
    });
  });

  it("books a repayment sent like a loan given", async () => {
    const result = await callTool("add_debt", {
      direction: "repay_sent",
      account: "Checking",
      amount: 100,
      payee: "Masha",
      date: "2026-03-21",
    });

    expect(getTextContent(result)).toContain("your debt to Masha goes down");
    expect(sentTransaction()).toMatchObject({
      outcomeAccount: "acc-checking",
      incomeAccount: "acc-debt",
    });
  });

  it("uses the non-debt account's currency on both legs", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Euro Card",
      amount: 200,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(getTextContent(result)).toContain("200 EUR");
    expect(sentTransaction()).toMatchObject({
      outcomeInstrument: 2,
      incomeInstrument: 2,
      incomeAccount: "acc-debt",
    });
  });

  it("links an existing merchant whose title matches the payee", async () => {
    await setup({ merchants: [MERCHANT_CAFE, MERCHANT_MASHA] });

    await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "masha",
      date: "2026-03-20",
    });

    expect(sentTransaction().merchant).toBe("merchant-masha");
  });

  it("leaves the merchant unset when no title matches", async () => {
    await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Someone New",
      date: "2026-03-20",
    });

    expect(sentTransaction().merchant).toBeNull();
  });

  it("attaches a category when asked", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
      category: "Food",
      comment: "lunch money",
    });

    expect(getTextContent(result)).toContain("Food");
    expect(getTextContent(result)).toContain("lunch money");
    expect(sentTransaction()).toMatchObject({
      tag: ["tag-food"],
      comment: "lunch money",
    });
  });

  it("errors on an unknown category instead of silently dropping it", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
      category: "Nonexistent",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("records the debt in local state", async () => {
    await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 500,
      payee: "Masha",
      date: "2026-03-20",
    });

    const stored = state.transactions.find((t) => t.payee === "Masha");
    expect(stored).toBeDefined();
    expect(stored?.incomeAccount).toBe("acc-debt");
  });

  it("errors when the account is unknown", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Nonexistent",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
  });

  it("rejects the debt account as the user's own side", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Debts",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("is the debt account itself");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("explains how to get a debt account when there is none", async () => {
    await setup({ accounts: [CHECKING, SAVINGS] });

    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("No debt account found");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("rejects an explicit debt_account that is not a debt account", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
      debt_account: "Savings",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not a debt account");
  });

  it("rejects a malformed date", async () => {
    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "20/03/2026",
    });

    expect(result.isError).toBe(true);
  });

  it("syncs automatically when not synced yet", async () => {
    await setup({ synced: false });

    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(result.isError).toBeFalsy();
    expect(state.isSynced).toBe(true);
  });

  it("reports a failed automatic sync", async () => {
    await setup({ synced: false, syncError: new Error("Auth failed") });

    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Automatic sync failed");
  });

  it("reports API errors", async () => {
    vi.mocked(api.diff).mockRejectedValue(new Error("Network error"));

    const result = await callTool("add_debt", {
      direction: "lend",
      account: "Checking",
      amount: 10,
      payee: "Masha",
      date: "2026-03-20",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Network error");
  });
});
