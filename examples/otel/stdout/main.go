// Example: SEP-414 — OpenTelemetry trace context propagation via the
// ext/otel adapter. Two exporter modes:
//
//   - --exporter=stdout (default) — spans pretty-print as JSON on the
//     respective process stdout. CI-friendly; no external stack
//     required. Match by TraceID across two terminals to see SEP-414
//     stitching.
//
//   - --exporter=otlp — spans ship via OTLP gRPC (default endpoint
//     localhost:4317) to the docker/observability/ stack. Open
//     http://localhost:3000 and search by service name otel-stdout-demo
//     (server) / otel-stdout-host (client walkthrough) to see the
//     stitched trace in Grafana.
//
// Two-process architecture:
//
//	Terminal 1:  make serve                       # stdouttrace, CI mode
//	Terminal 2:  make demo                        # stdouttrace, CI mode
//
//	Terminal 1:  make serve EXPORTER=otlp         # OTLP → stack
//	Terminal 2:  make demo EXPORTER=otlp          # OTLP → stack
//
// `make -C ../../../docker up` brings the stack up before either
// EXPORTER=otlp invocation.
//
// The server is a real MCP server: any host (VS Code, Claude Desktop,
// MCPJam) can connect. The walkthrough scripts the host side — it
// drives a tools/call so a reader can see both sides emit matching
// TraceIDs.
package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"github.com/panyam/mcpkit/server"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Service names baked into the OTel Resource on each process's
// pipeline so Grafana can index server-emitted vs walkthrough-emitted
// spans separately. See examples/common/otel/pipeline.go for the
// generic boilerplate these names plug into.
const (
	serverServiceName = "otel-stdout-demo"
	hostServiceName   = "otel-stdout-host"
	defaultExporter   = commonotel.ExporterStdout
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

// serve boots the OpenTelemetry pipeline (per --exporter mode),
// wraps it with the mcpkit ext/otel adapter, and starts the MCP
// server. Each dispatched JSON-RPC request emits a span — to stdout
// in --exporter=stdout mode, to the OTLP collector in --exporter=otlp
// mode.
func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	exporter := flag.String("exporter", defaultExporter, "trace exporter: stdout | otlp")
	otlpEndpoint := flag.String("otlp-endpoint", commonotel.DefaultOTLPEndpoint, "OTLP gRPC endpoint when --exporter=otlp")
	// --exporter / --otlp-endpoint are stdlib flag.String — they
	// MUST NOT be registered with FilterArgs, which would strip
	// them from os.Args before flag.Parse sees them (the file-inputs
	// pattern handles example-specific flags via a custom os.Args
	// scan; we go the simpler stdlib route).
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	otelTP, shutdown, err := commonotel.BuildPipeline(*exporter, *otlpEndpoint, serverServiceName, os.Stdout)
	if err != nil {
		log.Fatalf("otel pipeline: %v", err)
	}
	defer shutdown()
	log.Printf("[otel-stdout-demo] exporter=%s service.name=%s", *exporter, serverServiceName)
	if *exporter == commonotel.ExporterOTLP {
		log.Printf("[otel-stdout-demo] OTLP gRPC endpoint: %s", *otlpEndpoint)
		log.Printf("[otel-stdout-demo] view in Grafana: http://localhost:3000  (search service.name=%s)", serverServiceName)
	}

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

// newOTelPipeline preserves the original stdout-pipeline entrypoint
// the e2e test calls. Equivalent to commonotel.NewStdoutPipeline with
// the server-side service name — kept as a thin wrapper so the
// existing TestNewOTelPipeline_HappyPath signature didn't have to
// change just because OTLP-mode landed via the shared helpers.
func newOTelPipeline(w *os.File) (*sdktrace.TracerProvider, func(), error) {
	return commonotel.NewStdoutPipeline(w, serverServiceName)
}
