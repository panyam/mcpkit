// Example: SEP-2322 MRTR (Multi Round-Trip Requests) — ephemeral
// InputRequiredResult flow.
//
// Two-process architecture:
//
//	Terminal 1:  just serve         # MCP server on :8080
//	Terminal 2:  just demo          # demokit walkthrough (or `just demo --tui`)
//
// The server is a real MCP server — any host can connect to it. The
// walkthrough acts as a scripted MCP host that drives `tools/call`
// through one full InputRequiredResult round-trip and prints the wire
// fields at each step.
//
// The same binary doubles as the conformance fixture: pass `--serve`
// to run the server. The conformance/mrtr/ suite spawns one process and
// drives the seven scenarios against it.
//
// Tools (named to match the upstream conformance contract —
// modelcontextprotocol/conformance PR 188):
//
//   - test_tool_with_elicitation             (A1) basic elicitation round-trip
//   - test_incomplete_result_sampling        (A2) basic sampling round-trip
//   - test_incomplete_result_list_roots      (A3) basic roots/list round-trip
//   - test_incomplete_result_request_state   (A4) requestState echo
//   - test_incomplete_result_multiple_inputs (A5) multiple inputRequests in one round
//   - test_incomplete_result_multi_round     (A6) two rounds of elicitation
//   - test_incomplete_result_elicitation     (A7) graceful handling when client
//     sends a wrong inputResponses key
//
// The MRTR loop is server-driven and stateless: the server returns
// InputRequiredResult{inputRequests, requestState}; the client retries the
// SAME tools/call with inputResponses + the echoed requestState. Dispatch
// transparently merges accumulated answers across rounds via requestState
// (see core.MRTRRoundState), so handlers stay stateless.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/examples/mrtr/mcp"
	tasks "github.com/panyam/mcpkit/ext/tasks"
	"github.com/panyam/mcpkit/server"
)

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	signingKey := flag.String("signing-key", mcp.DefaultSigningKey, "HMAC key for requestState signing (empty = plaintext mode)")
	tel := common.RegisterTelemetryFlags(flag.CommandLine)
	wire := common.RegisterWireFlags(flag.CommandLine)
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*tel.Exporter),
		commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
		commonotel.WithServiceName("mrtr-demo"),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	extraOpts := mcp.Options(*signingKey)

	if err := common.RunServer(common.ServerConfig{
		Name:           "mrtr-demo",
		Addr:           *addr,
		TracerProvider: tp,
		Wire:           wire,
		Options:        extraOpts,
		Register: func(srv *server.Server) {
			mcp.Register(srv)
			// SEP-2322 + SEP-2663 composition: the registry holds at least one
			// task-eligible tool (test_tool_with_task) that gathers input via
			// MRTR rounds and then escalates to async via the GoAsync sentinel.
			// The taskV2Middleware is what observes the GoAsync return and spawns
			// the continuation goroutine.
			tasks.Register(tasks.Config{Server: srv})
		},
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// --- Tool registrations ---
