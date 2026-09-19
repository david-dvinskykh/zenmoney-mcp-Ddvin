package zen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubAPI serves canned diff responses in order, recording each request.
type stubAPI struct {
	t         *testing.T
	responses []DiffResponse
	requests  []DiffRequest
	server    *httptest.Server
}

func newStub(t *testing.T, responses ...DiffResponse) (*API, *stubAPI) {
	t.Helper()
	s := &stubAPI{t: t, responses: responses}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req DiffRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		s.requests = append(s.requests, req)

		if len(s.responses) == 0 {
			t.Errorf("unexpected extra request to %s", r.URL.Path)
			http.Error(w, "no canned response", http.StatusInternalServerError)
			return
		}
		resp := s.responses[0]
		s.responses = s.responses[1:]
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(s.server.Close)

	t.Setenv("ZENMONEY_API_BASE", s.server.URL)
	return NewAPI("test-token"), s
}

func account(id, title, accType string, instrument int64) Account {
	return Account{ID: id, Title: title, Type: accType, Instrument: &instrument}
}

func tx(id, date string, outcome, income float64) Transaction {
	return Transaction{ID: id, Date: date, Outcome: outcome, Income: income,
		IncomeAccount: "acc", OutcomeAccount: "acc"}
}

func TestSyncForcesFullFetchOnFirstRun(t *testing.T) {
	api, stub := newStub(t, DiffResponse{ServerTimestamp: 100})
	st := NewState(api, nil)

	if _, err := st.Sync(context.Background(), false); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	req := stub.requests[0]
	if req.ServerTimestamp != 0 {
		t.Errorf("first sync should ask from timestamp 0, got %d", req.ServerTimestamp)
	}
	if len(req.ForceFetch) == 0 {
		t.Fatal("first sync should send forceFetch")
	}
	for _, want := range []string{"reminder", "reminderMarker", "transaction"} {
		found := false
		for _, got := range req.ForceFetch {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("forceFetch is missing %q: %v", want, req.ForceFetch)
		}
	}
	if st.ServerTimestamp() != 100 {
		t.Errorf("serverTimestamp = %d, want 100", st.ServerTimestamp())
	}
}

func TestSyncIsIncrementalAfterTheFirst(t *testing.T) {
	api, stub := newStub(t,
		DiffResponse{ServerTimestamp: 100},
		DiffResponse{ServerTimestamp: 200},
	)
	st := NewState(api, nil)
	ctx := context.Background()

	mustSync(t, st, ctx, false)
	mustSync(t, st, ctx, false)

	if got := stub.requests[1].ServerTimestamp; got != 100 {
		t.Errorf("second sync should resume from 100, got %d", got)
	}
	if len(stub.requests[1].ForceFetch) != 0 {
		t.Errorf("incremental sync should not force-fetch: %v", stub.requests[1].ForceFetch)
	}
}

func TestMergeKeepsPositionAndAppendsNewEntities(t *testing.T) {
	api, _ := newStub(t,
		DiffResponse{ServerTimestamp: 1, Account: []Account{
			account("a", "Cash", "cash", 1),
			account("b", "Card", "ccard", 1),
		}},
		DiffResponse{ServerTimestamp: 2, Account: []Account{
			account("a", "Wallet", "cash", 1), // renamed, keeps its slot
			account("c", "Savings", "deposit", 1),
		}},
	)
	st := NewState(api, nil)
	ctx := context.Background()
	mustSync(t, st, ctx, false)
	mustSync(t, st, ctx, false)

	got := st.Accounts()
	want := []struct{ id, title string }{{"a", "Wallet"}, {"b", "Card"}, {"c", "Savings"}}
	if len(got) != len(want) {
		t.Fatalf("got %d accounts, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Title != w.title {
			t.Errorf("account %d = %s/%s, want %s/%s", i, got[i].ID, got[i].Title, w.id, w.title)
		}
	}
}

