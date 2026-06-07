# Tracing

How one logical operation that crosses the client→server boundary (and back) stitches into a single distributed trace — OpenTelemetry trace-context propagation as a first-class MCP extension.

> **Kind:** root *(FAQ-style)* · **Prerequisites:** [request-anatomy](./request-anatomy.md), [extension-mechanisms](./extension-mechanisms.md), [reverse-call](./reverse-call.md)
> **Reachable from:** [README](./README.md), [extension-mechanisms](./extension-mechanisms.md) Next-to-read (the `_meta`-field extension surface, worked out)
> **Branches into:** — *(cross-replica events `EventBus` tracing is a forward pointer in Next-to-read, not yet its own page)*
> **Spec:** SEP-414 (OpenTelemetry trace context propagation) + SEP-2028 (HTTP-header → `_meta` bridge), wire format is [W3C Trace Context][w3c-tc]; mcpkit-side rollout in [`docs/SEP_414_OTEL.md`][mcpkit-sep414] · **Code:** [`core/trace.go`](https://github.com/panyam/mcpkit/blob/main/core/trace.go) · [`server/trace_middleware.go`](https://github.com/panyam/mcpkit/blob/main/server/trace_middleware.go) · [`client/trace_middleware.go`](https://github.com/panyam/mcpkit/blob/main/client/trace_middleware.go) · [`ext/otel/provider.go`](https://github.com/panyam/mcpkit/blob/main/ext/otel/provider.go) · [`ext/otel/propagation.go`](https://github.com/panyam/mcpkit/blob/main/ext/otel/propagation.go)

## Prerequisites

- The request journey — the four middleware stacks (client × {send, recv}, server × {send, recv}), the handler context, and the `_meta` passthrough. Tracing is a middleware + `_meta` mechanism; this page assumes you know where both live. → If not, read [per-request anatomy](./request-anatomy.md).
- Extension-surface vocabulary — what a `_meta` field is as an extension knob, and the `core/` → `ext/` → `experimental/ext/` tiering (the OTel SDK adapter lives in its own `ext/otel/` go.mod). → If not, read [extension mechanisms](./extension-mechanisms.md).
- Reverse calls — server→client requests (`sampling/createMessage`, `elicitation/create`, `roots/list`). Outbound propagation is what keeps a trace connected when the *server* dials the client mid-handler. → If not, read [reverse-call mechanics](./reverse-call.md).

> [!NOTE]
> You'll also meet the HTTP `traceparent` header → `_meta` bridge (SEP-2028). If you want the HTTP/SSE wire under it, [transport-mechanics](./transport-mechanics.md) has it — but it isn't required to follow this page.

## Context

[Extension mechanisms](./extension-mechanisms.md) listed `_meta` fields as one of the four extension knobs and called list-TTL "the canonical `_meta`-only extension." Tracing is the canonical **`_meta`-field-plus-middleware** extension: the wire surface is two `_meta` keys (`traceparent` / `tracestate`), and the behavior is a span started in middleware around every dispatch. There is no new method, no new capability flag, no new notification.

The interesting questions: when does tracing actually run, and what does it cost when off (Q1)? why does cross-process tracing need a *standard* wire format, and why does it ride `_meta` (Q2)? how do you wire it, and what is the TracerProvider-vs-Tracer thing (Q3)? what happens to one request on the server (Q4)? how does a single trace stay connected as it crosses the wire and comes back (Q5)? and what's symmetric vs. not on the client side (Q6)?

> [!NOTE]
> SEP-414 shipped in five phases (P1 core contract, P2 server spans, P3 client spans, P4 the `ext/otel` adapter, P5 example). This page teaches the end state, not the rollout — for phase status see [`docs/SEP_414_OTEL.md`][mcpkit-sep414].

## Q1 — When does tracing "kick in", and what does it cost when off?

Three layers, and the OpenTelemetry SDK only enters at the third:

| Layer | Package | Imports OTel SDK? | Present when? |
|-------|---------|-------------------|---------------|
| **Contract** — `TracerProvider`, `Span`, `TraceContext`, W3C extract/inject | [`core/trace.go`](https://github.com/panyam/mcpkit/blob/main/core/trace.go) | no | always (dependency-free) |
| **Wiring** — start spans, inject/extract `_meta.traceparent` | [`server/trace_middleware.go`](https://github.com/panyam/mcpkit/blob/main/server/trace_middleware.go), [`client/trace_middleware.go`](https://github.com/panyam/mcpkit/blob/main/client/trace_middleware.go) | no | installed **only** when a non-Noop provider is configured |
| **Adapter** — bridge to `go.opentelemetry.io/otel` + an exporter | [`ext/otel/`](https://github.com/panyam/mcpkit/blob/main/ext/otel/provider.go) (own `go.mod`) | **yes** | only if you import it |

Tracing is **off by default**. The default provider is `core.NoopTracerProvider{}` — `StartSpan` returns the input context unchanged plus a span whose methods do nothing, with zero allocation on the hot path. The trace middleware isn't even added to the chain unless you wire a real provider: the install is gated by a check that treats both `nil` and `NoopTracerProvider` as "disabled."

```go
// client/trace_middleware.go — the gate, mirrored on the server
func tracingEnabled(tp core.TracerProvider) bool {
    if tp == nil { return false }
    if _, isNoop := tp.(core.NoopTracerProvider); isNoop { return false }
    return true
}
```

So "tracing kicks in" the moment you pass a non-Noop provider to `server.WithTracerProvider(...)` / `client.WithTracerProvider(...)` — and not one instruction before. A server that never wires it pays nothing and never imports OpenTelemetry.

> [!IMPORTANT]
> The base `mcpkit` module has **no dependency on the OpenTelemetry SDK**. The SDK only arrives through `ext/otel/`, a separate `go.mod` — same dependency-isolation pattern as `ext/auth` / `ext/tasks` / `ext/ui`. Servers that don't trace don't pull ~10 transitive OTel deps.

## Q2 — Why a wire *standard* for tracing, and why does it ride `_meta`?

A trace spans **multiple processes**. For a span created in the server to attach *under* a span created in the client, the server needs the client span's identity (`trace-id` + `span-id`) handed across the process boundary in a format both sides agree on. That cross-process identity is exactly what [W3C Trace Context][w3c-tc] standardizes:

```
traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
             └┬┘ └──────────────┬───────────────┘ └──────┬───────┘ └┬┘
           version          trace-id (32 hex)      parent span-id   flags
```

Why a *standard* rather than something ad-hoc: **interoperability**. Before W3C Trace Context, every vendor had its own header (Zipkin B3, Jaeger `uber-trace-id`, Datadog). A request crossing two differently-instrumented systems lost its trace at the seam. W3C Trace Context is a W3C Recommendation — the lingua franca so a Go server, a Python client, and a Datadog-instrumented proxy all parse the same `traceparent`. **MCP is precisely this situation**: client and server are routinely different SDKs and languages, so SEP-414 adopts W3C Trace Context rather than inventing an MCP-specific format.

The MCP-specific twist is **where** the value rides. MCP isn't always HTTP (stdio, SSE, streamable HTTP), so there's no header on every message. SEP-414 carries the W3C value **inside the JSON-RPC `_meta` envelope** that every method already has — byte-identical to the HTTP header value, just relocated:

```jsonc
{"method": "tools/call", "params": {
  "name": "echo",
  "arguments": {"text": "hi"},
  "_meta": {
    "traceparent": "00-4bf9...4736-00f0...02b7-01",
    "tracestate": "vendor=opaque"
  }
}}
```

[SEP-2028](https://github.com/panyam/mcpkit/blob/main/server/trace_middleware.go) then *additionally* bridges the real HTTP `traceparent` header → `_meta` for the streamable transport, so a trace originating in an HTTP-layer proxy still lands on the span. In-band `_meta` always wins over the out-of-band HTTP header.

Two details in [`core/trace.go`](https://github.com/panyam/mcpkit/blob/main/core/trace.go) are worth a teaching beat:

- **The keys are un-namespaced** — `traceparent`, not `io.modelcontextprotocol/traceparent`. The `io.modelcontextprotocol/` prefix is reserved for MCP-*defined* fields; `traceparent` / `tracestate` are W3C-defined, so they keep their standard names.
- **mcpkit validates structure but treats IDs as opaque.** `ExtractTraceContext` enforces the version-00 form (length, lowercase hex, non-zero trace-id/span-id) but never parses the IDs — the adapter feeds the raw strings to OTel's propagator.

> [!IMPORTANT]
> **W3C "MUST NOT forward a `tracestate` you can't associate with a valid `traceparent`."** mcpkit honors this literally: a malformed `traceparent` yields a *zero* `TraceContext` — both fields empty — so a bad `traceparent` also drops the `tracestate`. See `ExtractTraceContext` / `isValidTraceparent`.

## Q3 — How do you wire it, and what is TracerProvider vs Tracer?

Three steps in your `main.go`, all in the adapter's quickstart:

```go
import (
    sdktrace "go.opentelemetry.io/otel/sdk/trace"
    "go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
    mcpotel "github.com/panyam/mcpkit/ext/otel"
    "github.com/panyam/mcpkit/server"
)

exp, _ := stdouttrace.New()                                      // 1. an exporter
otelTP := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))  // 2. an OTel TracerProvider
defer otelTP.Shutdown(ctx)                                       //    ← flushes batched spans

srv := server.NewServer(info, server.WithTracerProvider(mcpotel.NewProvider(otelTP)))  // 3. wrap + wire
```

The **TracerProvider vs Tracer** confusion is a vanilla OpenTelemetry distinction the adapter hides:

- A **`TracerProvider`** is the heavyweight, one-per-process object. It owns the exporter pipeline, the batch/span processors, and resource attributes. You configure it and you **must `Shutdown()` it** — that flush is what actually pushes batched spans to the exporter.
- A **`Tracer`** is a lightweight handle obtained via `provider.Tracer("name")`. The `Tracer` is what calls `.Start()` to create spans; its name is the "instrumentation library" label backends use to group spans.

mcpkit's own `core.TracerProvider` is deliberately *not* OTel's — it is a one-method interface (`StartSpan`). The `ext/otel` adapter bridges them: at construction it grabs **one** `Tracer` from your OTel TracerProvider and caches it, so `StartSpan` is a single `tracer.Start(...)` call with no per-request lookup.

```go
// ext/otel/provider.go
func NewProvider(otelTP oteltrace.TracerProvider, opts ...Option) *Provider {
    if otelTP == nil { panic("ext/otel: NewProvider called with nil TracerProvider") }
    // ...
    return &Provider{tracer: otelTP.Tracer(cfg.instrumentationName)} // one Tracer, cached
}
```

So the rule to teach: **you wire the OTel *TracerProvider* and own its lifecycle (`Shutdown`); the adapter manages the *Tracer* internally — you never touch a Tracer directly.**

> [!TIP]
> Forgetting `defer otelTP.Shutdown(ctx)` is the #1 "my spans never appear" bug. Spans are *batched*; without the shutdown flush they're still in the buffer when the process exits. This is OTel-SDK behavior, not an mcpkit quirk — but it bites everyone once.

## Q4 — What happens to one request on the server?

When tracing is enabled, `traceMiddleware` wraps every JSON-RPC dispatch. The sequence:

1. **Resolve the inbound trace context.** Prefer `params._meta.traceparent` (in-band); fall back to whatever the transport attached to `ctx` (the SEP-2028 HTTP-header bridge). In-band wins. Attach the result via `core.WithTraceContext(ctx, tc)` so handlers can read it through `ctx.TraceContext()`.
2. **Start the span** named after the JSON-RPC method (`tools/call`, `prompts/get`, …), with attributes.
3. **Run the handler inside the span**, plus wrap the session's notify/request funcs so outbound messages carry the active `_meta.traceparent`.
4. **End the span**, recording the outcome (`mcp.error.code` + `RecordError` on a JSON-RPC error; `mcp.tool.is_error="true"` on a `tools/call` result with `isError`).

The attributes set on the inbound span:

| Attribute | When | Source |
|-----------|------|--------|
| `mcp.method` | always | the JSON-RPC method |
| `mcp.session.id` | when a session exists | `core.GetSessionID(ctx)` |
| `mcp.tool.name` | `tools/call` only | best-effort parse of `params.name` |
| `mcp.error.code` | JSON-RPC error response | `resp.Error.Code` |
| `mcp.tool.is_error` | tool returned `isError` | the `ToolResult` |

The **inner-vs-outer** question is really about *two boundaries*, and the answer is "outermost" for both — for opposite-sounding but consistent reasons:

- **The inbound span is OUTERMOST in the user middleware chain.** Your rate-limit / auth / audit middleware runs *inside* the span, so their latency is part of the recorded span. Innermost would time only the handler and miss everything wrapping it.
- **The outbound `_meta` injection sits OUTSIDE any user `NotifyInterceptor` / `RequestInterceptor`.** Here the goal is that user interceptors observe the *same wire form the client will receive* — `traceparent` already stamped — which is what an audit log wants.

> [!NOTE]
> Mental model: the trace layer is the **outermost skin on both paths**. Inbound, "outermost" means "wraps the most" → captures everyone's time. Outbound, "outermost" means "stamps last" → everyone downstream sees the final bytes.

## Q5 — How does one trace stay connected across the wire?

This is the payoff. A `tools/call` whose handler calls `ctx.Sample()` (a [reverse call](./reverse-call.md)) produces a single trace threaded through four spans across two processes:

```
client.Call("tools/call")               → span A   ; injects _meta.traceparent = A
   └─ server traceMiddleware             → extracts A as parent, starts span B (child of A)
        └─ handler calls ctx.Sample()    → server injects _meta.traceparent = B on the
           outbound sampling/createMessage request
             └─ client inbound wrap      → extracts B as parent, starts span C (child of B)
```

The subtlety that explains the most code — and deserves a sidebar — is **whose** traceparent gets injected outbound. After `StartSpan`, the `ext/otel` adapter rewrites `ctx`'s `TraceContext` to the **new child span's** id, so the next outbound hop links to the child, not the original parent:

```go
// ext/otel/provider.go — tail of StartSpan
ctx, otelSpan := p.tracer.Start(ctx, name, startOpts...)
if childTC := spanContextToTraceContext(otelSpan.SpanContext()); !childTC.IsZero() {
    ctx = core.WithTraceContext(ctx, childTC)   // outbound _meta now carries the child's traceparent
}
```

The middleware then re-reads `ctx` *after* `StartSpan` and injects whatever it finds:

```go
// server/trace_middleware.go
ctx, span := tp.StartSpan(ctx, spanName, attrs...)
defer span.End()
outbound := core.TraceContextFromContext(ctx)   // child TC under the otel adapter; inbound TC under Noop
if !outbound.IsZero() {
    ctx = core.WrapSessionNotifyFunc(ctx, traceInjectNotifyWrap(outbound))
    ctx = core.WrapSessionRequestFunc(ctx, traceInjectRequestWrap(outbound))
}
```

So **parent-child precision lives in the adapter, not the middleware.** Under the `Noop` default the context is unchanged, so outbound `_meta` carries the *inbound* traceparent verbatim — still correlates into the same trace, just without the finer parent-child link at the next hop. Wire a real adapter and the links tighten for free.

> [!IMPORTANT]
> Injection **never clobbers** a caller-set `_meta.traceparent`. `InjectTraceContextIntoParams` only writes the key when it's absent — an explicit value a handler set wins. This is why both the server and client outbound wraps share the same `core` helper.

## Q6 — Client side: what's symmetric, what's not?

The design is mirror-image. Same primitives, opposite direction:

| | Server (P2) | Client (P3) |
|---|---|---|
| Outbound span | on server→client requests + notifications | on every `Client.Call` |
| Inbound span | every JSON-RPC dispatch (`traceMiddleware`) | server→client request dispatch (`traceInboundDispatch`) |
| Injects `_meta.traceparent` | outbound (sampling / elicit / roots / notifs) | outbound calls |
| Extracts `_meta.traceparent` | inbound `params._meta` | inbound server→client `params._meta` |
| Wire-up | `server.WithTracerProvider` | `client.WithTracerProvider` |
| Default | Noop (off) | Noop (off) |
| Install position | outermost in the chain | outermost in the chain |

Client-side span attributes mirror the server's, with `mcp.client.session.id` in place of `mcp.session.id`.

Two documented **asymmetries** (deliberate scope cuts, not bugs):

- **No SEP-2028 HTTP header on outbound client calls** — the client stamps `_meta`, not the HTTP `traceparent` header. Additive; deferred until HTTP-layer infra observability needs it.
- **No SpanKind** — `core.Span` has no `Client`/`Server` kind surface, so the OTel SDK records every mcpkit span as `Internal` in both directions. A typed attribute / kind surface may land when a real adapter needs it.

## Q7 — Where does tracing *not* reach yet, and is that a SEP-414 gap?

The five phases instrumented the **dispatch spine** — so every method, including tasks / apps / auth-gated calls, already gets one inbound span and wire propagation for free. What they did *not* do is instrument the work each surface does that **escapes** or **hides inside** that single span. Before treating that as a spec hole, separate three layers:

| Layer | Who owns it | Status |
|---|---|---|
| **Wire contract** — the `_meta` keys, format, precedence, SEP-2028 bridge | SEP-414 (normative — cross-SDK interop) | **complete** |
| **Local span richness** — span count, names, attributes, links | the SDK (latitude; invisible to the peer) | mcpkit-completeness work |
| **Adjacent-transport hops** — events bus, apps Bridge | mostly the SDK; the Apps Bridge *may* belong in the Apps spec | open |

So **SEP-414 is not incomplete.** Span richness can't be specified because it never crosses the wire — it's quality-of-implementation. The gaps are mcpkit's to close, surface by surface (tracked in the [SEP-414 P6 umbrella][p6]):

- **Auth** *(inside the span)* — JWKS / introspection / OAuth I/O wants sub-spans, and the resolved principal wants to be an attribute. Blocked on a `core` addition: there's no `SpanFromContext`, so inner code can't enrich the *active* span — only start a child or reach past the abstraction.
- **Tasks** *(escapes the span)* — task work runs after the create span ends; polls are separate traces. The fit is **span links**, not parentage — a second `core` contract gap (`StartSpan` is parent-from-ctx only).
- **Apps** *(boundary)* — a browser-originated trace connects to the backend only if the Bridge `postMessage` envelope relays `traceparent`. Same mechanism as the events bus, across JS↔Go. No new `core` API — `Inject`/`Extract` already work on any map.

Two observations are the through-line of the whole "across surfaces" story:

- The **two new propagation surfaces** (apps Bridge, events bus) need **no new contract** — `core.ExtractTraceContext` / `InjectTraceContext` operate on any `map[string]any`, transport-agnostic by design.
- The **two enrichment surfaces** (auth, tasks) reveal the only real P1 gaps — `SpanFromContext` and span links — neither of which the spine needed, which is exactly why P1 omitted them.

> [!NOTE]
> **Branch →** [SEP-414 P6 umbrella][p6] tracks all five (auth / tasks / apps + the two `core` contract gaps), with the events bus (#642 / #629) as the sibling case.

## End-state

After this page you can:

- Explain that tracing is **off by default** (Noop, zero-alloc, no OTel import in base), gated on `WithTracerProvider`, with the SDK isolated in `ext/otel/`'s own `go.mod`.
- Read a `_meta.traceparent` / `tracestate` pair, explain **why W3C Trace Context** (cross-SDK interop) and **why it rides `_meta`** (MCP isn't always HTTP), plus the SEP-2028 HTTP-header bridge and the un-namespaced keys.
- Distinguish the OTel **TracerProvider** (heavyweight, per-process, you `Shutdown()` it) from the **Tracer** (lightweight, cached in the adapter), and avoid the missing-flush "no spans" trap.
- Trace one request through the server middleware: extract → `WithTraceContext` → `StartSpan` → handler → outbound inject → `End` + outcome recording; and articulate the **inner-vs-outer** rule (outermost on both paths, for different reasons).
- Follow a **single trace across the wire** (client A → server B → client C), and explain why parent-child precision lives in the **adapter** (the child-TC rewrite) while the Noop path still correlates coarsely.
- State the **client/server symmetry** and the two documented asymmetries (no outbound HTTP header, no SpanKind).
- Separate the **three layers** (wire contract / local span richness / adjacent-transport hops) and explain why "tracing doesn't reach auth/tasks/apps yet" is mcpkit-completeness work, **not** a SEP-414 spec gap.

## Next to read

- **[Reverse-call mechanics](./reverse-call.md)** — the server→client requests (sampling / elicit / roots) that make outbound propagation matter; Q5's chained trace passes straight through this machinery.
- **[Extension mechanisms](./extension-mechanisms.md)** — revisit the `_meta`-field knob now that you've seen the canonical `_meta`-field-plus-middleware extension worked end to end.
- **[`experimental/ext/events/`](../../experimental/ext/events/README.md)** *(branch, target-shape)* — the cross-replica events `EventBus` is a **third** trace-propagation surface SEP-414's two hops don't cover: events cross replicas over Redis/Kafka, *not* the MCP wire, so the bus envelope must carry `traceparent` / `tracestate` to keep a trace connected from ingest-replica to egress-replica.
- **[SEP-414 P6 umbrella][p6]** *(tracking issue)* — tracing across the auth / tasks / apps surfaces + the two `core` contract gaps (`SpanFromContext`, span links); see Q7.

> [!WARNING]
> **Target-shape gap (extension).** SEP-414 propagates trace context over the MCP wire only (in-band `_meta`, plus the SEP-2028 HTTP-header bridge). Tracing the work each surface does beyond the dispatch span — auth sub-spans, async task links, the apps Bridge hop, the cross-replica events bus — is additive mcpkit-completeness work tracked in the [SEP-414 P6 umbrella][p6] (and #642 / #629 for events), not yet shipped. It reconciles by addition; the `core.TracerProvider` contract on this page is the foundation, with two small `core` additions (`SpanFromContext`, span links) called out in Q7.

<!-- ─────────────────────────────────────────────────────────────────────────
     Spec citation links.
       - W3C Trace Context is a stable W3C Recommendation (external, won't move).
       - SEP-414 / SEP-2028 are MCP spec proposals; the mcpkit-side rollout doc
         is the stable in-repo anchor. Find-replace the prefix if it moves.
     ───────────────────────────────────────────────────────────────────────── -->

[w3c-tc]:        https://www.w3.org/TR/trace-context/
[mcpkit-sep414]: https://github.com/panyam/mcpkit/blob/main/docs/SEP_414_OTEL.md
[p6]:            https://github.com/panyam/mcpkit/issues/663
