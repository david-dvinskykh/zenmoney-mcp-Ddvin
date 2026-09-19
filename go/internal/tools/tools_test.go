package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

// harness wires a real MCP client to a real MCP server over an in-memory
// transport, backed by a stub ZenMoney API. Tests exercise the tools exactly as
// a client would.
type harness struct {
	t       *testing.T
	session *mcp.ClientSession
	state   *zen.State
	// pushed collects the diff requests the tools sent, so a test can assert on
	// what actually went to ZenMoney.
	pushed *[]zen.DiffRequest
}

func newHarness(t *testing.T, initial zen.DiffResponse) *harness {
	t.Helper()
	return newHarnessWith(t, initial, nil)
}

// newHarnessWith is newHarness with a say in how writes are answered, for tests
// that need a write response carrying more than a fresh timestamp. The answer is
// fixed before the stub starts serving, so it stays race-free.
func newHarnessWith(
	t *testing.T,
	initial zen.DiffResponse,
	writeResponse func(zen.DiffRequest) zen.DiffResponse,
) *harness {
	t.Helper()

	var pushed []zen.DiffRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/suggest/") {
			_ = json.NewEncoder(w).Encode([]zen.SuggestResponse{})
			return
		}
		var req zen.DiffRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if len(req.Transaction) > 0 || len(req.Deletion) > 0 ||
			len(req.Reminder) > 0 || len(req.ReminderMarker) > 0 {
			pushed = append(pushed, req)
			if writeResponse != nil {
				_ = json.NewEncoder(w).Encode(writeResponse(req))
				return
			}
			// A write response is a diff since the caller's timestamp. The stub
			// reports nothing new, only a fresh timestamp.
			_ = json.NewEncoder(w).Encode(zen.DiffResponse{ServerTimestamp: req.ServerTimestamp + 1})
			return
		}
		_ = json.NewEncoder(w).Encode(initial)
	}))
	t.Cleanup(server.Close)
	t.Setenv("ZENMONEY_API_BASE", server.URL)

	api := zen.NewAPI("test-token")
	state := zen.NewState(api, nil)

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	Register(mcpServer, api, state)

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := mcpServer.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return &harness{t: t, session: session, state: state, pushed: &pushed}
}

// call runs a tool and returns its text output, failing the test if the tool
// reported an error.
func (h *harness) call(name string, args map[string]any) string {
	h.t.Helper()
	text, isError := h.callRaw(name, args)
	if isError {
		h.t.Fatalf("%s returned an error result: %s", name, text)
	}
	return text
}

// callErr runs a tool that is expected to fail and returns its message.
func (h *harness) callErr(name string, args map[string]any) string {
	h.t.Helper()
	text, isError := h.callRaw(name, args)
	if !isError {
		h.t.Fatalf("%s should have failed, got: %s", name, text)
	}
	return text
}

func (h *harness) callRaw(name string, args map[string]any) (string, bool) {
	h.t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: name, Arguments: args,
	})
	if err != nil {
		h.t.Fatalf("CallTool(%s): %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(text.Text)
		}
	}
	return sb.String(), res.IsError
}

func instrument(id int64, short string) zen.Instrument {
	return zen.Instrument{ID: id, ShortTitle: short, Title: short, Symbol: short}
}

func acct(id, title, accType string, instrumentID int64, balance float64) zen.Account {
	return zen.Account{ID: id, Title: title, Type: accType,
		Instrument: &instrumentID, Balance: &balance}
}