func TestDeletedTransactionsAreDroppedFromTheMerge(t *testing.T) {
	gone := tx("t1", "2026-01-01", 10, 0)
	gone.Deleted = true

	api, _ := newStub(t,
		DiffResponse{ServerTimestamp: 1, Transaction: []Transaction{
			tx("t1", "2026-01-01", 10, 0),
			tx("t2", "2026-01-02", 20, 0),
		}},
		DiffResponse{ServerTimestamp: 2, Transaction: []Transaction{gone}},
	)
	st := NewState(api, nil)
	ctx := context.Background()
	mustSync(t, st, ctx, false)
	mustSync(t, st, ctx, false)

	if got := st.Transactions(); len(got) != 1 || got[0].ID != "t2" {
		t.Errorf("transactions = %+v, want only t2", ids(got))
	}
}

func TestDeletingAnAccountRemovesItsTransactions(t *testing.T) {
	onAccount := tx("t1", "2026-01-01", 10, 0)
	onAccount.OutcomeAccount, onAccount.IncomeAccount = "a", "a"
	elsewhere := tx("t2", "2026-01-02", 20, 0)
	elsewhere.OutcomeAccount, elsewhere.IncomeAccount = "b", "b"

	api, _ := newStub(t,
		DiffResponse{ServerTimestamp: 1,
			Account:     []Account{account("a", "Cash", "cash", 1), account("b", "Card", "ccard", 1)},
			Transaction: []Transaction{onAccount, elsewhere}},
		DiffResponse{ServerTimestamp: 2,
			Deletion: []Deletion{{ID: "a", Object: "account", Stamp: 2, User: 1}}},
	)
	st := NewState(api, nil)
	ctx := context.Background()
	mustSync(t, st, ctx, false)
	mustSync(t, st, ctx, false)

	if len(st.Accounts()) != 1 || st.Accounts()[0].ID != "b" {
		t.Errorf("accounts = %v, want only b", st.Accounts())
	}
	if got := st.Transactions(); len(got) != 1 || got[0].ID != "t2" {
		t.Errorf("transactions = %v, want only t2", ids(got))
	}
}

func TestDeletingATagUntagsTransactionsAndDropsChildren(t *testing.T) {
	parent := "p"
	tagged := tx("t1", "2026-01-01", 10, 0)
	tagged.Tag = []string{"p", "other"}
	onlyTag := tx("t2", "2026-01-02", 10, 0)
	onlyTag.Tag = []string{"p"}

	api, _ := newStub(t,
		DiffResponse{ServerTimestamp: 1,
			Tag: []Tag{
				{ID: "p", Title: "Food"},
				{ID: "c", Title: "Groceries", Parent: &parent},
				{ID: "other", Title: "Fun"},
			},
			Transaction: []Transaction{tagged, onlyTag}},
		DiffResponse{ServerTimestamp: 2,
			Deletion: []Deletion{{ID: "p", Object: "tag", Stamp: 2, User: 1}}},
	)
	st := NewState(api, nil)
	ctx := context.Background()
	mustSync(t, st, ctx, false)
	mustSync(t, st, ctx, false)

	if got := st.Tags(); len(got) != 1 || got[0].ID != "other" {
		t.Errorf("tags = %v, want only the unrelated one", got)
	}

	byID := map[string]Transaction{}
	for _, t2 := range st.Transactions() {
		byID[t2.ID] = t2
	}
	if got := byID["t1"].Tag; len(got) != 1 || got[0] != "other" {
		t.Errorf("t1 tags = %v, want [other]", got)
	}
	if got := byID["t2"].Tag; got != nil {
		t.Errorf("t2 tags = %v, want nil once its only tag is gone", got)
	}
}

