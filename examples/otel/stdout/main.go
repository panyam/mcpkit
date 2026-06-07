// Example: SEP-414 — OpenTelemetry trace context propagation via the
// ext/otel adapter with the stdouttrace exporter.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # MCP server on :8080 — spans dump to its stdout
//	Terminal 2:  make demo          # demokit walkthrough (--tui for the TUI)
//
// The server is a real MCP server: any host (VS Code, Claude Desktop,
// MCPJam) can connect and watch the OpenTelemetry pipeline export spans
// as JSON on its stdout. The walkthrough scripts the host side — it
// drives a tools/call carrying an explicit W3C `_meta.traceparent` so a
// reader can see the inbound trace ID land on the server-emitted span.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"github.com/panyam/mcpkit/server"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
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

// serve boots the OpenTelemetry pipeline (stdouttrace exporter → SDK
// TracerProvider), wraps it with the mcpkit ext/otel adapter, and starts
// the MCP server. Every dispatched JSON-RPC request prints a span as
// pretty-printed JSON on stdout — the demo's whole point.
func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	otelTP, shutdown, err := newOTelPipeline(os.Stdout)
	if err != nil {
		log.Fatalf("otel pipeline: %v", err)
	}
	defer shutdown()

	log.Printf("[otel-stdout-demo] POST /mcp")
	log.Printf("[otel-stdout-demo] tools: echo")
	log.Printf("[otel-stdout-demo] every dispatched request will print one span on this stdout")

	if err := common.RunServer(common.ServerConfig{
		Name: "otel-stdout-demo",
		Addr: *addr,
		Options: []server.Option{
			// SEP-414 P2 wires this option into an outermost trace
			// middleware that emits a span on every JSON-RPC dispatch and
			// injects _meta.traceparent on every outbound notification +
			// server-to-client request. The adapter (P4) is the OTel SDK
			// binding that makes spans actually flow to the exporter.
			server.WithTracerProvider(mcpotel.NewProvider(otelTP)),
		},
		Register: registerEcho,
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// registerEcho installs the single demo tool. Trivially synchronous so
// the demo focuses on the trace mechanics rather than the tool surface.
func registerEcho(srv *server.Server) {
	srv.RegisterTool(
		core.ToolDef{
			Name:        "echo",
			Description: "Returns the message argument unchanged. Trivial body — the demo is about the span the dispatch emits, not the result.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "Text to echo back.",
					},
				},
			},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			var args struct {
				Message string `json:"message"`
			}
			_ = req.Bind(&args)
			if args.Message == "" {
				args.Message = "ok"
			}
			return core.TextResult(args.Message), nil
		},
	)
}

// newOTelPipeline constructs an SDK TracerProvider that writes spans to
// the supplied writer via the stdouttrace exporter. The returned
// shutdown closure flushes the exporter; defer it so the buffered span
// lands on stdout before the process exits. Errors propagate so the
// caller decides whether to fatal — serve() does; the e2e test uses an
// in-memory exporter directly and skips this helper.
func newOTelPipeline(w *os.File) (*sdktrace.TracerProvider, func(), error) {
	exp, err := stdouttrace.New(
		stdouttrace.WithWriter(w),
		stdouttrace.WithPrettyPrint(),
	)
	if err != nil {
		return nil, nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	shutdown := func() { _ = tp.Shutdown(context.Background()) }
	return tp, shutdown, nil
}
