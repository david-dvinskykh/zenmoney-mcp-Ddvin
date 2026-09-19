package zen

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// State is the in-memory snapshot every tool reads from.
//
// Unlike the single-threaded TypeScript original, one State is shared by all
// concurrent MCP sessions in HTTP mode, so it is guarded by a mutex. Writers
// always replace a slice rather than mutate it in place, which lets a reader
// keep using the slice it was handed after the lock is released.
type State struct {
	api   *API
	cache *Cache

	mu              sync.RWMutex
	serverTimestamp int64
	accounts        []Account
	tags            []Tag
	merchants       []Merchant
	companies       []Company
	instruments     []Instrument
	transactions    []Transaction
	users           []User
	reminders       []Reminder
	reminderMarkers []ReminderMarker
	// syncedAt is the unix second of the snapshot currently in memory (0 if
	// never synced).
	syncedAt int64
	// staleReason is set when the data came from cache because a live sync
	// failed.
	staleReason       string
	synced            bool
	restoredFromCache bool

	// syncOnce guards the initial sync so concurrent callers share one attempt.
	initMu  sync.Mutex
	initing chan struct{}
	initErr error
}

func NewState(api *API, cache *Cache) *State {
	return &State{api: api, cache: cache}
}

func (s *State) ServerTimestamp() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serverTimestamp
}

func (s *State) Accounts() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accounts
}

func (s *State) Tags() []Tag {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tags
}

func (s *State) Merchants() []Merchant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.merchants
}

func (s *State) Instruments() []Instrument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.instruments
}

func (s *State) Transactions() []Transaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.transactions
}

func (s *State) Reminders() []Reminder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reminders
}

func (s *State) ReminderMarkers() []ReminderMarker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reminderMarkers
}

func (s *State) IsFromCache() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.restoredFromCache
}

func (s *State) StaleReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.staleReason
}

func (s *State) CachePath() string {
	if s.cache == nil {
		return ""
	}
	return s.cache.Path
}

