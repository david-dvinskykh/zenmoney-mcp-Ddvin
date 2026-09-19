package tools

import (
	"context"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

type listAccountsArgs struct {
	IncludeArchived *bool `json:"include_archived,omitempty" jsonschema:"Include archived accounts"`
}

func registerAccounts(server *mcp.Server, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_accounts",
		Description: "List all user accounts (wallets, cards, cash). Syncs automatically if needed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listAccountsArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		accounts := st.ActiveAccounts()
		if args.IncludeArchived != nil && *args.IncludeArchived {
			accounts = st.Accounts()
		}

		lines := make([]string, 0, len(accounts))
		for _, a := range accounts {
			currency := "???"
			if a.Instrument != nil {
				if instr := st.Instrument(*a.Instrument); instr != nil {
					currency = instr.ShortTitle + " (" + instr.Title + ", " + instr.Symbol + ")"
				}
			}

			balance := "0"
			if a.Balance != nil {
				balance = num(*a.Balance)
			}

			bank := ""
			if a.Company != nil {
				if company := st.Company(*a.Company); company != nil {
					bank = " | Bank: " + company.Title
				}
			}

			archived := ""
			if a.Archive {
				archived = " (archived)"
			}

			lines = append(lines, "- **"+a.Title+"** ["+a.Type+"] — "+balance+" "+currency+
				bank+archived+"  \n  id: `"+a.ID+"`")
		}

		if len(lines) == 0 {
			return textResult("No accounts found."), nil, nil
		}
		return textResult("Accounts (" + strconv.Itoa(len(lines)) + "):\n\n" + strings.Join(lines, "\n")), nil, nil
	})
}
