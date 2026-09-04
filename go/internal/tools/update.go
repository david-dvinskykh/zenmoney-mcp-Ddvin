package tools

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

type updateArgs struct {
	ID            string   `json:"id" jsonschema:"UUID of the transaction to edit"`
	Date          *string  `json:"date,omitempty" jsonschema:"New date in YYYY-MM-DD format"`
	Amount        *float64 `json:"amount,omitempty" jsonschema:"New amount. For an expense or income it replaces the single amount; for a same-currency transfer or debt it replaces both sides. Cross-currency transfers need outcome_amount/income_amount."`
	OutcomeAmount *float64 `json:"outcome_amount,omitempty" jsonschema:"New amount leaving the source account (in the source account's currency)"`
	IncomeAmount  *float64 `json:"income_amount,omitempty" jsonschema:"New amount arriving on the destination account (in the destination account's currency)"`
	Account       *string  `json:"account,omitempty" jsonschema:"Move an expense or income to a different account (name or UUID)"`
	FromAccount   *string  `json:"from_account,omitempty" jsonschema:"New source account of a transfer or debt (name or UUID)"`
	ToAccount     *string  `json:"to_account,omitempty" jsonschema:"New destination account of a transfer or debt (name or UUID)"`
	Category      *string  `json:"category,omitempty" jsonschema:"New category name or UUID. Empty string removes the category."`
	Payee         *string  `json:"payee,omitempty" jsonschema:"New payee/counterparty name. Empty string removes it."`
	Comment       *string  `json:"comment,omitempty" jsonschema:"New comment. Empty string removes it."`
	Confirm       *bool    `json:"confirm,omitempty" jsonschema:"Set to true to actually save the edit. When false (the default) the tool only reports what would change."`
}

// editableFields names the arguments whose presence means the caller actually
// asked for a change, in the order the error message lists them.
var editableFields = []string{
	"date", "amount", "outcome_amount", "income_amount",
	"account", "from_account", "to_account", "category", "payee", "comment",
}

func (a updateArgs) touched() bool {
	return a.Date != nil || a.Amount != nil || a.OutcomeAmount != nil || a.IncomeAmount != nil ||
		a.Account != nil || a.FromAccount != nil || a.ToAccount != nil ||
		a.Category != nil || a.Payee != nil || a.Comment != nil
}

