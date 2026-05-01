// Example: SEP-2549 TTL for List Results.
//
// Server fixture for the conformance/list-ttl/ suite. Registers one tool,
// one resource, one resource template, and one prompt — just enough surface
// for all four list endpoints to return a non-empty page — and configures
// WithListTTL(60) so every list response carries `"ttl": 60`.
//
// Usage:
//
//	go run . --serve [--addr :8080] [--ttl 60]
//
// Pass `--ttl 0` to test the explicit "do not cache" wire shape, or omit
// `--ttl` (or pass a negative value) to test the "absent" shape. The
// conformance suite drives all three modes via separate processes.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	fmt.Println("Run with --serve to start the SEP-2549 list-ttl demo server.")
	fmt.Println("  go run . --serve --addr :8080 --ttl 60")
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	// Negative default = unset → no ttl emitted on list responses.
	ttl := flag.Int("ttl", -1, "list TTL in seconds (negative = unset, 0 = do not cache, positive = N seconds)")
	flag.CommandLine.Parse(filterFlags(os.Args[1:]))

	opts := []server.Option{
		server.WithListen(*addr),
	}
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

func filterFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--serve" {
			continue
		}
		out = append(out, a)
	}
	return out
}