func TestDeletingAReminderTakesItsMarkers(t *testing.T) {
	api, _ := newStub(t,
		DiffResponse{ServerTimestamp: 1,
			Reminder: []Reminder{{ID: "r1"}, {ID: "r2"}},
			ReminderMarker: []ReminderMarker{
				{ID: "m1", Reminder: "r1", State: "planned", Date: "2026-02-01"},
				{ID: "m2", Reminder: "r2", State: "planned", Date: "2026-02-02"},
			}},
		DiffResponse{ServerTimestamp: 2,
			Deletion: []Deletion{{ID: "r1", Object: "reminder", Stamp: 2, User: 1}}},
	)
	st := NewState(api, nil)
	ctx := context.Background()
	mustSync(t, st, ctx, false)
	mustSync(t, st, ctx, false)

	if got := st.Reminders(); len(got) != 1 || got[0].ID != "r2" {
		t.Errorf("reminders = %v, want only r2", got)
	}
	if got := st.ReminderMarkers(); len(got) != 1 || got[0].ID != "m2" {
		t.Errorf("markers = %v, want only m2", got)
	}
}

func TestForceFullDropsStaleEntities(t *testing.T) {
	api, stub := newStub(t,
		DiffResponse{ServerTimestamp: 1, Account: []Account{account("a", "Cash", "cash", 1)}},
		DiffResponse{ServerTimestamp: 2, Account: []Account{account("b", "Card", "ccard", 1)}},
	)
	st := NewState(api, nil)
	ctx := context.Background()
	mustSync(t, st, ctx, false)
	mustSync(t, st, ctx, true)

	if got := stub.requests[1].ServerTimestamp; got != 0 {
		t.Errorf("force-full sync should ask from 0, got %d", got)
	}
	if got := st.Accounts(); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("accounts = %v, want only the re-downloaded one", got)
	}
}

func TestCacheRestoreMakesTheNextSyncIncremental(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZENMONEY_CACHE_DIR", dir)

	api, stub := newStub(t,
		DiffResponse{ServerTimestamp: 100, Account: []Account{account("a", "Cash", "cash", 1)}},
		DiffResponse{ServerTimestamp: 150},
	)
	cache := NewCache("test-token")
	ctx := context.Background()

	first := NewState(api, cache)
	mustSync(t, first, ctx, false)

	// A second process, as if the stdio server were restarted.
	second := NewState(api, cache)
	if err := second.EnsureSynced(ctx); err != nil {
		t.Fatalf("EnsureSynced: %v", err)
	}

	if got := stub.requests[1].ServerTimestamp; got != 100 {
		t.Errorf("restarted process should resume from 100, got %d", got)
	}
	if got := second.Accounts(); len(got) != 1 || got[0].ID != "a" {
		t.Errorf("restarted process lost the cached accounts: %v", got)
	}
}

func TestCacheOfAnOlderVersionIsIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZENMONEY_CACHE_DIR", dir)

	cache := NewCache("test-token")
	stale := map[string]any{"version": 1, "serverTimestamp": 100, "accounts": []any{}}
	raw, _ := json.Marshal(stale)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := cache.Load(); got != nil {
		t.Errorf("a version 1 snapshot should be dropped, got %+v", got)
	}
}

func TestCacheFileIsNamedByTokenHashAndKeepsNoToken(t *testing.T) {
	dir := t.TempDir()
	cache := NewCacheIn("super-secret-token", dir)

	if filepath.Base(cache.Path) != TokenHash("super-secret-token")+".json" {
		t.Errorf("cache file %q is not named after the token hash", cache.Path)
	}
	if err := cache.Save(CacheData{ServerTimestamp: 7}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(cache.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret-token") {
		t.Error("the snapshot must never contain the token itself")
	}
	if got := cache.Load(); got == nil || got.ServerTimestamp != 7 {
		t.Errorf("round trip lost the timestamp: %+v", got)
	}
}

func TestStaleCacheIsServedWhenTheLiveSyncFails(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZENMONEY_CACHE_DIR", dir)
	cache := NewCache("test-token")
	if err := cache.Save(CacheData{
		ServerTimestamp: 100,
		Accounts:        []Account{account("a", "Cash", "cash", 1)},
	}); err != nil {
		t.Fatal(err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream is down", http.StatusBadGateway)
	}))
	defer down.Close()
	t.Setenv("ZENMONEY_API_BASE", down.URL)

	st := NewState(NewAPI("test-token"), cache)
	if err := st.EnsureSynced(context.Background()); err != nil {
		t.Fatalf("a cached snapshot should be served instead of failing: %v", err)
	}
	if got := st.Accounts(); len(got) != 1 {
		t.Errorf("accounts = %v, want the cached one", got)
	}
	if st.StaleReason() == "" {
		t.Error("serving stale data should record why")
	}
}