// fixture is a small but complete account: two currencies, a debt account, a
// category tree and one of each kind of transaction.
func fixture() zen.DiffResponse {
	food := "food"
	return zen.DiffResponse{
		ServerTimestamp: 100,
		User:            []zen.User{{ID: 1, Currency: 1}},
		Instrument:      []zen.Instrument{instrument(1, "PLN"), instrument(2, "EUR")},
		Account: []zen.Account{
			acct("cash", "Cash PLN", "cash", 1, 500),
			acct("eur", "Euro card", "ccard", 2, 300),
			acct("debt", "Debts", "debt", 1, 0),
			acct("old", "Closed card", "ccard", 1, 0),
		},
		Tag: []zen.Tag{
			{ID: "food", Title: "Food", ShowOutcome: true},
			{ID: "groceries", Title: "Groceries", Parent: &food, ShowOutcome: true},
			{ID: "salary", Title: "Salary", ShowIncome: true},
		},
		Merchant: []zen.Merchant{{ID: "m1", Title: "Biedronka"}},
		Transaction: []zen.Transaction{
			{ID: "t-expense", Date: "2026-03-01", Outcome: 25.5, Income: 0,
				OutcomeAccount: "cash", IncomeAccount: "cash",
				OutcomeInstrument: 1, IncomeInstrument: 1,
				Tag: []string{"groceries"}, Payee: ptr("Biedronka"), Merchant: ptr("m1")},
			{ID: "t-income", Date: "2026-03-02", Outcome: 0, Income: 8000,
				OutcomeAccount: "cash", IncomeAccount: "cash",
				OutcomeInstrument: 1, IncomeInstrument: 1, Tag: []string{"salary"}},
			{ID: "t-transfer", Date: "2026-03-03", Outcome: 100, Income: 23,
				OutcomeAccount: "cash", IncomeAccount: "eur",
				OutcomeInstrument: 1, IncomeInstrument: 2},
			{ID: "t-debt", Date: "2026-03-04", Outcome: 200, Income: 200,
				OutcomeAccount: "cash", IncomeAccount: "debt",
				OutcomeInstrument: 1, IncomeInstrument: 1, Payee: ptr("Anna")},
		},
		Reminder: []zen.Reminder{
			{ID: "r-rent", StartDate: "2026-01-10", Interval: ptr("month"), Step: ptr(int64(1)),
				Outcome: 3200, OutcomeAccount: "cash", IncomeAccount: "cash",
				OutcomeInstrument: 1, IncomeInstrument: 1, Payee: ptr("Landlord")},
			{ID: "r-once", StartDate: "2026-05-05",
				Outcome: 99, OutcomeAccount: "cash", IncomeAccount: "cash",
				OutcomeInstrument: 1, IncomeInstrument: 1, Comment: ptr("insurance")},
		},
		ReminderMarker: []zen.ReminderMarker{
			{ID: "mk1", Reminder: "r-rent", State: "planned", Date: "2099-04-10"},
			{ID: "mk2", Reminder: "r-rent", State: "planned", Date: "2099-05-10"},
			{ID: "mk3", Reminder: "r-once", State: "processed", Date: "2020-05-05"},
		},
	}
}

func TestListAccountsHidesArchivedUnlessAsked(t *testing.T) {
	data := fixture()
	data.Account[3].Archive = true
	h := newHarness(t, data)

	out := h.call("list_accounts", nil)
	if strings.Contains(out, "Closed card") {
		t.Error("archived accounts should be hidden by default")
	}
	if !strings.Contains(out, "**Cash PLN** [cash] — 500 PLN (PLN, PLN)") {
		t.Errorf("account line is not rendered as expected:\n%s", out)
	}

	out = h.call("list_accounts", map[string]any{"include_archived": true})
	if !strings.Contains(out, "Closed card") || !strings.Contains(out, "(archived)") {
		t.Errorf("include_archived should show it and mark it:\n%s", out)
	}
}

func TestListCategoriesShowsTheHierarchy(t *testing.T) {
	h := newHarness(t, fixture())
	out := h.call("list_categories", nil)

	if !strings.Contains(out, "- **Food** (expense) — id: `food`") {
		t.Errorf("parent category line missing:\n%s", out)
	}
	if !strings.Contains(out, "  - Groceries — id: `groceries`") {
		t.Errorf("child category should be indented under its parent:\n%s", out)
	}
	if strings.Contains(out, "- **Groceries**") {
		t.Error("a child category must not also be listed as a parent")
	}
}

