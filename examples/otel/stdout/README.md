# examples/otel/stdout — SEP-414 Trace Context Propagation

Interactive demokit walkthrough showing how to wire OpenTelemetry
tracing into BOTH sides of an MCP exchange using the
[`ext/otel`](../../../ext/otel/) adapter — `server.WithTracerProvider`
on the server and `client.WithTracerProvider` on the walkthrough.

The walkthrough's "Explore trace shapes" step loops through a
**menu of four tool calls**, each chosen to produce a distinct trace
shape an operator can compare in their backend of choice:

- `echo` — baseline single-RPC trace
- `slow_echo` — 750ms `time.Sleep` in the handler; visible span duration
- `failing_tool` — IsError result; span carries `mcp.tool.is_error="true"`
- `count_tool` — emits 3 `notifications/progress`; trace fan-out shows the parent plus 3 outbound notification spans with matching `_meta.traceparent`

Pick `quit` from the menu to exit the loop.

Two exporter modes:

- **`--exporter=stdout` (default).** Both processes use the
  `stdouttrace` exporter; each terminal prints its own side's spans
  as pretty-printed JSON. CI-friendly: no external stack required.
  Match TraceID across the two terminals to see SEP-414 stitching.

- **`--exporter=otlp`.** Both processes ship spans via OTLP gRPC to
  the `docker/observability/` stack (default endpoint
  `localhost:4317`). Spans land in Grafana, indexed by `service.name`
  (`otel-stdout-demo` for the server, `otel-stdout-host` for the
  walkthrough). Each Explore-step iteration prints a pre-filtered
  Grafana Explore deep link so you drill straight into Tempo.

## Quick Start

### Stdout mode — default (no infrastructure)

```
Terminal 1:  make serve         # server-side spans dump here
Terminal 2:  make demo          # client-side spans dump here
```

Keep both terminals visible. Match TraceID across them to see the
client→server stitch.

### OTLP mode (Grafana UI) — pass `EXPORTER=otlp`

```
Terminal 1:  make -C ../../../docker up           # bring observability stack up
Terminal 2:  make serve EXPORTER=otlp             # server → OTLP collector
Terminal 3:  make demo EXPORTER=otlp              # walkthrough → OTLP collector
Browser:     open http://localhost:3000           # Grafana — anonymous Admin

# When done:
make -C ../../../docker down
```

Each iteration of the Explore step prints a pre-filtered Grafana
Explore deep link — open it directly to see the trace you just
emitted. Both spans for one `tools/call` (client + server) appear in
the same trace view, linked by parent-of via `_meta.traceparent`.

## What it demonstrates

- **Symmetric wiring.** `server.WithTracerProvider(...)` in `main.go::serve()` and `client.WithTracerProvider(...)` in `walkthrough.go::runDemo()`. Each line is one call against the same `ext/otel.NewProvider` adapter — the in-tree implementation of `core.TracerProvider`.
- **Two processes, two pipelines, one trace.** Each side runs its own `stdouttrace`+`sdktrace` pipeline because they're separate OS processes. The SEP-414 `_meta.traceparent` plumbing carries trace identity across the wire so the two exporters record the *same* `TraceID`.
- **Every dispatch emits a span on BOTH sides.** `initialize`, `notifications/initialized`, `tools/list`, `tools/call` each produce a client-side span (on the walkthrough terminal) AND a server-side span (on the serve terminal). The walkthrough surfaces matching `TraceID`s on the `tools/call` step explicitly.
- **Client-side `_meta.traceparent` auto-injection (P3).** The walkthrough's tools/call carries no caller-supplied `_meta`. The client trace middleware starts a span, then stamps the new span's traceparent into outbound `params._meta.traceparent` via `core.InjectTraceContextIntoParams`. On the server side, the trace middleware extracts that `_meta.traceparent`; the adapter installs the OTel SpanContext as the new server-side span's parent (`Remote=true`). End result: matching TraceID across both spans, server's `Parent.SpanID` equals the client's `SpanID`.
- **Outbound context sync.** After `StartSpan`, the adapter re-attaches the *child* span's traceparent to ctx via `core.WithTraceContext`. SEP-414 P2's outbound `_meta` injection wraps read that ctx, so every server-to-client notification or sampling request carries the new child traceparent — a downstream MCP server stitches into the same trace.
- **Distinct instrumentation names.** The walkthrough's pipeline tags spans with `"github.com/panyam/mcpkit/client"`; the server's defaults to `"github.com/panyam/mcpkit/server"`. Observability backends group by emitting side.

