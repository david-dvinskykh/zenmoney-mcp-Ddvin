package tools

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

type addExpenseArgs struct {
	Account  string  `json:"account" jsonschema:"Account name or UUID to deduct from"`
	Amount   float64 `json:"amount" jsonschema:"Expense amount (positive number)"`
	Date     string  `json:"date" jsonschema:"Transaction date in YYYY-MM-DD format"`
	Category *string `json:"category,omitempty" jsonschema:"Category name or UUID"`
	Payee    *string `json:"payee,omitempty" jsonschema:"Payee/merchant name"`
	Comment  *string `json:"comment,omitempty" jsonschema:"Transaction comment"`
}

type addIncomeArgs struct {
	Account  string  `json:"account" jsonschema:"Account name or UUID to credit"`
	Amount   float64 `json:"amount" jsonschema:"Income amount (positive number)"`
	Date     string  `json:"date" jsonschema:"Transaction date in YYYY-MM-DD format"`
	Category *string `json:"category,omitempty" jsonschema:"Category name or UUID"`
	Payee    *string `json:"payee,omitempty" jsonschema:"Payer name"`
	Comment  *string `json:"comment,omitempty" jsonschema:"Transaction comment"`
}

type addTransferArgs struct {
	FromAccount   string   `json:"from_account" jsonschema:"Source account name or UUID"`
	ToAccount     string   `json:"to_account" jsonschema:"Destination account name or UUID"`
	Amount        *float64 `json:"amount,omitempty" jsonschema:"Transfer amount (alias for outcome_amount, for same-currency transfers)"`
	OutcomeAmount *float64 `json:"outcome_amount,omitempty" jsonschema:"Amount debited from source account (in source account currency)"`
	IncomeAmount  *float64 `json:"income_amount,omitempty" jsonschema:"Amount credited to destination account (in destination account currency). Required for cross-currency transfers."`
	Date          string   `json:"date" jsonschema:"Transaction date in YYYY-MM-DD format"`
	Comment       *string  `json:"comment,omitempty" jsonschema:"Transfer comment"`
}

type listTransactionsArgs struct {
	Days      *int    `json:"days,omitempty" jsonschema:"Number of days to look back from today (default 30). Ignored if start_date or end_date is provided."`
	StartDate *string `json:"start_date,omitempty" jsonschema:"Start of date range (inclusive), YYYY-MM-DD. If omitted while end_date is set, defaults to unbounded."`
	EndDate   *string `json:"end_date,omitempty" jsonschema:"End of date range (inclusive), YYYY-MM-DD. If omitted while start_date is set, defaults to today."`
	Account   *string `json:"account,omitempty" jsonschema:"Filter by account name or UUID"`
	Category  *string `json:"category,omitempty" jsonschema:"Filter by category name or UUID"`
	Limit     *int    `json:"limit,omitempty" jsonschema:"Max number of transactions to return (default 50)"`
}

