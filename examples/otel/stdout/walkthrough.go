package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
)

// inboundTraceparent is a deterministic W3C version-00 traceparent the
// walkthrough sends on every tools/call so a reader can scroll back to
// the server terminal and visually confirm the inbound trace ID landed
// as the span's Parent. The trace ID matches the W3C spec example
// (`4bf92f3577b34da6a3ce929d0e0e4736`) for cross-document recognizability.
const inboundTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func runDemo() {
	serverURL := common.ServerURL()

	demo := demokit.New("MCP SEP-414 — OpenTelemetry Trace Context Propagation").
		Dir("otel/stdout").
		Description("Walks through SEP-414, which propagates W3C Trace Context through MCP using the `_meta.traceparent` / `_meta.tracestate` envelope (and, on streamable HTTP, the matching HTTP headers per SEP-2028). The server wires the new `ext/otel` adapter into `server.WithTracerProvider` so every JSON-RPC dispatch emits an OpenTelemetry span — exported as pretty-printed JSON on the server's stdout via the `stdouttrace` exporter. The walkthrough drives a real `tools/call` with a known inbound traceparent so a reader can see the inbound trace ID become the span's Parent on the server side.").
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
		Note("`client.NewClient(...)` + `Connect()`. The handshake itself dispatches through the trace middleware on the server, so the *Server* terminal will print three spans during this walkthrough: `initialize`, `notifications/initialized`, and the `tools/call` from the next step. No client-side instrumentation is wired here — P3 (client-side spans) lives on a separate branch.").
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(serverURL+"/mcp",
				core.ClientInfo{Name: "otel-stdout-host", Version: "1.0"},
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return nil
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			fmt.Printf("    Look at the server's stdout — you should see initialize and notifications/initialized spans already exported.\n")
			return nil
		})

	demo.Step("tools/list — every dispatch is a span").
		Arrow("Host", "Server", "tools/list").
		DashedArrow("Server", "Host", "tools[]").
		Note("The trace middleware sits OUTERMOST in the dispatch chain, so *every* JSON-RPC request emits a span — not just tools/call. This step doesn't pass an inbound traceparent, so the resulting span starts a fresh trace (no Parent). Compare the Server terminal's span for this list call with the next step's span: this one has no Parent; the next one does.").
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

	demo.Step("tools/call echo — with explicit inbound _meta.traceparent").
		Arrow("Host", "Server", "tools/call echo { _meta: { traceparent: 00-4bf9…-00f0…-01 } }").
		DashedArrow("Server", "Host", "text result + (under the hood) child-span _meta on any outbound").
		Note(fmt.Sprintf("The Host sends `params._meta.traceparent=%s` — the W3C-spec example trace ID, used here so the trace ID is visually recognizable. On the Server terminal, scroll to the `tools/call` span and look for two things:\n\n1. `SpanContext.TraceID` equals `4bf92f3577b34da6a3ce929d0e0e4736` — proves the inbound trace ID carried through.\n2. `Parent.SpanID` equals `00f067aa0ba902b7` and `Parent.Remote == true` — proves the middleware recognized the in-band traceparent as a remote parent.\n\nIf you instead remove the `_meta` field from the call below, the span starts a fresh trace (no Parent), matching the previous step's tools/list behavior.", inboundTraceparent)).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream, application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hello"},"_meta":{"traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}}}' | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `res, _ := c.Call("tools/call", map[string]any{
    "name":      "echo",
    "arguments": map[string]any{"message": "hello"},
    "_meta":     map[string]any{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
})`),
		).
		Run(func(ctx demokit.StepContext) *demokit.StepResult {
			res, err := c.Call("tools/call", map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"message": "hello"},
				"_meta":     map[string]any{"traceparent": inboundTraceparent},
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
			fmt.Printf("\n    Server terminal: look for a span with Name=\"tools/call\", TraceID=4bf92f3577b34da6a3ce929d0e0e4736, Parent.SpanID=00f067aa0ba902b7.\n")
			return nil
		})

	demo.Section("Where to look in the code",
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
