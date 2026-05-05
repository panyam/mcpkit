# Events

How a server tells a client "this domain thing happened" — events as a first-class extension, beyond the raw SSE-event-id replay that streamable HTTP gives you for free.

> **Kind:** root *(FAQ-style)* · **Prerequisites:** [bring-up](./bringup.md), [transport-mechanics](./transport-mechanics.md), [notifications](./notifications.md), [request-anatomy](./request-anatomy.md), [extension-mechanisms](./extension-mechanisms.md)
> **Reachable from:** [README](./README.md), [extension-mechanisms](./extension-mechanisms.md) Next-to-read + Q5 case-study row, [transport-mechanics](./transport-mechanics.md) "events as first-class" branch
> **Branches into:** [events SSRF deep dive](./events-ssrf.md) *(planned, leaf)*, [HMAC + Standard Webhooks deep dive](./events-hmac.md) *(planned, leaf)*, [subscription identity tuple proof](./events-identity.md) *(planned, leaf)*
> **Spec:** triggers-events WG design sketch (snapshot: `mcpcontribs/proposals/triggers-events-wg/spec-snapshots/design-sketch-2026-04-30.md`) · upstream: https://github.com/modelcontextprotocol/experimental-ext-triggers-events · **Code:** `experimental/ext/events/{events,yield,webhook,stream,control,identity,headers}.go`

## Prerequisites

- Live MCP session — capabilities negotiated, `initialized` sent. → If not, read [bring-up](./bringup.md).
- You can read JSON-RPC + SSE off the wire and follow the per-direction id model. → If not, read [transport mechanics](./transport-mechanics.md).
- Notifications model — events/stream's wire surface IS notifications, gated by capability. → If not, read [notifications](./notifications.md).
- You know what handler context, registries, and middleware are — the events handlers receive a `core.MethodContext` and notify via `ctx.Notify(...)`. → If not, read [per-request anatomy](./request-anatomy.md).
- Extension surface vocabulary — what `experimental.events` means as a capability, why this lives in `experimental/ext/`, what graduation looks like. → If not, read [extension mechanisms](./extension-mechanisms.md).

## Context

[Extension mechanisms](./extension-mechanisms.md) classified events at one row of its case-study table: "experimental, target-shape, `experimental.events` capability." This page opens that row up. The interesting questions are: how does events use the four extension knobs (Q1)? what does a client *do* with events — three delivery modes, pick by use case (Q2)? what stays stable across modes — subscription identity, source abstraction (Q3-Q4)? and what do the two delivery loops actually look like end-to-end (Q5-Q6)? Plus how upstream failures bubble out as first-class signals (Q7).

We are NOT re-explaining what `experimental.<name>` means, how the SEP process works, or why extensions get their own `go.mod`. That all lives in extension-mechanisms.md. This page assumes you know the vocabulary; it shows the worked example.

> [!NOTE]
> **Spec is a living draft.** The triggers-events WG iterates on the design sketch in the open. Section names and line numbers in this page reference the 2026-04-30 snapshot in mcpcontribs; if the upstream sketch has moved on, treat the snapshot as the authoritative version-of-record for what mcpkit's `experimental/ext/events` was built against.

## Q1 — How does events dial the four extension knobs?