func TestListTransactionsRendersEveryKind(t *testing.T) {
	h := newHarness(t, fixture())
	out := h.call("list_transactions", map[string]any{"start_date": "2026-01-01", "end_date": "2026-12-31"})

	for _, want := range []string{
		"expense  | -25.5 PLN",
		"income   | +8000 PLN",
		"transfer | 100 PLN → 23 EUR (Cash PLN → Euro card)",
		"debt     | 200 (Cash PLN → Debts)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Newest first.
	if strings.Index(out, "t-debt") > strings.Index(out, "t-expense") {
		t.Errorf("transactions should be listed newest first:\n%s", out)
	}
}

func TestListTransactionsFiltersAndValidates(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("list_transactions", map[string]any{
		"start_date": "2026-01-01", "end_date": "2026-12-31", "account": "Euro card"})
	if !strings.Contains(out, "t-transfer") || strings.Contains(out, "t-expense") {
		t.Errorf("account filter should keep only transactions touching it:\n%s", out)
	}

	out = h.call("list_transactions", map[string]any{
		"start_date": "2026-01-01", "end_date": "2026-12-31", "category": "Groceries"})
	if !strings.Contains(out, "t-expense") || strings.Contains(out, "t-income") {
		t.Errorf("category filter should keep only tagged transactions:\n%s", out)
	}

	out = h.call("list_transactions", map[string]any{"start_date": "2026-06-01", "end_date": "2026-06-30"})
	if !strings.Contains(out, "No transactions found") {
		t.Errorf("an empty period should say so:\n%s", out)
	}

	msg := h.callErr("list_transactions", map[string]any{"start_date": "2026-12-31", "end_date": "2026-01-01"})
	if !strings.Contains(msg, "must be on or before") {
		t.Errorf("a reversed range should be rejected: %s", msg)
	}
}

func TestAddExpenseSendsTheTransactionAndReportsIt(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("add_expense", map[string]any{
		"account": "Cash", "amount": 42.5, "date": "2026-03-10",
		"category": "Groceries", "payee": "Zabka", "comment": "coffee",
	})

	if !strings.Contains(out, "Amount: 42.5 PLN") || !strings.Contains(out, "Category: Groceries") {
		t.Errorf("result does not describe the expense:\n%s", out)
	}

	if len(*h.pushed) != 1 {
		t.Fatalf("expected exactly one write, got %d", len(*h.pushed))
	}
	sent := (*h.pushed)[0].Transaction[0]
	if sent.Outcome != 42.5 || sent.Income != 0 {
		t.Errorf("an expense must sit on the outcome leg, got outcome=%v income=%v", sent.Outcome, sent.Income)
	}
	if sent.IncomeAccount != "cash" || sent.OutcomeAccount != "cash" {
		t.Errorf("both legs should be the same account, got %s/%s", sent.OutcomeAccount, sent.IncomeAccount)
	}
	if len(sent.Tag) != 1 || sent.Tag[0] != "groceries" {
		t.Errorf("category was not resolved to its id: %v", sent.Tag)
	}
}

func TestAddExpenseRejectsAnUnknownAccount(t *testing.T) {
	h := newHarness(t, fixture())
	msg := h.callErr("add_expense", map[string]any{
		"account": "Nonexistent", "amount": 1, "date": "2026-03-10"})
	if !strings.Contains(msg, "not found") || !strings.Contains(msg, "list_accounts") {
		t.Errorf("the error should point at list_accounts: %s", msg)
	}
	if len(*h.pushed) != 0 {
		t.Error("nothing should have been sent to ZenMoney")
	}
}

func TestAddTransferRequiresIncomeAmountAcrossCurrencies(t *testing.T) {
	h := newHarness(t, fixture())

	msg := h.callErr("add_transfer", map[string]any{
		"from_account": "Cash PLN", "to_account": "Euro card", "amount": 100, "date": "2026-03-10"})
	if !strings.Contains(msg, "income_amount") || !strings.Contains(msg, "EUR") {
		t.Errorf("a cross-currency transfer should ask for income_amount in EUR: %s", msg)
	}
	if len(*h.pushed) != 0 {
		t.Error("nothing should have been sent")
	}

	out := h.call("add_transfer", map[string]any{
		"from_account": "Cash PLN", "to_account": "Euro card",
		"outcome_amount": 100, "income_amount": 23, "date": "2026-03-10"})
	if !strings.Contains(out, "From amount: 100 PLN") || !strings.Contains(out, "To amount: 23 EUR") {
		t.Errorf("both legs should be reported:\n%s", out)
	}
	sent := (*h.pushed)[0].Transaction[0]
	if sent.OutcomeInstrument != 1 || sent.IncomeInstrument != 2 {
		t.Errorf("each leg keeps its own currency, got %d/%d", sent.OutcomeInstrument, sent.IncomeInstrument)
	}
}

func TestAddDebtBooksAgainstTheDebtAccount(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("add_debt", map[string]any{
		"direction": "lend", "account": "Cash PLN", "amount": 300,
		"payee": "Anna", "date": "2026-03-11"})
	if !strings.Contains(out, "Lent:") || !strings.Contains(out, "Anna now owes you this amount") {
		t.Errorf("result does not read like a loan given:\n%s", out)
	}

	sent := (*h.pushed)[0].Transaction[0]
	if sent.OutcomeAccount != "cash" || sent.IncomeAccount != "debt" {
		t.Errorf("lending moves money to the debt account, got %s → %s", sent.OutcomeAccount, sent.IncomeAccount)
	}
	if sent.Outcome != 300 || sent.Income != 300 {
		t.Errorf("both legs carry the same amount, got %v/%v", sent.Outcome, sent.Income)
	}
	if sent.OutcomeInstrument != 1 || sent.IncomeInstrument != 1 {
		t.Errorf("both legs carry the non-debt account's currency, got %d/%d",
			sent.OutcomeInstrument, sent.IncomeInstrument)
	}
}

func TestAddDebtBorrowReversesTheLegs(t *testing.T) {
	h := newHarness(t, fixture())
	h.call("add_debt", map[string]any{
		"direction": "borrow", "account": "Cash PLN", "amount": 300,
		"payee": "Anna", "date": "2026-03-11"})

	sent := (*h.pushed)[0].Transaction[0]
	if sent.OutcomeAccount != "debt" || sent.IncomeAccount != "cash" {
		t.Errorf("borrowing brings money in, got %s → %s", sent.OutcomeAccount, sent.IncomeAccount)
	}
}

func TestAddDebtRefusesTheDebtAccountItself(t *testing.T) {
	h := newHarness(t, fixture())
	msg := h.callErr("add_debt", map[string]any{
		"direction": "lend", "account": "Debts", "amount": 10,
		"payee": "Anna", "date": "2026-03-11"})
	if !strings.Contains(msg, "is the debt account itself") {
		t.Errorf("unexpected message: %s", msg)
	}
}

func TestDeleteTransactionPreviewsBeforeDeleting(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("delete_transaction", map[string]any{"id": "t-expense"})
	if !strings.Contains(out, "About to delete 1 transaction") || !strings.Contains(out, "confirm=true") {
		t.Errorf("the first call should only preview:\n%s", out)
	}
	if len(*h.pushed) != 0 {
		t.Fatal("a preview must not delete anything")
	}

	out = h.call("delete_transaction", map[string]any{"id": "t-expense", "confirm": true})
	if !strings.Contains(out, "Deleted 1 transaction") {
		t.Errorf("unexpected result:\n%s", out)
	}
	if len(*h.pushed) != 1 || (*h.pushed)[0].Deletion[0].Object != "transaction" {
		t.Fatalf("expected one transaction deletion, got %+v", *h.pushed)
	}
	if got := h.call("list_transactions", map[string]any{
		"start_date": "2026-01-01", "end_date": "2026-12-31"}); strings.Contains(got, "t-expense") {
		t.Error("the deleted transaction should be gone from local state")
	}
}

func TestDeleteTransactionRefusesUnknownIDsWithoutDeletingAnything(t *testing.T) {
	h := newHarness(t, fixture())
	msg := h.callErr("delete_transaction", map[string]any{
		"ids": []any{"t-expense", "does-not-exist"}, "confirm": true})
	if !strings.Contains(msg, "not found: does-not-exist") || !strings.Contains(msg, "Nothing was deleted") {
		t.Errorf("unexpected message: %s", msg)
	}
	if len(*h.pushed) != 0 {
		t.Error("a partially valid batch must delete nothing")
	}
}

func TestDeleteObjectExplainsTheConsequences(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("delete_object", map[string]any{"type": "account", "name_or_id": "Cash PLN"})
	if !strings.Contains(out, "transactions will be deleted with it") {
		t.Errorf("deleting an account should warn about its transactions:\n%s", out)
	}

	out = h.call("delete_object", map[string]any{"type": "category", "name_or_id": "Food"})
	if !strings.Contains(out, "1 subcategory will be deleted with it") {
		t.Errorf("deleting a parent category should warn about children:\n%s", out)
	}

	msg := h.callErr("delete_object", map[string]any{"type": "merchant", "name_or_id": "Nope"})
	if !strings.Contains(msg, "list_merchants") {
		t.Errorf("the error should point at the listing tool: %s", msg)
	}
}

func TestUpdateTransactionPreviewsBeforeAndAfter(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("update_transaction", map[string]any{"id": "t-expense", "amount": 30})
	if !strings.Contains(out, "Before:") || !strings.Contains(out, "After:") {
		t.Errorf("the preview should show both sides:\n%s", out)
	}
	if !strings.Contains(out, "outcome: 25.5 PLN → 30 PLN") {
		t.Errorf("the change list is missing the amount:\n%s", out)
	}
	if len(*h.pushed) != 0 {
		t.Fatal("a preview must not save anything")
	}

	out = h.call("update_transaction", map[string]any{"id": "t-expense", "amount": 30, "confirm": true})
	if !strings.Contains(out, "Transaction updated") {
		t.Errorf("unexpected result:\n%s", out)
	}
	sent := (*h.pushed)[0].Transaction[0]
	if sent.Outcome != 30 || sent.Income != 0 {
		t.Errorf("the edit should replace the outcome only, got %v/%v", sent.Outcome, sent.Income)
	}
}

func TestUpdateTransactionClearsFieldsWithAnEmptyString(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("update_transaction", map[string]any{
		"id": "t-expense", "category": "", "payee": "", "confirm": true})
	if !strings.Contains(out, "category: Groceries → uncategorized") {
		t.Errorf("clearing the category should be reported:\n%s", out)
	}
	if !strings.Contains(out, "merchant: Biedronka → (none)") {
		t.Errorf("clearing the payee must drop the merchant too:\n%s", out)
	}

	sent := (*h.pushed)[0].Transaction[0]
	if sent.Tag != nil || sent.Payee != nil || sent.Merchant != nil {
		t.Errorf("cleared fields must go out as null, got tag=%v payee=%v merchant=%v",
			sent.Tag, sent.Payee, sent.Merchant)
	}
}

func TestUpdateTransactionRejectsAmbiguousEdits(t *testing.T) {
	h := newHarness(t, fixture())

	msg := h.callErr("update_transaction", map[string]any{"id": "t-transfer", "account": "Cash PLN"})
	if !strings.Contains(msg, "use from_account") && !strings.Contains(msg, "Use from_account") {
		t.Errorf("a two-sided transaction should reject 'account': %s", msg)
	}

	msg = h.callErr("update_transaction", map[string]any{"id": "t-transfer", "amount": 50})
	if !strings.Contains(msg, "cross-currency") {
		t.Errorf("a cross-currency transaction should reject a single amount: %s", msg)
	}

	msg = h.callErr("update_transaction", map[string]any{"id": "t-expense"})
	if !strings.Contains(msg, "Nothing to update") {
		t.Errorf("an edit with no fields should say so: %s", msg)
	}
}

func TestUpdateTransactionKeepsTheDebtCurrencyInvariant(t *testing.T) {
	h := newHarness(t, fixture())

	// Move the non-debt leg of a debt onto the EUR account: both legs must
	// follow it, because the debt account's own currency never appears.
	h.call("update_transaction", map[string]any{
		"id": "t-debt", "from_account": "Euro card", "confirm": true})

	sent := (*h.pushed)[0].Transaction[0]
	if sent.OutcomeInstrument != 2 || sent.IncomeInstrument != 2 {
		t.Errorf("both legs should carry EUR, got %d/%d", sent.OutcomeInstrument, sent.IncomeInstrument)
	}
}

func TestListRemindersShowsScheduleAndNextOccurrence(t *testing.T) {
	h := newHarness(t, fixture())
	out := h.call("list_reminders", nil)

	if !strings.Contains(out, "2099-04-10") || !strings.Contains(out, "every month") {
		t.Errorf("the recurring reminder is not rendered as expected:\n%s", out)
	}
	if !strings.Contains(out, "nothing planned") || !strings.Contains(out, "one-off on 2026-05-05") {
		t.Errorf("a spent one-off should say nothing is planned:\n%s", out)
	}

	out = h.call("list_reminders", map[string]any{"upcoming_only": true})
	if strings.Contains(out, "r-once") {
		t.Errorf("upcoming_only should drop the spent one-off:\n%s", out)
	}
}

func TestDeleteReminderRefusesToGuessBetweenMatches(t *testing.T) {
	data := fixture()
	data.Reminder = append(data.Reminder, zen.Reminder{
		ID: "r-other", StartDate: "2026-06-01", Comment: ptr("landlord deposit"),
		OutcomeAccount: "cash", IncomeAccount: "cash"})
	h := newHarness(t, data)

	msg := h.callErr("delete_reminder", map[string]any{"name_or_id": "landlord"})
	if !strings.Contains(msg, "matches 2 reminders") || !strings.Contains(msg, "Nothing was deleted") {
		t.Errorf("an ambiguous match must not delete anything: %s", msg)
	}
	if len(*h.pushed) != 0 {
		t.Error("nothing should have been sent")
	}
}

func TestDeleteReminderPreviewsThenDeletesTheSeries(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("delete_reminder", map[string]any{"name_or_id": "r-rent"})
	if !strings.Contains(out, "2 planned occurrences will be deleted with it") {
		t.Errorf("the preview should count the occurrences:\n%s", out)
	}
	if len(*h.pushed) != 0 {
		t.Fatal("a preview must not delete anything")
	}

	out = h.call("delete_reminder", map[string]any{"name_or_id": "r-rent", "confirm": true})
	if !strings.Contains(out, "Deleted reminder") {
		t.Errorf("unexpected result:\n%s", out)
	}
	if got := (*h.pushed)[0].Deletion; len(got) != 1 || got[0].Object != "reminder" {
		t.Fatalf("only the reminder itself should be sent, got %+v", got)
	}
	if got := h.call("list_reminders", nil); strings.Contains(got, "r-rent") {
		t.Error("the deleted series should be gone from local state")
	}
}

func TestSyncDataReportsASummary(t *testing.T) {
	h := newHarness(t, fixture())
	out := h.call("sync_data", nil)

	for _, want := range []string{`"accounts": 4`, `"active_accounts": 4`, `"transactions": 4`, `"reminders": 2`} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %s:\n%s", want, out)
		}
	}
}

