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

type addReminderArgs struct {
	Type          string   `json:"type" jsonschema:"What the planned operation does with money: expense, income or transfer"`
	Account       *string  `json:"account,omitempty" jsonschema:"Account name or UUID — for an expense or income reminder"`
	FromAccount   *string  `json:"from_account,omitempty" jsonschema:"Source account name or UUID — for a transfer reminder"`
	ToAccount     *string  `json:"to_account,omitempty" jsonschema:"Destination account name or UUID — for a transfer reminder"`
	Amount        *float64 `json:"amount,omitempty" jsonschema:"Planned amount. On a transfer it is the amount leaving the source account (alias for outcome_amount)."`
	OutcomeAmount *float64 `json:"outcome_amount,omitempty" jsonschema:"Amount debited from the source account, in that account's currency"`
	IncomeAmount  *float64 `json:"income_amount,omitempty" jsonschema:"Amount credited to the destination account, in that account's currency. Required for a cross-currency transfer."`
	StartDate     string   `json:"start_date" jsonschema:"First planned date, YYYY-MM-DD. For a one-off reminder this is the date it falls on."`
	EndDate       *string  `json:"end_date,omitempty" jsonschema:"Last date the series may fire, YYYY-MM-DD. Omit to leave a series open-ended."`
	Interval      *string  `json:"interval,omitempty" jsonschema:"Repeat unit: day, week, month or year. Omit for a one-off planned transaction."`
	Step          *int64   `json:"step,omitempty" jsonschema:"How many intervals between repeats (default 1): step=2 with interval=week is fortnightly."`
	Points        []int64  `json:"points,omitempty" jsonschema:"Advanced. Which interval units inside the step window fire, counted from start_date and zero-based, so each one is below step. Defaults to [0] — once per window. interval=day, step=7, points=[0,2,4] repeats weekly on the start weekday plus two and four days later."`
	Category      *string  `json:"category,omitempty" jsonschema:"Category name or UUID"`
	Payee         *string  `json:"payee,omitempty" jsonschema:"Payee/payer name"`
	Comment       *string  `json:"comment,omitempty" jsonschema:"Comment for the planned operation"`
	Notify        *bool    `json:"notify,omitempty" jsonschema:"Whether ZenMoney notifies about each occurrence (default true, as in the app)."`
}

type addReminderMarkerArgs struct {
	Reminder      string   `json:"reminder" jsonschema:"Reminder UUID from list_reminders, or text matched loosely against its payee, comment, merchant and category."`
	Date          string   `json:"date" jsonschema:"Date of the occurrence, YYYY-MM-DD"`
	Amount        *float64 `json:"amount,omitempty" jsonschema:"Override the reminder's amount. Only for a one-sided reminder (an expense or an income) — on a transfer, pass outcome_amount and income_amount instead."`
	OutcomeAmount *float64 `json:"outcome_amount,omitempty" jsonschema:"Override the amount leaving the source account"`
	IncomeAmount  *float64 `json:"income_amount,omitempty" jsonschema:"Override the amount arriving on the destination account"`
	Category      *string  `json:"category,omitempty" jsonschema:"Override the category (name or UUID). An empty string drops it."`
	Payee         *string  `json:"payee,omitempty" jsonschema:"Override the payee/payer name. An empty string drops it."`
	Comment       *string  `json:"comment,omitempty" jsonschema:"Override the comment. An empty string drops it."`
	Notify        *bool    `json:"notify,omitempty" jsonschema:"Whether ZenMoney notifies about it (defaults to the reminder's own setting)"`
}

// legs are the two halves every ZenMoney operation carries.
type legs struct {
	incomeAccount     string
	income            float64
	incomeInstrument  int64
	outcomeAccount    string
	outcome           float64
	outcomeInstrument int64
}

// schedule is a normalized repeat rule.
type schedule struct {
	interval *string
	step     *int64
	points   []int64
	endDate  *string
}

var reminderIntervals = map[string]bool{"day": true, "week": true, "month": true, "year": true}

