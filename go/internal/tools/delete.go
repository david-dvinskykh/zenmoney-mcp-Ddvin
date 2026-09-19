package tools

import (
	"context"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

// objectTypes are the entity types delete_object accepts, mapped to their
// diff-protocol names.
var objectTypes = map[string]string{
	"account":  "account",
	"category": "tag",
	"merchant": "merchant",
}

type deleteTransactionArgs struct {
	ID      *string  `json:"id,omitempty" jsonschema:"Transaction UUID to delete"`
	IDs     []string `json:"ids,omitempty" jsonschema:"Several transaction UUIDs to delete at once"`
	Confirm *bool    `json:"confirm,omitempty" jsonschema:"Set to true to actually delete. When false (the default) the tool only reports what would be deleted."`
}

type deleteObjectArgs struct {
	Type     string `json:"type" jsonschema:"What to delete: account, category or merchant"`
	NameOrID string `json:"name_or_id" jsonschema:"Name or UUID of the object. Names are matched loosely."`
	Confirm  *bool  `json:"confirm,omitempty" jsonschema:"Set to true to actually delete. When false (the default) the tool only reports what would be deleted."`
}

func registerDelete(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_transaction",
		Description: "Permanently delete one or more transactions from ZenMoney. Works for every kind of " +
			"transaction — expenses, income, transfers between accounts, and debts (loans given or taken). " +
			"Get the ids from list_transactions, which prints one at the end of each row. The first call " +
			"previews what would be deleted and changes nothing; repeat it with confirm=true to actually " +
			"delete. Deletion cannot be undone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args deleteTransactionArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		var requested []string
		seen := map[string]bool{}
		if args.ID != nil {
			requested = append(requested, *args.ID)
			seen[*args.ID] = true
		}
		for _, id := range args.IDs {
			if !seen[id] {
				requested = append(requested, id)
				seen[id] = true
			}
		}

		if len(requested) == 0 {
			return errorResult("Provide 'id' (a single transaction UUID) or 'ids' (several). " +
				"Use list_transactions to look them up."), nil, nil
		}

		byID := map[string]zen.Transaction{}
		for _, t := range st.Transactions() {
			if !t.Deleted {
				byID[t.ID] = t
			}
		}

		var targets []zen.Transaction
		var missing []string
		for _, id := range requested {
			if t, ok := byID[id]; ok {
				targets = append(targets, t)
			} else {
				missing = append(missing, id)
			}
		}

		if len(missing) > 0 {
			return errorResult("Transaction" + plural(len(missing), "", "s") + " not found: " +
				strings.Join(missing, ", ") + ".\nNothing was deleted. Use list_transactions to find " +
				"the right ids (each row ends with one), or sync_data if the transaction was added " +
				"from another client just now."), nil, nil
		}

		lines := make([]string, 0, len(targets))
		for _, t := range targets {
			lines = append(lines, "- "+formatTransactionLine(st, t))
		}
		preview := strings.Join(lines, "\n")
		count := strconv.Itoa(len(targets))
		s := plural(len(targets), "", "s")

		if args.Confirm == nil || !*args.Confirm {
			return textResult("About to delete " + count + " transaction" + s + ":\n\n" + preview +
				"\n\nNothing has been deleted yet. Call delete_transaction again with confirm=true " +
				"to delete permanently."), nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		now := nowUnix()
		deletions := make([]zen.Deletion, 0, len(targets))
		for _, t := range targets {
			deletions = append(deletions, zen.Deletion{
				ID: t.ID, Object: "transaction", Stamp: now, User: user.ID,
			})
		}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: now,
			ServerTimestamp:        st.ServerTimestamp(),
			Deletion:               deletions,
		})
		if err != nil {
			return errorResult("Failed to delete: " + err.Error()), nil, nil
		}
		st.ApplyLocalDeletions(deletions, resp)

		return textResult("Deleted " + count + " transaction" + s + ":\n\n" + preview), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "delete_object",
		Description: "Permanently delete an account, a category, or a merchant from ZenMoney. Deleting an " +
			"account (including a debt account) also deletes every transaction on it. The first call " +
			"previews the consequences and changes nothing; repeat it with confirm=true to actually " +
			"delete. Deletion cannot be undone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args deleteObjectArgs) (*mcp.CallToolResult, any, error) {
		object, ok := objectTypes[args.Type]
		if !ok {
			return errorResult(`Unknown type "` + args.Type +
				`". Use one of: account, category, merchant.`), nil, nil
		}
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		targetID, targetTitle, found := resolveObject(st, args.Type, args.NameOrID)
		if !found {
			return errorResult("No " + args.Type + ` matching "` + args.NameOrID +
				`" found. Nothing was deleted. Use ` + listToolFor(args.Type) +
				" to see what exists."), nil, nil
		}

		impact := describeImpact(st, args.Type, targetID)

		if args.Confirm == nil || !*args.Confirm {
			return textResult("About to delete " + args.Type + " **" + targetTitle + "** (id: `" +
				targetID + "`).\n" + impact + "\n\nNothing has been deleted yet. Call delete_object " +
				"again with confirm=true to delete permanently."), nil, nil
		}

		user := st.User()
		if user == nil {
			return errorResult("User not found. Try sync_data with force_full=true."), nil, nil
		}

		now := nowUnix()
		deletions := []zen.Deletion{{ID: targetID, Object: object, Stamp: now, User: user.ID}}

		resp, err := api.Diff(ctx, zen.DiffRequest{
			CurrentClientTimestamp: now,
			ServerTimestamp:        st.ServerTimestamp(),
			Deletion:               deletions,
		})
		if err != nil {
			return errorResult("Failed to delete: " + err.Error()), nil, nil
		}
		st.ApplyLocalDeletions(deletions, resp)

		return textResult("Deleted " + args.Type + " **" + targetTitle + "** (id: `" + targetID +
			"`).\n" + impact), nil, nil
	})
}

