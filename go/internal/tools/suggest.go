package tools

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

type suggestArgs struct {
	Payee string `json:"payee" jsonschema:"The payee/merchant name from the receipt"`
}

func registerSuggest(server *mcp.Server, api *zen.API, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "suggest_category",
		Description: "Get ZenMoney's auto-suggestion for category and merchant based on a payee name. " +
			"Useful for categorizing receipts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args suggestArgs) (*mcp.CallToolResult, any, error) {
		// Best effort: state is only used to turn ids into names, so a failed
		// sync should not block the suggestion itself.
		_ = st.EnsureSynced(ctx)

		results, err := api.Suggest(ctx, []zen.SuggestRequest{{Payee: args.Payee}})
		if err != nil {
			return errorResult("Suggest failed: " + err.Error()), nil, nil
		}
		if len(results) == 0 {
			return textResult(`No suggestions found for "` + args.Payee + `".`), nil, nil
		}

		s := results[0]
		parts := []string{`Suggestions for "` + args.Payee + `":`}

		if len(s.Tag) > 0 {
			names := make([]string, 0, len(s.Tag))
			for _, id := range s.Tag {
				if tag := findTag(st, id); tag != nil {
					names = append(names, tag.Title+" (`"+tag.ID+"`)")
				} else {
					names = append(names, "`"+id+"`")
				}
			}
			parts = append(parts, "- Category: "+strings.Join(names, ", "))
		}

		if s.Merchant != nil {
			name := *s.Merchant
			if m := findMerchant(st, *s.Merchant); m != nil {
				name = m.Title
			}
			parts = append(parts, "- Merchant: "+name)
		}

		if s.Payee != nil {
			parts = append(parts, "- Normalized payee: "+*s.Payee)
		}

		return textResult(strings.Join(parts, "\n")), nil, nil
	})
}
