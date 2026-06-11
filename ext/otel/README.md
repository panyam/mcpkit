# ext/otel — OpenTelemetry adapter for SEP-414

Go module that adapts the [OpenTelemetry Go SDK](https://opentelemetry.io/docs/languages/go/)
to mcpkit's dependency-free `core.TracerProvider` contract. Pairs with
the SEP-414 server-side propagation surface that landed in PR 649 (P2):
once an adapter is wired via `server.WithTracerProvider`, every JSON-RPC
dispatch emits an inbound span and every outbound message (notification,
sampling, elicitation, roots) carries `_meta.traceparent` /
`_meta.tracestate` derived from the active span.

> Looking for the SEP-414 design + phase status? See
> [`docs/SEP_414_OTEL.md`](../../docs/SEP_414_OTEL.md). This README is
> the adapter's API reference; the design doc is the rollout narrative.

## Why a sub-module

OpenTelemetry brings ~10 transitive dependencies (SDK, attribute,
codes, exporters, ...). Keeping the adapter out of the base module
matches the established `ext/auth` / `ext/tasks` / `ext/ui` precedent:
servers that don't trace pay nothing.

## Quickstart

```go
import (
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"

    "github.com/panyam/mcpkit/core"
    "github.com/panyam/mcpkit/server"
    mcpotel "github.com/panyam/mcpkit/ext/otel"
)

exp, err := stdouttrace.New()
if err != nil { /* handle */ }
otelTP := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
defer otelTP.Shutdown(ctx)

srv := server.NewServer(
    core.ServerInfo{Name: "my-server", Version: "0.1.0"},
    server.WithTracerProvider(mcpotel.NewProvider(otelTP)),
)
srv.RegisterTool(def, handler)
srv.Run(":8787")
```

Swap `stdouttrace` for any OTel exporter (OTLP, Jaeger, Datadog, ...)
and the surface is unchanged — the adapter consumes an
`otel/trace.TracerProvider`, not a specific exporter.

## What the adapter does

- **Implements `core.TracerProvider`.** `mcpotel.NewProvider(otelTP)`
  returns a value that mcpkit's server dispatch can call into. The
  internal Tracer is created once at construction; StartSpan is a
  single OTel `Tracer.Start` call plus a context plumbing step.
- **Bridges W3C trace context to OTel SpanContext, both ways.**
  Inbound: the SEP-414 P2 trace middleware extracts
  `params._meta.traceparent` (or the SEP-2028 HTTP header bridge) into
  ctx; the adapter parses it into an `otel/trace.SpanContext` and
  installs it as the new span's parent. Outbound: after `StartSpan`,
  the adapter updates ctx via `core.WithTraceContext` so the SEP-414
  P2 outbound `_meta` injection wraps stamp every server-to-client
  message with the child span's traceparent.
- **Maps `core.Attribute` to `attribute.String`.** P1's contract scopes
  attributes to `string`/`string`; typed attributes arrive when the
  core surface widens.
- **Enforces idempotent `End`.** Mcpkit's `core.Span` contract treats
  a second `End` as a no-op. The wrapper short-circuits before the
  underlying SDK can log its "span already ended" warning.
- **`RecordError(err)` emits both an OTel exception event and sets the
  span status to `codes.Error`.** This matches the OTel idiom — backends
  use `Status.Code` for filtering / counting error spans, separately
  from the recorded event payload.

## Options

| Option | Effect |
|---|---|
| `mcpotel.WithInstrumentationName(name string)` | Override the OTel instrumentation library name (default: `"github.com/panyam/mcpkit/server"`). |

## Metrics (issue 7)

`mcpotel.NewMeterProvider(otelMP)` is the sibling to `NewProvider` for
the `core.MeterProvider` seam. Wire it via `server.WithMeterProvider`
and the dispatch path will emit the canonical MCP server metrics:

```go
import (
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
    "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"

    "github.com/panyam/mcpkit/core"
    "github.com/panyam/mcpkit/server"
    mcpotel "github.com/panyam/mcpkit/ext/otel"
)

exp, err := otlpmetricgrpc.New(ctx)
if err != nil { /* handle */ }
otelMP := sdkmetric.NewMeterProvider(
    sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
)
defer otelMP.Shutdown(ctx)

srv := server.NewServer(
    core.ServerInfo{Name: "my-server", Version: "0.1.0"},
    server.WithTracerProvider(mcpotel.NewProvider(otelTP)),
    server.WithMeterProvider(mcpotel.NewMeterProvider(otelMP)),
)
```

Canonical instruments emitted from `server/metrics_middleware.go`:

| Metric | Type | Unit | Labels | Meaning |
|---|---|---|---|---|
| `mcp.tool.calls` | int64 counter | `1` | `tool` | Every `tools/call` dispatch. |
| `mcp.jsonrpc.errors` | int64 counter | `1` | `code` | Every JSON-RPC error response. |
| `mcp.tool.duration` | float64 histogram | `ms` | `tool` | Handler-attributable tool latency. |
| `mcp.sessions.active` | int64 up-down counter | `1` | — | Currently active streamable HTTP sessions. |

### Exemplars (default-on)

Every measurement forwards the active context to the underlying OTel
instrument. The OTel SDK's default exemplar filter
(`AlwaysOnSampleParent`) reads the active span via the same ctx
accessor `core.SpanFromContext` consumes and stamps an exemplar on
the measurement — Grafana + Mimir render clickable dots that pivot
to the matching Tempo trace, closing the metric ↔ trace loop the
SEP-414 work opened.

| Option | Effect |
|---|---|
| `mcpotel.WithMeterInstrumentationName(name string)` | Override the OTel instrumentation library name for the meter scope (default: `"github.com/panyam/mcpkit/server"`). |

### Coexistence with traces

`MeterProvider` and `Provider` are independent objects backed by
distinct OTel SDK providers (`MeterProvider` and `TracerProvider`).
Typical wiring uses a single OTel resource (service.name,
deployment.environment, ...) across both so Grafana's pivot finds
both halves of the same service.

## Verification

```
make test-otel              # unit tests against a real OTel SDK pipeline
make test-otel-example      # smoke test for the stdout example
```

The runnable demo lives at [`examples/otel/stdout`](../../examples/otel/stdout/)
and prints exported spans as JSON on stdout — no exporter
infrastructure required.

## Out of scope (other SEP-414 phases)

- **P3 — client-side spans.** Outbound `client.Call` wrapping +
  inbound server-to-client request parent extraction. Lives in
  `client/`. Tracked under issue 312.
- **P5 — `examples/otel/{jaeger,otlp}` walkthroughs.** Polished
  end-to-end docs with collector + UI screenshots. The minimal
  `stdout` example here is enough to verify the adapter; the polished
  walkthroughs land separately.
- **Conformance suite `testconf-otel`.** Brand-neutral OTel scenarios
  in the `panyam/mcpconformance` fork. Tracked as issue 429.
