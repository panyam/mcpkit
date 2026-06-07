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
