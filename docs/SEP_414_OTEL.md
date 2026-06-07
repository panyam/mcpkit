# SEP-414: OpenTelemetry Trace Context Propagation

Status: **Phases 1–2 landed.** Adapter (Phase 4) and client-side spans
(Phase 3) still to come.

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

## What comes next

Tracked phases on [issue 312][issue]:

- **P3 — client-side spans.** Outbound client calls wrap in spans and
  inject `_meta.traceparent`; incoming server-to-client requests
  extract the parent.
- **P4 — `ext/otel/` adapter.** A new sub-module (separate `go.mod`)
  that implements `core.TracerProvider` on top of
  `go.opentelemetry.io/otel`. Required for traces to actually flow to
  an exporter (Jaeger, OTLP, etc.). Until this lands,
  `NoopTracerProvider` and hand-rolled adapters are the only choices.
- **P5 — examples.** `examples/otel/` ships a minimal end-to-end
  walkthrough once an adapter is available.

P3, P4, P5 can be developed in parallel branches on top of P2 — they
touch disjoint trees (P3 = `client/`, P4 = a new `ext/otel/` module,
P5 = examples).

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
