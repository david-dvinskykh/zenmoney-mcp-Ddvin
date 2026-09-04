package tools

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

// Register adds every ZenMoney tool to the server. The registration order
// follows the TypeScript entry point; the SDK sorts tools/list by name, which
// clients key on anyway.
func Register(server *mcp.Server, api *zen.API, st *zen.State) {
	registerSync(server, st)
	registerAccounts(server, st)
	registerCategories(server, st)
	registerTransactions(server, api, st)
	registerDebts(server, api, st)
	registerUpdate(server, api, st)
	registerDelete(server, api, st)
	registerReminders(server, api, st)
	registerSuggest(server, api, st)
}

// newUUID returns a random RFC 4122 version 4 UUID, the id format ZenMoney uses
// for every entity a client creates.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// today is the current date in YYYY-MM-DD, in UTC like the original's
// toISOString().slice(0, 10).
func today() string {
	return time.Now().UTC().Format("2006-01-02")
}

func nowUnix() int64 { return time.Now().Unix() }

func prettyJSON(v any) string {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(out)
}

// dateRE is the YYYY-MM-DD guard the TypeScript tools express as a zod regex.
// The Go SDK validates types, not string formats, so the tools check it.
var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
