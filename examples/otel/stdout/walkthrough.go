package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	commonotel "github.com/panyam/mcpkit/examples/common/otel"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
)

// toolMenu is the ordered list of tools the looping "Explore trace
// shapes" step cycles through. Each is registered on the server side
// in registerDemoTools (see main.go) and produces a distinct trace
// shape an operator can compare in Grafana / Tempo. The synthetic
// "quit" entry is the exit option in interactive mode; non-
// interactive mode drives through `toolMenu` once and exits.
var toolMenu = []string{"echo", "slow_echo", "failing_tool", "count_tool"}

func runDemo() {
	serverURL := common.ServerURL()

	// Walkthrough-side flags. Mirror serve()'s flag set so an operator
	// can pass --exporter=otlp to both processes and watch matching
	// TraceIDs land in Grafana.
	exporter := flag.String("exporter", defaultExporter, "trace exporter: stdout | otlp")
	otlpEndpoint := flag.String("otlp-endpoint", commonotel.DefaultOTLPEndpoint, "OTLP gRPC endpoint when --exporter=otlp")
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.ValueFlag("--url"),
	))

	// Stand up the walkthrough's OWN OTel pipeline. Each process gets
	// its own TracerProvider (the two are separate OS processes;
	// in-memory sharing isn't possible), but the SEP-414 wire
	// propagates trace context across them so the recorded spans share
	// a TraceID. The instrumentation name carries `mcpkit/client` so
	// observability backends group client-emitted spans separately;
	// the service.name attribute distinguishes them at the trace level
	// in Grafana / Tempo searches.
	clientOTelTP, shutdownClientOTel, err := commonotel.BuildPipeline(*exporter, *otlpEndpoint, hostServiceName, os.Stdout)
	if err != nil {
		log.Fatalf("failed to build client-side OTel pipeline: %v", err)
	}
	defer shutdownClientOTel()
	otlpMode := *exporter == commonotel.ExporterOTLP
	if otlpMode {
		log.Printf("[otel-stdout-host] exporter=otlp endpoint=%s service.name=%s", *otlpEndpoint, hostServiceName)
		log.Printf("[otel-stdout-host] view your traces: %s", tempoExploreURL(hostServiceName))
	}

	demo := demokit.New("MCP SEP-414 — OpenTelemetry Trace Context Propagation").
		Dir("otel/stdout").
		Description(buildDescription(otlpMode)).
		Actors(
			demokit.Actor("Host", "MCP Host (this walkthrough)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
		)

	demo.Section("Setup", buildSetupLines(otlpMode)...)

	demo.Section("Wire format",
		"SEP-414 carries W3C Trace Context two ways:",
		"",
		"- **In-band — `params._meta.traceparent` / `params._meta.tracestate`.** Authoritative; survives any transport.",
		"- **Out-of-band — HTTP `traceparent` / `tracestate` headers.** Streamable HTTP transports bridge the headers into ctx per SEP-2028 so the server's trace middleware can fall back to them when `_meta` is absent. In-band always wins.",
		"",
		"On the server side, `server.WithTracerProvider(tp)` installs an outermost trace middleware that extracts the inbound trace context, stamps `mcp.method` / `mcp.tool.name` / `mcp.session.id` attributes on a fresh span, and (P4) hands the child span back to the OpenTelemetry SDK so the exporter publishes it. Outbound notifications and server-to-client requests carry `_meta.traceparent` derived from the active span — a downstream MCP server receiving them stitches into the same trace.",
	)

	var c *client.Client

	demo.Step("Connect to the OTel-instrumented server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("`client.NewClient(...)` + `client.WithTracerProvider(...)` + `Connect()`. Each JSON-RPC dispatch emits a pair of spans — one on each side, stitched via `_meta.traceparent`. The initialize handshake produces the first pair before any tool call runs.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "otel-stdout-host", Version: "1.0"},
				client.WithTracerProvider(mcpotel.NewProvider(clientOTelTP,
					mcpotel.WithInstrumentationName("github.com/panyam/mcpkit/client"),
				)),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			if otlpMode {
				fmt.Printf("    Watch Grafana: %s\n", tempoExploreURL(hostServiceName))
			} else {
				fmt.Printf("    Watch BOTH terminals — matching TraceIDs prove the wire stitched the trace.\n")
			}
			return nil
		})

	// One looping "Explore" step that demokit re-enters via
	// StepResult.Next until the user picks "quit" (interactive) or the
	// Visits counter has cycled through every tool in `toolMenu`
	// (non-interactive). Demokit's Choice input handles the menu UX in
	// every renderer (TUI, plain, notebook, doc).
	demo.Step("Explore trace shapes").
		ID("explore").
		Input(demokit.Choice(append(toolMenu, "quit")...).
			Named("tool", "Pick a tool to call (or `quit` to exit)").
			WithDefault(toolMenu[0])).
		Note(buildExploreNote(otlpMode)).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			tool := selectTool(ctx)
			if tool == "" {
				if otlpMode {
					fmt.Printf("    Done. Final Grafana view: %s\n", tempoExploreURL(hostServiceName))
				}
				return nil
			}

			dispatchTool(c, tool)

			if otlpMode {
				fmt.Printf("\n    See this trace in Grafana: %s\n", tempoExploreURL(hostServiceName))
				fmt.Printf("    (server-side spans: %s)\n", tempoExploreURL(serverServiceName))
			} else {
				fmt.Printf("\n    Match the TraceID across THIS terminal and the SERVER terminal.\n")
			}

			return &demokit.StepResult{Next: "explore"}
		})

	if !otlpMode {
		demo.Section("Where to look in the code",
			"- `examples/otel/stdout/main.go::registerDemoTools` — the four tools that drive distinct trace shapes (echo / slow_echo / failing_tool / count_tool).",
			"- `examples/otel/stdout/walkthrough.go::runDemo` — the client-side wiring. `mcpotel.NewProvider(clientOTelTP, WithInstrumentationName(\".../client\"))` plugged into `client.WithTracerProvider`.",
			"- `client/trace_middleware.go` (in main mcpkit) — the SEP-414 P3 middleware. Outbound `Client.Call` is wrapped in a span; outbound params gain `_meta.traceparent`; inbound server-to-client requests (sampling/elicitation/roots) extract the inbound traceparent and emit a wrap span.",
			"- `ext/otel/provider.go` — `Provider.StartSpan` is the adapter's hot-path: parses inbound `core.TraceContext` into an OTel `SpanContext`, installs as the new span's parent, and re-attaches the child traceparent via `core.WithTraceContext` for outbound `_meta` injection.",
			"- `ext/otel/tracerprovider.go` — `mcpotel.NewTracerProvider` (issue 674): one-line helper that bakes `service.name` into the SDK Resource via `WithServiceName` without examples having to import `sdk/resource` + `semconv` themselves.",
			"- `server/trace_middleware.go` (in main mcpkit) — the SEP-414 P2 middleware. Sits outermost in the dispatch chain so user middleware runs INSIDE the span.",
		)
	} else {
		demo.Section("Where to view the traces",
			"All spans this walkthrough emits land in Tempo via the OTel Collector at `localhost:4317`. Pre-filtered Grafana Explore links:",
			"",
			fmt.Sprintf("- **Client-side spans** (this walkthrough): %s", tempoExploreURL(hostServiceName)),
			fmt.Sprintf("- **Server-side spans**: %s", tempoExploreURL(serverServiceName)),
			"",
			"Click any trace row in either view to see the full span tree, then use the \"View linked logs\" / \"View linked metrics\" buttons in the trace UI to follow the data into Loki / Mimir when those lanes light up (currently idle).",
		)
	}

	common.SetupRenderer(demo)
	demo.Execute()

	if c != nil {
		c.Close()
	}
}