func TestToolsSyncOnDemandWithoutSyncDataFirst(t *testing.T) {
	h := newHarness(t, fixture())
	// The very first call is a listing: it must sync by itself.
	if out := h.call("list_accounts", nil); !strings.Contains(out, "Cash PLN") {
		t.Errorf("list_accounts should have synced on demand:\n%s", out)
	}
}

func TestToolsReportASyncFailureAsAToolError(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream is down", http.StatusBadGateway)
	}))
	defer down.Close()
	t.Setenv("ZENMONEY_API_BASE", down.URL)

	api := zen.NewAPI("test-token")
	state := zen.NewState(api, nil)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	Register(server, api, state)

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := server.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_accounts"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("a failed sync should surface as a tool error, not a protocol error")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "Automatic sync failed") {
		t.Errorf("unexpected message: %s", text)
	}
}

func TestAddReminderCreatesARecurringSeries(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("add_reminder", map[string]any{
		"type":       "expense",
		"account":    "Cash PLN",
		"amount":     3200,
		"start_date": "2026-04-10",
		"interval":   "month",
		"category":   "Food",
		"comment":    "Rent",
	})
	if !strings.Contains(out, "Reminder added") {
		t.Errorf("unexpected result:\n%s", out)
	}

	if len(*h.pushed) != 1 {
		t.Fatalf("expected exactly one write, got %d", len(*h.pushed))
	}
	sent := (*h.pushed)[0].Reminder[0]
	if sent.Outcome != 3200 || sent.Income != 0 {
		t.Errorf("an expense reminder should carry only an outcome, got %+v", sent)
	}
	if sent.OutcomeAccount != "cash" || sent.IncomeAccount != "cash" {
		t.Errorf("a one-sided reminder sits on one account, got %+v", sent)
	}
	if sent.Interval == nil || *sent.Interval != "month" {
		t.Errorf("interval should be month, got %v", sent.Interval)
	}
	if sent.Step == nil || *sent.Step != 1 {
		t.Errorf("step should default to 1, got %v", sent.Step)
	}
	if len(sent.Points) != 1 || sent.Points[0] != 0 {
		t.Errorf("points should default to [0], got %v", sent.Points)
	}
	if sent.EndDate != nil {
		t.Errorf("an open-ended series has no end date, got %v", *sent.EndDate)
	}
	if !sent.Notify {
		t.Error("notify should default to true, as in the app")
	}
	if len(sent.Tag) != 1 || sent.Tag[0] != "food" {
		t.Errorf("the category should be resolved, got %v", sent.Tag)
	}

	if out := h.call("list_reminders", nil); !strings.Contains(out, sent.ID) {
		t.Error("the new reminder should be in local state")
	}
}

