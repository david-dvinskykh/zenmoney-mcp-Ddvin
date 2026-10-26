import { describe, it, expect, vi } from "vitest";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type { Budget, ZenMoneyAPI } from "../src/api.js";
import { ZenState } from "../src/state.js";
import { registerBudgetTools } from "../src/tools/budgets.js";
import { makeDiffResponse } from "./fixtures.js";
import { getTextContent } from "./helpers.js";

function makeBudget(overrides: Partial<Budget> = {}): Budget {
  return {
    user: 1,
    changed: 1000,
    tag: "tag-food",
    date: "2026-03-01",
    income: 0,
    outcome: 0,
    incomeLock: false,
    outcomeLock: false,
    ...overrides,
  };
}

async function setup(budgets: Budget[] = []) {
  const api = {
    diff: vi.fn().mockResolvedValue(makeDiffResponse({ budget: budgets })),
    suggest: vi.fn(),
  } as unknown as ZenMoneyAPI;

  const state = new ZenState(api);
  await state.sync();
  vi.mocked(api.diff).mockClear();
  vi.mocked(api.diff).mockResolvedValue(
    makeDiffResponse({
      serverTimestamp: 1700000001,
      instrument: [],
      company: [],
      user: [],
      account: [],
      tag: [],
      merchant: [],
    })
  );

  const server = new McpServer({ name: "test", version: "1.0.0" });
  registerBudgetTools(server, api, state);
  const client = new Client({ name: "test-client", version: "1.0.0" });
  const [ct, st] = InMemoryTransport.createLinkedPair();
  await server.connect(st);
  await client.connect(ct);

  const call = (name: string, args: Record<string, unknown>) =>
    client.callTool({ name, arguments: args });
  return { api, state, call };
}

describe("list_budgets", () => {
  it("lists the month's budgets, month-wide row first, skipping empty ones", async () => {
    const { call } = await setup([
      makeBudget({ tag: "tag-salary", income: 3000, incomeLock: true }),
      makeBudget({ tag: "tag-food", outcome: 500, outcomeLock: true }),
      makeBudget({ tag: null, outcome: 2000 }),
      makeBudget({ tag: "tag-restaurants" }),
      makeBudget({ tag: "tag-food", date: "2026-04-01", outcome: 900, outcomeLock: true }),
    ]);

    const text = getTextContent(await call("list_budgets", { month: "2026-03" }));
    expect(text).toBe(
      "Budgets for 2026-03 (3):\n\n" +
        "- **(whole month)**: expense 2000 USD, income none\n" +
        "- **Food**: expense 500 USD, income none — id: `tag-food`\n" +
        "- **Salary**: expense none, income 3000 USD — id: `tag-salary`"
    );
  });

  it("says so when the month has no budgets", async () => {
    const { call } = await setup();
    const text = getTextContent(await call("list_budgets", { month: "2026-05" }));
    expect(text).toBe("No budgets for 2026-05.");
  });
});

describe("set_budget", () => {
  it("previews without writing", async () => {
    const { api, call } = await setup([
      makeBudget({ tag: "tag-food", outcome: 500, outcomeLock: true }),
    ]);

    const result = await call("set_budget", {
      month: "2026-03",
      category: "Food",
      outcome: 650,
    });

    expect(result.isError).toBeFalsy();
    expect(getTextContent(result)).toBe(
      "Budget for **Food**, 2026-03:\n- expense: 500 USD → 650 USD\n\n" +
        "Nothing has been saved yet. Call set_budget again with confirm=true to save."
    );
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("writes the amount, keeps the lock and the other side", async () => {
    const { api, state, call } = await setup([
      makeBudget({ tag: "tag-food", outcome: 500, outcomeLock: true, income: 10, incomeLock: true }),
    ]);

    const result = await call("set_budget", {
      month: "2026-03",
      category: "tag-food",
      outcome: 650,
      confirm: true,
    });

    expect(getTextContent(result)).toBe(
      "Saved. Budget for **Food**, 2026-03:\n- expense: 500 USD → 650 USD"
    );
    const sent = vi.mocked(api.diff).mock.calls[0][0].budget![0];
    expect(sent).toMatchObject({
      user: 1,
      tag: "tag-food",
      date: "2026-03-01",
      outcome: 650,
      outcomeLock: true,
      income: 10,
      incomeLock: true,
    });
    expect(state.budgets).toHaveLength(1);
    expect(state.budgets[0].outcome).toBe(650);
  });

  it("leaves an unlocked budget unlocked", async () => {
    const { api, call } = await setup([makeBudget({ tag: "tag-food", outcome: 4700 })]);

    await call("set_budget", { month: "2026-03", category: "Food", outcome: 5000, confirm: true });

    const sent = vi.mocked(api.diff).mock.calls[0][0].budget![0];
    expect(sent).toMatchObject({ outcome: 5000, outcomeLock: false });
  });

  it("creates a budget the month did not have", async () => {
    const { api, state, call } = await setup();

    await call("set_budget", {
      month: "2026-06",
      category: "Salary",
      income: 3000,
      confirm: true,
    });

    const sent = vi.mocked(api.diff).mock.calls[0][0].budget![0];
    expect(sent).toMatchObject({
      tag: "tag-salary",
      date: "2026-06-01",
      income: 3000,
      incomeLock: false,
      outcome: 0,
      outcomeLock: false,
    });
    expect(state.budgets.map((b) => b.date)).toEqual(["2026-06-01"]);
  });

  it("removes a side with 0 by unlocking it", async () => {
    const { api, call } = await setup([
      makeBudget({ tag: "tag-food", outcome: 500, outcomeLock: true }),
    ]);

    const result = await call("set_budget", {
      month: "2026-03",
      category: "Food",
      outcome: 0,
      confirm: true,
    });

    expect(getTextContent(result)).toContain("- expense: 500 USD → none");
    const sent = vi.mocked(api.diff).mock.calls[0][0].budget![0];
    expect(sent).toMatchObject({ outcome: 0, outcomeLock: false });
  });

  it("rejects a call with no amount", async () => {
    const { call } = await setup();
    const result = await call("set_budget", { month: "2026-03", category: "Food" });
    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("Nothing to set");
  });

  it("rejects an unknown category", async () => {
    const { call } = await setup();
    const result = await call("set_budget", {
      month: "2026-03",
      category: "Nope",
      outcome: 1,
    });
    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain('Category "Nope" not found');
  });
});

describe("budgets in state", () => {
  it("asks for budgets in a full fetch", async () => {
    const { api, state } = await setup();
    await state.sync(true);
    const req = vi.mocked(api.diff).mock.calls[0][0];
    expect(req.forceFetch).toContain("budget");
  });

  it("drops the budgets of a deleted category and its children", async () => {
    const { state } = await setup([
      makeBudget({ tag: "tag-food", outcome: 1, outcomeLock: true }),
      makeBudget({ tag: "tag-restaurants", outcome: 1, outcomeLock: true }),
      makeBudget({ tag: "tag-salary", income: 1, incomeLock: true }),
    ]);
    await state.applyLocalDeletions(
      [{ id: "tag-food", object: "tag", stamp: 1, user: 1 }],
      makeDiffResponse({ instrument: [], account: [], tag: [], user: [], company: [], merchant: [] })
    );
    expect(state.budgets.map((b) => b.tag)).toEqual(["tag-salary"]);
  });
});
