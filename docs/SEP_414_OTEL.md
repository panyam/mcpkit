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
- **P6 — tracing across surfaces.** Umbrella [issue 663][p6]. See below.

## P6 — tracing across surfaces (auth / tasks / apps)

P1–P5 instrumented the **dispatch spine**: every JSON-RPC method gets one
inbound span plus W3C wire propagation, for free. P6 extends tracing into
the work each surface does that the single dispatch span doesn't cover.

**Is this a SEP-414 gap? No.** Separate three layers:

| Layer | Owner | Status |
|---|---|---|
| Wire contract (the `_meta` keys, format, precedence, SEP-2028 bridge) | SEP-414 — normative, for cross-SDK interop | complete |
| Local span richness (count, names, attributes, links) | the SDK — latitude; invisible to the peer | mcpkit-completeness work (P6) |
| Adjacent-transport hops (events bus, apps Bridge) | mostly the SDK; the Apps Bridge *may* belong in the Apps spec | open |

Span richness never crosses the wire, so a SEP can't (and shouldn't)
mandate it. The one genuine spec question is the **apps Bridge**
(`postMessage`): if cross-SDK app-tracing interop is a goal, whether
`traceparent` rides the bridge envelope belongs in the **ext-ui / Apps
spec**, not SEP-414.

Two categories of "work the spine misses":

