import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type {
  Account,
  DiffResponse,
  Reminder,
  ReminderMarker,
  ZenMoneyAPI,
} from "../src/api.js";
import { ZenState } from "../src/state.js";
import { registerReminderTools } from "../src/tools/reminders.js";
import {
  makeDiffResponse,
  makeReminder,
  makeReminderMarker,
  CHECKING,
  SAVINGS,
  EURO_CARD,
  FOOD,
  MERCHANT_CAFE,
} from "./fixtures.js";
import { getTextContent } from "./helpers.js";

let server: McpServer;
let client: Client;
let api: ZenMoneyAPI;
let state: ZenState;

const RENT = makeReminder({
  id: "rem-rent",
  outcome: 1200,
  payee: "Landlord",
  comment: "Rent",
  interval: "month",
  step: 1,
});

const GYM = makeReminder({
  id: "rem-gym",
  outcome: 40,
  payee: "City Gym",
  tag: ["tag-food"],
  merchant: "merchant-cafe",
  interval: "week",
  step: 2,
  endDate: "2026-12-31",
});

const BONUS = makeReminder({
  id: "rem-bonus",
  income: 500,
  payee: "Employer",
  comment: "Yearly bonus",
  interval: null,
  step: null,
  points: null,
  startDate: "2026-02-01",
});

const RENT_MARKERS = [
  makeReminderMarker({ id: "mk-rent-1", reminder: "rem-rent", date: "2026-04-01" }),
  makeReminderMarker({ id: "mk-rent-2", reminder: "rem-rent", date: "2026-05-01" }),
  // Already turned into a transaction — not something a delete would remove.
  makeReminderMarker({
    id: "mk-rent-0",
    reminder: "rem-rent",
    date: "2026-03-01",
    state: "processed",
  }),
];

const GYM_MARKERS = [
  makeReminderMarker({ id: "mk-gym-1", reminder: "rem-gym", date: "2026-03-25" }),
];

// The one-off already fired, so nothing is planned for it any more.
const BONUS_MARKERS = [
  makeReminderMarker({
    id: "mk-bonus-1",
    reminder: "rem-bonus",
    date: "2026-02-01",
    state: "processed",
  }),
];

async function setup(opts?: {
  reminders?: Reminder[];
  reminderMarkers?: ReminderMarker[];
  accounts?: Account[];
  syncError?: Error;
}) {
  const diffResp = makeDiffResponse({
    account: opts?.accounts ?? [CHECKING, SAVINGS],
    reminder: opts?.reminders ?? [],
    reminderMarker: opts?.reminderMarkers ?? [],
  });

  api = {
    diff: opts?.syncError
      ? vi.fn().mockRejectedValue(opts.syncError)
      : vi.fn().mockResolvedValue(diffResp),
    suggest: vi.fn(),
  } as unknown as ZenMoneyAPI;

  state = new ZenState(api);
  if (!opts?.syncError) {
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
        reminder: [],
        reminderMarker: [],
        transaction: [],
        deletion: [],
      })
    );
  }

  server = new McpServer({ name: "test", version: "1.0.0" });
  registerReminderTools(server, api, state);

  client = new Client({ name: "test-client", version: "1.0.0" });
  const [ct, st] = InMemoryTransport.createLinkedPair();
  await server.connect(st);
  await client.connect(ct);
}