func TestEnsureSyncedFailsWhenThereIsNoCacheToFallBackOn(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream is down", http.StatusBadGateway)
	}))
	defer down.Close()
	t.Setenv("ZENMONEY_API_BASE", down.URL)

	st := NewState(NewAPI("test-token"), nil)
	if err := st.EnsureSynced(context.Background()); err == nil {
		t.Fatal("EnsureSynced should report the failure when nothing is cached")
	}
}

func TestApplyLocalDeletionsAppliesTheResponseDiffToo(t *testing.T) {
	api, _ := newStub(t, DiffResponse{ServerTimestamp: 1, Transaction: []Transaction{
		tx("mine", "2026-01-01", 10, 0),
		tx("theirs", "2026-01-02", 20, 0),
	}})
	st := NewState(api, nil)
	mustSync(t, st, context.Background(), false)

	// The write response carries a deletion made by another client. Applying it
	// matters: advancing the timestamp past it would strand the transaction.
	st.ApplyLocalDeletions(
		[]Deletion{{ID: "mine", Object: "transaction", Stamp: 5, User: 1}},
		&DiffResponse{ServerTimestamp: 5,
			Deletion: []Deletion{{ID: "theirs", Object: "transaction", Stamp: 4, User: 1}}},
	)

	if got := st.Transactions(); len(got) != 0 {
		t.Errorf("transactions = %v, want both gone", ids(got))
	}
	if st.ServerTimestamp() != 5 {
		t.Errorf("serverTimestamp = %d, want 5", st.ServerTimestamp())
	}
}

func TestApplyLocalTransactionLetsTheServerEchoWin(t *testing.T) {
	api, _ := newStub(t, DiffResponse{ServerTimestamp: 1})
	st := NewState(api, nil)
	mustSync(t, st, context.Background(), false)

	optimistic := tx("t1", "2026-01-01", 10, 0)
	echoed := tx("t1", "2026-01-01", 10, 0)
	echoed.Comment = strPtr("normalized by the server")

	st.ApplyLocalTransaction(optimistic, &DiffResponse{
		ServerTimestamp: 2,
		Transaction:     []Transaction{echoed},
	})

	got := st.Transactions()
	if len(got) != 1 {
		t.Fatalf("transactions = %v, want exactly one", ids(got))
	}
	if got[0].Comment == nil || *got[0].Comment != "normalized by the server" {
		t.Errorf("the server's copy should win, got %+v", got[0].Comment)
	}
}

func TestUserPrefersTheAccountOwner(t *testing.T) {
	parent := int64(1)
	api, _ := newStub(t, DiffResponse{ServerTimestamp: 1, User: []User{
		{ID: 2, Parent: &parent},
		{ID: 1, Parent: nil},
	}})
	st := NewState(api, nil)
	mustSync(t, st, context.Background(), false)

	if u := st.User(); u == nil || u.ID != 1 {
		t.Errorf("User() = %+v, want the one without a parent", u)
	}
}

func mustSync(t *testing.T, st *State, ctx context.Context, forceFull bool) {
	t.Helper()
	if _, err := st.Sync(ctx, forceFull); err != nil {
		t.Fatalf("Sync: %v", err)
	}
}

func ids(txs []Transaction) []string {
	out := make([]string, len(txs))
	for i, t := range txs {
		out[i] = t.ID
	}
	return out
}

func strPtr(s string) *string { return &s }