- **(a) escapes the span** — async / out-of-band on a non-MCP transport:
  tasks background execution ([#659][p6-tasks]), events cross-replica bus
  ([#642][caps] / [#629][bus]).
- **(b) inside the span, not broken out / enriched** — auth validation
  sub-spans + principal attributes ([#658][p6-auth]).

The two categories map cleanly onto the contract:

- **New propagation surfaces need no new contract** — `core.ExtractTraceContext`
  / `InjectTraceContext` already operate on any `map[string]any`, so the
  apps Bridge ([#660][p6-apps]) and the events bus reuse them verbatim.
- **Enrichment surfaces reveal the only two P1 gaps**, neither of which the
  spine needed: `core.SpanFromContext` (active-span accessor, [#661][p6-spanctx];
  unblocks auth attributes) and **span links** on `core.Span` ([#662][p6-links];
  unblocks task lifecycle).

### Active-span accessor — landed (issue 661)

The first contract gap is closed: `core.SpanFromContext(ctx) core.Span`
returns the currently-active mcpkit Span (or a no-op Span when none is
attached). Sibling helper `core.WithActiveSpan(ctx, span) Context` is what
TracerProvider adapters call after `StartSpan` to publish the span. The
in-tree `NoopTracerProvider` and the `ext/otel` adapter both follow the
pattern, so the contract holds regardless of which provider is wired.

The intended use is **decorating the dispatch span**, not nesting a child:

```go
// inside a middleware or handler, no ext/otel import needed:
span := core.SpanFromContext(ctx)
span.SetAttribute("mcp.auth.principal", claims.Subject)
span.SetAttribute("mcp.auth.method", "jwt")
```

Typed handler contexts gain a `Span()` accessor that delegates to
`SpanFromContext`:

```go
func myTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
    ctx.Span().SetAttribute("mcp.tool.cache.hit", "true")
    // ...
}
```

The accessor never returns nil — call sites that always-decorate work
correctly whether or not a TracerProvider is configured. Issue 658
(`ext/auth` attributes) and any future "enrich the active span" use case
consume this surface without crossing the `core/`-only boundary.

### Span links — landed (issue 662)

The second contract gap is closed: span links are now expressible
dep-free, with the OTel-aligned shape that includes per-link
attributes. Two entry points:

```go
// Option at start (call site knows all links upfront):
links := []core.Link{
    {TraceContext: tc1, Attributes: []core.Attribute{{Key: "link.kind", Value: "originated-from"}}},
    core.LinkFromTraceContext(tc2),
}
ctx, span := core.StartSpanLinked(tp, ctx, "task.execute", links, attrs...)

// Mid-flight (links discovered during span execution):
span := core.SpanFromContext(ctx)
span.AddLink(core.Link{TraceContext: tc, Attributes: ...})
```

`core.Link` mirrors the OpenTelemetry spec `Link` definition: a
TraceContext (the upstream identity, same shape carried on the MCP
wire via `_meta`) plus optional per-link attributes. Per-link
attributes are how observability backends render the link UI —
Jaeger / Tempo / Honeycomb all show `link.kind=...` semantically
rather than as generic span attributes.

**Capability widening pattern:** `core.LinkedTracerProvider` is a
sibling of `TracerProvider` that adds the `StartSpanLinked` method;
the `core.StartSpanLinked(tp, ...)` package-level helper type-asserts
and falls back to plain `StartSpan` when the provider doesn't
implement the wider interface — links silently dropped, span emitted.
This kept the base `TracerProvider` interface unchanged so non-tracing
test fakes and the default `NoopTracerProvider` didn't need any
churn. The `ext/otel` adapter implements both interfaces and maps
each `core.Link` to an OTel `trace.Link` via the existing
`traceContextToSpanContext` helper.

**Invalid links are silently dropped** — both at `StartSpanLinked`
(filtered before passing to the OTel SDK) and at `Span.AddLink`
(short-circuited inside the wrapper). Defensive call sites can build
link slices from raw inputs (e.g., `core.ExtractTraceContext` outputs
that might be zero) without pre-filtering. Calling `AddLink` after
`End` is a no-op, matching every other Span method's contract.

Unblocks issue 659 (`ext/tasks` task lifecycle linking) and the
detached edge of issue 664 (server outbound reverse-call spans linked
to the originating client request).

### `mcpotel.NewTracerProvider` helper — landed (issue 674)

Examples and surface integrations no longer have to import
`go.opentelemetry.io/otel/sdk/resource` + `.../semconv` just to set a
`service.name` on their TracerProvider. `mcpotel.NewTracerProvider`
is a thin functional-options wrapper around `sdktrace.NewTracerProvider`:

```go
otelTP := mcpotel.NewTracerProvider(exp,
    mcpotel.WithServiceName("my-server"),
    mcpotel.WithSyncer(),
)
mcpotel.NewProvider(otelTP)
```

Options today:

- `mcpotel.WithServiceName(name)` — bakes the value into an OTel
  `Resource` with `semconv.ServiceName(...)`. Empty name is a no-op
  so defensive callers can pass through config values without
  branching.
- `mcpotel.WithSyncer()` — switches the default Batcher to a sync
  span processor. Right for teaching demos and tests; production
  servers should stay on the batched default and handle SIGTERM with
  explicit `ForceFlush` + `Shutdown`.

`NewTracerProvider(nil)` panics — silently constructing a
TracerProvider that emits no spans loses signals without surfacing
the misconfig. Matches `NewProvider(nil)`'s fail-fast.

Future options (`WithDeploymentEnvironment`, `WithServiceVersion`,
`WithResource(*resource.Resource)` escape hatch) compose against the
same internal config — file when a consumer asks. The
`examples/common/otel/` sub-package already migrated to consume this
helper; future P6 surface examples (issues 658 / 659 / 660 / 664)
inherit the cleanup without any extra plumbing.

The pattern is identical across all five: each `ext/` surface optionally
accepts a `core.TracerProvider` and instruments its own work, depending on
the **`core` abstraction only** — never `ext/otel`. Same composition shape
`ext/events` uses for `core.Claims`. `Noop` / nil = zero overhead.

### `examples/common/otel.SetupTelemetry` — landed (issue 666; PR 684 + PR 689)

The example layer now ships a uniform, env-gated observability
surface so every example presents the same `--exporter` / `--otlp-endpoint`
flag pair regardless of which SEP it demos. `examples/common/otel/setup.go`:

```go
tp, shutdown, err := commonotel.SetupTelemetry(ctx,
    commonotel.WithExporter(*tel.Exporter),
    commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
    commonotel.WithServiceName("my-example"),
)
defer shutdown(context.Background())
common.RunServer(common.ServerConfig{TracerProvider: tp, ...})
```

The `EXPORTER` selector is four-valued:

| Value | Behavior |
|---|---|
| `""` (default) | `core.NoopTracerProvider{}` + no-op shutdown. Zero overhead. |
| `"stdout"` | `stdouttrace` exporter (sync). Demo / teaching mode. |
| `"otlp"` | TCP-probe the endpoint; on success, `otlptracegrpc` exporter. On failure: Noop **with warning log**. |
| `"auto"` | TCP-probe the endpoint; on success, `otlptracegrpc` exporter. On failure: Noop **silently** (operator opted into maybe-on-maybe-off semantics). |

Three load-bearing details:

- **TCP probe gates OTLP.** `otlptracegrpc.New` is lazy and returns a
  non-nil exporter even when the endpoint refuses; without the
  500ms TCP probe in `probeOTLPEndpoint`, the "dial-failure → Noop"
  contract wouldn't fire (failures would surface later on first
  Export, way past `make demo` startup).
- **Service-name convention.** Server side uses `<example-name>`;
  walkthrough (client) side uses `<example-name>-host`. Grafana's
  service filter distinguishes the two halves of each stitched trace.
- **`examples/otel/stdout/` is the documented carve-out.** Its
  `defaultExporter = "stdout"` because the demo's whole purpose is
  showing spans. Every other example defaults to `""`.

Client-side counterpart:

```go
tel := common.ExporterFromArgs()  // os.Args scan, mirrors common.ServerURL pattern
tp, shutdown, err := commonotel.SetupClientTelemetry(ctx,
    commonotel.WithExporter(*tel.Exporter),
    commonotel.WithOTLPEndpoint(*tel.OTLPEndpoint),
    commonotel.WithServiceName("my-example-host"),
)
defer shutdown(context.Background())
c := client.NewClient(url, info, client.WithTracerProvider(tp))
```

`SetupClientTelemetry` pre-sets
`WithInstrumentationName("github.com/panyam/mcpkit/client")` so spans
group correctly by library in OTel backends.

PR 684 adopted across 10 example servers (auth, elicitation,
file-inputs, fine-grained-auth, list-ttl, mrtr, skills, stateless,
tasks, tasks-v2); PR 689 followed with the same uniform wiring across
9 walkthroughs. Pattern documented in `examples/CONVENTIONS.md`
§Telemetry wiring.

### `ext/auth` JWT validator instrumentation — landed (issue 658; PR 694)

First P6 surface child to land on the spine. `JWTConfig.TracerProvider core.TracerProvider` opts the validator into instrumentation:

- `auth.jwks_lookup` sub-span wraps each `JWKSKeyStore.GetKeyByKid` call (with `mcp.auth.jwks.kid` attribute; records error on lookup failure). Surfaces both cache-hit and cache-miss latency.
- `mcp.auth.*` attributes on the active dispatch span (via `core.SpanFromContext`): `mcp.auth.method = "jwt"` stamped early (failure paths included), `mcp.auth.subject` / `issuer` / `scopes` / `cache_hit` set after claims extraction.

ext/auth depends on `core.TracerProvider` only for its own spans — no compile-time dependency on `ext/otel`. Nil and `core.NoopTracerProvider{}` both produce zero spans, zero attributes, zero allocation. Documented in `ext/auth/docs/DESIGN.md` § Tracing.

### `oneauth` v0.1.17 wiring — landed (PR 699)

Threads oneauth's own internal spans through ext/auth so an end-to-end auth trace shows the inside of the JWKS call too. Three layered helpers enable this without abstraction leakage:

- `ext/otel.Provider.OTelTracerProvider() trace.TracerProvider` — Provider stashes the underlying OTel TP and exposes it. Use when a downstream library needs the OTel SDK type directly.
- `examples/common/otel.UnderlyingOTelTP(tp core.TracerProvider) trace.TracerProvider` — type-asserts to `*mcpotel.Provider`; returns nil for Noop or non-mcpotel providers (oneauth's options no-op cleanly on nil).
- `ext/auth.JWTConfig.OneauthTracerProvider trace.TracerProvider` — when set, threaded via `keys.WithTracerProvider` on the JWKSKeyStore so oneauth's internal HTTP / parsing / signature-verify work emits spans on the same OTel pipeline.

End-to-end trace shape (when both `TracerProvider` AND `OneauthTracerProvider` are wired):

```
client.tools/call          (mcpkit/client)
└─ server.tools/call       (mcpkit/server, parent via _meta.traceparent)
   ├─ auth.jwks_lookup     (ext/auth)
   │  └─ oneauth.jwks.refresh
   │     └─ oneauth.jwks.key_lookup
   └─ tool handler work
```

Tracked in panyam/oneauth#254 (closed in oneauth v0.1.14; mcpkit ext/auth on v0.1.17 after a CI fix that swept all 8 oneauth-using modules in lock-step). Adopter pattern shipped in `examples/auth/common/setup.go` via `WithMCPTracerProvider` / `WithOneauthTracerProvider` variadic options on `Env.NewValidator`. Demo wires both in `examples/auth/main.go`.

### Apps Bridge trace context relay — landed (issue 660; PR 702)

Bidirectional W3C trace context propagation across the iframe ↔ host postMessage boundary so a browser-side trace (browser OTel SDK / RUM / hard-coded demo traceparent) stitches with the backend tool-call span. Off by default on both sides; adopters opt in independently per side.

**TS bridge (`ext/ui/assets/mcp-app-bridge.ts`):**

```ts
MCPApp.setTraceContextProvider(() => ({
  traceparent: "00-...-...-01",
  tracestate: "vendor=val",  // optional
}));
```

The provider is called before every outbound `request()` / `notify()`. Result merges into `params._meta.traceparent` / `_meta.tracestate`. Caller-set `_meta` wins (provider is a fallback). Provider throws are caught and logged; the request still sends without propagation. dep-free — no OTel JS dependency.

**Go AppHost (`ext/ui/app_host.go`):**

```go
host := ui.NewAppHost(c, bridge, ui.WithTracerProvider(tp))
```

`handleAppRequest` extracts inbound `params._meta.traceparent` via `core.ExtractTraceContextFromParams`, wraps the forward to the MCP server in an `apps.host.forward` span parented by the iframe's traceparent. The outbound `client.Call` preserves the iframe's `_meta.traceparent` on the wire (SEP-414 P3's caller-set-wins contract) so the server's P2 dispatch span stitches as a child.

End-to-end trace shape:

```
iframe traceparent          (browser-side OTel; demo: hard-coded random)
└─ apps.host.forward        (ext/ui AppHost)
   └─ server tools/call     (mcpkit server, SEP-414 P2)
      └─ tool handler work
```

Demo wiring in `examples/apps/vanilla/dice.html` (per-page-load random traceparent + `window.__MCPAPP_DEMO_TRACEPARENT__` for on-page TraceID copy). Host wiring in `examples/host/01-apphost/main.go`. Design doc: `docs/APPS_DESIGN.md` § Tracing across the Apps Bridge.

**Open spec question:** the Apps Bridge is a non-MCP transport; whether the relay belongs in the Apps spec for cross-SDK interop is open. mcpkit ships the relay; upstream note filed only if working-group interest surfaces.

### Events bus trace context relay — landed (issue 683; PR 712 + PR 714)

W3C Trace Context propagates across every gate in the events lifecycle so a yield on replica A and a downstream webhook delivery (or a poll-side replay on replica B) appear in Tempo as one stitched trace. Sister to the Apps Bridge relay — same "non-MCP propagation hop" pattern across a different boundary topology.

**Four gates, three carriers, one consistent rule.** Each gate picks the carrier appropriate to its boundary:

| Gate | Carrier | Where |
|---|---|---|
| 1. `yield(ctx, data)` | `event.Meta.traceparent` (persistent) + `ctx` (in-process) | `YieldingSource.yield` stamps `Meta` from `ctx`; emit hook receives the same `ctx` |
| 2. emit hook → `Emitter.Emit(ctx, event)` | `ctx` | `Register` passes the hook's `ctx` straight to the configured Emitter |
| 3. `WebhookRegistry.Deliver(ctx, event)` → outbound HTTP | HTTP `traceparent` header | `deliver()` extracts from `ctx` (preferred) or `event.Meta` (fallback for replayed events), stamps the header before `client.Do` |
| 4. `HTTPSource.serveInject` (receiving replica) | inbound HTTP header → `ctx` → `event.Meta` | Handler reads the `traceparent` header, builds `core.TraceContext`, attaches via `core.WithTraceContext`, calls `s.yield(ctx, data)` — closes the round-trip |

**Caller-preserves rule.** If `SetMetaFunc` pre-stamps `event.Meta.traceparent`, the yield-time auto-injection is skipped — uniform with `core.InjectTraceContextIntoParams` and the TS-side Apps Bridge relay.

**`events.webhook.deliver` span (PR 714).** `WebhookRegistry.WithWebhookTracerProvider(tp)` opts the registry into emitting a span around each retry loop. Attributes: `webhook.target.id`, `webhook.url`, `mcp.event.name`, `http.method`, `http.response.status_code`, `webhook.retry.attempts`. `RecordError` fires on retries-exhausted with the categorical bucket. Nil = Noop, zero overhead. **Live-verified** against the LGTM stack: span landed with all attributes set, 4 attempts counted, `STATUS_CODE_ERROR` on the failure path, duration matched the 0.5s + 1s + 2s backoff schedule exactly.

**`Server.Broadcast(ctx, ...)` (PR 714).** Signature widened to accept ctx so `events.Emit` can thread the trace context through the SSE push path. Underlying session broadcasters don't consume ctx yet — that plumbing is tracked separately on panyam/mcpkit#715. The events module already passes the trace context via `event.Meta` regardless, so push subscribers see it on the wire today; the ticket is about SSE-side span emission, not propagation.

End-to-end trace shape (multi-replica with webhook fanout):

```
yield(ctx, data) on replica A
└─ event.Meta.traceparent stamped
   └─ emit hook (ctx) → LocalEmitter → WebhookRegistry.Deliver(ctx, event)
      └─ events.webhook.deliver span (PR 714)
         └─ HTTP POST to replica B with traceparent header
            └─ replica B HTTPSource.serveInject reads header
               └─ yield(ctx, data) on replica B (same TraceID)
```

Demo wiring lives in `examples/events/whole-enchilada/event-server/main.go` (`events.WithWebhookTracerProvider(tp)` threaded against the existing `commonotel.SetupTelemetry` TP). Adopter sweep (PR 714) updated `examples/events/discord` and `examples/events/telegram` to the new `yield(ctx, ...)` / `SetMetaFunc(ctx, ...)` signatures. Cross-replica peer-fanout Emitter (Redis pubsub) tracked on panyam/mcpkit#634 + panyam/mcpkit#639 with concrete design comments added.

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
[p6]: https://github.com/panyam/mcpkit/issues/663
[p6-auth]: https://github.com/panyam/mcpkit/issues/658
[p6-tasks]: https://github.com/panyam/mcpkit/issues/659
[p6-apps]: https://github.com/panyam/mcpkit/issues/660
[p6-spanctx]: https://github.com/panyam/mcpkit/issues/661
[p6-links]: https://github.com/panyam/mcpkit/issues/662
[caps]: https://github.com/panyam/mcpkit/issues/642
[bus]: https://github.com/panyam/mcpkit/issues/629
