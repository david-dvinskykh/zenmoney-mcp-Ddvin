import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { InMemoryTransport } from "@modelcontextprotocol/sdk/inMemory.js";
import type { Reminder, ReminderMarker, ZenMoneyAPI } from "../src/api.js";
import { ZenState } from "../src/state.js";
import { registerReminderTools } from "../src/tools/reminders.js";
import {
  makeDiffResponse,
  makeReminder,
  makeReminderMarker,
  CHECKING,
  SAVINGS,
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
  syncError?: Error;
}) {
  const diffResp = makeDiffResponse({
    account: [CHECKING, SAVINGS],
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
