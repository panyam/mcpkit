// Example: SEP-2549 TTL for List Results.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # MCP server on :8080 with WithListTTL(60)
//	Terminal 2:  make demo          # demokit walkthrough (or `make demo --tui`)
//
// The server is a real MCP server — any host can connect to it (Claude
// Desktop, MCPJam, VS Code) and observe the SEP-2549 `ttl` field on every
// list response. The walkthrough acts as a scripted MCP host that calls
// each list endpoint via the new client helpers and prints the TTL.
//
// The same binary doubles as the conformance fixture: pass `--serve`
// to run the server. The conformance/list-ttl/ suite spawns three
// processes (positive / zero / unset) via the `--ttl` flag.
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
	// Negative default = unset → no ttl emitted on list responses.
	ttl := flag.Int("ttl", -1, "list TTL in seconds (negative = unset, 0 = do not cache, positive = N seconds)")
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	logger := common.NewMCPLogger("[mcp] ")
	opts := []server.Option{server.WithListen(*addr)}
	opts = append(opts, common.WithMCPLogging(logger)...)
	if *ttl >= 0 {
		opts = append(opts, server.WithListTTL(*ttl))
	}

	srv := server.NewServer(core.ServerInfo{Name: "list-ttl-demo", Version: "0.1.0"}, opts...)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "ping",
			Description: "Returns 'pong'",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
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
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{}, nil
		},
	)

	mode := "unset"
	if *ttl == 0 {
		mode = "0 (do not cache)"
	} else if *ttl > 0 {
		mode = fmt.Sprintf("%d seconds", *ttl)
	}
	log.Printf("[list-ttl-demo] listening on %s — list TTL: %s", *addr, mode)
	if err := srv.ListenAndServe(server.WithStreamableHTTP(true)); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
