// Command zenmoney-mcp serves the ZenMoney MCP tools over stdio or streamable
// HTTP.
//
// Stdio is the drop-in replacement for the TypeScript server: one process per
// client session. The -http flag serves the same tools as a long-lived service
// instead, so every session shares one process, one synced snapshot and one warm
// cache — which is the point of running this on a Raspberry Pi.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/tools"
	"github.com/david-dvinskykh/zenmoney-mcp-ddvin/go/internal/zen"
)

// version tracks the TypeScript server this port mirrors.
const version = "0.7.0"

func main() {
	addr := flag.String("http", "", "serve streamable HTTP on this address (e.g. :8081) instead of stdio")
	flag.Parse()

	// Logs go to stderr: on stdio, stdout carries the JSON-RPC stream and
	// anything else printed there corrupts it.
	log.SetFlags(0)
	log.SetOutput(os.Stderr)

	token := os.Getenv("ZENMONEY_TOKEN")
	if token == "" {
		log.Println("ZENMONEY_TOKEN environment variable is required.")
		log.Println("Get your token from https://zerro.app/token and set it in .env")
		os.Exit(1)
	}

	api := zen.NewAPI(token)

	// Snapshots are stored per token hash so data survives between processes
	// (and separate accounts never share a file).
	var cache *zen.Cache
	if os.Getenv("ZENMONEY_NO_CACHE") != "1" {
		cache = zen.NewCache(token)
	}
	state := zen.NewState(api, cache)

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "zenmoney-mcp-ddvin",
		Version: version,
	}, nil)
	tools.Register(server, api, state)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Warm up in the background so the first tool call doesn't pay for the sync.
	// Tool calls await this same attempt via EnsureSynced().
	go warmUp(ctx, state)

	if *addr != "" {
		if err := serveHTTP(ctx, server, *addr); err != nil {
			log.Fatalf("Fatal error: %v", err)
		}
		return
	}

	log.Println("ZenMoney MCP server running on stdio")
	logCache(state)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("Fatal error: %v", err)
	}
}

func warmUp(ctx context.Context, state *zen.State) {
	if err := state.EnsureSynced(ctx); err != nil {
		log.Printf("Initial sync failed, will retry on first tool call: %v", err)
		return
	}
	restored := ""
	if state.IsFromCache() {
		restored = ", restored from cache"
	}
	log.Printf("Initial sync done (%d transactions%s)", len(state.Transactions()), restored)
	if reason := state.StaleReason(); reason != "" {
		log.Printf("Serving a cached snapshot — the live sync failed: %s", reason)
	}
}

func logCache(state *zen.State) {
	if path := state.CachePath(); path != "" {
		log.Printf("Cache file: %s", path)
	}
}

// serveHTTP runs the same server for every session, so the synced snapshot is
// built once and shared instead of re-downloaded per connection.
func serveHTTP(ctx context.Context, server *mcp.Server, addr string) error {
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		nil,
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("ZenMoney MCP server listening on %s (POST /mcp)", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