async function callTool(name: string, args: Record<string, unknown> = {}) {
  return client.callTool({ name, arguments: args });
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-03-20T12:00:00Z"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("list_reminders", () => {
  it("should report an empty list when there are no reminders", async () => {
    await setup();

    const text = getTextContent(await callTool("list_reminders"));
    expect(text).toContain("No reminders");
  });

  it("should list reminders with their next occurrence and id", async () => {
    await setup({
      reminders: [RENT, GYM],
      reminderMarkers: [...RENT_MARKERS, ...GYM_MARKERS],
    });

    const text = getTextContent(await callTool("list_reminders"));

    expect(text).toContain("2 reminders");
    expect(text).toContain("2026-04-01");
    expect(text).toContain("Landlord");
    expect(text).toContain("rem-rent");
    expect(text).toContain("every month");
    expect(text).toContain("every 2 weeks until 2026-12-31");
  });

  it("should sort by the soonest planned occurrence", async () => {
    await setup({
      reminders: [RENT, GYM],
      reminderMarkers: [...RENT_MARKERS, ...GYM_MARKERS],
    });

    const text = getTextContent(await callTool("list_reminders"));
    expect(text.indexOf("rem-gym")).toBeLessThan(text.indexOf("rem-rent"));
  });

  it("should ignore occurrences in the past when picking the next one", async () => {
    await setup({
      reminders: [RENT],
      reminderMarkers: RENT_MARKERS,
    });

    const text = getTextContent(await callTool("list_reminders"));
    expect(text).toContain("2026-04-01");
    expect(text).not.toContain("2026-03-01");
  });

  it("should describe a one-off reminder by its date", async () => {
    await setup({ reminders: [BONUS], reminderMarkers: BONUS_MARKERS });

    const text = getTextContent(await callTool("list_reminders"));
    expect(text).toContain("one-off on 2026-02-01");
    expect(text).toContain("nothing planned");
  });

  it("should skip spent reminders when upcoming_only is set", async () => {
    await setup({
      reminders: [RENT, BONUS],
      reminderMarkers: [...RENT_MARKERS, ...BONUS_MARKERS],
    });

    const text = getTextContent(
      await callTool("list_reminders", { upcoming_only: true })
    );
    expect(text).toContain("rem-rent");
    expect(text).not.toContain("rem-bonus");
  });

  it("should honour limit and say how many were left out", async () => {
    await setup({
      reminders: [RENT, GYM],
      reminderMarkers: [...RENT_MARKERS, ...GYM_MARKERS],
    });

    const text = getTextContent(await callTool("list_reminders", { limit: 1 }));
    expect(text).toContain("rem-gym");
    expect(text).not.toContain("rem-rent");
    expect(text).toContain("1 more");
  });

  it("should surface a sync failure instead of an empty list", async () => {
    await setup({ syncError: new Error("network down") });

    const result = await callTool("list_reminders");
    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("network down");
  });
});

describe("delete_reminder", () => {
  it("should preview without deleting when confirm is not set", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });

    const result = await callTool("delete_reminder", { name_or_id: "rem-rent" });
    const text = getTextContent(result);

    expect(result.isError).toBeFalsy();
    expect(text).toContain("About to delete reminder");
    expect(text).toContain("2 planned occurrences will be deleted with it");
    expect(text).toContain("confirm=true");
    expect(api.diff).not.toHaveBeenCalled();
    expect(state.reminders).toHaveLength(1);
  });

  it("should delete the reminder and its planned occurrences with confirm=true", async () => {
    await setup({
      reminders: [RENT, GYM],
      reminderMarkers: [...RENT_MARKERS, ...GYM_MARKERS],
    });

    const result = await callTool("delete_reminder", {
      name_or_id: "rem-rent",
      confirm: true,
    });

    expect(result.isError).toBeFalsy();
    expect(getTextContent(result)).toContain("Deleted reminder");
    expect(api.diff).toHaveBeenCalledWith(
      expect.objectContaining({
        serverTimestamp: 1700000000,
        deletion: [
          expect.objectContaining({
            id: "rem-rent",
            object: "reminder",
            user: 1,
          }),
        ],
      })
    );
    expect(state.reminders.map((r) => r.id)).toEqual(["rem-gym"]);
    expect(state.reminderMarkers.map((m) => m.id)).toEqual(["mk-gym-1"]);
  });

  it("should find a reminder by payee", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });

    const text = getTextContent(
      await callTool("delete_reminder", { name_or_id: "landlord" })
    );
    expect(text).toContain("About to delete reminder");
    expect(text).toContain("rem-rent");
  });

  it("should find a reminder by comment", async () => {
    await setup({ reminders: [RENT, GYM] });

    const text = getTextContent(
      await callTool("delete_reminder", { name_or_id: "Rent" })
    );
    expect(text).toContain("rem-rent");
  });

  it("should find a reminder by merchant or category", async () => {
    await setup({ reminders: [RENT, GYM] });

    expect(
      getTextContent(await callTool("delete_reminder", { name_or_id: "corner" }))
    ).toContain("rem-gym");
    expect(
      getTextContent(await callTool("delete_reminder", { name_or_id: "food" }))
    ).toContain("rem-gym");
  });

  it("should refuse to guess between several matches", async () => {
    await setup({ reminders: [RENT, GYM] });

    const result = await callTool("delete_reminder", { name_or_id: "n" });
    const text = getTextContent(result);

    expect(result.isError).toBe(true);
    expect(text).toContain("matches 2 reminders");
    expect(text).toContain("rem-rent");
    expect(text).toContain("rem-gym");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should prefer an exact id over a loose text match", async () => {
    const decoy = makeReminder({ id: "rem-decoy", payee: "rem-rent" });
    await setup({ reminders: [RENT, decoy] });

    const text = getTextContent(
      await callTool("delete_reminder", { name_or_id: "rem-rent" })
    );
    expect(text).toContain("About to delete reminder");
    expect(text).toContain("Landlord");
  });

  it("should report an unknown reminder without deleting anything", async () => {
    await setup({ reminders: [RENT] });

    const result = await callTool("delete_reminder", {
      name_or_id: "mortgage",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("No reminder matching");
    expect(api.diff).not.toHaveBeenCalled();
    expect(state.reminders).toHaveLength(1);
  });

  it("should keep the reminder when the API call fails", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });
    vi.mocked(api.diff).mockRejectedValueOnce(new Error("server on fire"));

    const result = await callTool("delete_reminder", {
      name_or_id: "rem-rent",
      confirm: true,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("server on fire");
    expect(state.reminders).toHaveLength(1);
    expect(state.reminderMarkers).toHaveLength(3);
  });

  it("should say when a reminder has nothing planned left", async () => {
    await setup({ reminders: [BONUS], reminderMarkers: BONUS_MARKERS });

    const text = getTextContent(
      await callTool("delete_reminder", { name_or_id: "rem-bonus" })
    );
    expect(text).toContain("no planned occurrences left");
  });
});