func registerUpdate(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "update_transaction",
		Description: "Edit an existing transaction in ZenMoney — its date, amount, account, category, payee, or " +
			"comment. Works for every kind of transaction: expenses, income, transfers between accounts, " +
			"and debts. Get the id from list_transactions, which prints one at the end of each row. Only " +
			"the fields you pass are changed; pass an empty string to category, payee, or comment to clear " +
			"it. The first call previews the before/after and changes nothing; repeat it with confirm=true " +
			"to save. Editing overwrites the old values and cannot be undone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args updateArgs) (*mcp.CallToolResult, any, error) {
		if args.Date != nil && !dateRE.MatchString(*args.Date) {
			return errorResult("Date must be in YYYY-MM-DD format"), nil, nil
		}
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		if !args.touched() {
			return errorResult("Nothing to update. Pass at least one of: " +
				strings.Join(editableFields, ", ") + "."), nil, nil
		}

		var original *zen.Transaction
		for _, t := range st.Transactions() {
			if t.ID == args.ID && !t.Deleted {
				copied := t
				original = &copied
				break
			}
		}
		if original == nil {
			return errorResult("Transaction " + args.ID + " not found. Use list_transactions to find " +
				"the right id (each row ends with one), or sync_data if it was added from another " +
				"client just now."), nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		updated := *original
		wasTwoSided := original.IncomeAccount != original.OutcomeAccount

		if args.Date != nil {
			updated.Date = *args.Date
		}

		// --- accounts -------------------------------------------------------
		if args.Account != nil {
			if wasTwoSided {
				return errorResult("This transaction moves money between two accounts, so 'account' " +
					"is ambiguous. Use from_account and/or to_account instead."), nil, nil
			}
			acc := resolveAccount(st, *args.Account)
			if acc == nil {
				return errorResult(`Account "` + *args.Account +
					`" not found. Use list_accounts to see available accounts.`), nil, nil
			}
			updated.OutcomeAccount = acc.ID
			updated.IncomeAccount = acc.ID
			updated.OutcomeInstrument = instrumentOf(acc, user.Currency)
			updated.IncomeInstrument = updated.OutcomeInstrument
		}

		if args.FromAccount != nil {
			acc := resolveAccount(st, *args.FromAccount)
			if acc == nil {
				return errorResult(`Source account "` + *args.FromAccount +
					`" not found. Use list_accounts to see available accounts.`), nil, nil
			}
			updated.OutcomeAccount = acc.ID
			updated.OutcomeInstrument = instrumentOf(acc, user.Currency)
		}

		if args.ToAccount != nil {
			acc := resolveAccount(st, *args.ToAccount)
			if acc == nil {
				return errorResult(`Destination account "` + *args.ToAccount +
					`" not found. Use list_accounts to see available accounts.`), nil, nil
			}
			updated.IncomeAccount = acc.ID
			updated.IncomeInstrument = instrumentOf(acc, user.Currency)
		}

		normalizeDebtInstruments(st, &updated, user.Currency)

		// --- amounts --------------------------------------------------------
		twoSided := updated.IncomeAccount != updated.OutcomeAccount

		if args.Amount != nil {
			switch {
			case twoSided:
				if updated.IncomeInstrument != updated.OutcomeInstrument {
					from := orQuestionMark(st.InstrumentTitle(updated.OutcomeInstrument))
					to := orQuestionMark(st.InstrumentTitle(updated.IncomeInstrument))
					return errorResult("This is a cross-currency transaction (" + from + " → " + to +
						"), so a single 'amount' is ambiguous. Pass outcome_amount and income_amount " +
						"instead."), nil, nil
				}
				updated.Outcome = *args.Amount
				updated.Income = *args.Amount
			case original.Income > 0 && original.Outcome == 0:
				updated.Income = *args.Amount
				updated.Outcome = 0
			default:
				updated.Outcome = *args.Amount
				updated.Income = 0
			}
		}

		if args.OutcomeAmount != nil {
			updated.Outcome = *args.OutcomeAmount
		}
		if args.IncomeAmount != nil {
			updated.Income = *args.IncomeAmount
		}

		if msg := validateAmounts(updated, twoSided); msg != "" {
			return errorResult(msg), nil, nil
		}

		// --- category, payee, comment ---------------------------------------
		if args.Category != nil {
			if strings.TrimSpace(*args.Category) == "" {
				updated.Tag = nil
			} else {
				tagIDs := resolveTag(st, *args.Category)
				if tagIDs == nil {
					return errorResult(`Category "` + *args.Category +
						`" not found. Use list_categories to see available categories.`), nil, nil
				}
				updated.Tag = tagIDs
			}
		}

		if args.Payee != nil {
			var payee *string
			if strings.TrimSpace(*args.Payee) != "" {
				payee = args.Payee
			}
			updated.Payee = payee
			// The merchant is the normalized counterparty and wins over payee in
			// ZenMoney's UI, so it has to follow the new name or be dropped.
			updated.Merchant = nil
			if payee != nil {
				if m := resolveMerchantByTitle(st, *payee); m != nil {
					updated.Merchant = &m.ID
				}
			}
		}

		if args.Comment != nil {
			if strings.TrimSpace(*args.Comment) == "" {
				updated.Comment = nil
			} else {
				updated.Comment = args.Comment
			}
		}

		changes := describeChanges(st, *original, updated)
		if len(changes) == 0 {
			return textResult("Transaction `" + original.ID + "` already has these values — nothing " +
				"to change.\n\n" + formatTransactionLine(st, *original)), nil, nil
		}

		diff := "Before: " + formatTransactionLine(st, *original) + "\n" +
			"After:  " + formatTransactionLine(st, updated) + "\n\nChanges:\n" +
			"- " + strings.Join(changes, "\n- ")

		if args.Confirm == nil || !*args.Confirm {
			return textResult("About to update transaction `" + original.ID + "`:\n\n" + diff +
				"\n\nNothing has been saved yet. Call update_transaction again with confirm=true " +
				"to apply."), nil, nil
		}

		now := nowUnix()
		updated.Changed = now

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: now,
			ServerTimestamp:        st.ServerTimestamp(),
			Transaction:            []zen.Transaction{updated},
		})
		if err != nil {
			return errorResult("Failed to update transaction: " + err.Error()), nil, nil
		}
		st.ApplyLocalTransaction(updated, resp)

		return textResult("Transaction updated:\n\n" + diff), nil, nil
	})
}

