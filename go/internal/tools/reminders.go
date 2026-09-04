package tools

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

type listRemindersArgs struct {
	Limit        *int  `json:"limit,omitempty" jsonschema:"Maximum number of reminders to return"`
	UpcomingOnly *bool `json:"upcoming_only,omitempty" jsonschema:"Only reminders that still have a planned occurrence ahead, skipping series that have already run out."`
}

type deleteReminderArgs struct {
	NameOrID string `json:"name_or_id" jsonschema:"Reminder UUID from list_reminders, or text matched loosely against its payee, comment, merchant and category."`
	Confirm  *bool  `json:"confirm,omitempty" jsonschema:"Set to true to actually delete. When false (the default) the tool only reports what would be deleted."`
}

// plannedReminder pairs a reminder with the date of its next planned occurrence,
// which is empty when the series has run out.
type plannedReminder struct {
	reminder zen.Reminder
	next     string
}

func registerReminders(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_reminders",
		Description: "List planned transactions (ZenMoney reminders) — recurring ones like rent or a " +
			"subscription, and one-off entries scheduled for a future date. Shows the next planned " +
			"occurrence, the amount, the schedule and the id needed by delete_reminder.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listRemindersArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		limit := 50
		if args.Limit != nil {
			limit = *args.Limit
		}
		upcomingOnly := args.UpcomingOnly != nil && *args.UpcomingOnly

		now := today()
		var reminders []plannedReminder
		for _, r := range st.Reminders() {
			next := nextOccurrence(st, r.ID, now)
			if upcomingOnly && next == "" {
				continue
			}
			reminders = append(reminders, plannedReminder{reminder: r, next: next})
		}

		if len(reminders) == 0 {
			if upcomingOnly {
				return textResult("No reminders with an upcoming occurrence."), nil, nil
			}
			return textResult("No reminders. Planned transactions created in the ZenMoney app show up here."), nil, nil
		}

		// Soonest first; series with nothing planned ahead sink to the bottom.
		sort.SliceStable(reminders, func(i, j int) bool {
			return sortKey(reminders[i].next) < sortKey(reminders[j].next)
		})

		shown := reminders
		if len(shown) > limit {
			shown = shown[:limit]
		}

		lines := make([]string, 0, len(shown))
		for _, r := range shown {
			lines = append(lines, "- "+formatReminderLine(st, r.reminder, r.next))
		}

		omitted := len(reminders) - len(shown)
		more := ""
		if omitted > 0 {
			more = "\n\n(" + strconv.Itoa(omitted) + " more — raise limit to see them)"
		}

		return textResult(strconv.Itoa(len(reminders)) + " reminder" + plural(len(reminders), "", "s") +
			":\n\n" + strings.Join(lines, "\n") + more), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_reminder",
		Description: "Permanently delete a planned transaction (ZenMoney reminder). For a recurring series " +
			"this removes the series and every occurrence still planned; transactions already created " +
			"from past occurrences are left alone. The first call previews what would be deleted and " +
			"changes nothing; repeat it with confirm=true to actually delete. Deletion cannot be undone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args deleteReminderArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		matches := findReminders(st, args.NameOrID)

		if len(matches) == 0 {
			return errorResult(`No reminder matching "` + args.NameOrID +
				`" found. Nothing was deleted. Use list_reminders to see what exists.`), nil, nil
		}

		if len(matches) > 1 {
			now := today()
			lines := make([]string, 0, len(matches))
			for _, r := range matches {
				lines = append(lines, "- "+formatReminderLine(st, r, nextOccurrence(st, r.ID, now)))
			}
			return errorResult(`"` + args.NameOrID + `" matches ` + strconv.Itoa(len(matches)) +
				" reminders:\n\n" + strings.Join(lines, "\n") +
				"\n\nNothing was deleted. Call delete_reminder again with the id of the one you mean."), nil, nil
		}

		target := matches[0]
		impact := describeReminderImpact(st, target)
		line := formatReminderLine(st, target, nextOccurrence(st, target.ID, today()))

		if args.Confirm == nil || !*args.Confirm {
			return textResult("About to delete reminder:\n\n- " + line + "\n\n" + impact +
				"\n\nNothing has been deleted yet. Call delete_reminder again with confirm=true " +
				"to delete permanently."), nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		// Only the reminder is sent: ZenMoney deletes the markers of a deleted
		// series itself, and ApplyLocalDeletions mirrors that locally.
		stamp := nowUnix()
		deletions := []zen.Deletion{{ID: target.ID, Object: "reminder", Stamp: stamp, User: user.ID}}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: stamp,
			ServerTimestamp:        st.ServerTimestamp(),
			Deletion:               deletions,
		})
		if err != nil {
			return errorResult("Failed to delete: " + err.Error()), nil, nil
		}
		st.ApplyLocalDeletions(deletions, resp)

		return textResult("Deleted reminder:\n\n- " + line + "\n\n" + impact), nil, nil
	})
}

