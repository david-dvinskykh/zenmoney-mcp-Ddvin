package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

type syncArgs struct {
	ForceFull *bool `json:"force_full,omitempty" jsonschema:"Force a full sync instead of incremental"`
}

func registerSync(server *mcp.Server, st *zen.State) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "sync_data",
		Description: "Sync data with ZenMoney. Other tools sync automatically, so this is only needed " +
			"to refresh on demand. Use force_full=true to discard the cache and re-download everything.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args syncArgs) (*mcp.CallToolResult, any, error) {
		forceFull := args.ForceFull != nil && *args.ForceFull

		if forceFull {
			st.ClearCache()
		}
		if _, err := st.Sync(ctx, forceFull); err != nil {
			return errorResult("Sync failed: " + err.Error()), nil, nil
		}

		return textResult("Sync complete. Summary:\n" + prettyJSON(st.Summary())), nil, nil
	})
}
