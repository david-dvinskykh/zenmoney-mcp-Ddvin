import { describe, it, expect, vi, beforeEach } from "vitest";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type { ZenMoneyAPI } from "../src/api.js";
import { ZenState } from "../src/state.js";
import { registerDeleteTools } from "../src/tools/delete.js";
import {
  makeDiffResponse,
  makeTransaction,
  ARCHIVED_ACCOUNT,
  CHECKING,
  DEBT_ACCOUNT,
  EURO_CARD,
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
  transactions?: ReturnType<typeof makeTransaction>[];
}) {
  const diffResp = makeDiffResponse({
    transaction: opts?.transactions ?? [],
    account: [CHECKING, SAVINGS, EURO_CARD, ARCHIVED_ACCOUNT, DEBT_ACCOUNT],
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
  registerDeleteTools(server, api, state);

  client = new Client({ name: "test-client", version: "1.0.0" });
  const [ct, st] = InMemoryTransport.createLinkedPair();
  await server.connect(st);
  await client.connect(ct);
}

async function callTool(name: string, args: Record<string, unknown>) {
  return client.callTool({ name, arguments: args });
}

const EXPENSE = makeTransaction({
  id: "tx-expense",
  outcome: 50,
  outcomeAccount: "acc-checking",
  incomeAccount: "acc-checking",
  tag: ["tag-food"],
  payee: "Grocery Store",
  date: "2026-03-20",
});

const TRANSFER = makeTransaction({
  id: "tx-transfer",
  outcome: 500,
  income: 500,
  outcomeAccount: "acc-checking",
  incomeAccount: "acc-savings",
  date: "2026-03-15",
});

const DEBT = makeTransaction({
  id: "tx-debt",
  outcome: 300,
  income: 300,
  outcomeAccount: "acc-checking",
  incomeAccount: "acc-debt",
  payee: "Alice",
  date: "2026-03-10",
});

describe("delete_transaction", () => {
  it("should preview without deleting when confirm is not set", async () => {
    await setup({ transactions: [EXPENSE] });

    const result = await callTool("delete_transaction", { id: "tx-expense" });
    const text = getTextContent(result);

    expect(result.isError).toBeFalsy();
    expect(text).toContain("About to delete 1 transaction");
    expect(text).toContain("confirm=true");
    expect(text).toContain("Grocery Store");
    expect(api.diff).not.toHaveBeenCalled();
    expect(state.transactions.map((t) => t.id)).toContain("tx-expense");
  });

  it("should delete a transaction with confirm=true", async () => {
    await setup({ transactions: [EXPENSE] });

    const result = await callTool("delete_transaction", {
      id: "tx-expense",
      confirm: true,
    });

    expect(result.isError).toBeFalsy();
    expect(getTextContent(result)).toContain("Deleted 1 transaction");
    expect(api.diff).toHaveBeenCalledWith(
      expect.objectContaining({
        serverTimestamp: 1700000000,
        deletion: [
          expect.objectContaining({
            id: "tx-expense",
            object: "transaction",
            user: 1,
          }),
        ],
      })
    );
    expect(state.transactions).toHaveLength(0);
  });

  it("should delete several transactions at once", async () => {
    await setup({ transactions: [EXPENSE, TRANSFER, DEBT] });

    const result = await callTool("delete_transaction", {
      ids: ["tx-expense", "tx-transfer"],
      confirm: true,
    });

    expect(getTextContent(result)).toContain("Deleted 2 transactions");
    expect(api.diff).toHaveBeenCalledWith(
      expect.objectContaining({
        deletion: [
          expect.objectContaining({ id: "tx-expense" }),
          expect.objectContaining({ id: "tx-transfer" }),
        ],
      })
    );
    expect(state.transactions.map((t) => t.id)).toEqual(["tx-debt"]);
  });

  it("should merge id and ids and drop duplicates", async () => {
    await setup({ transactions: [EXPENSE, TRANSFER] });

    await callTool("delete_transaction", {
      id: "tx-expense",
      ids: ["tx-expense", "tx-transfer"],
      confirm: true,
    });

    const deletions = vi.mocked(api.diff).mock.calls[0][0].deletion!;
    expect(deletions.map((d) => d.id)).toEqual(["tx-expense", "tx-transfer"]);
  });

  it("should delete a transfer", async () => {
    await setup({ transactions: [TRANSFER] });

    const result = await callTool("delete_transaction", {
      id: "tx-transfer",
      confirm: true,
    });

    const text = getTextContent(result);
    expect(text).toContain("Deleted 1 transaction");
    expect(text).toContain("transfer");
    expect(text).toContain("Checking → Savings");
    expect(state.transactions).toHaveLength(0);
  });

  it("should label and delete a debt transaction", async () => {
    await setup({ transactions: [DEBT] });

    const preview = await callTool("delete_transaction", { id: "tx-debt" });
    expect(getTextContent(preview)).toContain("debt");
    expect(getTextContent(preview)).toContain("Alice");

    const result = await callTool("delete_transaction", {
      id: "tx-debt",
      confirm: true,
    });
    expect(getTextContent(result)).toContain("Deleted 1 transaction");
    expect(state.transactions).toHaveLength(0);
  });

  it("should error and delete nothing when an id is unknown", async () => {
    await setup({ transactions: [EXPENSE] });

    const result = await callTool("delete_transaction", {
      ids: ["tx-expense", "tx-nope"],
      confirm: true,
    });

    expect(result.isError).toBe(true);
    const text = getTextContent(result);
    expect(text).toContain("tx-nope");
    expect(text).toContain("Nothing was deleted");
    expect(api.diff).not.toHaveBeenCalled();
    expect(state.transactions).toHaveLength(1);
  });

  it("should error when neither id nor ids is given", async () => {
    await setup({ transactions: [EXPENSE] });

    const result = await callTool("delete_transaction", { confirm: true });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("list_transactions");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should not delete a transaction that is already marked deleted", async () => {
    await setup({ transactions: [EXPENSE] });
    state.transactions.push(
      makeTransaction({ id: "tx-gone", outcome: 1, deleted: true })
    );

    const result = await callTool("delete_transaction", {
      id: "tx-gone",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should report API failures and keep local state", async () => {
    await setup({ transactions: [EXPENSE] });
    vi.mocked(api.diff).mockRejectedValue(new Error("Network error"));

    const result = await callTool("delete_transaction", {
      id: "tx-expense",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Network error");
    expect(state.transactions).toHaveLength(1);
  });

  it("should apply the diff carried by the delete response", async () => {
    await setup({
      transactions: [
        EXPENSE,
        makeTransaction({ id: "tx-elsewhere", outcome: 777, date: "2026-03-21" }),
      ],
    });

    // Another client deleted tx-elsewhere in the meantime; the response to our
    // own deletion is the only place that is ever reported.
    vi.mocked(api.diff).mockResolvedValue(
      makeDiffResponse({
        serverTimestamp: 1700000002,
        deletion: [
          { id: "tx-elsewhere", object: "transaction", stamp: 1700000001, user: 1 },
        ],
      })
    );

    await callTool("delete_transaction", { id: "tx-expense", confirm: true });

    expect(state.transactions).toHaveLength(0);
    expect(state.serverTimestamp).toBe(1700000002);
  });

  it("should sync automatically when not synced yet", async () => {
    await setup({ synced: false, transactions: [EXPENSE] });

    const result = await callTool("delete_transaction", { id: "tx-expense" });

    expect(result.isError).toBeFalsy();
    expect(state.isSynced).toBe(true);
    expect(getTextContent(result)).toContain("About to delete");
  });

  it("should report an error when the automatic sync fails", async () => {
    await setup({ synced: false, syncError: new Error("Auth failed") });

    const result = await callTool("delete_transaction", { id: "tx-expense" });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Automatic sync failed");
  });
});

describe("delete_object", () => {
  it("should preview an account deletion with its transaction count", async () => {
    await setup({ transactions: [EXPENSE, TRANSFER, DEBT] });

    const result = await callTool("delete_object", {
      type: "account",
      name_or_id: "Checking",
    });

    const text = getTextContent(result);
    expect(text).toContain("About to delete account");
    expect(text).toContain("Checking");
    expect(text).toContain("3 transactions will be deleted with it");
    expect(api.diff).not.toHaveBeenCalled();
    expect(state.accounts.find((a) => a.id === "acc-checking")).toBeDefined();
  });

  it("should delete an account and its transactions", async () => {
    await setup({ transactions: [EXPENSE, TRANSFER, DEBT] });

    const result = await callTool("delete_object", {
      type: "account",
      name_or_id: "acc-checking",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("Deleted account");
    expect(api.diff).toHaveBeenCalledWith(
      expect.objectContaining({
        deletion: [
          expect.objectContaining({ id: "acc-checking", object: "account" }),
        ],
      })
    );
    expect(state.accounts.find((a) => a.id === "acc-checking")).toBeUndefined();
    expect(state.transactions).toHaveLength(0);
  });

  it("should delete a debt account", async () => {
    await setup({ transactions: [DEBT] });

    const result = await callTool("delete_object", {
      type: "account",
      name_or_id: "Debts",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("Deleted account");
    expect(state.accounts.find((a) => a.id === "acc-debt")).toBeUndefined();
    expect(state.transactions).toHaveLength(0);
  });

  it("should delete a category and keep its transactions", async () => {
    await setup({ transactions: [EXPENSE] });

    const preview = await callTool("delete_object", {
      type: "category",
      name_or_id: "Food",
    });
    const previewText = getTextContent(preview);
    expect(previewText).toContain("1 subcategory will be deleted with it");
    expect(previewText).toContain("1 transaction will become uncategorized");

    const result = await callTool("delete_object", {
      type: "category",
      name_or_id: "Food",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("Deleted category");
    expect(api.diff).toHaveBeenCalledWith(
      expect.objectContaining({
        deletion: [expect.objectContaining({ id: "tag-food", object: "tag" })],
      })
    );
    expect(state.tags.find((t) => t.id === "tag-food")).toBeUndefined();
    expect(state.tags.find((t) => t.id === "tag-restaurants")).toBeUndefined();
    expect(state.transactions).toHaveLength(1);
    expect(state.transactions[0].tag).toBeNull();
  });

  it("should delete a merchant and clear it from transactions", async () => {
    await setup({
      transactions: [
        makeTransaction({
          id: "tx-cafe",
          outcome: 5,
          merchant: "merchant-cafe",
          date: "2026-03-20",
        }),
      ],
    });

    const result = await callTool("delete_object", {
      type: "merchant",
      name_or_id: "Corner Cafe",
      confirm: true,
    });

    expect(getTextContent(result)).toContain("Deleted merchant");
    expect(api.diff).toHaveBeenCalledWith(
      expect.objectContaining({
        deletion: [
          expect.objectContaining({ id: "merchant-cafe", object: "merchant" }),
        ],
      })
    );
    expect(state.merchants).toHaveLength(0);
    expect(state.transactions[0].merchant).toBeNull();
  });

  it("should error when the object is not found", async () => {
    await setup({ transactions: [] });

    const result = await callTool("delete_object", {
      type: "account",
      name_or_id: "Nonexistent",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    const text = getTextContent(result);
    expect(text).toContain("Nothing was deleted");
    expect(text).toContain("list_accounts");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should reject an unknown object type", async () => {
    await setup({ transactions: [] });

    const result = await callTool("delete_object", {
      type: "budget",
      name_or_id: "whatever",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should report API failures and keep local state", async () => {
    await setup({ transactions: [EXPENSE] });
    vi.mocked(api.diff).mockRejectedValue(new Error("Network error"));

    const result = await callTool("delete_object", {
      type: "account",
      name_or_id: "Checking",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Network error");
    expect(state.accounts.find((a) => a.id === "acc-checking")).toBeDefined();
    expect(state.transactions).toHaveLength(1);
  });

  it("should sync automatically when not synced yet", async () => {
    await setup({ synced: false, transactions: [EXPENSE] });

    const result = await callTool("delete_object", {
      type: "merchant",
      name_or_id: "Corner Cafe",
    });

    expect(result.isError).toBeFalsy();
    expect(state.isSynced).toBe(true);
    expect(getTextContent(result)).toContain("About to delete merchant");
  });
});