Per [extension-mechanisms Q1](./extension-mechanisms.md#q1--what-counts-as-an-extension-in-mcp), every MCP extension picks which of four knobs to turn: **method namespace**, **capability flags**, **notification methods**, **`_meta` fields**. Events turns *all four*.

| Knob | What events adds | Code |
|------|------------------|------|
| **Method namespace** | `events/list`, `events/poll`, `events/stream`, `events/subscribe`, `events/unsubscribe` — five methods under the `events/` prefix | `experimental/ext/events/events.go` (registerList, registerPoll, registerSubscribe, registerUnsubscribe), `stream.go` (registerStream) |
| **Capability flag** | `experimental.events` declared by the server. Per the experimental-namespace contract from [extension-mechanisms Q2](./extension-mechanisms.md#q2--how-does-a-new-capability-get-into-the-protocol), receivers MUST ignore unrecognized experimentals; clients that do recognize it can call the methods above. | server's `initialize` response `capabilities.experimental` |
| **Notification methods** | Five push frames (`notifications/events/active`, `…/event`, `…/heartbeat`, `…/error`, `…/terminated`) ride the events/stream POST per Q5 below. Two webhook control envelopes (`type:gap`, `type:terminated`) ride outbound HTTP per Q6. | `stream.go` (notification frames), `control.go` (control envelopes) |
| **`_meta` field** | Optional `_meta` on every `Event` envelope (per-occurrence metadata) and on every `EventDef` (per-event-type metadata). Same convention as `_meta` on Tool / Resource / Prompt in base MCP — opaque, app-defined. | `events.go` `Event.Meta`, `EventDef.Meta` (spec follow-on commit d4faef9, 2026-05-01) |

### Events versus notifications

If events ship over notification frames (Q5), why aren't they just notifications?

| | Notification | Event |
|---|---|---|
| **Originates in** | MCP session state — the session itself changed | Domain logic — something happened in the world the server represents |
| **Examples** | `notifications/tools/list_changed`, `notifications/cancelled`, `notifications/progress` | "Discord message arrived", "incident filed", "telegram typing indicator" |
| **Identity** | Method name; payload is a hint or pairing key | Server-assigned `eventId`; payload is the domain object |
| **Replayable** | No — refetch is the protocol ([notifications Q2](./notifications.md#q2--how-does-the-server-tell-the-client-its-tools-list-changed)) | Yes (when cursored) — a buffered ring + cursor lets the client backfill |
| **Subscription needed?** | Capability gate at bring-up; no per-call setup | `events/subscribe` (webhook) or `events/stream` (push) — explicit |
| **Survives reconnect?** | Cache invalidation does | Cursor + replay does, up to retention/maxAge |

Events ride the **notifications surface** (knob #3) because that's the existing wire-level fire-and-forget channel — but events are a domain abstraction *built on top of* notifications, not a kind of notification. Q5 makes this concrete: a single events/stream call wraps five distinct notification methods plus a typed final response.

## Q2 — Three delivery modes: poll, push, webhook — which to use when?

All three modes are method-namespace extensions per Q1; what differs is the conversation shape. Pick by who initiates, what network reachability you have, and how much state you can hold.

| Mode | Method | Who initiates | Wire shape | Reachability needed | Latency | Statefulness |
|------|--------|---------------|------------|---------------------|---------|--------------|
| **Poll** | `events/poll` | Client (each call) | One-shot JSON-RPC request; response carries `events[]`, fresh `cursor`, `nextPollSeconds` hint | None beyond MCP transport | Bounded by poll interval | Client persists `cursor`; server is idempotent on re-poll |
| **Push** | `events/stream` | Client (one long-lived call) | Long-lived JSON-RPC request returning SSE; events arrive as `notifications/events/event` frames; final empty `StreamEventsResult` on close | Client must hold the request open (HTTP) or the pipe (stdio) | Server-push; bounded by handler latency | Server holds per-call state for the open stream |
| **Webhook** | `events/subscribe` | Client (subscribe), Server (delivery) | Subscribe is one-shot JSON-RPC with TTL; deliveries are HMAC-signed POSTs from server to a callback URL | **Server must be able to dial the client's callback URL** | Server-push; bounded by webhook handler retries | Server holds subscription registry; client refreshes by-tuple before TTL expiry |

The picking rule:

- **Pure remote, low-latency, client-can-stay-online** → push. The client SDK opens one events/stream and the events flow until either side closes. Cheapest for both sides on a hot path.
- **Polling fits the workload** (rare events, batch processing, audit-log-style backfill) → poll. No long-lived call to manage; client just remembers the cursor.
- **Client cannot stay online but has a public callback URL** (third-party apps, automations, integrations whose process restarts often) → webhook. The server delivers when there's something to deliver; the client just needs to handle the POST.

> [!IMPORTANT]
> **Webhook reachability flips the topology.** Push and poll work over the same MCP transport the session was bring-up'd on — the server only ever responds to client-initiated requests. Webhook is the one mode where the *server* dials *out* to a URL the client supplies. That's why webhook is the only mode that ships with an SSRF guard (Q6) and an authentication requirement on subscribe (Q3): the URL is server-controllable input, and a misconfigured server is a confused-deputy waiting to happen.

> [!NOTE]
> The three modes are not mutually exclusive per event source. The same `EventSource` can serve poll, push, and webhook simultaneously — `EventDef.Delivery` advertises which subset is offered. The library wires fanout once: a single `yield(data)` call inside the source goroutine reaches every push subscriber AND every webhook target AND becomes available to the next `events/poll`.

## Q3 — What identifies a subscription?

Per spec §"Subscription Identity" → "Key composition" L363, a webhook subscription is identified by the **canonical tuple**:

```
(principal, delivery.url, name, params)
```

where `principal` is the authenticated subject (`claims.Subject`), `delivery.url` is the callback URL, `name` is the event-type name, and `params` is the canonical-JSON encoding of the subscription params object (sorted keys for stability). The server derives a routing handle:

```
id = "sub_" + base64(SHA256(canonical)[:16])     // experimental/ext/events/identity.go
```

…and surfaces it on every delivery POST as `X-MCP-Subscription-Id`. The id is **non-load-bearing for security** — knowing another tenant's id grants no operations, because every call resolves on the canonical tuple, not on the id.

> [!IMPORTANT]
> **Three rules fall out of the tuple immediately.** Each is enforced in `experimental/ext/events/events.go`:
>
> 1. **No client-supplied id.** A subscribe request that includes an `id` field is rejected with `-32602 InvalidParams`. The id is server-derived; accepting one would let clients alias subscriptions and break tenant isolation. (`registerSubscribe`, the `req.ID != ""` guard.)
> 2. **Authentication required on subscribe and unsubscribe.** Without `claims.Subject` the principal is undefined and the canonical tuple is uncomputable; the handler returns `-32012 Unauthorized`. The `UnsafeAnonymousPrincipal` config field is a deliberate spec deviation for demos — gated by an `Unsafe` prefix and a startup warning. (`resolvePrincipal`.)
> 3. **Secret is client-supplied and required.** `delivery.secret` must be `whsec_<base64 of 24-64 random bytes>` per Standard Webhooks. Server-generated secrets would let anyone subscribe with `url=<victim>` and have the server happily POST signed events to the victim — HMAC would prove "the MCP server sent this," not "the URL owner asked for it." Client-supplied flips that. (`validateClientSecret`.)

### Worked example: refresh vs. distinct subscription

Two subscribes with **identical** canonical bytes → same subscription, TTL refreshed in place:

```jsonc
// Call 1
{"method": "events/subscribe", "params": {
  "name": "discord.message",
  "params": {"channel": "alerts"},
  "delivery": {"mode": "webhook", "url": "https://hook.example/recv", "secret": "whsec_AAAA..."}
}}
// → response.id = "sub_xR9vK..."

// Call 2 (a few seconds later, same caller)
{"method": "events/subscribe", "params": { /* identical to Call 1 */ }}
// → response.id = "sub_xR9vK..."   ← same id, refreshed expiry
```

Two subscribes with **different params** (or different url, name, or principal) → distinct subscriptions:

```jsonc
// Call 3 — params differ
{"method": "events/subscribe", "params": {
  "name": "discord.message",
  "params": {"channel": "general"},        // ← different
  "delivery": {"mode": "webhook", "url": "https://hook.example/recv", "secret": "whsec_BBBB..."}
}}
// → response.id = "sub_zP4cM..."   ← different id
```

`webhooks.Register(canonicalKey, derivedID, ...)` is keyed on `string(canonicalKey)`; second call with the same key updates expiry + secret in place, second call with a different key creates a fresh entry. Cross-tenant isolation is by construction — different `principal` → different canonical bytes → different id.

> [!NOTE]
> **Branch →** [subscription identity tuple proof](./events-identity.md) *(planned, leaf)* — formal walk-through of why the four-tuple is necessary and sufficient: the cross-tenant isolation argument, the secret-rotation flow under multi-signature, and what changes if a deployment maps multiple OAuth principals to one subject.

## Q4 — What's a source?

A **source** is the thing that produces events. Two abstractions in `experimental/ext/events/`:

| | `YieldingSource[Data]` (recommended) | `TypedSource[Data]` |
|---|---|---|
| **Who owns the buffer** | Library — bounded ring, default 1000 events | Caller — your DB, event log, external queue |
| **Construction** | `events.NewYieldingSource[Data](def)` returns `(*source, yield func(Data) error)` | `events.TypedSource[Data](def, poll, latest)` |
| **How events get in** | Call `yield(data)` from wherever you produce events (bot callback, channel reader, HTTP handler) | Server calls your `Poll(cursor, limit)` / `Latest()` callbacks |
| **Push fanout** | Library — `yield()` automatically fans out to push subscribers + webhook targets via the `SetEmitHook` wiring in `events.Register` | You — call `events.Emit(srv, e)` and `events.EmitToWebhooks(wh, e)` from your write path |
| **Cursorless option** | `events.WithoutCursors()` — events emit with `cursor: null`, no buffer, poll always empty | Return `""` from `Latest()` and the wire layer handles the rest |
| **Code** | `yield.go` | `events.go` `TypedSource` + `typedSource` struct |

Pick `YieldingSource` when the source pushes at the library; pick `TypedSource` when the source already owns its storage and prefers to be polled.

### Cursored versus cursorless

`EventDef.Cursorless` is advertised on `events/list` so clients plan accordingly:

| | Cursored (default) | Cursorless |
|---|---|---|
| `Event.cursor` on the wire | string (monotonic int as default in `YieldingSource`) | `null` |
| Internal buffer | yes — `WithMaxSize(N)` caps the ring | no — events emitted and forgotten |
| `events/poll` | returns events since the supplied cursor | always returns empty + `cursor: null` |
| `events/subscribe` with `cursor: null` | resolves to `source.Latest()` ("from now") | stays null |
| Push (`events/stream`) | works; events carry their cursor; replay possible via `Recent(n)` / `ByCursor(c)` | works; events carry `cursor: null` |
| When to pick | messages, alerts, audit logs — anything where replay matters | typing indicators, presence, current readings — anything ephemeral where replay is meaningless |

### Worked example

```go
// One source, one yield function.
source, yield := events.NewYieldingSource[AlertData](events.EventDef{
    Name:        "alert.fired",
    Description: "Fires when a new alert is triggered",
    Delivery:    []string{"push", "poll", "webhook"},
})

// Register it. The library installs the push + webhook fanout hook.
events.Register(events.Config{
    Sources:  []events.EventSource{source},
    Webhooks: webhooks,
    Server:   srv,
})

// Yield from your domain code. One call → live push subscribers, webhook
// targets, and the next events/poll all see it.
go alertWatcher(func(a AlertData) { _ = yield(a) })
```

`yield.go`'s `yield()` does, in order: marshal Data → assign monotonic cursor → append to ring (cursored only) → fanout to live `Subscribe()` channels under lock (drop-with-truncated-flag on a full subscriber buffer) → call the registered `emitHook` (which Emits to push and to webhooks). The author writes no fanout code.

## Q5 — Push delivery walkthrough: what does `events/stream` look like on the wire?

These are the notifications surface from the four-knob table (Q1); capability gate is `experimental.events`. The shape is "long-lived JSON-RPC POST returning SSE" — same pattern as a `tools/call` whose response upgrades to SSE for progress (see [transport-mechanics worked example](./transport-mechanics.md#worked-example-a-tool-call-with-progress-plus-an-unrelated-push)) — except the SSE stream stays open as long as the subscription is live, the events are domain-scoped, and the final response is an empty typed `StreamEventsResult`.

**Setup assumed.** Session `abc123` is live; bring-up negotiated `experimental.events` on the server side; the server has registered an `alert.fired` source via `events.Register`.

**Step 1 — client opens the stream.** One POST, one JSON-RPC request:

```http
POST /mcp HTTP/1.1                                   ← HTTP request #N (events/stream POST)
Mcp-Session-Id: abc123
Accept: application/json, text/event-stream

{"jsonrpc":"2.0","id":42,"method":"events/stream","params":{
  "name":"alert.fired",
  "cursor":null
}}
```

**Step 2 — server upgrades to SSE and emits the confirmation frame** (`notifications/events/active`, spec §"Push-Based Delivery" → "Request: events/stream" L240). `requestId: 42` echoes the originating request id so a stdio client can demux when push and other traffic interleave on the same pipe; `cursor` resolves `null` → `source.Latest()`:

```http
HTTP/1.1 200 OK                                      ← still HTTP request #N
Content-Type: text/event-stream
Mcp-Session-Id: abc123

id: 1
data: {"jsonrpc":"2.0","method":"notifications/events/active","params":{
  "requestId":42,"cursor":"137"
}}
```

**Step 3 — events arrive.** Each `yield()` in the source becomes one SSE event carrying `notifications/events/event` (spec L243-271):

```http
id: 2
data: {"jsonrpc":"2.0","method":"notifications/events/event","params":{
  "requestId":42,
  "eventId":"evt_138","name":"alert.fired","timestamp":"2026-05-04T10:42:00Z",
  "data":{"severity":"P1","service":"checkout","message":"5xx spike"},
  "cursor":"138"
}}

id: 3
data: {"jsonrpc":"2.0","method":"notifications/events/event","params":{
  "requestId":42,
  "eventId":"evt_139","name":"alert.fired","timestamp":"2026-05-04T10:42:07Z",
  "data":{...}, "cursor":"139"
}}
```

**Step 4 — heartbeat during quiet periods** (`notifications/events/heartbeat`, spec L294, default every 30s; `Config.StreamHeartbeatInterval` overrides). Cursor carries the source's *current* head so the client's persisted cursor advances even with no event traffic — useful for clients that want to see the watermark move:

```http
id: 4
data: {"jsonrpc":"2.0","method":"notifications/events/heartbeat","params":{
  "requestId":42,"cursor":"139"
}}
```

**Step 5 — close.** When the client disconnects (or `notifications/cancelled` arrives over stdio), the handler returns the typed final frame (`StreamEventsResult{Meta: {}}`, spec L293):

```http
id: 5
data: {"jsonrpc":"2.0","id":42,"result":{"_meta":{}}}
```

Stream closes. HTTP request #N is now complete.

**Things to notice:**

- **Five distinct notification methods, one stream.** active (open), event (each delivery), heartbeat (idle), error (transient — Q7), terminated (terminal — Q7). Plus the typed result frame on close. The wire shape in `stream.go` `registerStream` is exactly this select loop: `evCh / ticker.C / ctx.Done`.
- **`requestId` echo on every notification.** The notifications carry the originating events/stream request id in their params. On stdio (one pipe, multiplexed traffic) this is how a client demuxes events for *this* stream from notifications for some other in-flight call. On streamable HTTP, the SSE upgrade scopes the notifications to the POST already, but the field stays for stdio symmetry — same wire shape both transports.
- **Cursor flows through the event payload.** Unlike `notifications/progress` where the pairing key is `progressToken` in `_meta`, events carry `cursor` as a top-level field on the notification params. Persist it client-side; pass it back on reconnect to replay missed events (cursored sources only).
- **`Truncated` is a back-pressure signal.** If `yield()` finds a subscriber's channel full, it drops the event for that subscriber and sets `pendingTruncated`. The next successful send carries `truncated:true` on a fresh `notifications/events/active` frame (spec L285) before the resumed event — the client knows it missed events and can re-fetch authoritative state if it cares. Riding the marker on the next event (rather than a separate frame) keeps channel order trivially correct under any buffer size; see `yield.go` `SubscriberEvent` discriminator commentary.

## Q6 — Webhook delivery walkthrough: HMAC, retries, suspend, control envelopes

Per the [extension-mechanisms Q1](./extension-mechanisms.md#q1--what-counts-as-an-extension-in-mcp) styles table, webhook delivery is *not* a method-namespace extension at the wire layer — `events/subscribe` is, but the deliveries themselves are outbound HTTP-with-HMAC. That makes webhook-the-delivery-loop a closer analog of the **bring-up extension** style (auth's WWW-Authenticate / OAuth dance): it extends a layer below MCP, not the JSON-RPC message exchange.

The subscribe call (Q3) registers `(canonicalKey, derivedID, url, secret, ttl)` in `WebhookRegistry`. After that, every `yield()` in the source fans out to `Deliver(event)` which fires one `deliver(target)` goroutine per non-expired non-suspended target.

**The signed POST** (Standard Webhooks scheme, spec §"Webhook Event Delivery" L390+L431, default `WithWebhookHeaderMode(StandardWebhooks)`):

```http
POST /recv HTTP/1.1                                  ← server → callback URL
Host: hook.example
Content-Type: application/json
webhook-id: evt_138                                  ← stable across retries; receiver dedups on this
webhook-timestamp: 1714814520
webhook-signature: v1,5HxN...base64...               ← HMAC-SHA256(secret, id + "." + ts + "." + body)
X-MCP-Subscription-Id: sub_xR9vK...                  ← MCP-specific; lets receiver pick the right secret

{"eventId":"evt_138","name":"alert.fired","timestamp":"2026-05-04T10:42:00Z",
 "data":{"severity":"P1","service":"checkout","message":"5xx spike"},"cursor":"138"}
```

Receiver verifies signature → looks up secret by `X-MCP-Subscription-Id` → checks `webhook-timestamp` is not stale (>5 min old per spec L431) → dedups on `webhook-id` → processes.

### The hardened delivery loop

`webhook.go` `deliver()` is short but each guard is load-bearing:

| Guard | What | Why | Code |
|-------|------|-----|------|
| **SSRF — dial-time** | `net.Dialer.Control` callback rejects loopback, RFC1918 private, link-local (incl. AWS metadata), IPv6 ULA, multicast, broadcast, IPv4-mapped forms of all of the above | DNS rebinding: a hostname resolved at subscribe-time can resolve elsewhere at delivery-time. Dial-time check is TOCTOU-safe; the address passed to `Control` is exactly the one `connect(2)` will use. Per spec §"Webhook Security" → "SSRF prevention" L464. | `webhook.go` `dialContext`, `isBlockedIP` |
| **No redirect-following** | `http.Client.CheckRedirect` returns `ErrUseLastResponse` | A receiver returning 3xx to an internal address would otherwise bypass the dial-time guard via Go's redirect chain. Treat 3xx as terminal `http_3xx_redirect`. | `NewWebhookRegistry` |
| **Body cap** | 256 KiB default (`WithWebhookMaxBodyBytes`); REJECT mode, not TRUNCATE | Truncation would corrupt the HMAC signature and silently drop event content. Retrying won't shrink the body — terminal for the event. Per spec L487. | `Deliver()` `len(body) > r.maxBodyBytes` |
| **413 non-retryable** | `StatusRequestEntityTooLarge` short-circuits the retry loop | Receiver rejects our payload size; retrying won't change that. | `deliver()` switch |
| **5xx retry, exponential backoff** | 4 attempts (1 initial + 3 retries), 500ms → 1s → 2s → 5s cap | Standard webhook convention; matches Stripe / GitHub / Standard Webhooks spec. | `deliver()` `for attempt := 0; ...` |
| **Suspend after N consecutive failures** | Default 5 failures within a 10-minute sliding window flips `Status.Active = false`; suspended targets are excluded from `Targets()` until refresh | A dead receiver shouldn't keep getting retry traffic forever. Per spec L413+L460 ("after repeated failures the server SHOULD set active: false"). | `recordDeliveryFailure`, `Targets()` |
| **Auto-PostTerminated on suspend transition** | On the `true → false` transition, automatically POST a `{type:terminated}` control envelope (Q7 below) so the receiver learns the subscription died courtesy-style | Receiver may otherwise discover via a polled refresh — auto-post is a hint that the next refresh is needed. | `recordDeliveryFailure` ζ-7.3 block |

> [!IMPORTANT]
> **The dial-time SSRF guard runs on every connect, including retries and redirect-target dials.** A subscribe-time URL check (`ValidateWebhookURL`) catches obvious mistakes — bad scheme, literal `localhost` — but is not the load-bearing protection. Only the dialer's `Control` callback is TOCTOU-safe under DNS rebinding. The `WithWebhookAllowPrivateNetworks(true)` option bypasses both for demos against local httptest servers; **never enable it in production**.

> [!NOTE]
> **Branch →** [events SSRF deep dive](./events-ssrf.md) *(planned, leaf)* — full IP blocklist matrix with worked CIDR examples, the dial-time vs subscribe-time decomposition argument, and a DNS-rebinding attack walkthrough showing why the subscribe-time check alone fails.

> [!NOTE]
> **Branch →** [HMAC + Standard Webhooks deep dive](./events-hmac.md) *(planned, leaf)* — `webhook-id` semantics across event vs control deliveries, the multi-signature secret-rotation grace window, the `MCPHeaders` opt-in mode, and full receiver verification examples in Go and Python.

### Control envelopes — non-event webhook bodies

Two cases break the "every POST body is an event" pattern (spec §"Non-event webhook bodies" L415-423):

| Envelope | Purpose | When emitted | webhook-id format | Removes registry entry? |
|----------|---------|--------------|-------------------|--------------------------|
| `{type:"gap", cursor:"<fresh>"}` | Tell the receiver to reset its persisted cursor — a gap was detected (yield queue overflowed, retention boundary crossed) | Server-initiated when the source detects it can't backfill from the receiver's last-known position | `msg_gap_<random>` | No |
| `{type:"terminated", error:{code,message}}` | The subscription has ended (auth revoked, source terminated, suspend-transition courtesy) | Manual `PostTerminated`, OR auto-emitted by `postTerminatedSilent` on suspend transition | `msg_terminated_<random>` | `PostTerminated` removes; `postTerminatedSilent` does NOT (target stays observable as `Active=false` so refresh-reactivation still works) |

Same Standard Webhooks signature scheme as event deliveries; same `X-MCP-Subscription-Id` header. The `webhook-id` prefix lets receivers distinguish control from event in their dedup table.

### `deliveryStatus` on subscribe refresh

Per spec §"Webhook Delivery Status" L425-460, `events/subscribe` refresh responses carry a `deliveryStatus` block when the target has prior delivery attempts:

```jsonc
{
  "id": "sub_xR9vK...",
  "cursor": "139",
  "refreshBefore": "2026-05-04T11:00:00Z",
  "deliveryStatus": {
    "active": true,                          // false after suspend; refresh reactivates
    "lastDeliveryAt": "2026-05-04T10:42:00Z",
    "lastError": "http_5xx",                 // categorical bucket; spec forbids raw response content
    "failedSince": "2026-05-04T10:30:00Z"
  }
}
```

> [!IMPORTANT]
> **`lastError` is a closed categorical set** (`connection_refused`, `timeout`, `tls_error`, `http_3xx_redirect`, `http_4xx`, `http_5xx`, `challenge_failed`). The spec explicitly forbids raw response bodies, headers, or status lines because the subscribe response is visible to the subscriber and arbitrary receiver responses must not become a data oracle. `classifyTransportError` and `recordDeliveryFailure` enforce this — `lastError` only ever takes a value from `DeliveryErrorBucket`.

Successful refresh of a suspended subscription (`Active=false` → refresh) reactivates it: clears `failureCount`, resets `LastError` and `FailedSince`, flips `Active=true`. Pending events do **not** auto-replay (would re-flood a recovering receiver); the client signals replay intent by passing the persisted cursor on the refresh.

## Q7 — Source health signals

Domain sources fail. The upstream Discord gateway disconnects, the database driver stops returning rows, an auth token expires. The library surfaces these as **first-class signals on the subscriber channel**, with explicit transient-vs-terminal semantics. (`yield.go` `SubscriberEvent` discriminator.)

| Signal | Source-side call | Subscriber channel field | Stream wire mapping | Webhook wire mapping | Stream stays open? |
|--------|------------------|--------------------------|---------------------|----------------------|--------------------|
| **Event** | `yield(data)` | `Event` populated | `notifications/events/event` (Q5 step 3) | Standard Webhooks POST (Q6) | yes |
| **Truncated** (back-pressure) | implicit — set when `yield` drops on a full subscriber buffer | `Truncated:true` riding next successful send | fresh `notifications/events/active{truncated:true, cursor:source.Latest()}` precedes the event | n/a (webhook delivery is independent — no per-subscriber back-pressure) | yes |
| **Transient error** | `source.YieldError(EventDeliveryError{Code, Message})` | `Error` populated | `notifications/events/error{requestId, error{code,message}}` (spec L255+L261) | n/a (errors are upstream-side, not delivery-side) | yes |
| **Terminal** | `source.YieldTerminated(EventDeliveryError{Code, Message})` | `Terminated` populated; subscriber chan closed | `notifications/events/terminated{requestId, error{code,message}}` (spec L783-795) → handler returns `StreamEventsResult{Meta:{}}` | auto-emitted `{type:terminated}` control envelope to every webhook target on this source — see `postTerminatedSilent` | **no** |

`YieldError` is repeatable; `YieldTerminated` is **one-shot** — subsequent yields on the same source are silent no-ops, and `Poll()` returns empty. The terminated source is dead; recovery requires re-subscribing against a fresh source (typically after the host restarts the upstream connection).

### Worked example with the discord demo

`examples/events/discord/Makefile` ships injection targets that exercise the signal paths end-to-end:

```bash
# Inject a transient upstream error — stream stays open, webhook unaffected
make inject-error CODE=-32603 MESSAGE="upstream gateway disconnected"

# Inject a terminal — stream closes; webhook targets get an auto control envelope
make inject-terminate CODE=-32012 MESSAGE="auth token revoked"

# Normal event for comparison
make inject TEXT="hello from make inject"
```

Run a `make webhook` receiver alongside and you'll see the control envelope POST land on the same callback URL as event deliveries, distinguishable only by the `webhook-id: msg_terminated_...` prefix and the `{type:"terminated", error:...}` body.

> [!NOTE]
> **The drop policy on the Error variant is intentional.** Like event drops, error fanout is non-blocking — a slow consumer that backs up doesn't block the source. Unlike event drops, errors don't carry recovery semantics, so missing one is acceptable; future events still get the `Truncated` flag if any actual events were dropped. See `fanoutLocked` commentary.

## End-state (what downstream pages can assume)

After reading this page, downstream pages can assume:

- **Events dial all four extension knobs.** Method namespace (`events/*`), capability (`experimental.events`), notifications (5 push frames + 2 control envelopes), and `_meta` (on `Event` and `EventDef`).
- **Events ≠ notifications.** Events are domain-defined and replayable; notifications are session-state-change and idempotent-on-refetch. Events ride the notifications surface but are a domain abstraction layered on top.
- **Three delivery modes** — poll, push, webhook — all method-namespace extensions, picked by topology and statefulness, NOT mutually exclusive per source. Webhook is the only mode where the server dials the client.
- **Subscription identity is the canonical tuple** `(principal, delivery.url, name, params)`. The id is server-derived (`sub_<base64>`), non-load-bearing for security; idempotent refresh on the same tuple, distinct subscriptions on any tuple difference, cross-tenant isolation by construction.
- **Three rules from the tuple:** no client-supplied id; auth required on subscribe/unsubscribe; client-supplied required `whsec_` secret.
- **`YieldingSource` is the default abstraction** (library owns the buffer; one `yield()` reaches push + webhook + future poll). `TypedSource` is for caller-owned stores. Cursored vs cursorless is a per-source choice advertised on `events/list`.
- **Push delivery** is a long-lived `events/stream` POST returning SSE with five distinct notifications (`active`/`event`/`heartbeat`/`error`/`terminated`) plus a typed empty `StreamEventsResult` on close. `requestId` echoes on every notification for stdio demux.
- **Webhook delivery** is HMAC-signed Standard Webhooks with a hardened delivery loop: dial-time SSRF guard (TOCTOU-safe under DNS rebinding), no redirects, 256 KiB body cap (REJECT not TRUNCATE), 413 non-retryable, exponential backoff on 5xx, suspend after N failures in a sliding window, auto-PostTerminated on the suspend transition.
- **`deliveryStatus`** rides subscribe-refresh responses with a categorical `lastError` (closed set; spec forbids raw receiver content). Refresh of a suspended subscription reactivates it.
- **Source health signals are first-class:** `YieldError` (transient — stream stays) and `YieldTerminated` (terminal one-shot — stream closes, control envelopes posted to webhooks).

## Next to read

- **[events SSRF deep dive](./events-ssrf.md)** *(planned, leaf)* — full IP blocklist matrix with worked CIDR examples, dial-time vs subscribe-time decomposition, DNS rebinding attack walkthrough.
- **[HMAC + Standard Webhooks deep dive](./events-hmac.md)** *(planned, leaf)* — `webhook-id` semantics across event/control deliveries, multi-signature secret-rotation grace window, `MCPHeaders` opt-in mode, receiver verification in Go and Python.
- **[subscription identity tuple proof](./events-identity.md)** *(planned, leaf)* — formal walk-through of why the four-tuple is necessary and sufficient: cross-tenant isolation, secret rotation, principal-mapping edge cases.
- **[Tasks v1/v2/hybrid](./tasks.md)** *(planned, root)* — another method-namespace extension on the same maturity curve; useful contrast for what graduation from `experimental/ext/` to `ext/` looks like.
- **[Reverse-call mechanics](./reverse-call.md)** *(planned, root)* — server-originated requests against a handler context; relevant if you ever want to push back at the model from inside an event-driven flow.
