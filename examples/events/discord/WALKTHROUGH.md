# MCP Events Extension — Discord reference walkthrough

Walks through the four delivery modes of the experimental MCP Events extension (events/list, push via SSE, poll, webhook with TTL refresh) plus the cursored vs cursorless source distinction. Webhook subscriber uses the typed Go SDK at experimental/ext/events/clients/go.

## What you'll learn

- **How do I open the conversation?** — Vanilla MCP `initialize` over Streamable HTTP. The events extension doesn't declare any new capability — `events/*` methods are registered server-side via the events library. Push delivery rides a long-lived per-subscription POST that returns SSE (`events/stream`), not the session GET back-channel, so the client doesn't need any transport-level wiring to receive events.
- **What kinds of events does this server even emit?** — `events/list` returns the catalog of event **types** the server can emit — not a list of recent event instances. (The naming is a touch misleading: it's much closer in spirit to `tools/list` than to a CRUD listing. Think of each entry as the schema for a kind of event that subscribers can ask for, not as data.)
- **Can I get events as they happen?** — Yes — `events/stream` is the answer. It's a long-lived JSON-RPC request, one per subscription, that returns its events as `notifications/events/event` frames on the call's own SSE response stream. Spec §"Push-Based Delivery" L223-296.
- **What if I can't keep a long-lived stream open?** — Poll instead. `events/poll` is single-subscription per call (multi-sub batching was removed) with a flat top-level shape: `{name, params, cursor, maxAge, maxEvents}` in, `{events, cursor, hasMore, truncated, nextPollSeconds}` out. Polling at the head returns no new events but advances the cursor — the response shape is identical whether or not events are waiting, so the client's polling loop has one code path.
- **What about events I don't need to replay, like 'user is typing'?** — On the wire, the event type is marked cursorless: `events/list` advertises `cursorless: true` for that EventDef, every event delivery emits `cursor: null`, and `events/poll` always returns empty with `cursor: null` (there's nothing buffered to serve). Push delivery still fans out events live — the only thing that changes versus a cursored source is replay. (in mcpkit: source authors opt in via `events.NewYieldingSource[T](def, events.WithoutCursors())`)
- **What happens when the upstream source has a hiccup?** — On the wire, two notification methods carry source health. `notifications/events/error` (spec L255+L261) is transient — the source had a failure, the stream stays open, subsequent events still arrive. `notifications/events/terminated` (spec L783-795) is terminal — the subscription has ended. This step exercises the transient path: `inject?action=error` causes the source to surface one upstream failure, the open stream sees `notifications/events/error` arrive while staying connected. (in mcpkit: server authors trigger these via `source.YieldError(err)` / `source.YieldTerminated(err)`)
- **What if my client itself keeps restarting, but I have a public callback URL?** — Use webhook delivery. `events/subscribe` registers a callback URL plus a client-supplied `whsec_` secret with a TTL; the server POSTs HMAC-signed events to that URL as they happen, the subscription is soft-state on the server (in-memory with TTL), and the client refreshes before `refreshBefore` to keep it alive. If the client process dies and reconnects later with the same canonical tuple, the subscription either is still alive (refresh is idempotent) or has lapsed and the next subscribe creates a fresh one with the supplied cursor as the replay point. (in mcpkit: `clients/go` provides `Subscription` for subscribe + auto-refresh and `Receiver[Data]` for a typed inbound channel)
- **Two subs to the same event with different params — how do I tell deliveries apart?** — Each delivery POST carries its own `X-MCP-Subscription-Id` header (per spec §"Webhook Event Delivery" L390), and on the push side every notification echoes the originating `events/stream` request id in `params.requestId`. Subscriptions are identified by the canonical tuple `(principal, delivery.url, name, params)` (spec §"Subscription Identity" → "Key composition" L363), so two subscribes with the same `(principal, url, name)` but different `params` produce different ids — and the receiver branches by header without parsing the body.
- **My webhook receiver just died. How does the server let me know?** — Two answers, layered. First, every subscribe-refresh response carries a `deliveryStatus` block when the target has prior delivery attempts (spec §"Webhook Delivery Status" L425-460): `active` / `lastDeliveryAt` / `lastError` / `failedSince`. Second, after N consecutive failures within a sliding window, the server flips `active: false` and auto-Posts a `{type:terminated}` control envelope to the receiver as a courtesy heads-up. Refresh of a suspended target reactivates it.
- **What if I forget the secret?** — Rejected with `-32602 InvalidParams` at subscribe time. `delivery.secret` is REQUIRED on every `events/subscribe` per spec — there's no server-side fallback. Rejecting at subscribe time means a malformed subscription never exists in the registry, so the server can't ever produce unverifiable deliveries.
- **What if I supply garbage instead of a `whsec_` value?** — Rejected with `-32602 InvalidParams`. The validator enforces the full Standard Webhooks format: `whsec_` followed by base64 of 24-64 random bytes. A non-prefixed value, a too-short value, or non-base64 garbage all fail at subscribe time — catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.
- **What if I try to pick my own subscription id?** — Rejected with `-32602 InvalidParams`. Per spec §"Subscription Identity" → "Key composition" L363, the id is server-derived from `(principal, name, params, url)` — there is no client-generated id. Old SDKs that send an `id` field get a loud error rather than a silent mis-keying that would alias subscriptions and break tenant isolation.
- **And when everything is right?** — Subscribe succeeds. The response carries the server-derived `id` (`sub_<base64>` per spec §"Subscription Identity" → "Derived id" L367), plus `cursor` and `refreshBefore`. Notably absent: the `secret` — the client supplied it, so the server doesn't echo it back. Echoing would risk leaks via proxies, logs, or IDE network panes.
- **Now let's see it against a real bot** — Setup: start the server with a Discord bot token and invite the bot to a channel you can post in.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)
    participant Receiver as Local webhook receiver (this process)

    Note over Host,Receiver: Step 1: How do I open the conversation?
    Host->>Server: POST /mcp — initialize
    Server-->>Host: serverInfo + capabilities

    Note over Host,Receiver: Step 2: What kinds of events does this server even emit?
    Host->>Server: events/list
    Server-->>Host: [discord.message (cursored), discord.typing (cursorless)]

    Note over Host,Receiver: Step 3: Can I get events as they happen?
    Host->>Server: events/stream { name: discord.message }
    Server-->>Host: notifications/events/active { requestId, cursor }
    Receiver->>Server: POST /inject (simulated Discord message)
    Server-->>Host: notifications/events/event { requestId, eventId, ... }
    Host-->>Server: (close request) → StreamEventsResult final frame

    Note over Host,Receiver: Step 4: What if I can't keep a long-lived stream open?
    Host->>Server: events/poll {name: discord.message, cursor: <head>}
    Server-->>Host: {events: [], cursor: <head>, hasMore: false}

    Note over Host,Receiver: Step 5: What about events I don't need to replay, like 'user is typing'?
    Host->>Server: events/stream { name: discord.typing }
    Server-->>Host: notifications/events/active { cursor: null }
    Receiver->>Server: POST /inject?event=discord.typing
    Server-->>Host: notifications/events/event { cursor: null }

    Note over Host,Receiver: Step 6: What happens when the upstream source has a hiccup?
    Host->>Server: events/stream { name: discord.message }
    Server-->>Host: notifications/events/active
    Receiver->>Server: POST /inject?action=error
    Server-->>Host: notifications/events/event/error { requestId, error: { code, message } }

    Note over Host,Receiver: Step 7: What if my client itself keeps restarting, but I have a public callback URL?
    Receiver->>Receiver: spin up local httptest receiver on :random
    Host->>Server: events/subscribe { mode: webhook, url, secret: whsec_<client-supplied> }
    Server-->>Host: { id, refreshBefore }   (response does NOT echo secret per spec)
    Receiver->>Server: POST /inject (simulated message)
    Server-->>Receiver: POST <url> + HMAC signature headers (default: webhook-* per Standard Webhooks; opt-in: X-MCP-* via -webhook-header-mode mcp)
    Host-->>Host: background loop: re-subscribe at 0.5 × TTL

    Note over Host,Receiver: Step 8: Two subs to the same event with different params — how do I tell deliveries apart?
    Host->>Server: events/subscribe { name: discord.message, params: {channel_id: 'alpha'}, ... }
    Server-->>Host: { id: sub_<A>, ... }
    Host->>Server: events/subscribe { name: discord.message, params: {channel_id: 'beta'}, ... }
    Server-->>Host: { id: sub_<B>, ... }   (id differs from A — different params → different canonical tuple)
    Receiver->>Server: POST /inject (one event)
    Server-->>Receiver: POST <url> + X-MCP-Subscription-Id: sub_<A>
    Server-->>Receiver: POST <url> + X-MCP-Subscription-Id: sub_<B>

    Note over Host,Receiver: Step 9: My webhook receiver just died. How does the server let me know?
    Receiver->>Receiver: spin up failing receiver (returns 500 on event POSTs)
    Host->>Server: events/subscribe { name: discord.message, ... }
    Server-->>Host: { id, refreshBefore }   (no deliveryStatus on first subscribe — nothing to report)
    Receiver->>Server: POST /inject (one event)
    Server-->>Receiver: POST <url>  → 500  (×4 retries with exponential backoff, then recordDeliveryFailure)
    Host->>Server: events/subscribe (refresh — same canonical tuple)
    Server-->>Host: { id, refreshBefore, deliveryStatus: { active, lastDeliveryAt, lastError, failedSince } }
    Server-->>Receiver: (if suspend fires) POST <url> body={type:terminated, error}  + webhook-id=msg_terminated_<random>

    Note over Host,Receiver: Step 10: What if I forget the secret?
    Host->>Server: events/subscribe { delivery: { ... } }   (no secret)
    Server-->>Host: -32602 InvalidParams: delivery.secret is required

    Note over Host,Receiver: Step 11: What if I supply garbage instead of a `whsec_` value?
    Host->>Server: events/subscribe { delivery: { secret: 'wrong' } }
    Server-->>Host: -32602 InvalidParams: delivery.secret invalid: must start with the whsec_ prefix

    Note over Host,Receiver: Step 12: What if I try to pick my own subscription id?
    Host->>Server: events/subscribe { id: 'mine', ... }
    Server-->>Host: -32602 InvalidParams: client-supplied id is not accepted

    Note over Host,Receiver: Step 13: And when everything is right?
    Host->>Host: events.GenerateSecret() → whsec_<base64 of 32 bytes>
    Host->>Server: events/subscribe { delivery: { secret: whsec_<valid> } }
    Server-->>Host: { id: sub_<base64-of-16-bytes>, cursor, refreshBefore }   (no secret per spec)

    Note over Host,Receiver: Step 14: Now let's see it against a real bot
    Discord->>Server: TypingStart event (when you start typing in the channel)
    Server-->>Host: notifications/events/event { name: discord.typing, cursor: null }
    Discord->>Server: MessageCreate event (when you press enter)
    Server-->>Host: notifications/events/event { name: discord.message, cursor: <new> }
