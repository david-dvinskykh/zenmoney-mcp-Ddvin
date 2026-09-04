package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

// A debt in ZenMoney is an ordinary transaction with the system "debt" account
// on one side, so there are only two mechanical shapes:
//
//	money leaves my account  → I am owed more / I owe less   (lend, repay_sent)
//	money enters my account  → I am owed less / I owe more   (borrow, repay_received)
//
// The four directions map onto those two, and differ only in what the user means
// by them — ZenMoney tracks the running balance per counterparty, not the intent
// of each leg.
type direction struct {
	outgoing bool
	label    string
	summary  func(payee string) string
}

var directions = map[string]direction{
	"lend": {
		outgoing: true,
		label:    "Lent",
		summary:  func(p string) string { return p + " now owes you this amount" },
	},
	"borrow": {
		outgoing: false,
		label:    "Borrowed",
		summary:  func(p string) string { return "you now owe " + p + " this amount" },
	},
	"repay_received": {
		outgoing: false,
		label:    "Repayment received",
		summary:  func(p string) string { return p + "'s debt to you goes down by this amount" },
	},
	"repay_sent": {
		outgoing: true,
		label:    "Repayment sent",
		summary:  func(p string) string { return "your debt to " + p + " goes down by this amount" },
	},
}

type addDebtArgs struct {
	Direction   string  `json:"direction" jsonschema:"lend = you gave money and are now owed it; borrow = you took money and now owe it; repay_received = someone paid you back; repay_sent = you paid someone back"`
	Account     string  `json:"account" jsonschema:"Your own account (name or UUID) the money leaves from or arrives on — not the debt account"`
	Amount      float64 `json:"amount" jsonschema:"Amount, in the currency of your own account"`
	Payee       string  `json:"payee" jsonschema:"Who the debt is with. Reuse the exact name across a loan and its repayments."`
	Date        string  `json:"date" jsonschema:"Transaction date in YYYY-MM-DD format"`
	Category    *string `json:"category,omitempty" jsonschema:"Category name or UUID (debts are usually uncategorized)"`
	Comment     *string `json:"comment,omitempty" jsonschema:"Transaction comment"`
	DebtAccount *string `json:"debt_account,omitempty" jsonschema:"The debt account to book against (name or UUID). Only needed if auto-detection picks the wrong one."`
}

func registerDebts(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_debt",
		Description: "Record a debt in ZenMoney — money you lent to someone, money you borrowed, " +
			"or a repayment in either direction. A debt is booked between one of your " +
			"accounts and ZenMoney's system debt account; the counterparty is the payee, " +
			"so use the same payee name for a loan and its repayments to keep one running " +
			"balance per person. The amount is always in the currency of your own account.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addDebtArgs) (*mcp.CallToolResult, any, error) {
		spec, ok := directions[args.Direction]
		if !ok {
			return errorResult(`Unknown direction "` + args.Direction +
				`". Use one of: lend, borrow, repay_received, repay_sent.`), nil, nil
		}
		if !dateRE.MatchString(args.Date) {
			return errorResult("Date must be in YYYY-MM-DD format"), nil, nil
		}
		if args.Payee == "" {
			return errorResult("payee is required — it is the counterparty the debt is tracked against."), nil, nil
		}

		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		acc := resolveAccount(st, args.Account)
		if acc == nil {
			return errorResult(`Account "` + args.Account +
				`" not found. Use list_accounts to see available accounts.`), nil, nil
		}
		if acc.Type == "debt" {
			return errorResult(`"` + acc.Title + `" is the debt account itself. Pass the account of ` +
				"yours the money moves to or from — add_debt books the debt side for you."), nil, nil
		}

		debtAcc := findDebtAccount(st)
		if args.DebtAccount != nil {
			debtAcc = resolveAccount(st, *args.DebtAccount)
		}
		if debtAcc == nil {
			return errorResult("No debt account found. ZenMoney creates its system debt account the " +
				"first time a debt is recorded — add one debt in the ZenMoney app (or Zerro), then " +
				"run sync_data and try again."), nil, nil
		}
		if debtAcc.Type != "debt" {
			return errorResult(`"` + debtAcc.Title + `" is not a debt account (its type is "` +
				debtAcc.Type + `"). Use add_transfer to move money between two ordinary accounts.`), nil, nil
		}

		var tagIDs []string
		if args.Category != nil {
			tagIDs = resolveTag(st, *args.Category)
			if tagIDs == nil {
				return errorResult(`Category "` + *args.Category +
					`" not found. Use list_categories to see available categories.`), nil, nil
			}
		}

		// Both legs carry the non-debt account's currency: the debt account's own
		// instrument is user.currency and is overridden here, per the ZenMoney
		// diff protocol.
		instrument := instrumentOf(acc, user.Currency)

		incomeAccount, outcomeAccount := acc.ID, debtAcc.ID
		if spec.outgoing {
			incomeAccount, outcomeAccount = debtAcc.ID, acc.ID
		}

		var merchantID *string
		if m := resolveMerchantByTitle(st, args.Payee); m != nil {
			merchantID = &m.ID
		}

		now := nowUnix()
		tx := zen.Transaction{
			ID:                newUUID(),
			Changed:           now,
			Created:           now,
			User:              user.ID,
			IncomeInstrument:  instrument,
			IncomeAccount:     incomeAccount,
			Income:            args.Amount,
			OutcomeInstrument: instrument,
			OutcomeAccount:    outcomeAccount,
			Outcome:           args.Amount,
			Tag:               tagIDs,
			Merchant:          merchantID,
			Payee:             &args.Payee,
			Comment:           args.Comment,
			Date:              args.Date,
		}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: now,
			ServerTimestamp:        st.ServerTimestamp(),
			Transaction:            []zen.Transaction{tx},
		})
		if err != nil {
			return errorResult("Failed to add debt: " + err.Error()), nil, nil
		}
		st.ApplyLocalTransaction(tx, resp)

		route := debtAcc.Title + " → " + acc.Title
		if spec.outgoing {
			route = acc.Title + " → " + debtAcc.Title
		}

		comment := ""
		if args.Comment != nil && *args.Comment != "" {
			comment = "\n- Comment: " + *args.Comment
		}

		return textResult(spec.label + ":\n" +
			"- Amount: " + num(args.Amount) + " " + st.InstrumentTitle(instrument) +
			" — " + spec.summary(args.Payee) + "\n" +
			"- Counterparty: " + args.Payee + "\n" +
			"- Booked: " + route + "\n" +
			"- Date: " + args.Date + "\n" +
			"- Category: " + tagTitles(st, tagIDs) + comment +
			"\n- ID: " + tx.ID), nil, nil
	})
}