/** A write response that carries nothing but a fresh timestamp. */
function emptyDiff(overrides: Partial<DiffResponse> = {}): DiffResponse {
  return makeDiffResponse({
    serverTimestamp: 1700000001,
    account: [],
    tag: [],
    instrument: [],
    merchant: [],
    company: [],
    user: [],
    reminder: [],
    reminderMarker: [],
    transaction: [],
    deletion: [],
    ...overrides,
  });
}

/** The reminder the tool pushed in its diff request. */
function pushedReminder(): Reminder {
  const req = vi.mocked(api.diff).mock.calls[0][0] as any;
  return req.reminder[0] as Reminder;
}

/** The occurrence the tool pushed in its diff request. */
function pushedMarker(): ReminderMarker {
  const req = vi.mocked(api.diff).mock.calls[0][0] as any;
  return req.reminderMarker[0] as ReminderMarker;
}

describe("add_reminder", () => {
  it("should create a monthly expense series", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 1200,
      start_date: "2026-04-01",
      interval: "month",
      comment: "Rent",
    });

    expect(result.isError).toBeFalsy();

    const sent = pushedReminder();
    expect(sent).toMatchObject({
      outcomeAccount: "acc-checking",
      outcome: 1200,
      income: 0,
      incomeAccount: "acc-checking",
      outcomeInstrument: 1,
      incomeInstrument: 1,
      interval: "month",
      step: 1,
      points: [0],
      startDate: "2026-04-01",
      endDate: null,
      notify: true,
      comment: "Rent",
    });

    expect(state.reminders).toHaveLength(1);
    expect(state.reminders[0].id).toBe(sent.id);
    expect(getTextContent(result)).toContain("Reminder added");
  });

  it("should keep a one-off reminder from repeating", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 90,
      start_date: "2026-05-02",
    });

    const sent = pushedReminder();
    expect(sent.interval).toBeNull();
    expect(sent.step).toBeNull();
    expect(sent.points).toBeNull();
    // It ends the day it starts, so nothing can expand it further.
    expect(sent.endDate).toBe("2026-05-02");
  });

  it("should create an income series", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "income",
      account: "Checking",
      amount: 5000,
      start_date: "2026-04-10",
      interval: "month",
      payee: "Employer",
    });

    const sent = pushedReminder();
    expect(sent.income).toBe(5000);
    expect(sent.outcome).toBe(0);
    expect(sent.payee).toBe("Employer");
  });

  it("should create a transfer series across two accounts", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "transfer",
      from_account: "Checking",
      to_account: "Savings",
      amount: 300,
      start_date: "2026-04-05",
      interval: "week",
      step: 2,
    });

    const sent = pushedReminder();
    expect(sent.outcomeAccount).toBe("acc-checking");
    expect(sent.incomeAccount).toBe("acc-savings");
    expect(sent.outcome).toBe(300);
    expect(sent.income).toBe(300);
    expect(sent.step).toBe(2);
    expect(sent.points).toEqual([0]);
  });

  it("should refuse a cross-currency transfer without income_amount", async () => {
    await setup({ accounts: [CHECKING, SAVINGS, EURO_CARD] });

    const result = await callTool("add_reminder", {
      type: "transfer",
      from_account: "Checking",
      to_account: "Euro Card",
      amount: 100,
      start_date: "2026-04-05",
      interval: "month",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("income_amount");
    expect(api.diff).not.toHaveBeenCalled();
    expect(state.reminders).toHaveLength(0);
  });

  it("should carry both amounts of a cross-currency transfer", async () => {
    await setup({ accounts: [CHECKING, SAVINGS, EURO_CARD] });

    await callTool("add_reminder", {
      type: "transfer",
      from_account: "Checking",
      to_account: "Euro Card",
      outcome_amount: 110,
      income_amount: 100,
      start_date: "2026-04-05",
      interval: "month",
    });

    const sent = pushedReminder();
    expect(sent.outcome).toBe(110);
    expect(sent.outcomeInstrument).toBe(1);
    expect(sent.income).toBe(100);
    expect(sent.incomeInstrument).toBe(2);
  });

  it("should reject an unknown account without pushing anything", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Nowhere",
      amount: 10,
      start_date: "2026-04-01",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should ask for from_account and to_account on a transfer", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "transfer",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("from_account");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should reject step and points without an interval", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
      step: 2,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("repeating reminder");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should reject a point that falls outside the step window", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
      interval: "day",
      step: 7,
      points: [0, 9],
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("0…6");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should sort and deduplicate points", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
      interval: "day",
      step: 7,
      points: [4, 0, 4, 2],
    });

    expect(pushedReminder().points).toEqual([0, 2, 4]);
  });

  it("should reject an end_date before the start", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
      end_date: "2026-03-01",
      interval: "month",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("on or after");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should resolve the category and link an existing merchant", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 12,
      start_date: "2026-04-01",
      interval: "week",
      category: "Food",
      payee: MERCHANT_CAFE.title,
    });

    const sent = pushedReminder();
    expect(sent.tag).toEqual([FOOD.id]);
    expect(sent.merchant).toBe(MERCHANT_CAFE.id);
  });

  it("should write the occurrences itself — ZenMoney does not expand a series", async () => {
    await setup();

    const text = getTextContent(
      await callTool("add_reminder", {
        type: "expense",
        account: "Checking",
        amount: 1200,
        start_date: "2026-04-01",
        interval: "month",
      })
    );

    const req = vi.mocked(api.diff).mock.calls[0][0] as any;
    const sent: ReminderMarker[] = req.reminderMarker;
    // A year of an open-ended monthly series, the horizon the app keeps.
    expect(sent).toHaveLength(13);
    expect(sent[0]).toMatchObject({
      date: "2026-04-01",
      reminder: req.reminder[0].id,
      state: "planned",
      outcome: 1200,
      outcomeAccount: "acc-checking",
    });
    expect(sent.at(-1)!.date).toBe("2027-04-01");

    expect(state.reminderMarkers).toHaveLength(13);
    expect(text).toContain("Planned 13 occurrences: 2026-04-01, 2026-05-01");
    expect(text).toContain("a year ahead");
  });

  it("should stop a series at its end date", async () => {
    await setup();

    const text = getTextContent(
      await callTool("add_reminder", {
        type: "expense",
        account: "Checking",
        amount: 10,
        start_date: "2026-04-01",
        end_date: "2026-06-15",
        interval: "month",
      })
    );

    const req = vi.mocked(api.diff).mock.calls[0][0] as any;
    expect(req.reminderMarker.map((m: ReminderMarker) => m.date)).toEqual([
      "2026-04-01",
      "2026-05-01",
      "2026-06-01",
    ]);
    expect(text).toContain("through 2026-06-15");
  });

  it("should write a single occurrence for a one-off", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 90,
      start_date: "2026-05-02",
    });

    const req = vi.mocked(api.diff).mock.calls[0][0] as any;
    expect(req.reminderMarker).toHaveLength(1);
    expect(req.reminderMarker[0].date).toBe("2026-05-02");
  });

  it("should spread points across the step window", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
      end_date: "2026-04-21",
      interval: "day",
      step: 7,
      points: [0, 2, 4],
    });

    const req = vi.mocked(api.diff).mock.calls[0][0] as any;
    expect(req.reminderMarker.map((m: ReminderMarker) => m.date)).toEqual([
      "2026-04-01", "2026-04-03", "2026-04-05",
      "2026-04-08", "2026-04-10", "2026-04-12",
      "2026-04-15", "2026-04-17", "2026-04-19",
    ]);
  });

  it("should clamp a month-end start instead of rolling into the next month", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-01-31",
      end_date: "2026-04-30",
      interval: "month",
    });

    const req = vi.mocked(api.diff).mock.calls[0][0] as any;
    expect(req.reminderMarker.map((m: ReminderMarker) => m.date)).toEqual([
      "2026-01-31",
      "2026-02-28",
      "2026-03-31",
      "2026-04-30",
    ]);
  });

  it("should apply the rest of the write response diff", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });
    vi.mocked(api.diff).mockResolvedValue(
      emptyDiff({
        deletion: [
          { id: "rem-rent", object: "reminder", stamp: 1700000001, user: 1 },
        ],
      })
    );

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
    });

    // The reminder deleted elsewhere is gone with its markers, the new one
    // stayed with the single occurrence it wrote.
    expect(state.reminders.map((r) => r.id)).not.toContain("rem-rent");
    expect(state.reminders).toHaveLength(1);
    expect(state.reminderMarkers).toHaveLength(1);
    expect(state.reminderMarkers[0].date).toBe("2026-04-01");
  });

  it("should reject a date that is not on the calendar", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-02-29",
      interval: "year",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not a date on the calendar");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should cap one write instead of materialising a decade", async () => {
    await setup();

    await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
      end_date: "2036-04-01",
      interval: "day",
    });

    const req = vi.mocked(api.diff).mock.calls[0][0] as any;
    expect(req.reminderMarker).toHaveLength(400);
    expect(req.reminderMarker[0].date).toBe("2026-04-01");
  });

  it("should reject an unknown category rather than dropping it", async () => {
    await setup();

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
      interval: "month",
      category: "Yachts",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should report a failed push without recording the reminder", async () => {
    await setup();
    vi.mocked(api.diff).mockRejectedValue(new Error("server on fire"));

    const result = await callTool("add_reminder", {
      type: "expense",
      account: "Checking",
      amount: 10,
      start_date: "2026-04-01",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("server on fire");
    expect(state.reminders).toHaveLength(0);
  });
});

