// Example: SEP-2549 TTL for List Results.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # MCP server on :8080 with WithListTTLMs(60000)
//	Terminal 2:  make demo          # demokit walkthrough (or `make demo --tui`)
//
// The server is a real MCP server — any host can connect to it (Claude
// Desktop, MCPJam, VS Code) and observe the SEP-2549 `ttlMs` and
// `cacheScope` fields on every list response and on resources/read. The
// walkthrough acts as a scripted MCP host that calls each endpoint via the
// client helpers and prints the cache hints.
//
// The same binary doubles as the conformance fixture: pass `--serve`
// to run the server. The SEP-2549 conformance suite (panyam/mcpconformance
// `pending` branch, `src/scenarios/server/list-ttl/`) spawns three
// processes (positive / zero / unset) via the `--ttl-ms` flag.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		switch strings.TrimSpace(arg) {
		case "--serve":
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	// Negative default = unset → no ttlMs emitted on responses.
	ttlMs := flag.Int("ttl-ms", -1, "cache TTL in milliseconds (negative = unset, 0 = immediately stale, positive = fresh for N ms)")
	scope := flag.String("cache-scope", "", `SEP-2549 cacheScope: "public", "private", or "" to omit`)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	opts := common.MCPServerOptions(*addr, "[mcp] ")
	// The same ttlMs / cacheScope applies to the four list endpoints and to
	// resources/read — SEP-2549 added resources/read to the coverage list.
	if *ttlMs >= 0 || *scope != "" {
		opts = append(opts,
			server.WithListCacheControl(*ttlMs, *scope),
			server.WithReadResourceCacheControl(*ttlMs, *scope),
		)
	}

	srv := server.NewServer(core.ServerInfo{Name: "list-ttl-demo", Version: "0.1.0"}, opts...)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "ping",
			Description: "Returns 'pong'",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("pong"), nil
		},
	)

	srv.RegisterResource(
		core.ResourceDef{
			URI:      "file:///fixture",
			Name:     "fixture",
			MimeType: "text/plain",
		},
		func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      req.URI,
					MimeType: "text/plain",
					Text:     "fixture",
				}},
			}, nil
		},
	)

	srv.RegisterResourceTemplate(
		core.ResourceTemplate{URITemplate: "file:///t/{name}", Name: "tmpl"},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      uri,
					MimeType: "text/plain",
					Text:     "templated:" + params["name"],
				}},
			}, nil
		},
	)

	srv.RegisterPrompt(
		core.PromptDef{Name: "hello", Description: "Sample prompt"},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
			return core.PromptResult{}, nil
		},
	)

	mode := "unset"
	if *ttlMs == 0 {
		mode = "0 (immediately stale)"
	} else if *ttlMs > 0 {
		mode = fmt.Sprintf("%d ms", *ttlMs)
	}
	scopeMode := *scope
	if scopeMode == "" {
		scopeMode = "unset"
	}
	log.Printf("[list-ttl-demo] listening on %s — ttlMs: %s, cacheScope: %s", *addr, mode, scopeMode)
	if err := srv.ListenAndServe(server.WithStreamableHTTP(true)); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
