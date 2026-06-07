package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
)

// (Previously this file defined an explicit inbound traceparent and
// supplied it in the tools/call. Removed once the client side was
// instrumented: SEP-414's "explicit caller-set _meta wins" rule means
// that an externally-supplied traceparent ends up on the SERVER side
// only, while the client emits a SEPARATE auto-generated trace — the
// two would NOT share a TraceID and the symmetric-stitching narrative
// would break. The auto-inject path the walkthrough now exercises
// produces matching TraceIDs on both terminals — what the reader is
// supposed to see.)

func runDemo() {
	serverURL := common.ServerURL()

	// Stand up the walkthrough's OWN OTel pipeline so client-side spans
	// land on this terminal's stdout via stdouttrace — symmetric with
	// the server's pipeline in serve(). Each process gets its own
	// TracerProvider (the two are separate OS processes; in-memory
	// sharing isn't possible), but the SEP-414 wire propagates trace
	// context across them so the recorded spans share a TraceID. The
	// instrumentation name carries `mcpkit/client` so observability
	// backends group client-emitted spans separately.
	clientOTelTP, shutdownClientOTel, err := newOTelPipeline(os.Stdout)
	if err != nil {
		fmt.Printf("failed to build client-side OTel pipeline: %v\n", err)
		return
	}
	defer shutdownClientOTel()

	demo := demokit.New("MCP SEP-414 — OpenTelemetry Trace Context Propagation").
		Dir("otel/stdout").
		Description("Walks through SEP-414, which propagates W3C Trace Context through MCP using the `_meta.traceparent` / `_meta.tracestate` envelope (and, on streamable HTTP, the matching HTTP headers per SEP-2028). BOTH sides are instrumented: the server (`make serve`) wires `server.WithTracerProvider` (P2); the walkthrough (`make demo`) wires `client.WithTracerProvider` (P3). Both pipelines feed the `stdouttrace` exporter, so each terminal prints its own side's spans as pretty-printed JSON. The walkthrough drives a real `tools/call` — the client trace middleware auto-stamps a `_meta.traceparent` on the outbound params and the server picks it up. Matching `TraceID` across the two terminals is the proof that the SEP-414 wire is actually stitching client and server into a single distributed trace.").
		Actors(
			demokit.Actor("Host", "MCP Host (this walkthrough)"),
			demokit.Actor("Server", "MCP Server (make serve) — spans dump on its stdout"),
		)

	demo.Section("Setup",
		"Start the MCP server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # OTel-instrumented server on :8080",
		"Terminal 2:  make demo          # this walkthrough (--tui for the interactive TUI)",
		"```",
		"",
		"Keep both terminals visible — the walkthrough surfaces what the *Host* sees on the wire (JSON-RPC results), while the OpenTelemetry spans land on the *Server* terminal's stdout via the `stdouttrace` exporter.",
	)

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
		Note("`client.NewClient(...)` + `client.WithTracerProvider(...)` + `Connect()`. Each JSON-RPC dispatch now emits TWO spans — one on each side. The initialize handshake will print `initialize` and `notifications/initialized` spans on BOTH terminals; matching them by TraceID is the visual proof that the SEP-414 wire is propagating from the very first call.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "otel-stdout-host", Version: "1.0"},
				// SEP-414 P3 — client-side trace surface. Outbound
				// Client.Call emits a span and stamps _meta.traceparent
				// on params; inbound server-to-client requests
				// (sampling/elicitation/roots) get wrap spans whose
				// parent is the inbound _meta.traceparent.
				client.WithTracerProvider(mcpotel.NewProvider(clientOTelTP,
					mcpotel.WithInstrumentationName("github.com/panyam/mcpkit/client"),
				)),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			fmt.Printf("    Watch THIS terminal: initialize and notifications/initialized client-side spans will appear below.\n")
			fmt.Printf("    Watch the SERVER terminal: matching server-side spans land there for the same JSON-RPC requests.\n")
			return nil
		})

	demo.Step("tools/list — every dispatch is a span").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[]").
		Note("The trace middleware sits OUTERMOST in the dispatch chain on both sides, so *every* JSON-RPC request emits a pair of spans (one per side, stitched via `_meta.traceparent`). This step is functionally identical to the next one in terms of propagation — the only difference is what attributes get set. Watch the TraceID match across terminals for this list call too.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			page, err := c.ListToolsPage("")
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			for _, tool := range page.Tools {
				fmt.Printf("    %-10s %s\n", tool.Name, tool.Description)
			}
			return nil
		})

	demo.Step("tools/call echo — let the client auto-inject _meta.traceparent").
		Arrow("Host", "Server", "tools/call echo (no caller _meta; client trace mw stamps its own)").
		DashedArrow("Server", "Host", "text result; server span's Parent matches the client SpanID").
		Note("With both wires instrumented (PR 649 server + PR 654 client), this single tools/call produces TWO spans on TWO terminals:\n\n1. **Client side (THIS terminal).** The `Client.Call` outbound trace middleware emits a span named `tools/call` with a fresh TraceID + SpanID. Before the call hits the wire, the middleware stamps that TraceID and SpanID into `params._meta.traceparent` via `core.InjectTraceContextIntoParams` (P3, shared helper).\n2. **Server side (the SERVER terminal).** The server trace middleware extracts the stamped `_meta.traceparent` and emits its own `tools/call` span whose `SpanContext.TraceID` MATCHES the client's, and whose `Parent.SpanID` MATCHES the client span's SpanID with `Parent.Remote == true`.\n\nMatch the spans by TraceID across the two terminals — that IS the SEP-414 wire stitching client and server into a single distributed trace. The TraceID is auto-generated each run (no synthetic override), so it'll be different every time — that's the point: real production traces won't have hardcoded IDs.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# When using curl directly, no client trace middleware exists, so
# YOU supply the traceparent. Match it across terminals by inspection.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello"}}}' | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// Note: the Client must be constructed with
// client.WithTracerProvider(mcpotel.NewProvider(...)) for the
// auto-inject to fire. See walkthrough.go::runDemo.
res, _ := c.Call("tools/call", map[string]any{
    "name":      "echo",
    "arguments": map[string]any{"message": "hello"},
})`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			res, err := c.Call("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"message": "hello"},
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return nil
			}
			var v any
			if err := json.Unmarshal(res.Raw, &v); err != nil {
				fmt.Printf("    ERROR decoding raw: %v\n", err)
				return nil
			}
			pretty, _ := json.MarshalIndent(v, "    ", "  ")
			fmt.Printf("    %s\n", string(pretty))
			fmt.Printf("\n    Match by TraceID:\n")
			fmt.Printf("    - THIS terminal: client-side span Name=\"tools/call\" with a fresh TraceID + SpanID.\n")
			fmt.Printf("    - SERVER terminal: a span with the SAME TraceID, plus Parent.SpanID == client SpanID and Parent.Remote=true.\n")
			return nil
		})

	demo.Section("Where to look in the code",
		"- `examples/otel/stdout/walkthrough.go::runDemo` — the client-side wiring. `mcpotel.NewProvider(clientOTelTP, WithInstrumentationName(\".../client\"))` plugged into `client.WithTracerProvider`. The pipeline uses the same `stdouttrace` exporter as the server, just pointing at this process's stdout.",
		"- `client/trace_middleware.go` (in main mcpkit) — the SEP-414 P3 middleware that consumes the adapter on the client side. Outbound `Client.Call` is wrapped in a span; outbound params gain `_meta.traceparent` derived from the active span; inbound server-to-client requests (sampling/elicitation/roots) extract the inbound traceparent and emit a wrap span.",
		"- `ext/otel/provider.go` — `Provider.StartSpan` is the adapter's single hot-path: parses inbound `core.TraceContext` into an OTel `SpanContext`, installs as the new span's parent, and after `tracer.Start` re-attaches the *child* span's traceparent via `core.WithTraceContext` so P2's outbound `_meta` injection stamps the right trace ID on downstream messages.",
		"- `ext/otel/span.go` — narrows OTel's broader Span surface to mcpkit's three-method `core.Span` contract. CAS-guarded `End` prevents the SDK's noisy double-End warning.",
		"- `ext/otel/propagation.go` — pure W3C ↔ OTel SpanContext conversions. `traceContextToSpanContext` validates structurally before installing; `spanContextToTraceContext` formats the new span back into the W3C version-00 string the wire expects.",
		"- `server/trace_middleware.go` (in main mcpkit) — the SEP-414 P2 middleware that consumes the adapter. Sits outermost in the dispatch chain so user middleware runs INSIDE the span.",
		"- `examples/otel/stdout/main.go` — the wiring: `server.WithTracerProvider(mcpotel.NewProvider(otelTP))` is the one new line in `serve()`. Everything else is canonical `common.RunServer` boilerplate.",
	)

	common.SetupRenderer(demo)
	demo.Execute()

	if c != nil {
		c.Close()
	}
}