describe("add_reminder_marker", () => {
  it("should add an occurrence copying the reminder's operation", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });

    const result = await callTool("add_reminder_marker", {
      reminder: "rem-rent",
      date: "2026-06-01",
    });

    expect(result.isError).toBeFalsy();

    const sent = pushedMarker();
    expect(sent).toMatchObject({
      reminder: "rem-rent",
      date: "2026-06-01",
      state: "planned",
      outcome: RENT.outcome,
      income: RENT.income,
      outcomeAccount: RENT.outcomeAccount,
      incomeAccount: RENT.incomeAccount,
      payee: RENT.payee,
      comment: RENT.comment,
    });

    expect(state.reminderMarkers.map((m) => m.id)).toContain(sent.id);
    expect(getTextContent(result)).toContain("Occurrence added");
  });

  it("should override the amount of a one-sided reminder", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });

    await callTool("add_reminder_marker", {
      reminder: "rem-rent",
      date: "2026-06-01",
      amount: 1300,
      comment: "Rent, raised",
    });

    const sent = pushedMarker();
    expect(sent.outcome).toBe(1300);
    expect(sent.income).toBe(0);
    expect(sent.comment).toBe("Rent, raised");
  });

  it("should refuse a bare amount on a two-sided reminder", async () => {
    const TRANSFER = makeReminder({
      id: "rem-move",
      outcomeAccount: "acc-checking",
      incomeAccount: "acc-savings",
      outcome: 300,
      income: 300,
    });
    await setup({ reminders: [TRANSFER] });

    const result = await callTool("add_reminder_marker", {
      reminder: "rem-move",
      date: "2026-06-01",
      amount: 400,
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("ambiguous");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should take both sides of a two-sided reminder explicitly", async () => {
    const TRANSFER = makeReminder({
      id: "rem-move",
      outcomeAccount: "acc-checking",
      incomeAccount: "acc-savings",
      outcome: 300,
      income: 300,
    });
    await setup({ reminders: [TRANSFER] });

    await callTool("add_reminder_marker", {
      reminder: "rem-move",
      date: "2026-06-01",
      outcome_amount: 420,
      income_amount: 400,
    });

    const sent = pushedMarker();
    expect(sent.outcome).toBe(420);
    expect(sent.income).toBe(400);
  });

  it("should refuse to plan the same reminder twice on one day", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });

    const result = await callTool("add_reminder_marker", {
      reminder: "rem-rent",
      date: "2026-04-01",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("already planned");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should plan a day an occurrence has already been processed on", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });

    // mk-rent-0 sits on 2026-03-01 but is processed, so the day is free again.
    const result = await callTool("add_reminder_marker", {
      reminder: "rem-rent",
      date: "2026-03-01",
    });

    expect(result.isError).toBeFalsy();
    expect(pushedMarker().date).toBe("2026-03-01");
  });

  it("should report an unknown reminder", async () => {
    await setup({ reminders: [RENT] });

    const result = await callTool("add_reminder_marker", {
      reminder: "nothing like this",
      date: "2026-06-01",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("add_reminder");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should refuse to guess between several matches", async () => {
    await setup({ reminders: [RENT, GYM] });

    const result = await callTool("add_reminder_marker", {
      reminder: "n",
      date: "2026-06-01",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("matches 2 reminders");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should override category, payee and notify", async () => {
    await setup({ reminders: [RENT] });

    await callTool("add_reminder_marker", {
      reminder: "rem-rent",
      date: "2026-06-01",
      category: "Food",
      payee: MERCHANT_CAFE.title,
      notify: true,
    });

    const sent = pushedMarker();
    expect(sent.tag).toEqual([FOOD.id]);
    expect(sent.merchant).toBe(MERCHANT_CAFE.id);
    expect(sent.payee).toBe(MERCHANT_CAFE.title);
    expect(sent.notify).toBe(true);
  });

  it("should clear an inherited value given an empty override", async () => {
    const TAGGED = makeReminder({
      id: "rem-tagged",
      outcome: 40,
      tag: [FOOD.id],
      payee: "City Gym",
      comment: "membership",
    });
    await setup({ reminders: [TAGGED] });

    await callTool("add_reminder_marker", {
      reminder: "rem-tagged",
      date: "2026-06-01",
      category: "",
      comment: "",
    });

    const sent = pushedMarker();
    expect(sent.tag).toBeNull();
    expect(sent.comment).toBeNull();
    // Untouched fields still come from the reminder.
    expect(sent.payee).toBe("City Gym");
  });

  it("should reject an unknown category override", async () => {
    await setup({ reminders: [RENT] });

    const result = await callTool("add_reminder_marker", {
      reminder: "rem-rent",
      date: "2026-06-01",
      category: "Yachts",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("not found");
    expect(api.diff).not.toHaveBeenCalled();
  });

  it("should report a failed push without recording the occurrence", async () => {
    await setup({ reminders: [RENT], reminderMarkers: RENT_MARKERS });
    vi.mocked(api.diff).mockRejectedValue(new Error("server on fire"));

    const result = await callTool("add_reminder_marker", {
      reminder: "rem-rent",
      date: "2026-06-01",
    });

    expect(result.isError).toBe(true);
    expect(getTextContent(result)).toContain("server on fire");
    expect(state.reminderMarkers).toHaveLength(3);
  });
});