// buildDescription returns the top-level description that demokit
// embeds in the rendered output, branched on the exporter mode so a
// reader doesn't see references to the WRONG mode's setup.
func buildDescription(otlpMode bool) string {
	if otlpMode {
		return "Walks through SEP-414 against the LGTM observability stack at `docker/observability/`. Both sides — server (`make serve EXPORTER=otlp`) and walkthrough (`make demo EXPORTER=otlp`) — ship spans via OTLP gRPC to the OTel Collector, which fans them out to Tempo. Grafana renders the stitched client→server trace under one TraceID per `tools/call`. Each step prints a pre-filtered Grafana Explore deep link so you can drill straight into Tempo. The looping \"Explore trace shapes\" step lets you A/B four distinct tools and compare their span shapes in Grafana."
	}
	return "Walks through SEP-414 in stdout-exporter mode (no external stack required). Both sides — server (`make serve`) and walkthrough (`make demo`) — print spans as pretty JSON on their respective terminals. The looping \"Explore trace shapes\" step lets you A/B four distinct tool calls; matching TraceIDs across the two terminals is the SEP-414 wire stitching client and server into a single distributed trace. Run with `EXPORTER=otlp` after `make -C docker up` to ship spans to Grafana instead."
}

// buildSetupLines returns the markdown lines for the Setup section,
// mode-branched. OTLP mode includes the docker stack-up step and the
// Grafana URL up-front so the reader knows where to look BEFORE the
// steps run.
func buildSetupLines(otlpMode bool) []string {
	if otlpMode {
		return []string{
			"Bring up the LGTM observability stack and start the OTel-instrumented server:",
			"",
			"```",
			"Terminal 1:  make -C ../../../docker up           # Tempo + Loki + Mimir + Grafana + OTel Collector",
			"Terminal 2:  make serve EXPORTER=otlp             # MCP server, exports OTLP",
			"Terminal 3:  make demo EXPORTER=otlp              # this walkthrough, exports OTLP",
			"```",
			"",
			"Then open Grafana — the steps below print pre-filtered Explore deep links you can click straight into:",
			"",
			fmt.Sprintf("- Grafana home: http://localhost:3000"),
			fmt.Sprintf("- Client-side traces: %s", tempoExploreURL(hostServiceName)),
			fmt.Sprintf("- Server-side traces: %s", tempoExploreURL(serverServiceName)),
			"",
			"Anonymous Admin access — no Grafana login form. Time range defaults to \"Last 15 minutes\" so just-emitted traces are in scope.",
		}
	}
	return []string{
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # OTel-instrumented server on :8080",
		"Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
		"",
		"Keep both terminals visible — the walkthrough surfaces what the *Host* sees on the wire (JSON-RPC results); the OpenTelemetry spans land on the *Server* terminal's stdout via the `stdouttrace` exporter. Match the TraceID across the two terminals to see the SEP-414 stitch.",
		"",
		"To send spans to a real backend instead, run with `EXPORTER=otlp` after `make -C ../../../docker up`.",
	}
}