func registerAddReminders(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_reminder",
		Description: "Plan a transaction in ZenMoney (a reminder): either a one-off entry dated ahead, " +
			"or a repeating series like rent, a subscription or a salary. Leave interval out for a " +
			"one-off; pass interval (and step) to repeat. ZenMoney expands a series into dated " +
			"occurrences itself — list_reminders shows them, add_reminder_marker adds an extra one, " +
			"delete_reminder removes the series.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addReminderArgs) (*mcp.CallToolResult, any, error) {
		if args.Type != "expense" && args.Type != "income" && args.Type != "transfer" {
			return errorResult(`Unknown type "` + args.Type +
				`". Use one of: expense, income, transfer.`), nil, nil
		}
		if !dateRE.MatchString(args.StartDate) {
			return errorResult("start_date must be in YYYY-MM-DD format."), nil, nil
		}
		if args.EndDate != nil && !dateRE.MatchString(*args.EndDate) {
			return errorResult("end_date must be in YYYY-MM-DD format."), nil, nil
		}

		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		l, fail := resolveLegs(st, user.Currency, args)
		if fail != "" {
			return errorResult(fail), nil, nil
		}

		sched, fail := resolveSchedule(args)
		if fail != "" {
			return errorResult(fail), nil, nil
		}

		var tagIDs []string
		if args.Category != nil && *args.Category != "" {
			tagIDs = resolveTag(st, *args.Category)
			if tagIDs == nil {
				return errorResult(`Category "` + *args.Category +
					`" not found. Use list_categories to see available categories.`), nil, nil
			}
		}
		var merchant *string
		if args.Payee != nil {
			if m := resolveMerchantByTitle(st, *args.Payee); m != nil {
				merchant = &m.ID
			}
		}
		notify := true
		if args.Notify != nil {
			notify = *args.Notify
		}

		now := nowUnix()
		reminder := zen.Reminder{
			ID:                newUUID(),
			Changed:           now,
			User:              user.ID,
			IncomeInstrument:  l.incomeInstrument,
			IncomeAccount:     l.incomeAccount,
			Income:            l.income,
			OutcomeInstrument: l.outcomeInstrument,
			OutcomeAccount:    l.outcomeAccount,
			Outcome:           l.outcome,
			Tag:               tagIDs,
			Merchant:          merchant,
			Payee:             args.Payee,
			Comment:           args.Comment,
			Interval:          sched.interval,
			Step:              sched.step,
			Points:            sched.points,
			StartDate:         args.StartDate,
			EndDate:           sched.endDate,
			Notify:            notify,
		}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: now,
			ServerTimestamp:        st.ServerTimestamp(),
			Reminder:               []zen.Reminder{reminder},
		})
		if err != nil {
			return errorResult("Failed to add reminder: " + err.Error()), nil, nil
		}
		st.ApplyLocalReminder(reminder, resp)

		dates := plannedDates(st, reminder.ID)
		next := ""
		if len(dates) > 0 {
			next = dates[0]
		}
		return textResult("Reminder added:\n\n- " + formatReminderLine(st, reminder, next) +
			"\n\n" + describeOccurrences(dates)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "add_reminder_marker",
		Description: "Add one planned occurrence (a ZenMoney reminder marker) to a reminder that already " +
			"exists — an extra rent month, a one-off top-up of a subscription series. It copies the " +
			"reminder's amount, accounts, category, payee and comment unless you override them. To plan " +
			"something that has no reminder yet, use add_reminder instead: every occurrence has to " +
			"belong to one.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addReminderMarkerArgs) (*mcp.CallToolResult, any, error) {
		if !dateRE.MatchString(args.Date) {
			return errorResult("date must be in YYYY-MM-DD format."), nil, nil
		}

		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		matches := findReminders(st, args.Reminder)

		if len(matches) == 0 {
			return errorResult(`No reminder matching "` + args.Reminder +
				`" found. Nothing was added. Use list_reminders to see what exists, or add_reminder ` +
				"to create a new series."), nil, nil
		}

		if len(matches) > 1 {
			now := today()
			lines := make([]string, 0, len(matches))
			for _, r := range matches {
				lines = append(lines, "- "+formatReminderLine(st, r, nextOccurrence(st, r.ID, now)))
			}
			return errorResult(`"` + args.Reminder + `" matches ` + strconv.Itoa(len(matches)) +
				" reminders:\n\n" + strings.Join(lines, "\n") +
				"\n\nNothing was added. Call add_reminder_marker again with the id of the one you mean."), nil, nil
		}

		target := matches[0]

		for _, m := range st.ReminderMarkers() {
			if m.Reminder == target.ID && m.Date == args.Date && m.State == "planned" {
				return errorResult("That reminder is already planned for " + args.Date +
					". Nothing was added — ZenMoney would show the day twice."), nil, nil
			}
		}

		income, outcome, fail := overrideAmounts(target, args)
		if fail != "" {
			return errorResult(fail), nil, nil
		}

		tagIDs := target.Tag
		if args.Category != nil {
			if strings.TrimSpace(*args.Category) == "" {
				tagIDs = nil
			} else {
				resolved := resolveTag(st, *args.Category)
				if resolved == nil {
					return errorResult(`Category "` + *args.Category +
						`" not found. Use list_categories to see available categories.`), nil, nil
				}
				tagIDs = resolved
			}
		}

		payee := target.Payee
		merchant := target.Merchant
		if args.Payee != nil {
			payee, merchant = nil, nil
			if strings.TrimSpace(*args.Payee) != "" {
				payee = args.Payee
				if m := resolveMerchantByTitle(st, *args.Payee); m != nil {
					merchant = &m.ID
				}
			}
		}

		comment := target.Comment
		if args.Comment != nil {
			comment = args.Comment
			if strings.TrimSpace(*args.Comment) == "" {
				comment = nil
			}
		}
		notify := target.Notify
		if args.Notify != nil {
			notify = *args.Notify
		}

		stamp := nowUnix()
		marker := zen.ReminderMarker{
			ID:                newUUID(),
			Changed:           stamp,
			User:              user.ID,
			IncomeInstrument:  target.IncomeInstrument,
			IncomeAccount:     target.IncomeAccount,
			Income:            income,
			OutcomeInstrument: target.OutcomeInstrument,
			OutcomeAccount:    target.OutcomeAccount,
			Outcome:           outcome,
			Tag:               tagIDs,
			Merchant:          merchant,
			Payee:             payee,
			Comment:           comment,
			Date:              args.Date,
			Reminder:          target.ID,
			State:             "planned",
			Notify:            notify,
		}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: stamp,
			ServerTimestamp:        st.ServerTimestamp(),
			ReminderMarker:         []zen.ReminderMarker{marker},
		})
		if err != nil {
			return errorResult("Failed to add occurrence: " + err.Error()), nil, nil
		}
		st.ApplyLocalReminderMarker(marker, resp)

		return textResult("Occurrence added:\n\n- " + formatMarkerLine(st, marker) +
			"\n\nIt belongs to reminder `" + target.ID + "` (" + describeSchedule(target) + ")."), nil, nil
	})
}