// normalizeDebtInstruments keeps the ZenMoney invariant that a debt transaction
// carries the *non-debt* account's currency on both legs — the debt account's own
// instrument is always the user's currency and never appears on the transaction.
func normalizeDebtInstruments(st *zen.State, t *zen.Transaction, userCurrency int64) {
	from := findAccount(st, t.OutcomeAccount)
	to := findAccount(st, t.IncomeAccount)

	debtSides := 0
	for _, a := range []*zen.Account{from, to} {
		if a != nil && a.Type == "debt" {
			debtSides++
		}
	}
	if debtSides != 1 {
		return
	}

	nonDebt := from
	if from != nil && from.Type == "debt" {
		nonDebt = to
	}
	if nonDebt == nil {
		return
	}

	instrument := instrumentOf(nonDebt, userCurrency)
	t.OutcomeInstrument = instrument
	t.IncomeInstrument = instrument
}

// validateAmounts returns the message explaining why the edit cannot stand, or
// "" when the amounts are coherent.
func validateAmounts(t zen.Transaction, twoSided bool) string {
	if twoSided {
		if t.Outcome <= 0 || t.Income <= 0 {
			return "A transfer or debt needs a positive amount on both sides. " +
				"Pass outcome_amount and income_amount (or 'amount' when both use the same currency)."
		}
		return ""
	}
	if t.Outcome > 0 && t.Income > 0 {
		return "A single-account transaction is either an expense or an income, not both. " +
			"Pass 'amount', or set from_account/to_account to turn it into a transfer."
	}
	if t.Outcome == 0 && t.Income == 0 {
		return "The transaction would end up with no amount. Pass a positive 'amount'."
	}
	return ""
}

// describeChanges lists what actually differs, for both the preview and the
// result.
func describeChanges(st *zen.State, before, after zen.Transaction) []string {
	var changes []string
	currency := func(id int64) string { return st.InstrumentTitle(id) }

	if before.Date != after.Date {
		changes = append(changes, "date: "+before.Date+" → "+after.Date)
	}
	if before.OutcomeAccount != after.OutcomeAccount {
		label := "from account"
		if before.IncomeAccount == before.OutcomeAccount {
			label = "account"
		}
		changes = append(changes, label+": "+accountName(st, before.OutcomeAccount)+
			" → "+accountName(st, after.OutcomeAccount))
	}
	if before.IncomeAccount != after.IncomeAccount {
		// A one-sided edit moves both legs at once; report that as a single change.
		alreadyReported := before.IncomeAccount == before.OutcomeAccount &&
			after.IncomeAccount == after.OutcomeAccount
		if !alreadyReported {
			changes = append(changes, "to account: "+accountName(st, before.IncomeAccount)+
				" → "+accountName(st, after.IncomeAccount))
		}
	}
	if before.Outcome != after.Outcome || before.OutcomeInstrument != after.OutcomeInstrument {
		changes = append(changes, "outcome: "+num(before.Outcome)+" "+currency(before.OutcomeInstrument)+
			" → "+num(after.Outcome)+" "+currency(after.OutcomeInstrument))
	}
	if before.Income != after.Income || before.IncomeInstrument != after.IncomeInstrument {
		changes = append(changes, "income: "+num(before.Income)+" "+currency(before.IncomeInstrument)+
			" → "+num(after.Income)+" "+currency(after.IncomeInstrument))
	}
	if beforeTags, afterTags := tagTitles(st, before.Tag), tagTitles(st, after.Tag); beforeTags != afterTags {
		changes = append(changes, "category: "+beforeTags+" → "+afterTags)
	}
	if !samePtr(before.Payee, after.Payee) {
		changes = append(changes, "payee: "+orNone(before.Payee)+" → "+orNone(after.Payee))
	}
	if !samePtr(before.Merchant, after.Merchant) {
		name := func(id *string) string {
			if id == nil {
				return "(none)"
			}
			if m := findMerchant(st, *id); m != nil {
				return m.Title
			}
			return *id
		}
		changes = append(changes, "merchant: "+name(before.Merchant)+" → "+name(after.Merchant))
	}
	if !samePtr(before.Comment, after.Comment) {
		changes = append(changes, "comment: "+orNone(before.Comment)+" → "+orNone(after.Comment))
	}
	return changes
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func orNone(s *string) string {
	if s == nil {
		return "(none)"
	}
	return *s
}
