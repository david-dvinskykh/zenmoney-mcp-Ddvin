package tools

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

// textResult wraps a plain string in the tool-result shape every tool returns.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
		IsError: true,
	}
}

// ensureSynced syncs on demand so callers never have to run sync_data first. It
// returns nil when data is ready, or a tool error result describing why it is
// not.
func ensureSynced(ctx context.Context, st *zen.State) *mcp.CallToolResult {
	if err := st.EnsureSynced(ctx); err != nil {
		return errorResult("Automatic sync failed: " + err.Error())
	}
	return nil
}

func findAccount(st *zen.State, id string) *zen.Account {
	accounts := st.Accounts()
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}

func findTag(st *zen.State, id string) *zen.Tag {
	tags := st.Tags()
	for i := range tags {
		if tags[i].ID == id {
			return &tags[i]
		}
	}
	return nil
}

func findMerchant(st *zen.State, id string) *zen.Merchant {
	merchants := st.Merchants()
	for i := range merchants {
		if merchants[i].ID == id {
			return &merchants[i]
		}
	}
	return nil
}

func findAccountByName(st *zen.State, name string) *zen.Account {
	lower := strings.ToLower(name)
	accounts := st.Accounts()
	for i := range accounts {
		if strings.Contains(strings.ToLower(accounts[i].Title), lower) {
			return &accounts[i]
		}
	}
	return nil
}

func findTagByName(st *zen.State, name string) *zen.Tag {
	lower := strings.ToLower(name)
	tags := st.Tags()
	for i := range tags {
		if strings.Contains(strings.ToLower(tags[i].Title), lower) {
			return &tags[i]
		}
	}
	return nil
}

func findMerchantByName(st *zen.State, name string) *zen.Merchant {
	lower := strings.ToLower(name)
	merchants := st.Merchants()
	for i := range merchants {
		if strings.Contains(strings.ToLower(merchants[i].Title), lower) {
			return &merchants[i]
		}
	}
	return nil
}

// resolveAccount looks up an account by exact UUID first, then by a loose name
// match.
func resolveAccount(st *zen.State, nameOrID string) *zen.Account {
	if a := findAccount(st, nameOrID); a != nil {
		return a
	}
	return findAccountByName(st, nameOrID)
}

// resolveTag looks up a category by UUID or name and returns it as a tag list.
func resolveTag(st *zen.State, nameOrID string) []string {
	if t := findTag(st, nameOrID); t != nil {
		return []string{t.ID}
	}
	if t := findTagByName(st, nameOrID); t != nil {
		return []string{t.ID}
	}
	return nil
}

// tagTitles renders tag ids as their titles, the way the add/update tools report
// them.
func tagTitles(st *zen.State, ids []string) string {
	if len(ids) == 0 {
		return "uncategorized"
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if tag := findTag(st, id); tag != nil {
			names = append(names, tag.Title)
		} else {
			names = append(names, id)
		}
	}
	return strings.Join(names, ", ")
}

// resolveMerchantByTitle finds the merchant ZenMoney would attach to this payee.
//
// merchant is the normalized counterparty and takes precedence over the raw
// payee string in the app's UI, so a payee change that left a stale merchant
// behind would look like nothing happened. The match is exact (case- and
// space-insensitive) on purpose — the loose matching used for accounts and
// categories would happily pick "Masha" for "Mashal".
func resolveMerchantByTitle(st *zen.State, title string) *zen.Merchant {
	normalized := strings.ToLower(strings.TrimSpace(title))
	if normalized == "" {
		return nil
	}
	merchants := st.Merchants()
	for i := range merchants {
		if strings.ToLower(strings.TrimSpace(merchants[i].Title)) == normalized {
			return &merchants[i]
		}
	}
	return nil
}

// findDebtAccount returns the single system account ZenMoney books every debt
// against.
func findDebtAccount(st *zen.State) *zen.Account {
	accounts := st.Accounts()
	for i := range accounts {
		if accounts[i].Type == "debt" {
			return &accounts[i]
		}
	}
	return nil
}

func accountTitleOr(a *zen.Account, fallback string) string {
	if a == nil {
		return fallback
	}
	return a.Title
}

// accountName renders an account id as its title, falling back to the id.
func accountName(st *zen.State, id string) string {
	if a := findAccount(st, id); a != nil {
		return a.Title
	}
	return id
}

// instrumentOf is the currency an account books in, falling back to the user's
// own currency and finally to 1 — the same chain the TypeScript server uses.
func instrumentOf(a *zen.Account, userCurrency int64) int64 {
	if a != nil && a.Instrument != nil {
		return *a.Instrument
	}
	if userCurrency != 0 {
		return userCurrency
	}
	return 1
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
