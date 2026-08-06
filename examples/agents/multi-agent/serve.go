package main

import (
	"log"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// searchInput / codeInput are the demo server's tool inputs.
type searchInput struct {
	Query string `json:"query"`
}

type codeInput struct {
	Code string `json:"code"`
}

// buildServer is the demo MCP server the config.json personas delegate over:
// web_search (the researcher's `allow`) and run_code (the coder's). It backs
// the live chat/web surfaces (just chat / just web against config.json). The
// deterministic golden scenario does NOT use this server — it wires each
// sub-agent's own FuncSource tools in code (see scenario.go); config.json is
// the declarative host counterpart to that hand-wired composition.
func buildServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "multi-agent-demo", Version: "0.1.0"})
	srv.Register(core.TextTool[searchInput]("web_search", "search the web for a query",
		func(ctx core.ToolContext, in searchInput) (string, error) {
			return "Go generics (added in 1.18) let functions and types take type parameters.", nil
		}))
	srv.Register(core.TextTool[codeInput]("run_code", "compile and run a Go snippet",
		func(ctx core.ToolContext, in codeInput) (string, error) {
			return "compiled and ran successfully", nil
		}))
	return srv
}

// serve runs the demo server as a standalone streamable-HTTP MCP server.
func serve(addr string) error {
	log.Printf("multi-agent demo server on http://localhost%s/mcp", addr)
	return buildServer().Run(addr)
}
