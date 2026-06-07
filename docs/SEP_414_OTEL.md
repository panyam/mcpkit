# SEP-414: OpenTelemetry Trace Context Propagation

Status: **Phase 1 landed (contract surface only).** Wiring to come.

This document is the future home of the OTel wiring guide for mcpkit.
Today it links the contract surface that Phase 1 ships and points at the
remaining phases tracked on [issue #312][issue].

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

## What comes next

Tracked phases on [issue #312][issue]:

- **P2 — server-side spans.** A `traceMiddleware` in `server/` wraps
  every dispatch in an inbound span; the streamable transport bridges
  the HTTP `traceparent` header into `_meta` per SEP-2028; outbound
  notifications and server-to-client requests inject the active span's
  trace context into `_meta`.
- **P3 — client-side spans.** Outbound client calls wrap in spans and
  inject `_meta.traceparent`; incoming server-to-client requests
  extract the parent.
- **P4 — `ext/otel/` adapter.** A new sub-module (separate `go.mod`)
  that implements `core.TracerProvider` on top of
  `go.opentelemetry.io/otel`. Required for traces to actually flow to
  an exporter (Jaeger, OTLP, etc.). Until this lands, the
  `NoopTracerProvider` default means traces go nowhere.
- **P5 — docs + examples.** This file fills out with the wiring recipe;
  `examples/otel/` ships a minimal end-to-end walkthrough.

The four phases can be developed in parallel branches on top of P1 —
they touch disjoint trees (P2 = `server/`, P3 = `client/`, P4 = a new
`ext/otel/` module, P5 = docs).

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
