import { z } from "zod";
import { randomUUID } from "node:crypto";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type {
  Deletion,
  Reminder,
  ReminderMarker,
  ZenMoneyAPI,
} from "../api.js";
import type { ZenState } from "../state.js";
import { ensureSynced } from "./ensure-synced.js";
import { summarizeTransaction } from "./format.js";
import {
  resolveAccount,
  resolveMerchantByTitle,
  resolveTag,
} from "./resolve.js";

function textResult(text: string, isError = false) {
  return {
    content: [{ type: "text" as const, text }],
    ...(isError ? { isError: true } : {}),
  };
}

function today(): string {
  return new Date().toISOString().slice(0, 10);
}

export function registerReminderTools(
  server: McpServer,
  api: ZenMoneyAPI,
  state: ZenState
) {
  server.tool(
    "list_reminders",
    "List planned transactions (ZenMoney reminders) — recurring ones like rent or a subscription, and one-off entries scheduled for a future date. Shows the next planned occurrence, the amount, the schedule and the id needed by delete_reminder.",
    {
      limit: z
        .number()
        .optional()
        .default(50)
        .describe("Maximum number of reminders to return"),
      upcoming_only: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Only reminders that still have a planned occurrence ahead, skipping series that have already run out."
        ),
    },
    async ({ limit, upcoming_only }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const now = today();
      let reminders = state.reminders.map((r) => ({
        reminder: r,
        next: nextOccurrence(state, r.id, now),
      }));

      if (upcoming_only) reminders = reminders.filter((r) => r.next !== null);

      if (reminders.length === 0) {
        return textResult(
          upcoming_only
            ? "No reminders with an upcoming occurrence."
            : "No reminders. Planned transactions created in the ZenMoney app show up here."
        );
      }

      // Soonest first; series with nothing planned ahead sink to the bottom.
      reminders.sort((a, b) => (a.next ?? "9999-99-99").localeCompare(b.next ?? "9999-99-99"));

      const shown = reminders.slice(0, limit);
      const lines = shown.map(
        ({ reminder, next }) => `- ${formatReminderLine(state, reminder, next)}`
      );

      const omitted = reminders.length - shown.length;
      return textResult(
        `${reminders.length} reminder${reminders.length > 1 ? "s" : ""}:\n\n${lines.join("\n")}` +
          (omitted > 0 ? `\n\n(${omitted} more — raise limit to see them)` : "")
      );
    }
  );

  server.tool(
    "delete_reminder",
    "Permanently delete a planned transaction (ZenMoney reminder). For a recurring series this removes the series and every occurrence still planned; transactions already created from past occurrences are left alone. The first call previews what would be deleted and changes nothing; repeat it with confirm=true to actually delete. Deletion cannot be undone.",
    {
      name_or_id: z
        .string()
        .describe(
          "Reminder UUID from list_reminders, or text matched loosely against its payee, comment, merchant and category."
        ),
      confirm: z
        .boolean()
        .optional()
        .default(false)
        .describe(
          "Set to true to actually delete. When false (the default) the tool only reports what would be deleted."
        ),
    },
    async ({ name_or_id, confirm }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const matches = findReminders(state, name_or_id);

      if (matches.length === 0) {
        return textResult(
          `No reminder matching "${name_or_id}" found. Nothing was deleted. Use list_reminders to see what exists.`,
          true
        );
      }

      if (matches.length > 1) {
        const now = today();
        const candidates = matches
          .map((r) => `- ${formatReminderLine(state, r, nextOccurrence(state, r.id, now))}`)
          .join("\n");
        return textResult(
          `"${name_or_id}" matches ${matches.length} reminders:\n\n${candidates}\n\n` +
            "Nothing was deleted. Call delete_reminder again with the id of the one you mean.",
          true
        );
      }

      const target = matches[0];
      const impact = describeImpact(state, target);

      if (!confirm) {
        return textResult(
          `About to delete reminder:\n\n- ${formatReminderLine(state, target, nextOccurrence(state, target.id, today()))}\n\n${impact}\n\n` +
            "Nothing has been deleted yet. Call delete_reminder again with confirm=true to delete permanently."
        );
      }

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }

      const stamp = Math.floor(Date.now() / 1000);
      // Only the reminder is sent: ZenMoney deletes the markers of a deleted
      // series itself, and applyLocalDeletions mirrors that locally.
      const deletions: Deletion[] = [
        { id: target.id, object: "reminder", stamp, user: user.id },
      ];

      const line = formatReminderLine(
        state,
        target,
        nextOccurrence(state, target.id, today())
      );

      try {
        const resp = await api.diff({
          currentClientTimestamp: stamp,
          serverTimestamp: state.serverTimestamp,
          deletion: deletions,
        });

        await state.applyLocalDeletions(deletions, resp);

        return textResult(`Deleted reminder:\n\n- ${line}\n\n${impact}`);
      } catch (error) {
        return textResult(
          `Failed to delete: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );

  server.tool(
    "add_reminder",
    "Plan a transaction in ZenMoney (a reminder): either a one-off entry dated ahead, or a repeating series like rent, a subscription or a salary. Leave interval out for a one-off; pass interval (and step) to repeat. ZenMoney expands a series into dated occurrences itself — list_reminders shows them, add_reminder_marker adds an extra one, delete_reminder removes the series.",
    {
      type: z
        .enum(["expense", "income", "transfer"])
        .describe("What the planned operation does with money"),
      account: z
        .string()
        .optional()
        .describe("Account name or UUID — for an expense or income reminder"),
      from_account: z
        .string()
        .optional()
        .describe("Source account name or UUID — for a transfer reminder"),
      to_account: z
        .string()
        .optional()
        .describe("Destination account name or UUID — for a transfer reminder"),
      amount: z
        .number()
        .positive()
        .optional()
        .describe(
          "Planned amount. On a transfer it is the amount leaving the source account (alias for outcome_amount)."
        ),
      outcome_amount: z
        .number()
        .positive()
        .optional()
        .describe("Amount debited from the source account, in that account's currency"),
      income_amount: z
        .number()
        .positive()
        .optional()
        .describe(
          "Amount credited to the destination account, in that account's currency. Required for a cross-currency transfer."
        ),
      start_date: z
        .string()
        .regex(/^\d{4}-\d{2}-\d{2}$/, "Date must be in YYYY-MM-DD format")
        .describe(
          "First planned date, YYYY-MM-DD. For a one-off reminder this is the date it falls on."
        ),
      end_date: z
        .string()
        .regex(/^\d{4}-\d{2}-\d{2}$/, "Date must be in YYYY-MM-DD format")
        .optional()
        .describe(
          "Last date the series may fire, YYYY-MM-DD. Omit to leave a series open-ended."
        ),
      interval: z
        .enum(["day", "week", "month", "year"])
        .optional()
        .describe("Repeat unit. Omit for a one-off planned transaction."),
      step: z
        .number()
        .int()
        .positive()
        .optional()
        .describe(
          "How many intervals between repeats (default 1): step=2 with interval=week is fortnightly."
        ),
      points: z
        .array(z.number().int().nonnegative())
        .optional()
        .describe(
          "Advanced. Which interval units inside the step window fire, counted from start_date and zero-based, so each one is below step. Defaults to [0] — once per window. interval=day, step=7, points=[0,2,4] repeats weekly on the start weekday plus two and four days later."
        ),
      category: z.string().optional().describe("Category name or UUID"),
      payee: z.string().optional().describe("Payee/payer name"),
      comment: z.string().optional().describe("Comment for the planned operation"),
      notify: z
        .boolean()
        .optional()
        .default(true)
        .describe(
          "Whether ZenMoney notifies about each occurrence (default true, as in the app)."
        ),
    },
    async ({
      type,
      account,
      from_account,
      to_account,
      amount,
      outcome_amount,
      income_amount,
      start_date,
      end_date,
      interval,
      step,
      points,
      category,
      payee,
      comment,
      notify,
    }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }

      const legs = resolveLegs(state, user.currency, {
        type,
        account,
        from_account,
        to_account,
        amount,
        outcome_amount,
        income_amount,
      });
      if ("error" in legs) return textResult(legs.error, true);

      const schedule = resolveSchedule({ interval, step, points, start_date, end_date });
      if ("error" in schedule) return textResult(schedule.error, true);

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
      const merchant = payee ? resolveMerchantByTitle(state, payee) : undefined;
      const now = Math.floor(Date.now() / 1000);

      const reminder: Reminder = {
        id: randomUUID(),
        changed: now,
        user: user.id,
        incomeInstrument: legs.incomeInstrument,
        incomeAccount: legs.incomeAccount,
        income: legs.income,
        outcomeInstrument: legs.outcomeInstrument,
        outcomeAccount: legs.outcomeAccount,
        outcome: legs.outcome,
        tag: tagIds,
        merchant: merchant?.id ?? null,
        payee: payee ?? null,
        comment: comment ?? null,
        interval: schedule.interval,
        step: schedule.step,
        points: schedule.points,
        startDate: start_date,
        endDate: schedule.endDate,
        notify,
      };

      try {
        const resp = await api.diff({
          currentClientTimestamp: now,
          serverTimestamp: state.serverTimestamp,
          reminder: [reminder],
        });

        await state.applyLocalReminder(reminder, resp);

        const dates = plannedDates(state, reminder.id);
        return textResult(
          `Reminder added:\n\n- ${formatReminderLine(state, reminder, dates[0] ?? null)}\n\n${describeOccurrences(dates)}`
        );
      } catch (error) {
        return textResult(
          `Failed to add reminder: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );

  server.tool(
    "add_reminder_marker",
    "Add one planned occurrence (a ZenMoney reminder marker) to a reminder that already exists — an extra rent month, a one-off top-up of a subscription series. It copies the reminder's amount, accounts, category, payee and comment unless you override them. To plan something that has no reminder yet, use add_reminder instead: every occurrence has to belong to one.",
    {
      reminder: z
        .string()
        .describe(
          "Reminder UUID from list_reminders, or text matched loosely against its payee, comment, merchant and category."
        ),
      date: z
        .string()
        .regex(/^\d{4}-\d{2}-\d{2}$/, "Date must be in YYYY-MM-DD format")
        .describe("Date of the occurrence, YYYY-MM-DD"),
      amount: z
        .number()
        .positive()
        .optional()
        .describe(
          "Override the reminder's amount. Only for a one-sided reminder (an expense or an income) — on a transfer, pass outcome_amount and income_amount instead."
        ),
      outcome_amount: z
        .number()
        .positive()
        .optional()
        .describe("Override the amount leaving the source account"),
      income_amount: z
        .number()
        .positive()
        .optional()
        .describe("Override the amount arriving on the destination account"),
      category: z
        .string()
        .optional()
        .describe("Override the category (name or UUID). An empty string drops it."),
      payee: z
        .string()
        .optional()
        .describe("Override the payee/payer name. An empty string drops it."),
      comment: z
        .string()
        .optional()
        .describe("Override the comment. An empty string drops it."),
      notify: z
        .boolean()
        .optional()
        .describe("Whether ZenMoney notifies about it (defaults to the reminder's own setting)"),
    },
    async ({
      reminder,
      date,
      amount,
      outcome_amount,
      income_amount,
      category,
      payee,
      comment,
      notify,
    }) => {
      const syncError = await ensureSynced(state);
      if (syncError) return syncError;

      const user = state.getUser();
      if (!user) {
        return textResult(
          "User not found. Try sync_data with force_full=true.",
          true
        );
      }

      const matches = findReminders(state, reminder);

      if (matches.length === 0) {
        return textResult(
          `No reminder matching "${reminder}" found. Nothing was added. Use list_reminders to see what exists, or add_reminder to create a new series.`,
          true
        );
      }

      if (matches.length > 1) {
        const now = today();
        const candidates = matches
          .map((r) => `- ${formatReminderLine(state, r, nextOccurrence(state, r.id, now))}`)
          .join("\n");
        return textResult(
          `"${reminder}" matches ${matches.length} reminders:\n\n${candidates}\n\n` +
            "Nothing was added. Call add_reminder_marker again with the id of the one you mean.",
          true
        );
      }

      const target = matches[0];

      const clash = state.reminderMarkers.find(
        (m) => m.reminder === target.id && m.date === date && m.state === "planned"
      );
      if (clash) {
        return textResult(
          `That reminder is already planned for ${date}. Nothing was added — ZenMoney would show the day twice.`,
          true
        );
      }

      const amounts = overrideAmounts(target, {
        amount,
        outcome_amount,
        income_amount,
      });
      if ("error" in amounts) return textResult(amounts.error, true);

      let tagIds = target.tag;
      if (category !== undefined) {
        if (category.trim() === "") {
          tagIds = null;
        } else {
          const resolved = resolveTag(state, category);
          if (!resolved) {
            return textResult(
              `Category "${category}" not found. Use list_categories to see available categories.`,
              true
            );
          }
          tagIds = resolved;
        }
      }

      let payeeValue = target.payee;
      let merchant = target.merchant;
      if (payee !== undefined) {
        const named = payee.trim() !== "";
        payeeValue = named ? payee : null;
        merchant = named
          ? (resolveMerchantByTitle(state, payee)?.id ?? null)
          : null;
      }

      const commentValue =
        comment === undefined ? target.comment : comment.trim() === "" ? null : comment;

      const stamp = Math.floor(Date.now() / 1000);

      const marker: ReminderMarker = {
        id: randomUUID(),
        changed: stamp,
        user: user.id,
        incomeInstrument: target.incomeInstrument,
        incomeAccount: target.incomeAccount,
        income: amounts.income,
        outcomeInstrument: target.outcomeInstrument,
        outcomeAccount: target.outcomeAccount,
        outcome: amounts.outcome,
        tag: tagIds,
        merchant,
        payee: payeeValue,
        comment: commentValue,
        date,
        reminder: target.id,
        state: "planned",
        notify: notify ?? target.notify,
      };

      try {
        const resp = await api.diff({
          currentClientTimestamp: stamp,
          serverTimestamp: state.serverTimestamp,
          reminderMarker: [marker],
        });

        await state.applyLocalReminderMarker(marker, resp);

        return textResult(
          `Occurrence added:\n\n- ${formatMarkerLine(state, marker)}\n\n` +
            `It belongs to reminder \`${target.id}\` (${describeSchedule(target)}).`
        );
      } catch (error) {
        return textResult(
          `Failed to add occurrence: ${error instanceof Error ? error.message : String(error)}`,
          true
        );
      }
    }
  );
}

/** Earliest planned occurrence on or after `from`, or null if none is left. */
function nextOccurrence(
  state: ZenState,
  reminderId: string,
  from: string
): string | null {
  const dates = state.reminderMarkers
    .filter(
      (m) =>
        m.reminder === reminderId && m.state === "planned" && m.date >= from
    )
    .map((m) => m.date)
    .sort();
  return dates[0] ?? null;
}

/**
 * Match by id first — an exact id is never ambiguous — then loosely by the
 * text a person would recognise the reminder from. Every loose match is
 * returned so the caller can refuse to guess between them.
 */
function findReminders(state: ZenState, nameOrId: string): Reminder[] {
  const byId = state.reminders.find((r) => r.id === nameOrId);
  if (byId) return [byId];

  const needle = nameOrId.toLowerCase();
  return state.reminders.filter((r) => {
    const merchant = r.merchant
      ? state.merchants.find((m) => m.id === r.merchant)?.title
      : undefined;
    const categories = (r.tag ?? []).map(
      (id) => state.tags.find((t) => t.id === id)?.title ?? ""
    );
    return [r.payee, r.comment, merchant, ...categories].some((field) =>
      field?.toLowerCase().includes(needle)
    );
  });
}

/** Human-readable repeat rule. */
function describeSchedule(r: Reminder): string {
  if (!r.interval) return `one-off on ${r.startDate}`;

  const every =
    r.step && r.step > 1
      ? `every ${r.step} ${r.interval}s`
      : `every ${r.interval}`;
  return r.endDate ? `${every} until ${r.endDate}` : every;
}

/** One-line rendering used by list_reminders and the delete preview. */
export function formatReminderLine(
  state: ZenState,
  r: Reminder,
  next: string | null
): string {
  const { kind, amount, categories, payee, comment } = summarizeTransaction(
    state,
    r
  );
  const commentStr = comment ? ` — "${comment}"` : "";
  const when = next ?? "nothing planned";
  return `${when.padEnd(10)} | ${kind.padEnd(8)} | ${amount.padEnd(20)} | ${categories.padEnd(15)} | ${payee}${commentStr} | ${describeSchedule(r)} | id: \`${r.id}\``;
}

/** Spell out what else disappears, so the confirmation is an informed one. */
function describeImpact(state: ZenState, r: Reminder): string {
  const planned = state.reminderMarkers.filter(
    (m) => m.reminder === r.id && m.state === "planned"
  ).length;

  const occurrences =
    planned > 0
      ? `${planned} planned occurrence${planned > 1 ? "s" : ""} will be deleted with it`
      : "it has no planned occurrences left";

  return `${occurrences}; transactions already created from it stay.`;
}

/** Dates of every occurrence still planned for a reminder, soonest first. */
function plannedDates(state: ZenState, reminderId: string): string[] {
  return state.reminderMarkers
    .filter((m) => m.reminder === reminderId && m.state === "planned")
    .map((m) => m.date)
    .sort();
}

/**
 * Report the dates a new reminder came back with. ZenMoney expands the series
 * itself, so an empty list is not an error — the markers simply have not
 * arrived yet.
 */
function describeOccurrences(dates: string[]): string {
  if (dates.length === 0) {
    return (
      "No occurrences came back with it yet — ZenMoney expands a series on its own side. " +
      "Run sync_data, or list_reminders in a moment, to see the dates."
    );
  }
  const shown = dates.slice(0, 5).join(", ");
  const rest = dates.length - 5;
  return `Planned: ${shown}${rest > 0 ? ` (+${rest} more)` : ""}.`;
}

/** One-line rendering of a single occurrence, matching the reminder's own. */
export function formatMarkerLine(state: ZenState, m: ReminderMarker): string {
  const { kind, amount, categories, payee, comment } = summarizeTransaction(
    state,
    m
  );
  const commentStr = comment ? ` — "${comment}"` : "";
  return `${m.date} | ${kind.padEnd(8)} | ${amount.padEnd(20)} | ${categories.padEnd(15)} | ${payee}${commentStr} | id: \`${m.id}\``;
}

interface LegInput {
  type: "expense" | "income" | "transfer";
  account?: string;
  from_account?: string;
  to_account?: string;
  amount?: number;
  outcome_amount?: number;
  income_amount?: number;
}

interface Legs {
  incomeAccount: string;
  income: number;
  incomeInstrument: number;
  outcomeAccount: string;
  outcome: number;
  outcomeInstrument: number;
}

/**
 * Turn the caller's account names and amounts into the two legs every
 * ZenMoney operation carries. An expense and an income both sit on a single
 * account with the other side zeroed; a transfer spans two, and a
 * cross-currency one needs both amounts spelled out because no rate is stored.
 */
function resolveLegs(
  state: ZenState,
  userCurrency: number,
  input: LegInput
): Legs | { error: string } {
  const instrumentOf = (a: { instrument: number | null }) =>
    a.instrument ?? userCurrency ?? 1;

  if (input.type === "transfer") {
    if (!input.from_account || !input.to_account) {
      return {
        error:
          "A transfer reminder needs both from_account and to_account. Use 'account' for an expense or an income instead.",
      };
    }

    const fromAcc = resolveAccount(state, input.from_account);
    if (!fromAcc) {
      return { error: `Source account "${input.from_account}" not found.` };
    }
    const toAcc = resolveAccount(state, input.to_account);
    if (!toAcc) {
      return { error: `Destination account "${input.to_account}" not found.` };
    }

    const outcome = input.outcome_amount ?? input.amount;
    if (!outcome) {
      return {
        error: "Either 'amount' or 'outcome_amount' must be provided.",
      };
    }

    const outcomeInstrument = instrumentOf(fromAcc);
    const incomeInstrument = instrumentOf(toAcc);

    if (outcomeInstrument !== incomeInstrument && !input.income_amount) {
      const fromInstr = state.getInstrument(outcomeInstrument);
      const toInstr = state.getInstrument(incomeInstrument);
      return {
        error: `Cross-currency transfer: source account is ${fromInstr?.shortTitle ?? "?"} and destination is ${toInstr?.shortTitle ?? "?"}. Please provide income_amount (the amount in ${toInstr?.shortTitle ?? "destination currency"}).`,
      };
    }

    return {
      outcomeAccount: fromAcc.id,
      outcome,
      outcomeInstrument,
      incomeAccount: toAcc.id,
      income: input.income_amount ?? outcome,
      incomeInstrument,
    };
  }

  if (!input.account) {
    return {
      error: `An ${input.type} reminder needs 'account'. Use from_account and to_account for a transfer instead.`,
    };
  }

  const acc = resolveAccount(state, input.account);
  if (!acc) {
    return {
      error: `Account "${input.account}" not found. Use list_accounts to see available accounts.`,
    };
  }

  const isIncome = input.type === "income";
  const amount =
    input.amount ?? (isIncome ? input.income_amount : input.outcome_amount);
  if (!amount) {
    return { error: `An ${input.type} reminder needs 'amount'.` };
  }

  const instrument = instrumentOf(acc);
  return {
    incomeAccount: acc.id,
    income: isIncome ? amount : 0,
    incomeInstrument: instrument,
    outcomeAccount: acc.id,
    outcome: isIncome ? 0 : amount,
    outcomeInstrument: instrument,
  };
}

interface ScheduleInput {
  interval?: "day" | "week" | "month" | "year";
  step?: number;
  points?: number[];
  start_date: string;
  end_date?: string;
}

interface Schedule {
  interval: string | null;
  step: number | null;
  points: number[] | null;
  endDate: string | null;
}

/**
 * Normalize the repeat rule. A one-off keeps all three schedule fields null
 * and ends on the day it falls, so nothing can expand it further; a series
 * defaults to one occurrence per window, which is what `points: [0]` means.
 */
function resolveSchedule(input: ScheduleInput): Schedule | { error: string } {
  if (input.end_date && input.end_date < input.start_date) {
    return {
      error: `end_date (${input.end_date}) must be on or after start_date (${input.start_date}).`,
    };
  }

  if (!input.interval) {
    if (input.step !== undefined || input.points !== undefined) {
      return {
        error:
          "step and points only apply to a repeating reminder. Pass interval as well, or drop them for a one-off.",
      };
    }
    // A one-off is a series that ends the day it starts.
    return {
      interval: null,
      step: null,
      points: null,
      endDate: input.end_date ?? input.start_date,
    };
  }

  const step = input.step ?? 1;
  const points = input.points ?? [0];

  if (points.length === 0) {
    return { error: "points must name at least one position, e.g. [0]." };
  }

  const outOfRange = points.filter((p) => p >= step);
  if (outOfRange.length > 0) {
    return {
      error: `points are counted inside the step window, so each one must be below step (${step}): ${outOfRange.join(", ")} ${outOfRange.length > 1 ? "are" : "is"} too large. With step=${step} the valid points are 0…${step - 1}.`,
    };
  }

  const deduped = Array.from(new Set(points)).sort((a, b) => a - b);

  return {
    interval: input.interval,
    step,
    points: deduped,
    endDate: input.end_date ?? null,
  };
}

/**
 * Work out what an occurrence should carry, starting from the reminder's own
 * amounts. A transfer has two sides and no rate, so a single `amount` cannot
 * say which one it means.
 */
function overrideAmounts(
  r: Reminder,
  input: { amount?: number; outcome_amount?: number; income_amount?: number }
): { income: number; outcome: number } | { error: string } {
  let income = input.income_amount ?? r.income;
  let outcome = input.outcome_amount ?? r.outcome;

  if (input.amount !== undefined) {
    const twoSided = r.incomeAccount !== r.outcomeAccount;
    if (twoSided) {
      return {
        error:
          "This reminder moves money between two accounts, so 'amount' is ambiguous. Pass outcome_amount and income_amount instead.",
      };
    }
    if (r.income > 0 && r.outcome === 0) {
      income = input.amount;
    } else if (r.outcome > 0 && r.income === 0) {
      outcome = input.amount;
    } else {
      return {
        error:
          "Cannot tell which side of this reminder 'amount' refers to. Pass outcome_amount or income_amount instead.",
      };
    }
  }

  return { income, outcome };
}
