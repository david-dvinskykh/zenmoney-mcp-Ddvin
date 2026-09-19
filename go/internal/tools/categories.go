package tools

import (
	"context"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

type noArgs struct{}

func registerCategories(server *mcp.Server, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_categories",
		Description: "List all expense/income categories (tags) with their hierarchy. " +
			"Syncs automatically if needed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		var lines []string
		for _, group := range st.TagHierarchy() {
			var flags []string
			if group.Parent.ShowOutcome {
				flags = append(flags, "expense")
			}
			if group.Parent.ShowIncome {
				flags = append(flags, "income")
			}
			lines = append(lines, "- **"+group.Parent.Title+"** ("+strings.Join(flags, ", ")+
				") — id: `"+group.Parent.ID+"`")
			for _, child := range group.Children {
				lines = append(lines, "  - "+child.Title+" — id: `"+child.ID+"`")
			}
		}

		if len(lines) == 0 {
			return textResult("No categories found."), nil, nil
		}
		return textResult("Categories (" + strconv.Itoa(len(st.Tags())) + "):\n\n" +
			strings.Join(lines, "\n")), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_merchants",
		Description: "List known merchants/payees. Syncs automatically if needed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		if fail := ensureSynced(ctx, st); fail != nil {
			return fail, nil, nil
		}

		merchants := st.Merchants()
		lines := make([]string, 0, len(merchants))
		for _, m := range merchants {
			lines = append(lines, "- **"+m.Title+"** — id: `"+m.ID+"`")
		}

		if len(lines) == 0 {
			return textResult("No merchants found."), nil, nil
		}
		return textResult("Merchants (" + strconv.Itoa(len(lines)) + "):\n\n" +
			strings.Join(lines, "\n")), nil, nil
	})
}