func TestAddReminderKeepsAOneOffFromRepeating(t *testing.T) {
	h := newHarness(t, fixture())

	h.call("add_reminder", map[string]any{
		"type": "expense", "account": "Cash PLN", "amount": 99, "start_date": "2026-05-05",
	})

	sent := (*h.pushed)[0].Reminder[0]
	if sent.Interval != nil || sent.Step != nil || sent.Points != nil {
		t.Errorf("a one-off carries no schedule, got %+v", sent)
	}
	// It ends the day it starts, so nothing can expand it further.
	if sent.EndDate == nil || *sent.EndDate != "2026-05-05" {
		t.Errorf("a one-off should end on its start date, got %v", sent.EndDate)
	}
}

func TestAddReminderCarriesBothSidesOfATransfer(t *testing.T) {
	h := newHarness(t, fixture())

	msg := h.callErr("add_reminder", map[string]any{
		"type": "transfer", "from_account": "Cash PLN", "to_account": "Euro card",
		"amount": 100, "start_date": "2026-04-05", "interval": "month",
	})
	if !strings.Contains(msg, "income_amount") {
		t.Errorf("a cross-currency transfer needs both amounts: %s", msg)
	}
	if len(*h.pushed) != 0 {
		t.Fatal("nothing should have been sent")
	}

	h.call("add_reminder", map[string]any{
		"type": "transfer", "from_account": "Cash PLN", "to_account": "Euro card",
		"outcome_amount": 110, "income_amount": 100,
		"start_date": "2026-04-05", "interval": "month",
	})

	sent := (*h.pushed)[0].Reminder[0]
	if sent.Outcome != 110 || sent.OutcomeInstrument != 1 {
		t.Errorf("the source leg is wrong: %+v", sent)
	}
	if sent.Income != 100 || sent.IncomeInstrument != 2 {
		t.Errorf("the destination leg is wrong: %+v", sent)
	}
}