func registerTransactions(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_expense",
		Description: "Add an expense transaction to ZenMoney. Requires account name/id, amount, and date. " +
			"Optionally accepts category, payee, and comment.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addExpenseArgs) (*mcp.CallToolResult, any, error) {
		return addOneSided(ctx, api, st, oneSided{
			account:  args.Account,
			amount:   args.Amount,
			date:     args.Date,
			category: args.Category,
			payee:    args.Payee,
			comment:  args.Comment,
			income:   false,
		})
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_income",
		Description: "Add an income transaction to ZenMoney.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addIncomeArgs) (*mcp.CallToolResult, any, error) {
		return addOneSided(ctx, api, st, oneSided{
			account:  args.Account,
			amount:   args.Amount,
			date:     args.Date,
			category: args.Category,
			payee:    args.Payee,
			comment:  args.Comment,
			income:   true,
		})
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "add_transfer",
		Description: "Transfer money between two accounts in ZenMoney. For cross-currency transfers, " +
			"specify both outcome_amount (source) and income_amount (destination). For same-currency " +
			"transfers, just use outcome_amount (or amount as alias).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addTransferArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		fromAcc := resolveAccount(st, args.FromAccount)
		toAcc := resolveAccount(st, args.ToAccount)
		if fromAcc == nil {
			return errorResult(`Source account "` + args.FromAccount + `" not found.`), nil, nil
		}
		if toAcc == nil {
			return errorResult(`Destination account "` + args.ToAccount + `" not found.`), nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		outcome := args.OutcomeAmount
		if outcome == nil {
			outcome = args.Amount
		}
		if outcome == nil || *outcome == 0 {
			return errorResult("Either 'amount' or 'outcome_amount' must be provided."), nil, nil
		}

		outcomeInstrument := instrumentOf(fromAcc, user.Currency)
		incomeInstrument := instrumentOf(toAcc, user.Currency)
		crossCurrency := outcomeInstrument != incomeInstrument

		if crossCurrency && args.IncomeAmount == nil {
			from := orQuestionMark(st.InstrumentTitle(outcomeInstrument))
			to := st.InstrumentTitle(incomeInstrument)
			return errorResult("Cross-currency transfer: source account is " + from +
				" and destination is " + orQuestionMark(to) +
				". Please provide income_amount (the amount in " +
				orDefault(to, "destination currency") + ")."), nil, nil
		}

		income := *outcome
		if args.IncomeAmount != nil {
			income = *args.IncomeAmount
		}

		now := nowUnix()
		tx := zen.Transaction{
			ID:                newUUID(),
			Changed:           now,
			Created:           now,
			User:              user.ID,
			IncomeInstrument:  incomeInstrument,
			IncomeAccount:     toAcc.ID,
			Income:            income,
			OutcomeInstrument: outcomeInstrument,
			OutcomeAccount:    fromAcc.ID,
			Outcome:           *outcome,
			Comment:           args.Comment,
			Date:              args.Date,
		}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: now,
			ServerTimestamp:        st.ServerTimestamp(),
			Transaction:            []zen.Transaction{tx},
		})
		if err != nil {
			return errorResult("Failed to add transfer: " + err.Error()), nil, nil
		}
		st.ApplyLocalTransaction(tx, resp)

		amountLine := "- Amount: " + num(*outcome) + " " + st.InstrumentTitle(outcomeInstrument)
		if crossCurrency {
			amountLine = "- From amount: " + num(*outcome) + " " + st.InstrumentTitle(outcomeInstrument) +
				"\n- To amount: " + num(income) + " " + st.InstrumentTitle(incomeInstrument)
		}

		return textResult("Transfer added:\n- From: " + fromAcc.Title + "\n- To: " + toAcc.Title +
			"\n" + amountLine + "\n- Date: " + args.Date + "\n- ID: " + tx.ID), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_transactions",
		Description: "List transactions (expenses, income, transfers, debts). By default returns the " +
			"last 30 days; pass start_date/end_date for an arbitrary period (e.g. Jan 1–31). Each row " +
			"ends with the transaction id, which delete_transaction takes. Syncs automatically if needed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listTransactionsArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		days := 30
		if args.Days != nil {
			days = *args.Days
		}
		limit := 50
		if args.Limit != nil {
			limit = *args.Limit
		}

		var lowerBound, upperBound string
		if args.StartDate != nil || args.EndDate != nil {
			lowerBound = "0000-01-01"
			if args.StartDate != nil {
				lowerBound = *args.StartDate
			}
			upperBound = today()
			if args.EndDate != nil {
				upperBound = *args.EndDate
			}
			if args.StartDate != nil && args.EndDate != nil && *args.StartDate > *args.EndDate {
				return errorResult("start_date (" + *args.StartDate + ") must be on or before end_date (" +
					*args.EndDate + ")."), nil, nil
			}
		} else {
			lowerBound = time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
			upperBound = "" // unbounded
		}

		var filtered []zen.Transaction
		for _, t := range st.Transactions() {
			if t.Deleted || t.Date < lowerBound {
				continue
			}
			if upperBound != "" && t.Date > upperBound {
				continue
			}
			filtered = append(filtered, t)
		}

		if args.Account != nil {
			if acc := resolveAccount(st, *args.Account); acc != nil {
				var kept []zen.Transaction
				for _, t := range filtered {
					if t.IncomeAccount == acc.ID || t.OutcomeAccount == acc.ID {
						kept = append(kept, t)
					}
				}
				filtered = kept
			}
		}

		if args.Category != nil {
			tagID := *args.Category
			if tag := findTagByName(st, *args.Category); tag != nil {
				tagID = tag.ID
			}
			var kept []zen.Transaction
			for _, t := range filtered {
				for _, id := range t.Tag {
					if id == tagID {
						kept = append(kept, t)
						break
					}
				}
			}
			filtered = kept
		}

		// Newest first, ties keeping their original order — the behaviour of the
		// original's stable sort with a comparator that never returns 0.
		sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Date > filtered[j].Date })
		if limit >= 0 && len(filtered) > limit {
			filtered = filtered[:limit]
		}

		lines := make([]string, 0, len(filtered))
		for _, t := range filtered {
			lines = append(lines, formatTransactionLine(st, t))
		}

		if len(lines) == 0 {
			return textResult("No transactions found in the given period."), nil, nil
		}
		return textResult("Transactions (" + strconv.Itoa(len(lines)) + "):\n\n" +
			strings.Join(lines, "\n")), nil, nil
	})
}

