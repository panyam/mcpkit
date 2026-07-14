# experimental/ext/events

> **EXPERIMENTAL** — this package will change as the triggers-events-wg iterates on the spec.

Go library for adding [MCP Events](https://github.com/modelcontextprotocol/experimental-ext-triggers-events) to an mcpkit server. Implements the protocol methods (`events/list`, `events/poll`, `events/subscribe`, `events/unsubscribe`) and webhook delivery so you only write the event source.

Two source styles, pick whichever matches who owns storage:

| Style | When to use | Who owns the buffer | Fanout to push + webhook |
|-------|-------------|---------------------|--------------------------|
| **`YieldingSource`** (recommended) | The source pushes events at you (bot callback, HTTP handler, channel reader) | Library — bounded ring, typed `Recent(n)` / `ByCursor(c)` accessors | Library wires it (`SetEmitHook` via `Register`) |
| **`TypedSource`** | The source already owns its storage (DB, event log, external queue) | You — implement `Poll(cursor, limit)` | You — call `events.Emit` and `events.EmitToWebhooks` yourself |

## Cursored vs cursorless sources

Sources come in two flavors:

| | Cursored (default) | Cursorless |
|---|---|---|
| Construction | `events.NewYieldingSource[Data](def)` | `events.NewYieldingSource[Data](def, events.WithoutCursors())` |
| `Event.cursor` on the wire | string | `null` |
| Internal buffer | yes (events retained for replay) | no (events emitted and forgotten) |
| `events/poll` | returns events since the supplied cursor | always returns empty + `cursor: null` |
| `events/subscribe` with `cursor: null` | resolves to source's current head ("from now") | stays null |
| `EventDef.cursorless` advertised in `events/list` | `false` | `true` |

Use cursorless for ephemeral state where replay carries no value (typing indicators, presence, current readings). Use cursored for messages, alerts, audit logs, etc.

## Cursor provenance — single vs multi-writer (issue 833)

A cursored event's `cursor` orders it within its source; `events/poll` returns events strictly after a client's last-seen cursor, so gap-free resume across a reconnect needs cursors that are **monotone and unique across every writer of that source**. Who mints the cursor is pluggable:

| Provenance | Wire it with | Use when |
|---|---|---|
| **In-process** (default) | nothing — `InProcessCursors` per source | single writer per source. Zero deps, but each process counts from 1 and resets on restart, so N replicas writing one source **collide**. |
| **Shared counter** | `WithCursorProvider(events.NewInt64IncrCursors(incr, ""))` | multiple replicas write one source. `incr` is any `Incrementer` (a Redis `INCR`, a SQL sequence, etc.); shared across replicas it is cross-replica + restart-safe. A ready-made Redis adapter can live in `stores/redis`. |
| **Store-minted** | a buffer store that implements `CursorProvidingStore` (e.g. `gormstore.NewEventBufferStore(db, gormstore.WithProvideCursors())`) | you already share a durable buffer store. The store assigns the cursor from its own write sequence on `Append` — mcpkit mints nothing, one round trip. |

Precedence per source: an explicit `WithCursorProvider` wins; otherwise a cursor-providing store mints on write; otherwise `InProcessCursors`. Bring your own by implementing `events.CursorProvider` (`Next(ctx, source) (string, error)` — must return a monotone base-10 integer).

The default is unchanged: with no store and no provider, cursors count `1, 2, 3` per source exactly as before.

## Quickstart — `YieldingSource`

```go
import "github.com/panyam/mcpkit/experimental/ext/events"

// 1. Define your event payload type
type AlertData struct {
    Severity string `json:"severity" jsonschema:"enum=P1,enum=P2,enum=P3"`
    Service  string `json:"service"`
    Message  string `json:"message"`
}

// 2. Construct the source. PayloadSchema is auto-derived from the type param.
source, yield := events.NewYieldingSource[AlertData](events.EventDef{
    Name:        "incident.created",
    Description: "Fires when a new incident is created",
    Delivery:    []string{"push", "poll", "webhook"},
})

// 3. Register on the server. The library installs the push + webhook
//    fanout hook so yield(...) automatically broadcasts via SSE and POSTs
//    to webhook subscribers — you don't write any fanout code.
webhooks := events.NewWebhookRegistry()
events.Register(events.Config{
    Sources:  []events.EventSource{source},
    Webhooks: webhooks,
    Server:   srv,
})

// 4. Yield from wherever you produce events.
go alertWatcher(func(a AlertData) { _ = yield(a) })

// 5. Optional: read typed payloads back without re-unmarshaling. Used by
//    MCP resource handlers in the demos.
recent := source.Recent(50)              // []AlertData, oldest-first
one, ok := source.ByCursor("42")         // (AlertData, bool)
```

That's it. No `OnMessage` callback, no `MakeEvent`, no per-event fanout calls.

## Quickstart — `TypedSource`

Use when you already have a store you can serve cursor-based reads from:

```go
source := events.TypedSource[AlertData](events.EventDef{
    Name:        "incident.created",
    Description: "...",
    Delivery:    []string{"push", "poll", "webhook"},
}, func(cursor string, limit int) events.PollResult {
    rows := myDB.GetSince(cursor, limit)
    return events.PollResult{Events: toEvents(rows), Cursor: ..., Truncated: ...}
})

events.Register(events.Config{Sources: []events.EventSource{source}, Webhooks: wh, Server: srv})

// You wire fanout yourself:
myDB.OnInsert = func(row Row) {
    e := events.MakeEvent[AlertData]("incident.created", row.ID, row.Cursor, row.TS, toData(row))
    events.Emit(srv, e)
    events.EmitToWebhooks(wh, e)
}
```

## Protocol methods registered

| Method | Description |
|--------|-------------|
| `events/list` | Returns event definitions with auto-derived `payloadSchema` and `cursorless` flag |
| `events/poll` | Single-subscription polling, flat top-level response shape (`{events, cursor, hasMore, truncated, nextPollSeconds}`); error responses use spec error codes `-32011 NotFound` (`data.kind: "event"`) / `-32015 CallbackEndpointError` |
| `events/subscribe` | Webhook registration with HMAC secret + TTL + `refreshBefore`. `cursor: null` means "from now" — server resolves to source's current head |
| `events/unsubscribe` | Webhook removal by `(url, id)` or `(url, secret)` |

## Key types

```go
// Event is the wire-format envelope. Cursor is a pointer so cursorless
// sources serialize as `cursor: null`; HasCursor / CursorStr hide the
// pointer at call sites that don't care.
type Event struct {
    EventID   string          `json:"eventId"`
    Name      string          `json:"name"`
    Timestamp string          `json:"timestamp"`
    Data      json.RawMessage `json:"data"`
    Cursor    *string         `json:"cursor"`
}

func (e Event) HasCursor() bool   // true for cursored events
func (e Event) CursorStr() string // "" for cursorless

// EventSource — what TypedSource implements; YieldingSource also satisfies it.
type EventSource interface {
    Def() EventDef
    Poll(cursor string, limit int) PollResult
}

// PollResult includes Truncated — true when the server started delivery
// from a position later than the cursor the client supplied (events were
// skipped: cursor outside retention, maxAge floor advanced, or server
// replay ceiling). Clients SHOULD treat as a possible gap, persist the
// fresh cursor, and re-fetch authoritative state via tools if it matters.
type PollResult struct {
    Events    []Event
    Cursor    string
    Truncated bool
}
```

## Webhook delivery

- TTL-based soft state with automatic expiry (default 1h within the spec envelope [5min, 24h]; override via `WithWebhookTTL`; sub-minimum TTLs for tests/demos require `WithUnsafeWebhookTTLBypass`)
- Retry with exponential backoff on 5xx (no retry on 4xx)
- Basic SSRF validation on callback URLs
- Pluggable signature format (see below)
- Pluggable subscription storage via `WithWebhookStore`, quota counts via `WithQuotaStore`, subscription-id routing via the `SubscriptionIndexStore` interface on `Config.SubscriptionIndex`, output delivery via `Config.Emitter` (default in-memory / local in all four; Postgres + Redis backends land in issues 630 / 634; multi-replica composition is `NewCompositeEmitter(local, yourPeerFanout)` and receive-side reuses `HTTPSource`). See [`STORAGE_SEAMS.md`](STORAGE_SEAMS.md) for the convention every seam in this package follows.

```go
webhooks := events.NewWebhookRegistry(
    events.WithWebhookTTL(5 * time.Second),
    events.WithWebhookHeaderMode(events.StandardWebhooks),
)
```

### Webhook secret

Per spec, `delivery.secret` is **client-supplied and REQUIRED** on every `events/subscribe` call. The format is `whsec_` + base64 of 24-64 random bytes (Standard Webhooks profile). The server validates the format at subscribe time and rejects malformed values with `-32602 InvalidParams`. The server stores the value as-is and signs every delivery with it; the receiver verifies with the same value.

The server does NOT generate or echo the secret — the client owns it from end to end. This closes a third-party-target abuse where, with server-generated secrets, anyone could subscribe with `url=<victim>` and the server would happily POST signed events to the victim. With client-supplied, HMAC proves "the endpoint owner asked for this delivery" rather than just "this came from the MCP server".

Both client SDKs auto-generate a spec-conformant `whsec_` value when the application doesn't supply one:

```go
// Go: events.GenerateSecret() — also called automatically by Subscribe when opts.Secret == ""
secret := events.GenerateSecret()

# Python: generate_webhook_secret() — also called automatically by WebhookSubscription when secret == ""
from events_client import generate_webhook_secret
secret = generate_webhook_secret()
```

### Subscription identity and auth gate

Per spec §"Subscription Identity" L361-378, webhook subscribe and unsubscribe MUST require an authenticated principal; the registry keys subscriptions on the canonical tuple `(principal, delivery.url, name, params)` and derives a routing handle (`X-MCP-Subscription-Id`) over the same canonical bytes. Two distinct tenants subscribing to the same `(name, params, url)` get distinct subscriptions — cross-tenant isolation by construction.

The handler reads the principal via mcpkit's core auth abstraction (`core.MethodContext.AuthClaims().Subject`), so **any** auth provider that populates `core.Claims` works — JWT/OIDC via mcpkit's `ext/auth`, mTLS-derived principals, session cookies, custom validators, etc. Events depends on the `core.Claims` interface, **not** on `ext/auth` or any specific implementation. See "Auth + extension composition" below.

For demos and unauthenticated mcpkit servers that want to exercise webhook delivery without standing up an auth provider, `events.Config.UnsafeAnonymousPrincipal` is a deliberate spec-deviating escape hatch:

```go
events.Register(events.Config{
    Sources:                  sources,
    Webhooks:                 webhooks,
    Server:                   srv,
    UnsafeAnonymousPrincipal: "demo-user", // ONLY for demos — see DEPLOYMENT.md
})
```

When set, anonymous webhook subscribes are accepted under the configured principal. The server logs a startup warning so deployments using it know they're off-spec. **Production deployments leave this empty AND wire `server.WithAuth(validator)` so unauthenticated subscribe attempts hit the spec-mandated `-32012 Forbidden` rejection.**

### Auth + extension composition

Events depends only on the `core.Claims` interface, not on any specific auth implementation. The wiring layer (a server's `main.go`) chooses the auth provider:

```
ext/events  ──→  core.Claims (the abstract contract)
                       ↑
ext/auth    ──→  populates Claims via JWT/OIDC validation
                       ↑
your code   ──→  server.WithAuth(your favorite validator)
```

The Events implementation has zero compile-time dependency on `ext/auth`. You can:

- Use mcpkit's `ext/auth` for JWT/OIDC (what the demos do)
- Use mTLS — populate Claims from the cert subject
- Use session cookies — populate Claims from your session store
- Use no auth at all — set `UnsafeAnonymousPrincipal` for demos

This is the right composition shape for MCP extensions — extensions depend on stable core abstractions, not on each other.

### Header mode (`WebhookHeaderMode`)

Two on-the-wire signature formats. Default is `MCPHeaders`. Only the headers and signature scheme are configurable; the body shape (the `events.Event` envelope) is unchanged.

| Mode | Headers | HMAC base |
|---|---|---|
| `StandardWebhooks` (default) | `webhook-id`, `webhook-timestamp`, `webhook-signature: v1,<base64>` per [standardwebhooks.com](https://www.standardwebhooks.com/) | `HMAC(secret, msg_id + "." + ts + "." + body)` |
| `MCPHeaders` (opt-in) | `X-MCP-Signature: sha256=<hex>` + `X-MCP-Timestamp: <unix>` | `HMAC(secret, ts + "." + body)` |

Verification helpers ship for both:

```go
events.VerifyMCPSignature(body, secret, ts, sig)
events.VerifyStandardWebhooksSignature(body, secret, msgID, ts, sig)
```

The Python `events_client.py` receiver auto-detects which header set is on the inbound request and verifies accordingly — no client-side mode flag needed.

### Unsubscribe

Today: keyed on the canonical tuple `(principal, name, params, url)` per spec §"Subscription Identity" → "Key composition" L363. The derived id is NOT accepted as input on `events/unsubscribe` — callers resolve via the same tuple they used to subscribe.

```jsonc
{"id": "sub-1", "delivery": {"url": "https://..."}}
```

Registry method: `WebhookRegistry.Unregister(url, id)`.

## Client SDKs

Two officially-shipped clients live under `clients/`. Both implement the same TTL refresh + signature verification contract.

| Language | Path | Style |
|---|---|---|
| Go | [`clients/go/`](clients/go/) | Typed `Subscription` + `Receiver[Data]` (channel-based delivery) |
| Python | [`clients/python/events_client.py`](clients/python/events_client.py) | Class-based `WebhookSubscription` + CLI subcommands (`list`, `listen`, `webhook`, `poll`) |

### Python — auto-refresh

`events_client.py` ships a `WebhookSubscription` helper that refreshes the subscription at `0.5 × TTL` (configurable) and transparently re-subscribes if a refresh hits the "subscription not found" race near the TTL boundary. Use it via the helper class or the bundled `webhook` subcommand:

```bash
python3 clients/python/events_client.py webhook --event discord.message --port 9999 --refresh-factor 0.5
```

```python
from events_client import MCPSession, WebhookSubscription

session = MCPSession("http://localhost:8080/mcp")
session.initialize()

sub = WebhookSubscription(
    session,
    event_name="discord.message",
    callback_url="http://localhost:9999",
    secret="my-secret",
    refresh_factor=0.5,
    on_refresh=lambda: print("refreshed"),
    on_recover=lambda: print("recovered after subscription expired"),
)
sub.start()
# ... receive webhooks on :9999 ...
sub.stop()
```

Smoke-tested by `make test-ttl` in [`examples/events/discord/`](../../../examples/events/discord/).

### Go — typed delivery

`clients/go/` provides `Subscription` (subscribe + auto-refresh) plus `Receiver[Data]` (typed webhook receiver implementing `http.Handler`). Same lifecycle and recovery semantics as the Python helper, with decoded payloads on a channel.

```go
import (
    eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

recv := eventsclient.NewReceiver[AlertData]("")
hookSrv := httptest.NewServer(recv)
defer hookSrv.Close()
defer recv.Close()

sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
    EventName:   "alert.fired",
    CallbackURL: hookSrv.URL,
})
if err != nil { ... }
defer sub.Stop()
recv.SetSecret(sub.Secret())

for ev := range recv.Events() {
    fmt.Printf("alert: %+v\n", ev.Data)
}
```

## Deployment

For private-cloud / WAF-fronted deployments, see [`DEPLOYMENT.md`](DEPLOYMENT.md) — covers egress patterns, WAF allowlist guidance, SSRF guards, retry/backoff timing for proxy tuning, and TTL refresh as keepalive.

## Multi-replica deployments

`notifications/events/event` is one of five server-pushed notification surfaces that silently break at N>1 (multiple server replicas) without explicit Pattern B wiring. The events SDK's `YieldingSource` implements `server.NotificationRelayReceiver` so the receive side of Pattern B routes through its slot system on every replica — per-slot `EventDef.Match` runs the same as for a local yield, preserving tenant scoping and per-subscription filters.

The full architecture (Pattern B, `redisstore.Bus` for events, `redisstore.CapabilityBus` for capability + sub-shaped notifications, the `NotificationRouter` recipe, per-surface end-to-end flows) lives in [`docs/MULTI_REPLICA.md`](../../../docs/MULTI_REPLICA.md). The configuration recipe for events specifically:

```go
import (
    "github.com/panyam/mcpkit/experimental/ext/events"
    redisstore "github.com/panyam/mcpkit/experimental/ext/events/stores/redis"
)

eventsBus, _ := redisstore.NewBus(opts, mySource)   // mySource is the receiver
defer eventsBus.Close()
_ = eventsBus.Subscribe(ctx, "chat.message", "presence.changed")
go eventsBus.Run(ctx)

cfg.Emitter = eventsBus
events.Register(cfg)
```

Project-wide constraint that flags this: `CONSTRAINTS.md` § C5.

## Spec alignment

Based on Peter Alexander's design sketch (triggers-events-wg PR #1). Notable choices:

| Topic | Our approach |
|-------|-------------|
| **Cursors** | Opaque strings — store defines format. `YieldingSource` defaults to monotonic int64 |
| **`truncated`** | Spec field — server signal that delivery started later than the supplied cursor (events skipped: retention, maxAge floor, or replay ceiling) |
| **`nextPollSeconds`** | Per-subscription (follows spec schema); client SDK coalesces |
| **`events/stream`** | Deferred — push uses `Server.Broadcast` for now |
| **Typed contexts** | Handlers receive `core.MethodContext` (EmitLog, AuthClaims, etc.) |
| **Yield-style SDK** | `YieldingSource` (Casey + Peter, WG PR #1 line 609) — non-normative SDK ergonomic |

## Tracing across the events bus (SEP-414 P6, issue 683)

W3C Trace Context propagates across every gate in the event lifecycle so a yield on replica A and a downstream webhook delivery (or a poll-side replay on replica B) appear in Tempo as one stitched trace.

**Four gates, three carriers, one consistent rule.** The carriers are picked per gate by what's available at that boundary:

| Gate | Carrier | Where |
|---|---|---|
| 1. `yield(ctx, data)` | `event.Meta.traceparent` (persistent) + `ctx` (in-process) | `YieldingSource.yield` stamps `Meta` from `ctx`; emit hook receives the same `ctx` |
| 2. emit hook → `Emitter.Emit(ctx, event)` | `ctx` | `Register` passes the hook's `ctx` straight to the configured Emitter |
| 3. `WebhookRegistry.Deliver(ctx, event)` → outbound HTTP | HTTP `traceparent` header | `deliver()` extracts from `ctx` (preferred) or `event.Meta` (fallback for replayed events), stamps the header before `client.Do` |
| 4. `HTTPSource.serveInject` (receiving replica) | inbound HTTP header → `ctx` → `event.Meta` | The handler reads the `traceparent` header, builds `core.TraceContext`, attaches via `core.WithTraceContext`, and calls `s.yield(ctx, data)` — closes the round-trip |

**Caller-preserves rule.** If `SetMetaFunc` pre-stamps `event.Meta.traceparent`, the yield-time auto-injection is skipped — matches the same caller-wins semantic used by `core.InjectTraceContextIntoParams` (server outbound `_meta`) and the TS-side Apps Bridge relay (PR 702). The rule is uniform across every trace-context carrier in mcpkit.

**Why three carriers, not one?** `ctx` is gone the moment a goroutine exits or an HTTP request crosses the wire. `event.Meta` survives JSON serialization, persistence, and replay. HTTP headers survive the network hop. Each carrier is appropriate to its gate; together they keep the trace ID intact across every transition.

**End-to-end demo shape** (multi-replica, with webhook fanout to a peer):

```
yield(ctx, data) on replica A
└─ event.Meta.traceparent stamped
   └─ emit hook (ctx) → LocalEmitter → WebhookRegistry.Deliver(ctx, event)
      └─ HTTP POST to replica B with `traceparent` header
         └─ replica B HTTPSource.serveInject reads header
            └─ yield(ctx, data) on replica B
               └─ event.Meta.traceparent stamped again (same trace ID)
                  └─ downstream subscribers / webhooks continue the chain
```

A single TraceID stitches all of this in Grafana / Tempo.