func TestAddReminderRejectsBadArguments(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown type", map[string]any{
			"type": "gift", "account": "Cash PLN", "amount": 10, "start_date": "2026-04-01",
		}, "Use one of: expense, income, transfer"},
		{"bad date", map[string]any{
			"type": "expense", "account": "Cash PLN", "amount": 10, "start_date": "01.04.2026",
		}, "YYYY-MM-DD"},
		{"unknown account", map[string]any{
			"type": "expense", "account": "Nowhere", "amount": 10, "start_date": "2026-04-01",
		}, "not found"},
		{"transfer without both accounts", map[string]any{
			"type": "transfer", "account": "Cash PLN", "amount": 10, "start_date": "2026-04-01",
		}, "from_account"},
		{"step without interval", map[string]any{
			"type": "expense", "account": "Cash PLN", "amount": 10,
			"start_date": "2026-04-01", "step": 2,
		}, "repeating reminder"},
		{"unknown interval", map[string]any{
			"type": "expense", "account": "Cash PLN", "amount": 10,
			"start_date": "2026-04-01", "interval": "fortnight",
		}, "Use one of: day, week, month, year"},
		{"point outside the window", map[string]any{
			"type": "expense", "account": "Cash PLN", "amount": 10, "start_date": "2026-04-01",
			"interval": "day", "step": 7, "points": []any{0, 9},
		}, "0…6"},
		{"end before start", map[string]any{
			"type": "expense", "account": "Cash PLN", "amount": 10,
			"start_date": "2026-04-01", "end_date": "2026-03-01", "interval": "month",
		}, "on or after"},
		{"unknown category", map[string]any{
			"type": "expense", "account": "Cash PLN", "amount": 10,
			"start_date": "2026-04-01", "interval": "month", "category": "Yachts",
		}, "not found"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, fixture())
			msg := h.callErr("add_reminder", tc.args)
			if !strings.Contains(msg, tc.want) {
				t.Errorf("expected %q in the error, got: %s", tc.want, msg)
			}
			if len(*h.pushed) != 0 {
				t.Error("a rejected reminder must not be sent")
			}
		})
	}
}