// EnsureSynced makes data available before serving a tool call: it restores the
// on-disk snapshot when there is one, then brings it up to date with an
// incremental sync. Concurrent callers share a single in-flight attempt.
func (s *State) EnsureSynced(ctx context.Context) error {
	s.mu.RLock()
	done := s.synced
	s.mu.RUnlock()
	if done {
		return nil
	}

	s.initMu.Lock()
	if s.initing == nil {
		ch := make(chan struct{})
		s.initing = ch
		go func() {
			err := s.initialSync(context.WithoutCancel(ctx))
			s.initMu.Lock()
			s.initErr = err
			s.initing = nil
			s.initMu.Unlock()
			close(ch)
		}()
	}
	waitOn := s.initing
	s.initMu.Unlock()

	if waitOn == nil {
		// The attempt finished between the check above and the lock.
		return s.readInitErr()
	}

	select {
	case <-waitOn:
		return s.readInitErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *State) readInitErr() error {
	s.initMu.Lock()
	defer s.initMu.Unlock()
	return s.initErr
}

func (s *State) initialSync(ctx context.Context) error {
	restored := s.RestoreFromCache()

	if restored && s.isCacheFresh() {
		s.mu.Lock()
		s.synced = true
		s.mu.Unlock()
		return nil
	}

	if _, err := s.Sync(ctx, false); err != nil {
		if !restored {
			return err
		}
		// Serve the cached snapshot rather than failing outright — the data is
		// usable, just possibly behind.
		s.mu.Lock()
		s.synced = true
		s.staleReason = err.Error()
		s.mu.Unlock()
	}
	return nil
}

// RestoreFromCache seeds state from the on-disk snapshot. It reports false on a
// cache miss.
func (s *State) RestoreFromCache() bool {
	if s.cache == nil {
		return false
	}
	data := s.cache.Load()
	if data == nil {
		return false
	}

	// A snapshot should never hold deleted transactions, but drop any that a
	// previous version (or a partial write) left behind rather than serving them.
	live := make([]Transaction, 0, len(data.Transactions))
	for _, t := range data.Transactions {
		if !t.Deleted {
			live = append(live, t)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.serverTimestamp = data.ServerTimestamp
	s.accounts = data.Accounts
	s.tags = data.Tags
	s.merchants = data.Merchants
	s.companies = data.Companies
	s.instruments = data.Instruments
	s.transactions = live
	s.users = data.Users
	s.reminders = data.Reminders
	s.reminderMarkers = data.ReminderMarkers
	s.syncedAt = data.SavedAt
	s.restoredFromCache = true
	return true
}

// isCacheFresh skips the network entirely while the snapshot is younger than
// ZENMONEY_CACHE_TTL seconds. It defaults to 0 — always revalidate, which is
// cheap because the sync is incremental from the cached timestamp.
func (s *State) isCacheFresh() bool {
	ttl, err := strconv.ParseFloat(os.Getenv("ZENMONEY_CACHE_TTL"), 64)
	if err != nil || ttl <= 0 {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return float64(time.Now().Unix()-s.syncedAt) < ttl
}

// Sync brings the snapshot up to date. With forceFull it re-downloads
// everything instead of asking for a diff.
func (s *State) Sync(ctx context.Context, forceFull bool) (*DiffResponse, error) {
	s.mu.Lock()
	if forceFull {
		// Drop everything so a full re-download cannot leave stale entities
		// behind (the merge is additive and would otherwise keep them).
		s.accounts = nil
		s.tags = nil
		s.merchants = nil
		s.companies = nil
		s.instruments = nil
		s.transactions = nil
		s.users = nil
		s.reminders = nil
		s.reminderMarkers = nil
		s.restoredFromCache = false
	}
	needRestore := !forceFull && !s.synced && !s.restoredFromCache
	s.mu.Unlock()

	if needRestore {
		// Fresh process: pick up where the last one left off so this sync is
		// incremental instead of a full re-download.
		s.RestoreFromCache()
	}

	var timestamp int64
	if !forceFull {
		timestamp = s.ServerTimestamp()
	}

	req := DiffRequest{
		CurrentClientTimestamp: time.Now().Unix(),
		ServerTimestamp:        timestamp,
	}
	if timestamp == 0 {
		req.ForceFetch = []string{
			"instrument", "company", "account", "tag", "merchant",
			"reminder", "reminderMarker", "transaction", "user",
		}
	}

	resp, err := s.api.Diff(ctx, req)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.applyDiffLocked(resp)
	s.synced = true
	s.syncedAt = time.Now().Unix()
	s.staleReason = ""
	s.mu.Unlock()

	s.Persist()
	return resp, nil
}

// ApplyLocalTransaction records a transaction that was just pushed to ZenMoney
// so the local snapshot stays consistent without another round trip.
//
// The write response is itself a diff since our last serverTimestamp, so it also
// carries everything that changed elsewhere in the meantime, deletions included.
// It has to be applied, not just read for its timestamp: advancing the timestamp
// past a deletion we never applied strands the deleted transaction in state, and
// no later incremental sync reports it again.
func (s *State) ApplyLocalTransaction(t Transaction, resp *DiffResponse) {
	s.mu.Lock()
	// Optimistic copy first so the server's view of it wins — an echo replaces
	// it, a deletion from another client removes it.
	kept := make([]Transaction, 0, len(s.transactions)+1)
	for _, existing := range s.transactions {
		if existing.ID != t.ID {
			kept = append(kept, existing)
		}
	}
	s.transactions = append(kept, t)
	s.applyDiffLocked(resp)
	s.mu.Unlock()

	s.Persist()
}

// ApplyLocalReminder records a reminder that was just pushed to ZenMoney. Same
// contract as ApplyLocalTransaction: the optimistic copy goes in first so the
// server's echo replaces it, then the whole response diff is applied.
//
// The occurrences are not written here — ZenMoney expands the series into
// reminderMarkers itself and returns them in the same response.
func (s *State) ApplyLocalReminder(r Reminder, resp *DiffResponse) {
	s.mu.Lock()
	kept := make([]Reminder, 0, len(s.reminders)+1)
	for _, existing := range s.reminders {
		if existing.ID != r.ID {
			kept = append(kept, existing)
		}
	}
	s.reminders = append(kept, r)
	s.applyDiffLocked(resp)
	s.mu.Unlock()

	s.Persist()
}

// ApplyLocalReminderMarker records a single occurrence that was just pushed, as
// above.
func (s *State) ApplyLocalReminderMarker(m ReminderMarker, resp *DiffResponse) {
	s.mu.Lock()
	kept := make([]ReminderMarker, 0, len(s.reminderMarkers)+1)
	for _, existing := range s.reminderMarkers {
		if existing.ID != m.ID {
			kept = append(kept, existing)
		}
	}
	s.reminderMarkers = append(kept, m)
	s.applyDiffLocked(resp)
	s.mu.Unlock()

	s.Persist()
}

// ApplyLocalDeletions records deletions that were just pushed to ZenMoney so the
// local snapshot matches the server without another round trip. Like a write,
// the response is a diff since our last serverTimestamp and has to be applied in
// full.
func (s *State) ApplyLocalDeletions(deletions []Deletion, resp *DiffResponse) {
	s.mu.Lock()
	for _, d := range deletions {
		s.applyDeletionLocked(d.Object, d.ID)
	}
	s.applyDiffLocked(resp)
	s.mu.Unlock()

	s.Persist()
}

// ClearCache drops the on-disk snapshot (used by force_full re-downloads).
func (s *State) ClearCache() {
	if s.cache == nil {
		return
	}
	// Best effort — the next successful sync overwrites it anyway.
	_ = s.cache.Clear()
}

// Persist writes the current snapshot to disk. It never fails the caller —
// caching is best effort.
func (s *State) Persist() {
	if s.cache == nil {
		return
	}
	s.mu.RLock()
	data := CacheData{
		ServerTimestamp: s.serverTimestamp,
		Accounts:        s.accounts,
		Tags:            s.tags,
		Merchants:       s.merchants,
		Companies:       s.companies,
		Instruments:     s.instruments,
		Transactions:    s.transactions,
		Users:           s.users,
		Reminders:       s.reminders,
		ReminderMarkers: s.reminderMarkers,
	}
	s.mu.RUnlock()

	if err := s.cache.Save(data); err != nil {
		log.Printf("Failed to write ZenMoney cache: %v", err)
	}
}

func (s *State) applyDiffLocked(resp *DiffResponse) {
	s.serverTimestamp = resp.ServerTimestamp
	s.instruments = mergeByID(s.instruments, resp.Instrument, func(e Instrument) string { return strconv.FormatInt(e.ID, 10) })
	s.accounts = mergeByID(s.accounts, resp.Account, func(e Account) string { return e.ID })
	s.tags = mergeByID(s.tags, resp.Tag, func(e Tag) string { return e.ID })
	s.merchants = mergeByID(s.merchants, resp.Merchant, func(e Merchant) string { return e.ID })
	s.companies = mergeByID(s.companies, resp.Company, func(e Company) string { return strconv.FormatInt(e.ID, 10) })
	s.users = mergeByID(s.users, resp.User, func(e User) string { return strconv.FormatInt(e.ID, 10) })
	s.reminders = mergeByID(s.reminders, resp.Reminder, func(e Reminder) string { return e.ID })
	s.reminderMarkers = mergeByID(s.reminderMarkers, resp.ReminderMarker, func(e ReminderMarker) string { return e.ID })
	s.mergeTransactionsLocked(resp.Transaction)

	for _, d := range resp.Deletion {
		s.applyDeletionLocked(d.Object, d.ID)
	}
}

// mergeByID upserts incoming entities into existing, keeping the position of
// entities that are replaced and appending genuinely new ones — the same
// ordering a JavaScript Map gives the original implementation.
func mergeByID[T any](existing, incoming []T, id func(T) string) []T {
	if len(incoming) == 0 {
		return existing
	}

	out := make([]T, len(existing))
	copy(out, existing)
	pos := make(map[string]int, len(out))
	for i, e := range out {
		pos[id(e)] = i
	}
	for _, e := range incoming {
		key := id(e)
		if i, ok := pos[key]; ok {
			out[i] = e
			continue
		}
		pos[key] = len(out)
		out = append(out, e)
	}
	return out
}

func (s *State) mergeTransactionsLocked(incoming []Transaction) {
	if len(incoming) == 0 {
		return
	}

	out := make([]Transaction, len(s.transactions))
	copy(out, s.transactions)
	pos := make(map[string]int, len(out))
	for i, t := range out {
		pos[t.ID] = i
	}

	deleted := make(map[string]bool)
	for _, t := range incoming {
		if t.Deleted {
			deleted[t.ID] = true
			continue
		}
		delete(deleted, t.ID)
		if i, ok := pos[t.ID]; ok {
			out[i] = t
			continue
		}
		pos[t.ID] = len(out)
		out = append(out, t)
	}

	if len(deleted) > 0 {
		kept := out[:0]
		for _, t := range out {
			if !deleted[t.ID] {
				kept = append(kept, t)
			}
		}
		out = kept
	}
	s.transactions = out
}

func (s *State) applyDeletionLocked(objectType, id string) {
	switch objectType {
	case "transaction":
		s.transactions = filter(s.transactions, func(t Transaction) bool { return t.ID != id })

	case "account":
		s.accounts = filter(s.accounts, func(a Account) bool { return a.ID != id })
		// ZenMoney removes an account's transactions along with it; mirror that
		// so the snapshot doesn't keep entries pointing at a missing account.
		s.transactions = filter(s.transactions, func(t Transaction) bool {
			return t.IncomeAccount != id && t.OutcomeAccount != id
		})

	case "tag":
		// Child categories go with the parent, and transactions lose the tag
		// rather than keeping a dangling id.
		s.tags = filter(s.tags, func(t Tag) bool {
			return t.ID != id && (t.Parent == nil || *t.Parent != id)
		})
		s.transactions = mapSlice(s.transactions, func(t Transaction) Transaction {
			if !contains(t.Tag, id) {
				return t
			}
			rest := filter(t.Tag, func(tagID string) bool { return tagID != id })
			if len(rest) == 0 {
				rest = nil
			}
			t.Tag = rest
			return t
		})

	case "merchant":
		s.merchants = filter(s.merchants, func(m Merchant) bool { return m.ID != id })
		s.transactions = mapSlice(s.transactions, func(t Transaction) Transaction {
			if t.Merchant != nil && *t.Merchant == id {
				t.Merchant = nil
			}
			return t
		})

	case "reminder":
		s.reminders = filter(s.reminders, func(r Reminder) bool { return r.ID != id })
		// The series goes with its template: ZenMoney drops the occurrences
		// too, so keeping them would leave planned entries with no reminder.
		s.reminderMarkers = filter(s.reminderMarkers, func(m ReminderMarker) bool { return m.Reminder != id })

	case "reminderMarker":
		s.reminderMarkers = filter(s.reminderMarkers, func(m ReminderMarker) bool { return m.ID != id })
	}
}

// ActiveAccounts returns the accounts that are not archived.
func (s *State) ActiveAccounts() []Account {
	return filter(s.Accounts(), func(a Account) bool { return !a.Archive })
}

func (s *State) Instrument(id int64) *Instrument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.instruments {
		if s.instruments[i].ID == id {
			return &s.instruments[i]
		}
	}
	return nil
}

// InstrumentTitle is the short currency code, or "" when the instrument is
// unknown — the shape every rendering path wants.
func (s *State) InstrumentTitle(id int64) string {
	if instr := s.Instrument(id); instr != nil {
		return instr.ShortTitle
	}
	return ""
}

func (s *State) Company(id int64) *Company {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.companies {
		if s.companies[i].ID == id {
			return &s.companies[i]
		}
	}
	return nil
}

// User returns the account owner: the one user without a parent, falling back to
// the first one.
func (s *State) User() *User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.users {
		if s.users[i].Parent == nil {
			return &s.users[i]
		}
	}
	if len(s.users) > 0 {
		return &s.users[0]
	}
	return nil
}

func (s *State) TagHierarchy() []TagGroup {
	tags := s.Tags()
	var groups []TagGroup
	for _, parent := range tags {
		if parent.Parent != nil {
			continue
		}
		children := filter(tags, func(t Tag) bool { return t.Parent != nil && *t.Parent == parent.ID })
		groups = append(groups, TagGroup{Parent: parent, Children: children})
	}
	return groups
}

// TagGroup is one top-level category with its subcategories.
type TagGroup struct {
	Parent   Tag
	Children []Tag
}

// SyncSummary is what sync_data reports back.
type SyncSummary struct {
	Accounts        int    `json:"accounts"`
	ActiveAccounts  int    `json:"active_accounts"`
	Categories      int    `json:"categories"`
	Merchants       int    `json:"merchants"`
	Transactions    int    `json:"transactions"`
	Reminders       int    `json:"reminders"`
	Currencies      int    `json:"currencies"`
	ServerTimestamp int64  `json:"serverTimestamp"`
	CacheFile       string `json:"cache_file"`
}

func (s *State) Summary() SyncSummary {
	path := s.CachePath()
	if path == "" {
		path = "disabled"
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	active := 0
	for _, a := range s.accounts {
		if !a.Archive {
			active++
		}
	}
	return SyncSummary{
		Accounts:        len(s.accounts),
		ActiveAccounts:  active,
		Categories:      len(s.tags),
		Merchants:       len(s.merchants),
		Transactions:    len(s.transactions),
		Reminders:       len(s.reminders),
		Currencies:      len(s.instruments),
		ServerTimestamp: s.serverTimestamp,
		CacheFile:       path,
	}
}

func filter[T any](in []T, keep func(T) bool) []T {
	out := make([]T, 0, len(in))
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func mapSlice[T any](in []T, f func(T) T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
