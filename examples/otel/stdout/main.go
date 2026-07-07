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
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	"github.com/panyam/mcpkit/server"
)

// Service names baked into the OTel Resource on each process's
// pipeline so Grafana can index server-emitted vs walkthrough-emitted
// spans separately. See examples/common/otel/setup.go for the helper
// these names plug into.
const (
	serverServiceName = "otel-stdout-demo"
	hostServiceName   = "otel-stdout-host"

	// defaultExporter is "stdout" here as a deliberate carve-out:
	// the whole point of THIS example is showing traces, so a bare
	// `go run .` with no flag should still produce visible output.
	// Every other example defaults to "" (no telemetry) per the
	// project-wide rule — see examples/CONVENTIONS.md §Telemetry.
	defaultExporter = commonotel.ExporterStdout
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
	exporter := flag.String("exporter", defaultExporter, "trace exporter: \"\" (off) | stdout (default here) | otlp")
	otlpEndpoint := flag.String("otlp-endpoint", commonotel.DefaultOTLPEndpoint, "OTLP gRPC endpoint when --exporter=otlp")
	// --exporter / --otlp-endpoint are stdlib flag.String — they
	// MUST NOT be registered with FilterArgs, which would strip
	// them from os.Args before flag.Parse sees them.
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
	))

	tp, shutdown, err := commonotel.SetupTelemetry(context.Background(),
		commonotel.WithExporter(*exporter),
		commonotel.WithOTLPEndpoint(*otlpEndpoint),
		commonotel.WithServiceName(serverServiceName),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupTelemetry: %v", err)
	}
	defer shutdown(context.Background())

	// Issue 668 (logs half): wire the otelslog bridge so handler-emitted
	// slog records ship to OTLP alongside the trace pipeline. The
	// bridge stamps trace_id + span_id on every record automatically
	// when the handler logs via slog.*Context — Grafana's Loki
	// datasource (docker/observability/grafana/) renders those as
	// clickable pivots back to Tempo.
	logsLogger, logsShutdown, err := commonotel.SetupLogs(context.Background(),
		commonotel.WithExporter(*exporter),
		commonotel.WithOTLPEndpoint(*otlpEndpoint),
		commonotel.WithServiceName(serverServiceName),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupLogs: %v", err)
	}
	defer logsShutdown(context.Background())
	slog.SetDefault(logsLogger)

	// Issue 668 (metrics half): wire the MeterProvider so the dispatch
	// path emits mcp.tool.calls / mcp.tool.duration / mcp.jsonrpc.errors
	// / mcp.sessions.active. The ext/otel adapter forwards ctx so every
	// measurement carries an exemplar pointing at the active span —
	// Grafana renders metric panels with clickable dots that jump to
	// the matching trace in Tempo.
	meterProvider, metricsShutdown, err := commonotel.SetupMetrics(context.Background(),
		commonotel.WithExporter(*exporter),
		commonotel.WithOTLPEndpoint(*otlpEndpoint),
		commonotel.WithServiceName(serverServiceName),
	)
	if err != nil {
		log.Fatalf("commonotel.SetupMetrics: %v", err)
	}
	defer metricsShutdown(context.Background())

	log.Printf("[otel-stdout-demo] exporter=%s service.name=%s", *exporter, serverServiceName)
	if *exporter == commonotel.ExporterOTLP {
		log.Printf("[otel-stdout-demo] OTLP gRPC endpoint: %s", *otlpEndpoint)
		log.Printf("[otel-stdout-demo] view in Grafana: http://localhost:3000  (search service.name=%s)", serverServiceName)
	}

	log.Printf("[otel-stdout-demo] POST /mcp")
	log.Printf("[otel-stdout-demo] tools: echo")
	if *exporter == commonotel.ExporterStdout {
		log.Printf("[otel-stdout-demo] every dispatched request will print one span on this stdout")
	}

	if err := common.RunServer(common.ServerConfig{
		Name:           "otel-stdout-demo",
		Addr:           *addr,
		TracerProvider: tp,
		MeterProvider:  meterProvider,
		Register:       registerDemoTools,
	}); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// registerDemoTools installs four demo tools, each chosen to produce