```

## Steps

### Setup — two modes

This walkthrough runs against either a test-mode server or a real Discord bot.

**Option A — Test mode** (no bot token needed). All steps run; the final live-interaction step skips with a 'no token' message. Drive synthetic events from a third terminal via `make inject` / `make inject-typing`.

```
Terminal 1:  make serve                                # server in test mode
Terminal 2:  make demo                                 # this walkthrough
Terminal 3:  make inject TEXT='hello'                  # message event
             make inject-typing                        # typing event (cursorless)
```

**Option B — Real bot mode** (requires `DISCORD_BOT_TOKEN`). Same walkthrough plus the live step captures real typing + message events from your Discord channel. Token setup in the demo's README.

```
Terminal 1:  DISCORD_BOT_TOKEN=... make serve          # server in bot mode
Terminal 2:  make demo                                 # this walkthrough
             # In Discord: type, then send. Live step captures both.
```

### What this demo covers

- **events/list** — the source catalog, including the `cursorless` flag and the `_meta` per-type metadata field.
- **Push** — long-lived SSE stream; `notifications/events/event` arrives in real time.
- **Poll** — single-subscription `events/poll` (multi-sub batching is not supported).
- **Cursorless source** — typing indicators that wire as `cursor: null`. Subscribers can't replay, only see live events.
- **Source-side health signals** — `YieldError` (transient `notifications/events/error`, stream stays open).
- **Webhook + auto-refresh** — `events/subscribe` with the typed `Subscription` + `Receiver[Data]` from `clients/go`. Includes the hardened delivery loop: dial-time SSRF guard, no-redirects, 256 KiB body cap with 413 non-retryable, Standard Webhooks signature scheme as default.
- **Multi-subscription routing** — two subs to `discord.message` with different params; one event fans out to both, distinguished by `X-MCP-Subscription-Id` plus push-side `requestId` echo on every notification.
- **Webhook delivery health** — `deliveryStatus` block on subscribe-refresh response after a failed delivery; suspend state machine flips Active=false after N consecutive failures and auto-Posts a `{type:terminated}` control envelope when run with `make serve-fast-suspend`.
- **Auth posture** — `events/subscribe` requires an authenticated principal per spec; demo runs anonymously via `UnsafeAnonymousPrincipal`. Production deployments wire real OIDC and reject anonymous subscribes with `-32012`.
- **Spec validation** — empty / malformed `delivery.secret` rejected; client-supplied `id` rejected; valid `whsec_` accepted with no secret echoed.

Identity-mode subscribe and Standard Webhooks header naming are exercised by the unit tests in `experimental/ext/events/` and by `discord-events`'s e2e tests; they require the server to be started with mode flags so they're documented in the README rather than driven from this walkthrough.

### Step 1: How do I open the conversation?

Vanilla MCP `initialize` over Streamable HTTP. The events extension doesn't declare any new capability — `events/*` methods are registered server-side via the events library. Push delivery rides a long-lived per-subscription POST that returns SSE (`events/stream`), not the session GET back-channel, so the client doesn't need any transport-level wiring to receive events.

### Step 2: What kinds of events does this server even emit?

`events/list` returns the catalog of event **types** the server can emit — not a list of recent event instances. (The naming is a touch misleading: it's much closer in spirit to `tools/list` than to a CRUD listing. Think of each entry as the schema for a kind of event that subscribers can ask for, not as data.)

Each entry advertises a name, description, the supported delivery modes, an auto-derived `payloadSchema`, and the `cursorless` flag — `discord.message` buffers events and accepts replay cursors, `discord.typing` emits ephemerally and always wires `cursor: null`.

- Each `EventDef` may carry an opaque `_meta` map for app-defined per-event-type metadata (mirrors the `_meta` convention on Tool / Resource / Prompt in base MCP). The same `_meta` convention applies on `EventOccurrence` (the wire-format Event envelope). The discord sources don't set `_meta` here; servers that want to surface trace ids, source-system tags, or other per-type annotations populate it on construction.
- The events/list response carries an optional `nextCursor` for forward-compatible pagination (mirrors the tools/list / resources/list convention). Library doesn't paginate today (advertised sets are small in practice); the field is plumbed for forward compatibility.

### Step 3: Can I get events as they happen?

Yes — `events/stream` is the answer. It's a long-lived JSON-RPC request, one per subscription, that returns its events as `notifications/events/event` frames on the call's own SSE response stream. Spec §"Push-Based Delivery" L223-296.

- Server confirms with notifications/events/active, then delivers events as notifications/events/event on the call's own SSE response stream.
- Heartbeats fire every ≥30s carrying the source's current cursor so the client's persisted cursor advances during quiet periods.
- Replaces the broadcast-to-all-listeners model from Phase 1; per-stream isolation comes for free since each stream is its own POST.
- Typed Go SDK Stream() helper (experimental/ext/events/clients/go) threads the per-call notification hook (client.CallContext.WithNotifyHook) so callbacks fire only for THIS stream's notifications.

### Step 4: What if I can't keep a long-lived stream open?

Poll instead. `events/poll` is single-subscription per call (multi-sub batching was removed) with a flat top-level shape: `{name, params, cursor, maxAge, maxEvents}` in, `{events, cursor, hasMore, truncated, nextPollSeconds}` out. Polling at the head returns no new events but advances the cursor — the response shape is identical whether or not events are waiting, so the client's polling loop has one code path.

### Step 5: What about events I don't need to replay, like 'user is typing'?

On the wire, the event type is marked cursorless: `events/list` advertises `cursorless: true` for that EventDef, every event delivery emits `cursor: null`, and `events/poll` always returns empty with `cursor: null` (there's nothing buffered to serve). Push delivery still fans out events live — the only thing that changes versus a cursored source is replay. (in mcpkit: source authors opt in via `events.NewYieldingSource[T](def, events.WithoutCursors())`)

- Push delivery via events/stream still works — there's just nothing to replay.
- Heartbeats also carry cursor:null (spec L294: "null for event types that do not support replay").
- Useful for ephemeral state (typing indicators, presence, current readings).

### Step 6: What happens when the upstream source has a hiccup?

On the wire, two notification methods carry source health. `notifications/events/error` (spec L255+L261) is transient — the source had a failure, the stream stays open, subsequent events still arrive. `notifications/events/terminated` (spec L783-795) is terminal — the subscription has ended. This step exercises the transient path: `inject?action=error` causes the source to surface one upstream failure, the open stream sees `notifications/events/error` arrive while staying connected. (in mcpkit: server authors trigger these via `source.YieldError(err)` / `source.YieldTerminated(err)`)

- Webhook subscribers don't see error envelopes (errors are upstream-side, not delivery-side); they DO see {type:terminated} control envelopes when the suspend state machine flips Active=false or when the source itself terminates.
- This walkthrough step exercises only the transient error path — calling `inject?action=terminate` would one-shot terminate the discord.message source, breaking subsequent walkthrough steps that depend on it. Full terminate flow is covered by TestE2EHealthSignalsEndToEnd in this demo's e2e_test.go.

### Step 7: What if my client itself keeps restarting, but I have a public callback URL?

Use webhook delivery. `events/subscribe` registers a callback URL plus a client-supplied `whsec_` secret with a TTL; the server POSTs HMAC-signed events to that URL as they happen, the subscription is soft-state on the server (in-memory with TTL), and the client refreshes before `refreshBefore` to keep it alive. If the client process dies and reconnects later with the same canonical tuple, the subscription either is still alive (refresh is idempotent) or has lapsed and the next subscribe creates a fresh one with the supplied cursor as the replay point. (in mcpkit: `clients/go` provides `Subscription` for subscribe + auto-refresh and `Receiver[Data]` for a typed inbound channel)

- HMAC signing secret is client-supplied per spec; SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty.
- Subscription.Secret() returns the value the SDK ended up using, so the receiver can verify with the same secret.
- Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape — consumer reads `ev.Data.Content` directly, no re-parsing JSON.
- Default header mode is **Standard Webhooks** (spec L390+L431): `webhook-id` / `webhook-timestamp` / `webhook-signature: v1,<base64>` plus the MCP-specific `X-MCP-Subscription-Id`. Off-the-shelf Svix-style verifiers work. `MCPHeaders` (`X-MCP-Signature` + `X-MCP-Timestamp`) is the opt-in legacy via `-webhook-header-mode mcp`.

**Hardened delivery loop** (`webhook.go` `deliver()`):

- Dial-time SSRF guard rejects loopback / RFC1918 / link-local / IPv6-ULA / multicast at the `net.Dialer.Control` callback (TOCTOU-safe under DNS rebinding). The demo bypasses this via `WithWebhookAllowPrivateNetworks(true)` because it delivers to a local httptest receiver; production deployments leave the guard ON. Spec §"Webhook Security" L464.
- No-redirect-following: `http.Client.CheckRedirect` returns `ErrUseLastResponse` so a receiver returning 3xx to an internal address can't bypass the dial-time guard via Go's redirect chain. 3xx is terminal `http_3xx_redirect`.
- 256 KiB body cap (REJECT not TRUNCATE — truncation would corrupt the HMAC); 413 from the receiver is non-retryable. Spec L487.
- 5xx retry with exponential backoff (4 attempts: 500ms → 1s → 2s → 5s cap). Standard webhook convention.

**Auth posture:** `events/subscribe` requires an authenticated principal per spec §"Subscription Identity" → "Authentication required" L361. The demo runs anonymously via `events.Config.UnsafeAnonymousPrincipal="demo-user"` (logged at startup as "auth: demo (anonymous → UnsafeAnonymousPrincipal)"). Production deployments unset that field AND wire `server.WithAuth(JWTValidator)` so anonymous subscribes hit the spec-mandated `-32012 Unauthorized`. See README "Auth posture: demo escape vs real OIDC".

### Step 8: Two subs to the same event with different params — how do I tell deliveries apart?

Each delivery POST carries its own `X-MCP-Subscription-Id` header (per spec §"Webhook Event Delivery" L390), and on the push side every notification echoes the originating `events/stream` request id in `params.requestId`. Subscriptions are identified by the canonical tuple `(principal, delivery.url, name, params)` (spec §"Subscription Identity" → "Key composition" L363), so two subscribes with the same `(principal, url, name)` but different `params` produce different ids — and the receiver branches by header without parsing the body.

- The library fans out one yielded event to **both** webhook targets today — there is no per-subscription `match` filter yet (that's the upcoming SDK-hooks plan; see `docs/EVENTS_ETA_PLAN.md`).
- Push side: the same routing works via the `requestId` echo on every `notifications/events/event` payload — each `events/stream` POST gets its own JSON-RPC id, and notifications carry it in `params.requestId`.

### Step 9: My webhook receiver just died. How does the server let me know?

Two answers, layered. First, every subscribe-refresh response carries a `deliveryStatus` block when the target has prior delivery attempts (spec §"Webhook Delivery Status" L425-460): `active` / `lastDeliveryAt` / `lastError` / `failedSince`. Second, after N consecutive failures within a sliding window, the server flips `active: false` and auto-Posts a `{type:terminated}` control envelope to the receiver as a courtesy heads-up. Refresh of a suspended target reactivates it.

- `lastError` is from a **closed categorical set** (`connection_refused`, `timeout`, `tls_error`, `http_3xx_redirect`, `http_4xx`, `http_5xx`, `challenge_failed`); the spec forbids raw response bodies / headers / status lines because the subscribe response is visible to the subscriber and arbitrary receiver responses must not become a data oracle.
- `failedSince` is set on the **first failure of the current run** and preserved across subsequent failures, so subscribers can see how long the receiver has been unreachable.
- Spec §"Webhook Event Delivery" L413+L460: "after repeated failures the server SHOULD set active: false." The transition fires after 5 consecutive failures within a 10-min sliding window. On the `true→false` transition the server auto-Posts a `{type:terminated}` control envelope to the (now-suspended) receiver — `webhook-id` prefix is `msg_terminated_<random>` so receivers can distinguish it from event deliveries (which use `evt_<eventId>`). (in mcpkit: knobs are `events.WithWebhookSuspendThreshold(n)` and `events.WithWebhookSuspendWindow(d)`)
- A successful refresh of a suspended target reactivates it: clears the failure run, resets `lastError` and `failedSince`, flips `active` back to true.

**Fast-mode tip:** with the default `make serve` (`-webhook-suspend-threshold 5`), this step demonstrates the deliveryStatus reporting (lastError populated, failedSince populated, active still true) — full suspend takes 5 failed deliveries × ~8.5s each. To see suspend fire after ONE failure (~12s total step time), restart the server with `make serve-fast-suspend` (sets `-webhook-suspend-threshold 1`).

### Step 10: What if I forget the secret?

Rejected with `-32602 InvalidParams` at subscribe time. `delivery.secret` is REQUIRED on every `events/subscribe` per spec — there's no server-side fallback. Rejecting at subscribe time means a malformed subscription never exists in the registry, so the server can't ever produce unverifiable deliveries.

- This step makes a raw client.Call to bypass the SDK and demonstrate the server-side validator directly. (in mcpkit: the Go SDK auto-generates a conforming whsec_ value via `events.GenerateSecret()` when `SubscribeOptions.Secret` is empty — this step skips that on purpose)

### Step 11: What if I supply garbage instead of a `whsec_` value?

Rejected with `-32602 InvalidParams`. The validator enforces the full Standard Webhooks format: `whsec_` followed by base64 of 24-64 random bytes. A non-prefixed value, a too-short value, or non-base64 garbage all fail at subscribe time — catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.

### Step 12: What if I try to pick my own subscription id?

Rejected with `-32602 InvalidParams`. Per spec §"Subscription Identity" → "Key composition" L363, the id is server-derived from `(principal, name, params, url)` — there is no client-generated id. Old SDKs that send an `id` field get a loud error rather than a silent mis-keying that would alias subscriptions and break tenant isolation.

### Step 13: And when everything is right?

Subscribe succeeds. The response carries the server-derived `id` (`sub_<base64>` per spec §"Subscription Identity" → "Derived id" L367), plus `cursor` and `refreshBefore`. Notably absent: the `secret` — the client supplied it, so the server doesn't echo it back. Echoing would risk leaks via proxies, logs, or IDE network panes.

- The id is non-load-bearing for security; it's surfaced as `X-MCP-Subscription-Id` on delivery POSTs but knowing the value grants no operations on the subscription.

### Step 14: Now let's see it against a real bot

Setup: start the server with a Discord bot token and invite the bot to a channel you can post in.

```
DISCORD_BOT_TOKEN=<your-token> make serve
```

Bot setup (token + invite URL) is documented in this demo's README.md.

- TypingStart handler in main.go yields a cursorless discord.typing event; MessageCreate yields the cursored discord.message.
- Discord's typing indicator fires once when you start (then refires every ~8s if you keep typing), not per keystroke.
- --non-interactive mode skips the wait so CI runs aren't slowed.

### Where each piece lives in mcpkit

- Events library: `experimental/ext/events/`
- Go client SDK (`Subscription`, `Receiver[Data]`): `experimental/ext/events/clients/go/`
- Python client SDK (`WebhookSubscription` class + `webhook` CLI): `experimental/ext/events/clients/python/events_client.py`
- Demo source: `examples/events/discord/`
- Companion demo: `examples/events/telegram/` (lighter walkthrough — same protocol, different bot SDK)

## Run it

```bash
go run ./examples/events/discord/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/events/discord/ --non-interactive
```