func resolveObject(st *zen.State, objectType, nameOrID string) (id, title string, found bool) {
	switch objectType {
	case "account":
		a := findAccount(st, nameOrID)
		if a == nil {
			a = findAccountByName(st, nameOrID)
		}
		if a != nil {
			return a.ID, a.Title, true
		}
	case "category":
		t := findTag(st, nameOrID)
		if t == nil {
			t = findTagByName(st, nameOrID)
		}
		if t != nil {
			return t.ID, t.Title, true
		}
	case "merchant":
		m := findMerchant(st, nameOrID)
		if m == nil {
			m = findMerchantByName(st, nameOrID)
		}
		if m != nil {
			return m.ID, m.Title, true
		}
	}
	return "", "", false
}

func listToolFor(objectType string) string {
	switch objectType {
	case "account":
		return "list_accounts"
	case "category":
		return "list_categories"
	default:
		return "list_merchants"
	}
}

// describeImpact spells out what else disappears, so the confirmation is an
// informed one.
func describeImpact(st *zen.State, objectType, id string) string {
	switch objectType {
	case "account":
		affected := 0
		for _, t := range st.Transactions() {
			if t.IncomeAccount == id || t.OutcomeAccount == id {
				affected++
			}
		}
		if affected == 0 {
			return "It has no transactions."
		}
		return "Its " + strconv.Itoa(affected) + " transaction" + plural(affected, "", "s") +
			" will be deleted with it."

	case "category":
		children, tagged := 0, 0
		for _, t := range st.Tags() {
			if t.Parent != nil && *t.Parent == id {
				children++
			}
		}
		for _, t := range st.Transactions() {
			if contains(t.Tag, id) {
				tagged++
			}
		}
		var parts []string
		if children > 0 {
			parts = append(parts, strconv.Itoa(children)+" subcategor"+
				plural(children, "y", "ies")+" will be deleted with it")
		}
		if tagged > 0 {
			parts = append(parts, strconv.Itoa(tagged)+" transaction"+plural(tagged, "", "s")+
				" will become uncategorized (the transactions themselves stay)")
		} else {
			parts = append(parts, "no transactions use it")
		}
		return strings.Join(parts, "; ") + "."

	case "merchant":
		used := 0
		for _, t := range st.Transactions() {
			if t.Merchant != nil && *t.Merchant == id {
				used++
			}
		}
		if used == 0 {
			return "No transactions use it."
		}
		return strconv.Itoa(used) + " transaction" + plural(used, "", "s") +
			" will lose this merchant (the transactions themselves stay)."
	}
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