func TestAddReminderSortsAndDeduplicatesPoints(t *testing.T) {
	h := newHarness(t, fixture())

	h.call("add_reminder", map[string]any{
		"type": "expense", "account": "Cash PLN", "amount": 10, "start_date": "2026-04-01",
		"interval": "day", "step": 7, "points": []any{4, 0, 4, 2},
	})

	got := (*h.pushed)[0].Reminder[0].Points
	if len(got) != 3 || got[0] != 0 || got[1] != 2 || got[2] != 4 {
		t.Errorf("points should be sorted and deduplicated, got %v", got)
	}
}

func TestAddReminderReportsTheOccurrencesItGotBack(t *testing.T) {
	h := newHarnessWith(t, fixture(), func(req zen.DiffRequest) zen.DiffResponse {
		return zen.DiffResponse{
			ServerTimestamp: req.ServerTimestamp + 1,
			ReminderMarker: []zen.ReminderMarker{
				{ID: "mk-new-2", Reminder: req.Reminder[0].ID, State: "planned", Date: "2026-05-10"},
				{ID: "mk-new-1", Reminder: req.Reminder[0].ID, State: "planned", Date: "2026-04-10"},
			},
		}
	})

	out := h.call("add_reminder", map[string]any{
		"type": "expense", "account": "Cash PLN", "amount": 3200,
		"start_date": "2026-04-10", "interval": "month",
	})
	if !strings.Contains(out, "Planned: 2026-04-10, 2026-05-10") {
		t.Errorf("the expanded dates should be reported:\n%s", out)
	}
}

func TestAddReminderSaysWhenNoOccurrencesCameBack(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("add_reminder", map[string]any{
		"type": "expense", "account": "Cash PLN", "amount": 3200,
		"start_date": "2026-04-10", "interval": "month",
	})
	if !strings.Contains(out, "No occurrences came back with it yet") {
		t.Errorf("an unexpanded series should say so:\n%s", out)
	}
}

