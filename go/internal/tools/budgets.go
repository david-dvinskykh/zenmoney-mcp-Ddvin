package tools

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

var monthRE = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

type listBudgetsArgs struct {
	Month *string `json:"month,omitempty" jsonschema:"Month in YYYY-MM format (default: current month)"`
}

type setBudgetArgs struct {
	Month    string   `json:"month" jsonschema:"Month in YYYY-MM format"`
	Category string   `json:"category" jsonschema:"Category name or UUID"`
	Outcome  *float64 `json:"outcome,omitempty" jsonschema:"Planned expense for the month. 0 removes it."`
	Income   *float64 `json:"income,omitempty" jsonschema:"Planned income for the month. 0 removes it."`
	Confirm  *bool    `json:"confirm,omitempty" jsonschema:"Set to true to actually save the budget. When false (the default) the tool only reports what would change."`
}

// isEmptyBudget is a budget with nothing in it — what ZenMoney leaves after a
// removal.
func isEmptyBudget(b zen.Budget) bool {
	return b.Income == 0 && b.Outcome == 0 && !b.IncomeLock && !b.OutcomeLock
}

// budgetSide renders one side of a budget: "none", or the amount.
func budgetSide(amount float64, locked bool, currency string) string {
	if amount == 0 && !locked {
		return "none"
	}
	return num(amount) + " " + currency
}

func userCurrency(st *zen.State) string {
	if user := st.User(); user != nil {
		return st.InstrumentTitle(user.Currency)
	}
	return ""
}

func registerBudgets(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_budgets",
		Description: "List the monthly category budgets in ZenMoney — the planned expense and income per category " +
			"for one month, in the user's main currency. Syncs automatically if needed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listBudgetsArgs) (*mcp.CallToolResult, any, error) {
		if args.Month != nil && !monthRE.MatchString(*args.Month) {
			return errorResult("Month must be in YYYY-MM format"), nil, nil
		}
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		target := time.Now().UTC().Format("2006-01")
		if args.Month != nil {
			target = *args.Month
		}
		date := target + "-01"
		currency := userCurrency(st)

		type row struct {
			budget zen.Budget
			title  string
		}
		var rows []row
		for _, b := range st.Budgets() {
			if b.Date != date || isEmptyBudget(b) {
				continue
			}
			title := "(whole month)"
			if b.Tag != nil {
				title = *b.Tag
				if tag := findTag(st, *b.Tag); tag != nil {
					title = tag.Title
				}
			}
			rows = append(rows, row{b, title})
		}
		// The month-wide row leads, categories follow by title.
		sort.SliceStable(rows, func(i, j int) bool {
			if (rows[i].budget.Tag == nil) != (rows[j].budget.Tag == nil) {
				return rows[i].budget.Tag == nil
			}
			return rows[i].title < rows[j].title
		})

		if len(rows) == 0 {
			return textResult("No budgets for " + target + "."), nil, nil
		}

		lines := make([]string, 0, len(rows))
		for _, r := range rows {
			id := ""
			if r.budget.Tag != nil {
				id = " — id: `" + *r.budget.Tag + "`"
			}
			lines = append(lines, "- **"+r.title+"**: expense "+
				budgetSide(r.budget.Outcome, r.budget.OutcomeLock, currency)+", income "+
				budgetSide(r.budget.Income, r.budget.IncomeLock, currency)+id)
		}
		return textResult("Budgets for " + target + " (" + strconv.Itoa(len(rows)) + "):\n\n" +
			strings.Join(lines, "\n")), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "set_budget",
		Description: "Set or change the monthly budget of a category in ZenMoney — the planned expense and/or income " +
			"for one month, in the user's main currency. Only the side you pass is changed; pass 0 to " +
			"remove it. The first call previews the before/after and changes nothing; repeat it with " +
			"confirm=true to save. Saving overwrites the old amount.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args setBudgetArgs) (*mcp.CallToolResult, any, error) {
		if !monthRE.MatchString(args.Month) {
			return errorResult("Month must be in YYYY-MM format"), nil, nil
		}
		if (args.Outcome != nil && *args.Outcome < 0) || (args.Income != nil && *args.Income < 0) {
			return errorResult("Budget amounts must not be negative"), nil, nil
		}
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		if args.Outcome == nil && args.Income == nil {
			return errorResult("Nothing to set. Pass outcome (planned expense) and/or income (planned income)."), nil, nil
		}

		tagIDs := resolveTag(st, args.Category)
		if tagIDs == nil {
			return errorResult(`Category "` + args.Category +
				`" not found. Use list_categories to see available categories.`), nil, nil
		}
		tag := findTag(st, tagIDs[0])

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}
		currency := st.InstrumentTitle(user.Currency)

		date := args.Month + "-01"
		original := zen.Budget{User: user.ID, Tag: ptr(tag.ID), Date: date}
		key := zen.BudgetKey(original)
		for _, b := range st.Budgets() {
			if zen.BudgetKey(b) == key {
				original = b
				break
			}
		}

		now := nowUnix()
		updated := original
		updated.User = user.ID
		updated.Changed = now
		// The lock is left as it was: it is not a "set by hand" flag (a live
		// account showed every budget unlocked, hand-typed ones included). Only a
		// removal clears it, which is how ZenMoney itself drops a budget.
		var changes []string
		if args.Outcome != nil {
			updated.Outcome = *args.Outcome
			if *args.Outcome == 0 {
				updated.OutcomeLock = false
			}
			changes = append(changes, "- expense: "+
				budgetSide(original.Outcome, original.OutcomeLock, currency)+" → "+
				budgetSide(updated.Outcome, updated.OutcomeLock, currency))
		}
		if args.Income != nil {
			updated.Income = *args.Income
			if *args.Income == 0 {
				updated.IncomeLock = false
			}
			changes = append(changes, "- income: "+
				budgetSide(original.Income, original.IncomeLock, currency)+" → "+
				budgetSide(updated.Income, updated.IncomeLock, currency))
		}
		summary := "Budget for **" + tag.Title + "**, " + args.Month + ":\n" + strings.Join(changes, "\n")

		if args.Confirm == nil || !*args.Confirm {
			return textResult(summary +
				"\n\nNothing has been saved yet. Call set_budget again with confirm=true to save."), nil, nil
		}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: now,
			ServerTimestamp:        st.ServerTimestamp(),
			Budget:                 []zen.Budget{updated},
		})
		if err != nil {
			return errorResult("Failed to save budget: " + err.Error()), nil, nil
		}
		st.ApplyLocalBudget(updated, resp)

		return textResult("Saved. " + summary), nil, nil
	})
}
