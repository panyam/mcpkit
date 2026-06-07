# SEP-414: OpenTelemetry Trace Context Propagation

Status: **Phases 1, 2, 3, and 4 landed.** Polished walkthroughs (Phase 5)
and the conformance suite (issue 429) still to come.

This document is the wiring guide for mcpkit's OpenTelemetry surface. It
covers what the shipped phases give you, what to expect once you wire a
real `TracerProvider`, and where the remaining work lives on
[issue 312][issue].

## What Phase 1 ships

Phase 1 lands the dependency-free contract surface in `core/`:

- `core.TracerProvider`, `core.Span`, `core.Attribute` — the minimal
  tracing seam mcpkit components consume. The default,
  `core.NoopTracerProvider`, performs no allocation and emits no spans.
- `core.TraceContext` — the W3C `traceparent` / `tracestate` pair as
  propagated on the MCP wire.
- `core.ExtractTraceContext` / `core.InjectTraceContext` — read/write
  W3C Trace Context from/to an MCP `_meta` map, with strict
  W3C-version-00 validation on extract.
- `core.WithTraceContext` / `core.TraceContextFromContext` —
  `context.Context` plumbing.
- `core.BaseContext.TraceContext()` — accessor exposed on every typed
  handler context (`ToolContext`, `PromptContext`, `ResourceContext`,
  `MethodContext`) so handlers can read the active trace context
  without import gymnastics.

The `MetaKeyTraceparent` / `MetaKeyTracestate` constants pin the wire
keys (bare `traceparent` and `tracestate`, matching W3C HTTP header
names — not under the `io.modelcontextprotocol/` namespace, which is
reserved for MCP-defined fields).

Phase 1 introduces **no behavior change**: nothing in mcpkit calls
`TracerProvider.StartSpan` yet, and no transport reads or writes the
`_meta` trace keys. The surface exists so downstream work can be
written and reviewed against a stable contract.

## What Phase 2 ships

Phase 2 wires the server-side propagation surface:

- `server.WithTracerProvider(tp core.TracerProvider) Option` — install a
  tracer. Default and `core.NoopTracerProvider{}` both skip the trace
  middleware entirely so the zero-overhead path stays untouched.
- An internal `traceMiddleware` wraps every JSON-RPC dispatch in an
  inbound span, positioned OUTERMOST so user-registered middleware
  contributes to the recorded latency. Span attributes:
  - `mcp.method` on every span
  - `mcp.tool.name` on `tools/call`
  - `mcp.session.id` when a session is attached
  - `mcp.error.code` + `Span.RecordError` on JSON-RPC errors
  - `mcp.tool.is_error="true"` on `tools/call` results carrying `IsError`
- Inbound trace context resolution: `params._meta.traceparent` wins
  over the out-of-band TraceContext bridged through `context.Context`.
  Malformed traceparent values are dropped per the W3C "MUST NOT
  forward" rule — the span still emits with no parent.
- The streamable HTTP transport bridges the W3C `traceparent` /
  `tracestate` HTTP headers (SEP-2028) into `context.Context` once at
  the POST entry point. All downstream dispatch paths (sync JSON, SSE,
  batch, stateless, initialize) inherit the bridge.
- Outbound `_meta.traceparent` / `_meta.tracestate` are injected on
  every server-to-client message — notifications (logging, progress,
  resource updates, custom) and server-to-client requests (legacy push
  for sampling/elicitation/roots). The wrap sits OUTSIDE any
  user-registered `NotifyInterceptor` / `RequestInterceptor`, so those
  interceptors observe the same wire form the client receives.

Wiring is a single line in your server bootstrap:

```go
srv := server.NewServer(info,
    // ...your other options...
    server.WithTracerProvider(myTracerProvider),
)
```

`myTracerProvider` must implement `core.TracerProvider`. Until the
Phase 4 `ext/otel/` adapter ships, the only options that emit spans are
custom implementations — the in-tree `core.NoopTracerProvider`
intentionally treats "spans" as no-ops.

## What Phase 4 ships

Phase 4 lands the OpenTelemetry SDK adapter as a new sub-module under
`ext/otel/` (separate `go.mod`, mirroring `ext/auth/` / `ext/tasks/` /
`ext/ui/`). Wire it in with one line:

```go
import (
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"

    "github.com/panyam/mcpkit/server"
    mcpotel "github.com/panyam/mcpkit/ext/otel"
)

exp, _ := stdouttrace.New()
otelTP := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
defer otelTP.Shutdown(ctx)

srv := server.NewServer(info,
    server.WithTracerProvider(mcpotel.NewProvider(otelTP)),
)
```