func TestAddReminderMarkerCopiesTheReminder(t *testing.T) {
	h := newHarness(t, fixture())

	out := h.call("add_reminder_marker", map[string]any{
		"reminder": "r-rent", "date": "2099-06-10",
	})
	if !strings.Contains(out, "Occurrence added") {
		t.Errorf("unexpected result:\n%s", out)
	}

	sent := (*h.pushed)[0].ReminderMarker[0]
	if sent.Reminder != "r-rent" || sent.Date != "2099-06-10" || sent.State != "planned" {
		t.Errorf("the occurrence is not anchored to the series: %+v", sent)
	}
	if sent.Outcome != 3200 || sent.OutcomeAccount != "cash" {
		t.Errorf("the reminder's operation should be copied: %+v", sent)
	}
	if sent.Payee == nil || *sent.Payee != "Landlord" {
		t.Errorf("the payee should be copied, got %v", sent.Payee)
	}

	if out := h.call("list_reminders", nil); !strings.Contains(out, "2099-04-10") {
		t.Error("the series should still list its soonest occurrence")
	}
}

func TestAddReminderMarkerOverridesWhatItIsGiven(t *testing.T) {
	h := newHarness(t, fixture())

	h.call("add_reminder_marker", map[string]any{
		"reminder": "r-rent", "date": "2099-06-10",
		"amount": 3400, "comment": "raised", "category": "Food", "notify": true,
	})

	sent := (*h.pushed)[0].ReminderMarker[0]
	if sent.Outcome != 3400 || sent.Income != 0 {
		t.Errorf("amount should replace the outcome side: %+v", sent)
	}
	if sent.Comment == nil || *sent.Comment != "raised" {
		t.Errorf("comment should be overridden, got %v", sent.Comment)
	}
	if len(sent.Tag) != 1 || sent.Tag[0] != "food" {
		t.Errorf("category should be overridden, got %v", sent.Tag)
	}
	if !sent.Notify {
		t.Error("notify should be overridden")
	}
}

func TestAddReminderMarkerRefusesWhatItCannotResolve(t *testing.T) {
	transfer := fixture()
	transfer.Reminder = append(transfer.Reminder, zen.Reminder{
		ID: "r-move", StartDate: "2026-04-01",
		Outcome: 300, Income: 300, OutcomeAccount: "cash", IncomeAccount: "eur",
		OutcomeInstrument: 1, IncomeInstrument: 2,
	})

	cases := []struct {
		name string
		data zen.DiffResponse
		args map[string]any
		want string
	}{
		{"unknown reminder", fixture(), map[string]any{
			"reminder": "nothing like this", "date": "2099-06-10",
		}, "add_reminder"},
		{"ambiguous match", fixture(), map[string]any{
			"reminder": "n", "date": "2099-06-10",
		}, "reminders:"},
		{"day already planned", fixture(), map[string]any{
			"reminder": "r-rent", "date": "2099-04-10",
		}, "already planned"},
		{"bad date", fixture(), map[string]any{
			"reminder": "r-rent", "date": "10.06.2099",
		}, "YYYY-MM-DD"},
		{"bare amount on a transfer", transfer, map[string]any{
			"reminder": "r-move", "date": "2099-06-10", "amount": 400,
		}, "ambiguous"},
		{"unknown category", fixture(), map[string]any{
			"reminder": "r-rent", "date": "2099-06-10", "category": "Yachts",
		}, "not found"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.data)
			msg := h.callErr("add_reminder_marker", tc.args)
			if !strings.Contains(msg, tc.want) {
				t.Errorf("expected %q in the error, got: %s", tc.want, msg)
			}
			if len(*h.pushed) != 0 {
				t.Error("a rejected occurrence must not be sent")
			}
		})
	}
}

func TestAddReminderMarkerClearsAnInheritedValue(t *testing.T) {
	data := fixture()
	data.Reminder[0].Tag = []string{"food"}
	data.Reminder[0].Comment = ptr("membership")
	h := newHarness(t, data)

	h.call("add_reminder_marker", map[string]any{
		"reminder": "r-rent", "date": "2099-06-10", "category": "", "comment": "",
	})

	sent := (*h.pushed)[0].ReminderMarker[0]
	if sent.Tag != nil {
		t.Errorf("an empty category should drop it, got %v", sent.Tag)
	}
	if sent.Comment != nil {
		t.Errorf("an empty comment should drop it, got %v", *sent.Comment)
	}
	// Untouched fields still come from the reminder.
	if sent.Payee == nil || *sent.Payee != "Landlord" {
		t.Errorf("the payee should be untouched, got %v", sent.Payee)
	}
}

func TestAddReminderMarkerPlansADayAlreadyProcessed(t *testing.T) {
	h := newHarness(t, fixture())

	// mk3 sits on 2020-05-05 but is processed, so the day is free again.
	h.call("add_reminder_marker", map[string]any{
		"reminder": "r-once", "date": "2020-05-05",
	})

	if got := (*h.pushed)[0].ReminderMarker[0].Date; got != "2020-05-05" {
		t.Errorf("unexpected date: %s", got)
	}
}