## Architecture

```mermaid
sequenceDiagram
    participant HostStdout as walkthrough stdout
    participant H as Host (make demo)
    participant SRV as Server (make serve)
    participant ServerStdout as serve stdout

    Note over H: client pipeline stdouttrace then sdktrace then mcpotel
    Note over SRV: server pipeline stdouttrace then sdktrace then mcpotel

    H->>H: traceMiddleware StartSpan tools/call
    H->>H: inject _meta.traceparent into params
    H->>HostStdout: client span exports to walkthrough terminal
    H->>SRV: HTTP POST tools/call with _meta.traceparent
    SRV->>SRV: traceMiddleware extracts _meta.traceparent
    SRV->>SRV: StartSpan tools/call with Remote parent
    SRV->>SRV: handler runs
    SRV->>ServerStdout: server span exports to serve terminal
    SRV-->>H: response
    H->>H: trace span End records error or status
    HostStdout-->>HostStdout: TraceID matches ServerStdout TraceID
```

## Where to look in the code

- `main.go::serve` — server-side wiring. Builds the OTel pipeline (stdouttrace → SDK TP), wraps with `mcpotel.NewProvider`, hands to `common.RunServer` via `server.WithTracerProvider`.
- `main.go::newOTelPipeline` — the one-shot helper that constructs the SDK TracerProvider with the stdout exporter and returns its shutdown closure. Reused by `walkthrough.go` for the client pipeline so both sides build their pipeline identically.
- `walkthrough.go::runDemo` — client-side wiring + the demokit script. Builds its own pipeline via the same `newOTelPipeline` helper and passes the adapter to `client.WithTracerProvider` with `WithInstrumentationName("github.com/panyam/mcpkit/client")` so observability backends group client vs server spans.
- `client/trace_middleware.go` (in main mcpkit) — the SEP-414 P3 middleware. Outbound `Client.Call` is wrapped in a span; outbound params gain `_meta.traceparent`; inbound server-to-client requests (sampling/elicitation/roots) get wrap spans whose parent is the inbound traceparent.
- `server/trace_middleware.go` (in main mcpkit) — the SEP-414 P2 middleware that consumes the adapter on the server side. Sits outermost so user middleware runs INSIDE the recorded span.
- `ext/otel/provider.go` (in the adapter module) — `Provider.StartSpan` is the hot-path: parses inbound `core.TraceContext` into an OTel SpanContext, calls `tracer.Start`, and re-attaches the child traceparent via `core.WithTraceContext` so the outbound `_meta` injection stamps the right ID downstream.

## Make targets

```
make demo      # run the walkthrough (TUI mode)
make note      # run the walkthrough in notebook mode
make serve     # start the server on :8080
make readme    # regenerate WALKTHROUGH.md
make build     # compile to ./otel-stdout-demo
make test      # run the e2e smoke test (uses an in-memory exporter)
```

## Beyond stdout

`mcpotel.NewProvider` accepts any `otel/trace.TracerProvider`. Swap the
`stdouttrace` exporter for OTLP, Jaeger, or anything else — the mcpkit
surface is unchanged. Polished walkthroughs for Jaeger and OTLP live in
SEP-414 P5 on [issue 312][issue].

[issue]: https://github.com/panyam/mcpkit/issues/312