// plannedDates lists the dates of every occurrence still planned for a
// reminder, soonest first.
func plannedDates(st *zen.State, reminderID string) []string {
	var dates []string
	for _, m := range st.ReminderMarkers() {
		if m.Reminder == reminderID && m.State == "planned" {
			dates = append(dates, m.Date)
		}
	}
	sort.Strings(dates)
	return dates
}

// describeOccurrences reports the dates a new reminder came back with. ZenMoney
// expands the series itself, so an empty list is not an error — the markers
// simply have not arrived yet.
func describeOccurrences(dates []string) string {
	if len(dates) == 0 {
		return "No occurrences came back with it yet — ZenMoney expands a series on its own side. " +
			"Run sync_data, or list_reminders in a moment, to see the dates."
	}
	shown := dates
	rest := 0
	if len(shown) > 5 {
		rest = len(shown) - 5
		shown = shown[:5]
	}
	more := ""
	if rest > 0 {
		more = " (+" + strconv.Itoa(rest) + " more)"
	}
	return "Planned: " + strings.Join(shown, ", ") + more + "."
}

// formatMarkerLine is the one-line rendering of a single occurrence, matching
// the reminder's own.
func formatMarkerLine(st *zen.State, m zen.ReminderMarker) string {
	s := summarize(st, operationOfMarker(m))
	comment := ""
	if s.Comment != "" {
		comment = ` — "` + s.Comment + `"`
	}
	return m.Date + " | " + padEnd(string(s.Kind), 8) + " | " + padEnd(s.Amount, 20) +
		" | " + padEnd(s.Categories, 15) + " | " + s.Payee + comment + " | id: `" + m.ID + "`"
}

