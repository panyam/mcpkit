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
| `events/poll` | Single-subscription polling, flat top-level response shape (`{events, cursor, hasMore, truncated, nextPollSeconds}`); error responses use spec error codes `-32011 EventNotFound` / `-32015 InvalidCallbackUrl` |
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

- TTL-based soft state with automatic expiry (default 60s, override via `WithWebhookTTL`)
- Retry with exponential backoff on 5xx (no retry on 4xx)
- Basic SSRF validation on callback URLs
- Pluggable signature format (see below)

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

### Subscription identity + auth gate (γ)

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

When set, anonymous webhook subscribes are accepted under the configured principal. The server logs a startup warning so deployments using it know they're off-spec. **Production deployments leave this empty AND wire `server.WithAuth(validator)` so unauthenticated subscribe attempts hit the spec-mandated `-32012 Unauthorized` rejection.**

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

Today: keyed on `(url, id)`. γ replaces this with the spec's `(principal, name, params, url)` tuple per the latest sketch.

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