// nextOccurrence is the earliest planned occurrence on or after from, or "" if
// none is left.
func nextOccurrence(st *zen.State, reminderID, from string) string {
	var dates []string
	for _, m := range st.ReminderMarkers() {
		if m.Reminder == reminderID && m.State == "planned" && m.Date >= from {
			dates = append(dates, m.Date)
		}
	}
	if len(dates) == 0 {
		return ""
	}
	sort.Strings(dates)
	return dates[0]
}

// sortKey pushes reminders with nothing planned ahead to the bottom, the way the
// original's "9999-99-99" placeholder does.
func sortKey(next string) string {
	if next == "" {
		return "9999-99-99"
	}
	return next
}

// findReminders matches by id first — an exact id is never ambiguous — then
// loosely by the text a person would recognise the reminder from. Every loose
// match is returned so the caller can refuse to guess between them.
func findReminders(st *zen.State, nameOrID string) []zen.Reminder {
	reminders := st.Reminders()
	for _, r := range reminders {
		if r.ID == nameOrID {
			return []zen.Reminder{r}
		}
	}

	needle := strings.ToLower(nameOrID)
	var matches []zen.Reminder
	for _, r := range reminders {
		fields := []string{deref(r.Payee), deref(r.Comment)}
		if r.Merchant != nil {
			if m := findMerchant(st, *r.Merchant); m != nil {
				fields = append(fields, m.Title)
			}
		}
		for _, id := range r.Tag {
			if tag := findTag(st, id); tag != nil {
				fields = append(fields, tag.Title)
			} else {
				fields = append(fields, "")
			}
		}
		for _, field := range fields {
			if field != "" && strings.Contains(strings.ToLower(field), needle) {
				matches = append(matches, r)
				break
			}
		}
	}
	return matches
}

// describeSchedule renders the repeat rule in words.
func describeSchedule(r zen.Reminder) string {
	if r.Interval == nil || *r.Interval == "" {
		return "one-off on " + r.StartDate
	}

	every := "every " + *r.Interval
	if r.Step != nil && *r.Step > 1 {
		every = "every " + strconv.FormatInt(*r.Step, 10) + " " + *r.Interval + "s"
	}
	if r.EndDate != nil && *r.EndDate != "" {
		return every + " until " + *r.EndDate
	}
	return every
}

// formatReminderLine is the one-line rendering used by list_reminders and the
// delete preview.
func formatReminderLine(st *zen.State, r zen.Reminder, next string) string {
	s := summarize(st, operationOfReminder(r))
	comment := ""
	if s.Comment != "" {
		comment = ` — "` + s.Comment + `"`
	}
	when := next
	if when == "" {
		when = "nothing planned"
	}
	return padEnd(when, 10) + " | " + padEnd(string(s.Kind), 8) + " | " + padEnd(s.Amount, 20) +
		" | " + padEnd(s.Categories, 15) + " | " + s.Payee + comment + " | " +
		describeSchedule(r) + " | id: `" + r.ID + "`"
}

// describeReminderImpact spells out what else disappears, so the confirmation is
// an informed one.
func describeReminderImpact(st *zen.State, r zen.Reminder) string {
	planned := 0
	for _, m := range st.ReminderMarkers() {
		if m.Reminder == r.ID && m.State == "planned" {
			planned++
		}
	}

	occurrences := "it has no planned occurrences left"
	if planned > 0 {
		occurrences = strconv.Itoa(planned) + " planned occurrence" + plural(planned, "", "s") +
			" will be deleted with it"
	}
	return occurrences + "; transactions already created from it stay."
}