// buildExploreNote returns the Note shown at the looping Explore step,
// branched to surface Grafana deep links in OTLP mode and terminal
// match instructions in stdout mode.
func buildExploreNote(otlpMode bool) string {
	if otlpMode {
		return "Pick a tool from the menu. Each one produces a distinct trace shape in Tempo:\n\n" +
			"- `echo` — baseline single-RPC trace.\n" +
			"- `slow_echo` — 750ms `time.Sleep` in the handler; span duration is visible in Tempo.\n" +
			"- `failing_tool` — handler returns IsError=true; span carries `mcp.tool.is_error=\"true\"` and Grafana renders it red.\n" +
			"- `count_tool` — handler emits 3 `notifications/progress`; the trace fan-out shows the parent `tools/call` plus 3 outbound notification spans, each carrying `_meta.traceparent` from the parent.\n" +
			"- `quit` — exit the loop.\n\n" +
			"After each call this step prints a pre-filtered Grafana Explore link so you can drill straight into the new trace."
	}
	return "Pick a tool from the menu. Each one produces a distinct trace shape on the stdout exporters:\n\n" +
		"- `echo` — baseline single span on each terminal.\n" +
		"- `slow_echo` — 750ms handler sleep; the span's StartTime → EndTime is visibly long.\n" +
		"- `failing_tool` — server span carries `mcp.tool.is_error=\"true\"`.\n" +
		"- `count_tool` — server emits 3 `notifications/progress`, each as its own outbound span with `_meta.traceparent` set.\n" +
		"- `quit` — exit the loop.\n\n" +
		"After each call, match the TraceID printed on this terminal against the SERVER terminal — that match IS the SEP-414 client↔server stitching."
}

// selectTool resolves which tool to dispatch on this Explore-step
// iteration. In interactive mode it reads the user's Choice input;
// the Sentinel "quit" returns "" so the caller stops looping. In non-
// interactive mode it walks toolMenu by Visits, returning "" once
// every tool has been exercised so the loop exits without manual
// intervention.
func selectTool(ctx demokit.StepContext) string {
	if demokit.IsNonInteractive() {
		if ctx.Visits > len(toolMenu) {
			return ""
		}
		return toolMenu[ctx.Visits-1]
	}
	tool, _ := ctx.Inputs["tool"].(string)
	if tool == "quit" {
		return ""
	}
	return tool
}

// dispatchTool calls the named tool through the *client.Client. Each
// tool's argument shape is intentionally minimal so the demo focuses
// on the resulting trace shape rather than tool-input mechanics.
func dispatchTool(c *client.Client, tool string) {
	args := map[string]any{}
	if tool == "echo" {
		args["message"] = "hello from explore step"
	}
	res, err := c.Call("tools/call", map[string]any{
		"name":      tool,
		"arguments": args,
	})
	if err != nil {
		fmt.Printf("    %s: ERROR: %v\n", tool, err)
		return
	}
	var v any
	if err := json.Unmarshal(res.Raw, &v); err != nil {
		fmt.Printf("    %s: ERROR decoding raw: %v\n", tool, err)
		return
	}
	pretty, _ := json.MarshalIndent(v, "    ", "  ")
	fmt.Printf("    %s →\n    %s\n", tool, string(pretty))
}