// resolveLegs turns the caller's account names and amounts into the two legs
// every ZenMoney operation carries. An expense and an income both sit on a
// single account with the other side zeroed; a transfer spans two, and a
// cross-currency one needs both amounts spelled out because no rate is stored.
// The second return value is a tool-error message, empty when the legs are good.
func resolveLegs(st *zen.State, userCurrency int64, args addReminderArgs) (legs, string) {
	if args.Type == "transfer" {
		if args.FromAccount == nil || args.ToAccount == nil {
			return legs{}, "A transfer reminder needs both from_account and to_account. " +
				"Use 'account' for an expense or an income instead."
		}

		fromAcc := resolveAccount(st, *args.FromAccount)
		if fromAcc == nil {
			return legs{}, `Source account "` + *args.FromAccount + `" not found.`
		}
		toAcc := resolveAccount(st, *args.ToAccount)
		if toAcc == nil {
			return legs{}, `Destination account "` + *args.ToAccount + `" not found.`
		}

		outcome := args.OutcomeAmount
		if outcome == nil {
			outcome = args.Amount
		}
		if outcome == nil || *outcome <= 0 {
			return legs{}, "Either 'amount' or 'outcome_amount' must be provided."
		}

		outcomeInstrument := instrumentOf(fromAcc, userCurrency)
		incomeInstrument := instrumentOf(toAcc, userCurrency)

		if outcomeInstrument != incomeInstrument && args.IncomeAmount == nil {
			from := orQuestionMark(st.InstrumentTitle(outcomeInstrument))
			to := st.InstrumentTitle(incomeInstrument)
			return legs{}, "Cross-currency transfer: source account is " + from +
				" and destination is " + orQuestionMark(to) +
				". Please provide income_amount (the amount in " +
				orDefault(to, "destination currency") + ")."
		}

		income := *outcome
		if args.IncomeAmount != nil {
			income = *args.IncomeAmount
		}

		return legs{
			outcomeAccount:    fromAcc.ID,
			outcome:           *outcome,
			outcomeInstrument: outcomeInstrument,
			incomeAccount:     toAcc.ID,
			income:            income,
			incomeInstrument:  incomeInstrument,
		}, ""
	}

	if args.Account == nil {
		// Both words this ever sees — expense, income — start with a vowel.
		return legs{}, "An " + args.Type + " reminder needs 'account'. " +
			"Use from_account and to_account for a transfer instead."
	}

	acc := resolveAccount(st, *args.Account)
	if acc == nil {
		return legs{}, `Account "` + *args.Account +
			`" not found. Use list_accounts to see available accounts.`
	}

	isIncome := args.Type == "income"
	amount := args.Amount
	if amount == nil {
		if isIncome {
			amount = args.IncomeAmount
		} else {
			amount = args.OutcomeAmount
		}
	}
	if amount == nil || *amount <= 0 {
		return legs{}, "An " + args.Type + " reminder needs 'amount'."
	}

	instrument := instrumentOf(acc, userCurrency)
	l := legs{
		incomeAccount:     acc.ID,
		incomeInstrument:  instrument,
		outcomeAccount:    acc.ID,
		outcomeInstrument: instrument,
	}
	if isIncome {
		l.income = *amount
	} else {
		l.outcome = *amount
	}
	return l, ""
}