// oneSided carries the arguments add_expense and add_income share; the two
// differ only in which leg the amount lands on and how the result reads.
type oneSided struct {
	account  string
	amount   float64
	date     string
	category *string
	payee    *string
	comment  *string
	income   bool
}

func addOneSided(ctx context.Context, api *zen.API, st *zen.State, in oneSided) (*mcp.CallToolResult, any, error) {
	if fail := ensureSynced(ctx, st); fail != nil {
		return fail, nil, nil
	}

	acc := resolveAccount(st, in.account)
	if acc == nil {
		return errorResult(`Account "` + in.account +
			`" not found. Use list_accounts to see available accounts.`), nil, nil
	}

	user := st.User()
	if user == nil {
		return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
	}

	instrumentID := instrumentOf(acc, user.Currency)

	var tagIDs []string
	if in.category != nil {
		tagIDs = resolveTag(st, *in.category)
	}

	now := nowUnix()
	tx := zen.Transaction{
		ID:                newUUID(),
		Changed:           now,
		Created:           now,
		User:              user.ID,
		IncomeInstrument:  instrumentID,
		IncomeAccount:     acc.ID,
		OutcomeInstrument: instrumentID,
		OutcomeAccount:    acc.ID,
		Tag:               tagIDs,
		Payee:             in.payee,
		Comment:           in.comment,
		Date:              in.date,
	}
	if in.income {
		tx.Income = in.amount
	} else {
		tx.Outcome = in.amount
	}

	resp, err := api.Diff(ctx, zen.DiffRequest{
		CurrentClientTimestamp: now,
		ServerTimestamp:        st.ServerTimestamp(),
		Transaction:            []zen.Transaction{tx},
	})
	if err != nil {
		what := "expense"
		if in.income {
			what = "income"
		}
		return errorResult("Failed to add " + what + ": " + err.Error()), nil, nil
	}
	st.ApplyLocalTransaction(tx, resp)

	currency := st.InstrumentTitle(instrumentID)

	if in.income {
		return textResult("Income added:\n- Amount: " + num(in.amount) + " " + currency +
			"\n- Account: " + acc.Title + "\n- Date: " + in.date + "\n- ID: " + tx.ID), nil, nil
	}

	var extra strings.Builder
	if in.payee != nil && *in.payee != "" {
		extra.WriteString("\n- Payee: " + *in.payee)
	}
	if in.comment != nil && *in.comment != "" {
		extra.WriteString("\n- Comment: " + *in.comment)
	}
	return textResult("Expense added:\n- Amount: " + num(in.amount) + " " + currency +
		"\n- Account: " + acc.Title + "\n- Date: " + in.date +
		"\n- Category: " + tagTitles(st, tagIDs) + extra.String() +
		"\n- ID: " + tx.ID), nil, nil
}

func orQuestionMark(s string) string { return orDefault(s, "?") }

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