Behavior:

- The adapter parses `core.TraceContext` from ctx into an OTel
  `SpanContext` so the inbound parent flows into the SDK's standard
  parent-tracking machinery.
- After `StartSpan`, it formats the new span's `SpanContext` as a W3C
  `traceparent` and re-attaches via `core.WithTraceContext`, so the
  Phase 2 outbound `_meta` injection wraps stamp every server-to-client
  message with the **child** span's traceparent (not the inbound
  parent's).
- `core.Attribute{Key, Value string}` maps to `attribute.String(...)`;
  `RecordError(err)` emits an OTel "exception" event AND sets the span
  status to `codes.Error`; `End()` is idempotent.
- `WithInstrumentationName(...)` overrides the default
  instrumentation library name (`"github.com/panyam/mcpkit/server"`).

Verification:

- `make test-otel` — adapter unit tests run against the real OTel SDK
  in-memory exporter; reads back `sdktrace.ReadOnlySpan` shapes.
- `make test-otel-example` — smoke test for `examples/otel/stdout/`
  asserting the exporter actually prints the expected span set.
- `go run examples/otel/stdout/...` — runnable demo, prints spans as
  JSON on stdout. No collector required.

See [`ext/otel/README.md`](../ext/otel/README.md) for the API reference
and [`examples/otel/stdout/README.md`](../examples/otel/stdout/README.md)
for the walkthrough.

## What Phase 3 ships

Phase 3 lands the symmetric client-side surface:

- `client.WithTracerProvider(tp core.TracerProvider)` — install a
  tracer. Default and `core.NoopTracerProvider{}` both skip the install
  (zero overhead on the unconfigured path).
- Outbound `Client.Call` is wrapped in a span via a `ClientMiddleware`
  installed as the OUTERMOST entry — user middleware (auth retry,
  header injection) runs inside the span so its latency is captured.
  Span attributes:
  - `mcp.method` on every span
  - `mcp.tool.name` on `tools/call`
  - `mcp.client.session.id` when a session exists (note `client.`
    namespace — operators want to filter by client- vs
    server-emitted)
  - `mcp.error.code` + `Span.RecordError` on `*RPCError`
- Outbound params gain `_meta.traceparent` / `_meta.tracestate`
  derived from the active span via `core.InjectTraceContextIntoParams`
  (promoted from the server-internal helper so both wires apply the
  same precedence rule — explicit caller-set values win).
- Inbound server-to-client requests
  (`sampling/createMessage`, `elicitation/create`, `roots/list`):
  `params._meta.traceparent` is extracted via
  `core.ExtractTraceContextFromParams`, attached to ctx via
  `core.WithTraceContext`, and a wrap span named after the request
  method is emitted with the inbound trace context as parent. The
  handler observes the inbound TC via `core.TraceContextFromContext(ctx)`
  and can use it as a parent for its own spans.

Wiring is a single line in your client bootstrap:

```go
c := client.NewClient(url, info,
    // ...your other options...
    client.WithTracerProvider(myTracerProvider),
)
```

`myTracerProvider` is the same `core.TracerProvider` you'd hand the
server side via `server.WithTracerProvider`. The Phase 4 `ext/otel/`
adapter (PR 652) is the in-tree implementation.

## What comes next

Tracked phases on [issue 312][issue]:

- **P5 — polished examples.** `examples/otel/jaeger/` +
  `examples/otel/otlp/` end-to-end walkthroughs with collector and UI
  screenshots. The minimal `examples/otel/stdout/` already covers
  smoke-verification.
- **Conformance suite `testconf-otel`** — issue 429.

## Downstream consumers of the Phase 1 contract

- **`experimental/ext/events/` cross-replica bus (issue #629).** The
  EventBus envelope will carry `traceparent` / `tracestate` using the
  `core.MetaKey*` constants, so a trace started on replica A and
  delivered from replica B stitches together once an OTel adapter is
  wired. The seam consumes `core.TracerProvider` directly — no
  `ext/otel` import in the base events module, mirroring how events
  already consumes `core.Claims` without depending on `ext/auth`.
- **`server/` topology preflight (issue #642).** The capability
  contract declares whether a configured seam supports trace
  propagation; the declaration uses the `core.TracerProvider`
  interface to keep the contract dep-free.

[issue]: https://github.com/panyam/mcpkit/issues/312
