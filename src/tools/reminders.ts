import { z } from "zod";
import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import type { Deletion, Reminder, ZenMoneyAPI } from "../api.js";
import type { ZenState } from "../state.js";
import { ensureSynced } from "./ensure-synced.js";
import { summarizeTransaction } from "./format.js";

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