// a distinct trace shape an operator can compare in Grafana / Tempo:
//
//   - echo          — baseline single-RPC trace; tells you what a
//     "normal" span looks like.
//   - slow_echo     — sleeps 750ms in the handler; the trace's span
//     duration shows how Tempo renders latency.
//   - failing_tool  — returns a ToolResult with IsError=true; the
//     span carries `mcp.tool.is_error="true"` and
//     Grafana renders it red.
//   - count_tool    — emits 3 `notifications/progress` via
//     ctx.Progress(); the parent span's children
//     include 3 outbound notification spans with
//     matching `_meta.traceparent` (SEP-414 P2's
//     outbound _meta injection in action).
//
// All four are intentionally trivial so the demo's value lives in
// the span shape, not the tool surface.
func registerDemoTools(srv *server.Server) {
	type echoInput struct {
		Message string `json:"message,omitempty" jsonschema:"description=Text to echo back"`
	}
	srv.Register(core.TextTool[echoInput]("echo", "Returns the message argument unchanged. Baseline span shape — single-RPC trace, no surprises.",
		func(ctx core.ToolContext, input echoInput) (string, error) {
			msg := input.Message
			if msg == "" {
				msg = "ok"
			}
			// Demonstration of issue 668's log↔trace pivot: the
			// otelslog bridge reads the active span via ctx and
			// stamps trace_id + span_id onto this record. Open the
			// Loki panel in Grafana, click the resulting log line's
			// `traceID` field — Grafana jumps to the matching Tempo
			// trace and renders this dispatch span as the root.
			slog.InfoContext(ctx, "echo tool invoked", "message", msg)
			return msg, nil
		},
	))

	srv.Register(core.TextTool[struct{}]("slow_echo", "Sleeps 750ms then echoes. Span duration is visible in Tempo's trace view.",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			time.Sleep(750 * time.Millisecond)
			return "slow_echo: slept 750ms", nil
		},
	))

	srv.Register(core.TypedTool[struct{}, core.ToolResult]("failing_tool", "Returns ToolResult{IsError: true}. Span carries mcp.tool.is_error=\"true\" — renders red in Grafana.",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResult, error) {
			return core.ToolResult{
				IsError: true,
				Content: []core.Content{{Type: "text", Text: "simulated tool failure"}},
			}, nil
		},
	))

	srv.Register(core.TextTool[struct{}]("count_tool", "Calls ctx.Progress 3 times. Parent span's notifications fan out as outbound spans with _meta.traceparent stamped.",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			for i := 1; i <= 3; i++ {
				ctx.Progress(float64(i), 3, fmt.Sprintf("step %d/3", i))
			}
			return "count_tool: emitted 3 progress notifications", nil
		},
	))
}

// tempoExploreURL builds a Grafana Explore deep link pre-filtered to
// traces from the given service. The URL pre-loads Grafana's Explore
// view with Tempo as the data source and a TraceQL search filtering
// by resource.service.name, plus a "Last 15 minutes" time range so
// the operator's just-emitted spans are in scope without manual
// adjustment.
//
// Encoding follows Grafana's documented Explore URL shape: the
// `left` query parameter is a URL-encoded JSON object specifying the
// data source + queries + range. Grafana parses this on page load
// and populates the UI as if the operator had constructed the query
// by hand. Tested against Grafana 11.x; the JSON shape may shift in
// future versions — re-validate after a major Grafana bump.
func tempoExploreURL(serviceName string) string {
	const left = `{"datasource":"tempo","queries":[{"refId":"A","queryType":"traceqlSearch","filters":[{"id":"service-name","tag":"service.name","operator":"=","value":"%s","scope":"resource"}]}],"range":{"from":"now-15m","to":"now"}}`
	encoded := url.QueryEscape(fmt.Sprintf(left, serviceName))
	return "http://localhost:3000/explore?orgId=1&left=" + encoded
}