// resolveSchedule normalizes the repeat rule. A one-off keeps all three
// schedule fields nil and ends on the day it falls, so nothing can expand it
// further; a series defaults to one occurrence per window, which is what
// points [0] means. The second return value is a tool-error message.
func resolveSchedule(args addReminderArgs) (schedule, string) {
	if args.EndDate != nil && *args.EndDate < args.StartDate {
		return schedule{}, "end_date (" + *args.EndDate + ") must be on or after start_date (" +
			args.StartDate + ")."
	}

	if args.Interval == nil || *args.Interval == "" {
		if args.Step != nil || args.Points != nil {
			return schedule{}, "step and points only apply to a repeating reminder. " +
				"Pass interval as well, or drop them for a one-off."
		}
		// A one-off is a series that ends the day it starts.
		endDate := args.StartDate
		if args.EndDate != nil {
			endDate = *args.EndDate
		}
		return schedule{endDate: &endDate}, ""
	}

	if !reminderIntervals[*args.Interval] {
		return schedule{}, `Unknown interval "` + *args.Interval +
			`". Use one of: day, week, month, year.`
	}

	step := int64(1)
	if args.Step != nil {
		step = *args.Step
	}
	if step < 1 {
		return schedule{}, "step must be at least 1."
	}

	points := args.Points
	if points == nil {
		points = []int64{0}
	}
	if len(points) == 0 {
		return schedule{}, "points must name at least one position, e.g. [0]."
	}

	var outOfRange []string
	for _, p := range points {
		if p < 0 {
			return schedule{}, "points cannot be negative."
		}
		if p >= step {
			outOfRange = append(outOfRange, strconv.FormatInt(p, 10))
		}
	}
	if len(outOfRange) > 0 {
		verb := "is"
		if len(outOfRange) > 1 {
			verb = "are"
		}
		return schedule{}, "points are counted inside the step window, so each one must be below step (" +
			strconv.FormatInt(step, 10) + "): " + strings.Join(outOfRange, ", ") + " " + verb +
			" too large. With step=" + strconv.FormatInt(step, 10) + " the valid points are 0…" +
			strconv.FormatInt(step-1, 10) + "."
	}

	seen := make(map[int64]bool, len(points))
	deduped := make([]int64, 0, len(points))
	for _, p := range points {
		if !seen[p] {
			seen[p] = true
			deduped = append(deduped, p)
		}
	}
	sort.Slice(deduped, func(i, j int) bool { return deduped[i] < deduped[j] })

	return schedule{
		interval: args.Interval,
		step:     &step,
		points:   deduped,
		endDate:  args.EndDate,
	}, ""
}

// overrideAmounts works out what an occurrence should carry, starting from the
// reminder's own amounts. A transfer has two sides and no rate, so a single
// amount cannot say which one it means. The last return value is a tool-error
// message.
func overrideAmounts(r zen.Reminder, args addReminderMarkerArgs) (float64, float64, string) {
	income := r.Income
	if args.IncomeAmount != nil {
		income = *args.IncomeAmount
	}
	outcome := r.Outcome
	if args.OutcomeAmount != nil {
		outcome = *args.OutcomeAmount
	}

	if args.Amount != nil {
		if r.IncomeAccount != r.OutcomeAccount {
			return 0, 0, "This reminder moves money between two accounts, so 'amount' is ambiguous. " +
				"Pass outcome_amount and income_amount instead."
		}
		switch {
		case r.Income > 0 && r.Outcome == 0:
			income = *args.Amount
		case r.Outcome > 0 && r.Income == 0:
			outcome = *args.Amount
		default:
			return 0, 0, "Cannot tell which side of this reminder 'amount' refers to. " +
				"Pass outcome_amount or income_amount instead."
		}
	}

	return income, outcome, ""
}
