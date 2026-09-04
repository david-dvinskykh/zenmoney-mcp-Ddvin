// Package tools registers the MCP tools over a shared ZenMoney state.
package tools

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

// Kind classifies a transaction the way ZenMoney presents it.
type Kind string

const (
	KindExpense  Kind = "expense"
	KindIncome   Kind = "income"
	KindTransfer Kind = "transfer"
	KindDebt     Kind = "debt"
	KindOther    Kind = "other"
)

// Operation is the money-carrying half of a transaction. A reminder describes
// its planned operation with the same fields, so both render through one
// summarizer.
type Operation struct {
	Income            float64
	Outcome           float64
	IncomeAccount     string
	OutcomeAccount    string
	IncomeInstrument  int64
	OutcomeInstrument int64
	Tag               []string
	Payee             *string
	Comment           *string
}

func operationOf(t zen.Transaction) Operation {
	return Operation{
		Income:            t.Income,
		Outcome:           t.Outcome,
		IncomeAccount:     t.IncomeAccount,
		OutcomeAccount:    t.OutcomeAccount,
		IncomeInstrument:  t.IncomeInstrument,
		OutcomeInstrument: t.OutcomeInstrument,
		Tag:               t.Tag,
		Payee:             t.Payee,
		Comment:           t.Comment,
	}
}

func operationOfReminder(r zen.Reminder) Operation {
	return Operation{
		Income:            r.Income,
		Outcome:           r.Outcome,
		IncomeAccount:     r.IncomeAccount,
		OutcomeAccount:    r.OutcomeAccount,
		IncomeInstrument:  r.IncomeInstrument,
		OutcomeInstrument: r.OutcomeInstrument,
		Tag:               r.Tag,
		Payee:             r.Payee,
		Comment:           r.Comment,
	}
}

// Summary is one operation rendered for display.
type Summary struct {
	Kind Kind
	// Amount carries the currency, plus account names for two-sided operations.
	Amount     string
	Categories string
	Payee      string
	Comment    string
}

// summarize classifies an operation and renders its amount the way ZenMoney
// shows it.
//
// A debt (loan given or taken) is an ordinary transaction with the user's "debt"
// account on one side, so it is detected from the account types rather than from
// a field of its own.
func summarize(st *zen.State, op Operation) Summary {
	from := findAccount(st, op.OutcomeAccount)
	to := findAccount(st, op.IncomeAccount)

	twoSided := op.IncomeAccount != op.OutcomeAccount
	touchesDebt := (from != nil && from.Type == "debt") || (to != nil && to.Type == "debt")

	kind := KindOther
	amount := ""

	switch {
	case twoSided:
		kind = KindTransfer
		if touchesDebt {
			kind = KindDebt
		}
		route := accountTitleOr(from, "?") + " → " + accountTitleOr(to, "?")
		if op.OutcomeInstrument != op.IncomeInstrument {
			amount = num(op.Outcome) + " " + st.InstrumentTitle(op.OutcomeInstrument) +
				" → " + num(op.Income) + " " + st.InstrumentTitle(op.IncomeInstrument) +
				" (" + route + ")"
		} else {
			amount = num(op.Outcome) + " (" + route + ")"
		}

	case op.Outcome > 0 && op.Income == 0:
		kind = KindExpense
		amount = "-" + num(op.Outcome) + " " + st.InstrumentTitle(op.OutcomeInstrument)

	case op.Income > 0 && op.Outcome == 0:
		kind = KindIncome
		amount = "+" + num(op.Income) + " " + st.InstrumentTitle(op.IncomeInstrument)
	}

	categories := ""
	if op.Tag != nil {
		names := make([]string, 0, len(op.Tag))
		for _, id := range op.Tag {
			if tag := findTag(st, id); tag != nil {
				names = append(names, tag.Title)
			} else {
				names = append(names, id)
			}
		}
		categories = strings.Join(names, ", ")
	}

	return Summary{
		Kind:       kind,
		Amount:     amount,
		Categories: categories,
		Payee:      deref(op.Payee),
		Comment:    deref(op.Comment),
	}
}

// formatTransactionLine is the one-line rendering used by list_transactions and
// the delete preview.
func formatTransactionLine(st *zen.State, t zen.Transaction) string {
	s := summarize(st, operationOf(t))
	comment := ""
	if s.Comment != "" {
		comment = ` — "` + s.Comment + `"`
	}
	return t.Date + " | " + padEnd(string(s.Kind), 8) + " | " + padEnd(s.Amount, 20) +
		" | " + padEnd(s.Categories, 15) + " | " + s.Payee + comment + " | id: `" + t.ID + "`"
}

// num renders a float the way a JavaScript template literal would: no trailing
// zeros, no thousands separator.
func num(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// padEnd right-pads to width, counting runes so Cyrillic category names line up
// the same way they do in the TypeScript original.
func padEnd(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr[T any](v T) *T { return &v }
