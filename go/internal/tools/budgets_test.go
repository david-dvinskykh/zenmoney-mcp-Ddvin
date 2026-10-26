package tools

import (
	"strings"
	"testing"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

func budgetFixture() zen.DiffResponse {
	data := fixture()
	data.Budget = []zen.Budget{
		{Tag: ptr("salary"), Date: "2026-03-01", Income: 8000, IncomeLock: true},
		{Tag: ptr("food"), Date: "2026-03-01", Outcome: 500, OutcomeLock: true},
		{Tag: nil, Date: "2026-03-01", Outcome: 2000},
		{Tag: ptr("groceries"), Date: "2026-03-01"},
		{Tag: ptr("food"), Date: "2026-04-01", Outcome: 900, OutcomeLock: true},
	}
	return data
}

func TestListBudgetsRendersTheMonth(t *testing.T) {
	h := newHarness(t, budgetFixture())

	out := h.call("list_budgets", map[string]any{"month": "2026-03"})
	want := "Budgets for 2026-03 (3):\n\n" +
		"- **(whole month)**: expense 2000 PLN (suggested), income none\n" +
		"- **Food**: expense 500 PLN, income none — id: `food`\n" +
		"- **Salary**: expense none, income 8000 PLN — id: `salary`"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}

	if out := h.call("list_budgets", map[string]any{"month": "2026-05"}); out != "No budgets for 2026-05." {
		t.Errorf("unexpected empty-month output: %s", out)
	}
	if msg := h.callErr("list_budgets", map[string]any{"month": "2026-13"}); !strings.Contains(msg, "YYYY-MM") {
		t.Errorf("a bad month should be rejected: %s", msg)
	}
}

func TestSetBudgetPreviewsThenWritesALockedAmount(t *testing.T) {
	h := newHarness(t, budgetFixture())

	out := h.call("set_budget", map[string]any{"month": "2026-03", "category": "Food", "outcome": 650})
	want := "Budget for **Food**, 2026-03:\n- expense: 500 PLN → 650 PLN\n\n" +
		"Nothing has been saved yet. Call set_budget again with confirm=true to save."
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if len(*h.pushed) != 0 {
		t.Fatal("a preview must not write anything")
	}

	out = h.call("set_budget", map[string]any{"month": "2026-03", "category": "food", "outcome": 650, "confirm": true})
	if out != "Saved. Budget for **Food**, 2026-03:\n- expense: 500 PLN → 650 PLN" {
		t.Errorf("unexpected result:\n%s", out)
	}
	sent := (*h.pushed)[0].Budget
	if len(sent) != 1 || deref(sent[0].Tag) != "food" || sent[0].Date != "2026-03-01" ||
		sent[0].Outcome != 650 || !sent[0].OutcomeLock || sent[0].User != 1 {
		t.Fatalf("unexpected budget sent: %+v", sent)
	}
	if got := h.call("list_budgets", map[string]any{"month": "2026-03"}); !strings.Contains(got, "expense 650 PLN") {
		t.Errorf("local state should carry the new amount:\n%s", got)
	}
}

func TestSetBudgetCreatesAndRemoves(t *testing.T) {
	h := newHarness(t, budgetFixture())

	h.call("set_budget", map[string]any{"month": "2026-06", "category": "Salary", "income": 3000, "confirm": true})
	created := (*h.pushed)[0].Budget[0]
	if created.Date != "2026-06-01" || created.Income != 3000 || !created.IncomeLock || created.OutcomeLock {
		t.Fatalf("unexpected new budget: %+v", created)
	}

	out := h.call("set_budget", map[string]any{"month": "2026-03", "category": "Food", "outcome": 0, "confirm": true})
	if !strings.Contains(out, "- expense: 500 PLN → none") {
		t.Errorf("unexpected removal output:\n%s", out)
	}
	removed := (*h.pushed)[1].Budget[0]
	if removed.Outcome != 0 || removed.OutcomeLock {
		t.Fatalf("a removal should write 0 unlocked: %+v", removed)
	}
}

func TestSetBudgetRejectsBadInput(t *testing.T) {
	h := newHarness(t, budgetFixture())

	if msg := h.callErr("set_budget", map[string]any{"month": "2026-03", "category": "Food"}); !strings.Contains(msg, "Nothing to set") {
		t.Errorf("no amount: %s", msg)
	}
	if msg := h.callErr("set_budget", map[string]any{"month": "2026-03", "category": "Nope", "outcome": 1}); !strings.Contains(msg, `Category "Nope" not found`) {
		t.Errorf("unknown category: %s", msg)
	}
	if msg := h.callErr("set_budget", map[string]any{"month": "2026-03", "category": "Food", "outcome": -1}); !strings.Contains(msg, "negative") {
		t.Errorf("negative amount: %s", msg)
	}
}
