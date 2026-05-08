# MCP Events Extension — Discord reference walkthrough

Walks through the four delivery modes of the experimental MCP Events extension (events/list, push via SSE, poll, webhook with TTL refresh) plus the cursored vs cursorless source distinction. Webhook subscriber uses the typed Go SDK at experimental/ext/events/clients/go.

## What you'll learn

- **Connect to the events server** — Plain MCP initialize over Streamable HTTP. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.
- **events/list — see the source catalog** — The `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.
- **Push: open events/stream, inject a message, observe per-call notifications** — events/stream is a long-lived JSON-RPC request — one per subscription. Spec §"Push-Based Delivery" L223-296.
- **Poll: events/poll with the cursor we just saw** — Single-subscription per call (PR B removed batching). Polling at the head returns no new events but advances the cursor — the same response shape that would carry events if any had arrived since the last poll.
- **Cursorless: open events/stream for typing, observe cursor:null on the wire** — WithoutCursors() sources don't buffer; the wire emits cursor:null.
- **Health signals: source bubbles a transient upstream failure → notifications/events/error** — Sources bubble health via YieldError(err) (transient, stream stays open) and YieldTerminated(err) (terminal, stream closes).
- **Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh** — clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel).
- **Multi-sub routing: two webhook subs to discord.message, distinguished by X-MCP-Subscription-Id** — Demonstrates that the spec's canonical-tuple identity (γ) plus the per-delivery `X-MCP-Subscription-Id` header (γ-4) make multi-sub-same-event-name routing unambiguous on the wire.
- **Webhook delivery health: deliveryStatus on subscribe-refresh + suspend transition** — Demonstrates ζ-5 (`deliveryStatus` block on subscribe-refresh) and ζ-6 (suspend state machine + auto-PostTerminated control envelope).
- **Spec validation: empty delivery.secret is rejected** — delivery.secret is REQUIRED on every events/subscribe — no server-side fallback per spec.
- **Spec validation: malformed delivery.secret is rejected** — The validator enforces the full Standard Webhooks format: `whsec_` followed by base64 of 24-64 random bytes.
- **Spec validation: client-supplied id is rejected** — Spec §"Subscription Identity" → "Key composition" L363: "There is no client-generated id — a subscription is fully determined by what it listens for, where it delivers, and who asked."
- **Spec validation: valid whsec_ accepted; response carries server-derived id, no secret** — Counter-test: a freshly-generated whsec_ value is accepted.
- **Live Discord interaction (typing + message from a real Discord channel)** — Setup: start the server with a Discord bot token and invite the bot to a channel you can post in.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)
    participant Receiver as Local webhook receiver (this process)

    Note over Host,Receiver: Step 1: Connect to the events server
    Host->>Server: POST /mcp — initialize
    Server-->>Host: serverInfo + capabilities

    Note over Host,Receiver: Step 2: events/list — see the source catalog
    Host->>Server: events/list
    Server-->>Host: [discord.message (cursored), discord.typing (cursorless)]

    Note over Host,Receiver: Step 3: Push: open events/stream, inject a message, observe per-call notifications
    Host->>Server: events/stream { name: discord.message }
    Server-->>Host: notifications/events/active { requestId, cursor }
    Receiver->>Server: POST /inject (simulated Discord message)
    Server-->>Host: notifications/events/event { requestId, eventId, ... }
    Host-->>Server: (close request) → StreamEventsResult final frame

    Note over Host,Receiver: Step 4: Poll: events/poll with the cursor we just saw
    Host->>Server: events/poll {subscriptions: [{name: discord.message, cursor: <head>}]}
    Server-->>Host: {events: [], cursor: <head>, hasMore: false}

    Note over Host,Receiver: Step 5: Cursorless: open events/stream for typing, observe cursor:null on the wire
    Host->>Server: events/stream { name: discord.typing }
    Server-->>Host: notifications/events/active { cursor: null }
    Receiver->>Server: POST /inject?event=discord.typing
    Server-->>Host: notifications/events/event { cursor: null }

    Note over Host,Receiver: Step 6: Health signals: source bubbles a transient upstream failure → notifications/events/error
    Host->>Server: events/stream { name: discord.message }
    Server-->>Host: notifications/events/active
    Receiver->>Server: POST /inject?action=error
    Server-->>Host: notifications/events/event/error { requestId, error: { code, message } }

    Note over Host,Receiver: Step 7: Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh
    Receiver->>Receiver: spin up local httptest receiver on :random
    Host->>Server: events/subscribe { mode: webhook, url, secret: whsec_<client-supplied> }
    Server-->>Host: { id, refreshBefore }   (response does NOT echo secret per spec)
    Receiver->>Server: POST /inject (simulated message)
    Server-->>Receiver: POST <url> + HMAC signature headers (default: webhook-* per Standard Webhooks; opt-in: X-MCP-* via -webhook-header-mode mcp)
    Host-->>Host: background loop: re-subscribe at 0.5 × TTL

    Note over Host,Receiver: Step 8: Multi-sub routing: two webhook subs to discord.message, distinguished by X-MCP-Subscription-Id
    Host->>Server: events/subscribe { name: discord.message, params: {channel_id: 'alpha'}, ... }
    Server-->>Host: { id: sub_<A>, ... }
    Host->>Server: events/subscribe { name: discord.message, params: {channel_id: 'beta'}, ... }
    Server-->>Host: { id: sub_<B>, ... }   (id differs from A — different params → different canonical tuple)
    Receiver->>Server: POST /inject (one event)
    Server-->>Receiver: POST <url> + X-MCP-Subscription-Id: sub_<A>
    Server-->>Receiver: POST <url> + X-MCP-Subscription-Id: sub_<B>

    Note over Host,Receiver: Step 9: Webhook delivery health: deliveryStatus on subscribe-refresh + suspend transition
    Receiver->>Receiver: spin up failing receiver (returns 500 on event POSTs)
    Host->>Server: events/subscribe { name: discord.message, ... }
    Server-->>Host: { id, refreshBefore }   (no deliveryStatus on first subscribe — nothing to report)
    Receiver->>Server: POST /inject (one event)
    Server-->>Receiver: POST <url>  → 500  (×4 retries with exponential backoff, then recordDeliveryFailure)
    Host->>Server: events/subscribe (refresh — same canonical tuple)
    Server-->>Host: { id, refreshBefore, deliveryStatus: { active, lastDeliveryAt, lastError, failedSince } }
    Server-->>Receiver: (if suspend fires) POST <url> body={type:terminated, error}  + webhook-id=msg_terminated_<random>

    Note over Host,Receiver: Step 10: Spec validation: empty delivery.secret is rejected
    Host->>Server: events/subscribe { delivery: { ... } }   (no secret)
    Server-->>Host: -32602 InvalidParams: delivery.secret is required

    Note over Host,Receiver: Step 11: Spec validation: malformed delivery.secret is rejected
    Host->>Server: events/subscribe { delivery: { secret: 'wrong' } }
    Server-->>Host: -32602 InvalidParams: delivery.secret invalid: must start with the whsec_ prefix

    Note over Host,Receiver: Step 12: Spec validation: client-supplied id is rejected
    Host->>Server: events/subscribe { id: 'mine', ... }
    Server-->>Host: -32602 InvalidParams: client-supplied id is not accepted

    Note over Host,Receiver: Step 13: Spec validation: valid whsec_ accepted; response carries server-derived id, no secret
    Host->>Host: events.GenerateSecret() → whsec_<base64 of 32 bytes>
    Host->>Server: events/subscribe { delivery: { secret: whsec_<valid> } }
    Server-->>Host: { id: sub_<base64-of-16-bytes>, cursor, refreshBefore }   (no secret per spec)

    Note over Host,Receiver: Step 14: Live Discord interaction (typing + message from a real Discord channel)
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

- **events/list** — the source catalog, including the new `cursorless` flag and the `_meta` per-type metadata field (δ-4).
- **Push** — long-lived SSE stream; `notifications/events/event` arrives in real time.
- **Poll** — single-subscription `events/poll` (multi-sub batching is not supported).
- **Cursorless source** — typing indicators that wire as `cursor: null`. Subscribers can't replay, only see live events.
- **Source-side health signals** — `YieldError` (transient `notifications/events/error`, stream stays open).
- **Webhook + auto-refresh** — `events/subscribe` with the typed `Subscription` + `Receiver[Data]` from `clients/go`. Includes the hardened delivery loop: dial-time SSRF guard (ζ-1), no-redirects (ζ-2), 256 KiB body cap with 413 non-retryable (ζ-3), Standard Webhooks signature scheme as default.
- **Multi-subscription routing** — two subs to `discord.message` with different params; one event fans out to both, distinguished by `X-MCP-Subscription-Id` (γ-4 + ε requestId echo).
- **Webhook delivery health** — `deliveryStatus` block on subscribe-refresh response after a failed delivery (ζ-5); suspend state machine flips Active=false after N consecutive failures and auto-Posts a `{type:terminated}` control envelope (ζ-6) when run with `make serve-fast-suspend`.
- **Auth posture** — `events/subscribe` requires an authenticated principal per spec; demo runs anonymously via `UnsafeAnonymousPrincipal` (γ-5). Production deployments wire real OIDC and reject anonymous subscribes with `-32012`.
- **Spec validation** — empty / malformed `delivery.secret` rejected; client-supplied `id` rejected; valid `whsec_` accepted with no secret echoed.

Identity-mode subscribe and Standard Webhooks header naming are exercised by the unit tests in `experimental/ext/events/` and by `discord-events`'s e2e tests; they require the server to be started with mode flags so they're documented in the README rather than driven from this walkthrough.

### Step 1: Connect to the events server

Plain MCP initialize over Streamable HTTP. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.

### Step 2: events/list — see the source catalog

The `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.

- δ-4: each `EventDef` may carry an opaque `_meta` map for app-defined per-event-type metadata (mirrors the `_meta` convention on Tool / Resource / Prompt in base MCP). The same `_meta` convention applies on `EventOccurrence` (the wire-format Event envelope). The discord sources don't set `_meta` here; servers that want to surface trace ids, source-system tags, or other per-type annotations populate it on construction.
- δ-5: events/list response carries an optional `nextCursor` for forward-compatible pagination (mirrors the tools/list / resources/list convention). Library doesn't paginate today (advertised sets are small in practice); the field is plumbed for forward compatibility.

### Step 3: Push: open events/stream, inject a message, observe per-call notifications

events/stream is a long-lived JSON-RPC request — one per subscription. Spec §"Push-Based Delivery" L223-296.

- Server confirms with notifications/events/active, then delivers events as notifications/events/event on the call's own SSE response stream.
- Heartbeats fire every ≥30s carrying the source's current cursor so the client's persisted cursor advances during quiet periods.
- Replaces the broadcast-to-all-listeners model from Phase 1; per-stream isolation comes for free since each stream is its own POST.
- Typed Go SDK Stream() helper (experimental/ext/events/clients/go) threads the per-call notification hook (client.CallContext.WithNotifyHook) so callbacks fire only for THIS stream's notifications.

### Step 4: Poll: events/poll with the cursor we just saw

Single-subscription per call (PR B removed batching). Polling at the head returns no new events but advances the cursor — the same response shape that would carry events if any had arrived since the last poll.

### Step 5: Cursorless: open events/stream for typing, observe cursor:null on the wire

WithoutCursors() sources don't buffer; the wire emits cursor:null.

- Push delivery via events/stream still works — there's just nothing to replay.
- Heartbeats also carry cursor:null (spec L294: "null for event types that do not support replay").
- Useful for ephemeral state (typing indicators, presence, current readings).

### Step 6: Health signals: source bubbles a transient upstream failure → notifications/events/error

Sources bubble health via YieldError(err) (transient, stream stays open) and YieldTerminated(err) (terminal, stream closes).

- Stream subscribers map onto notifications/events/error (spec L255+L261, transient) and notifications/events/terminated (spec L783-795, terminal).
- Webhook subscribers don't see error envelopes (errors are upstream-side, not delivery-side); they DO see {type:terminated} control envelopes when the suspend state machine flips Active=false (ζ-6) or when the source itself terminates (ζ-7.3).
- This walkthrough step exercises only the transient error path — calling `inject?action=terminate` would one-shot terminate the discord.message source, breaking subsequent walkthrough steps that depend on it. Full terminate flow is covered by TestE2EHealthSignalsEndToEnd in this demo's e2e_test.go.

### Step 7: Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh

clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel).

- HMAC signing secret is client-supplied per spec; SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty.
- Subscription.Secret() returns the value the SDK ended up using, so the receiver can verify with the same secret.
- Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape — consumer reads `ev.Data.Content` directly, no re-parsing JSON.
- Default header mode is **Standard Webhooks** (spec L390+L431): `webhook-id` / `webhook-timestamp` / `webhook-signature: v1,<base64>` plus the MCP-specific `X-MCP-Subscription-Id`. Off-the-shelf Svix-style verifiers work. `MCPHeaders` (`X-MCP-Signature` + `X-MCP-Timestamp`) is the opt-in legacy via `-webhook-header-mode mcp`.

**Hardened delivery loop** (`webhook.go` `deliver()`):

- ζ-1 dial-time SSRF guard rejects loopback / RFC1918 / link-local / IPv6-ULA / multicast at the `net.Dialer.Control` callback (TOCTOU-safe under DNS rebinding). The demo bypasses this via `WithWebhookAllowPrivateNetworks(true)` because it delivers to a local httptest receiver; production deployments leave the guard ON. Spec §"Webhook Security" L464.
- ζ-2 no-redirect-following: `http.Client.CheckRedirect` returns `ErrUseLastResponse` so a receiver returning 3xx to an internal address can't bypass the dial-time guard via Go's redirect chain. 3xx is terminal `http_3xx_redirect`.
- ζ-3 256 KiB body cap (REJECT not TRUNCATE — truncation would corrupt the HMAC); 413 from the receiver is non-retryable. Spec L487.
- 5xx retry with exponential backoff (4 attempts: 500ms → 1s → 2s → 5s cap). Standard webhook convention.

**Auth posture** (γ-5): `events/subscribe` requires an authenticated principal per spec §"Subscription Identity" → "Authentication required" L361. The demo runs anonymously via `events.Config.UnsafeAnonymousPrincipal="demo-user"` (logged at startup as "auth: demo (anonymous → UnsafeAnonymousPrincipal)"). Production deployments unset that field AND wire `server.WithAuth(JWTValidator)` so anonymous subscribes hit the spec-mandated `-32012 Unauthorized`. See README "Auth posture (γ): demo escape vs real OIDC".

### Step 8: Multi-sub routing: two webhook subs to discord.message, distinguished by X-MCP-Subscription-Id

Demonstrates that the spec's canonical-tuple identity (γ) plus the per-delivery `X-MCP-Subscription-Id` header (γ-4) make multi-sub-same-event-name routing unambiguous on the wire.

- Two subscribes with the same `(principal, url, name)` but different `params` produce different canonical bytes (`identity.go canonicalKey`) and therefore different derived ids (`deriveSubscriptionID`).
- The library fans out one yielded event to **both** webhook targets today — there is no per-subscription `match` filter yet (that's η-4 in the upcoming SDK-hooks plan).
- Each delivery POST carries its own `X-MCP-Subscription-Id` header so the receiver can route or branch by sub even when the body is identical.
- Push side: the same routing works via the `requestId` echo (ε) on every `notifications/events/event` payload — each `events/stream` POST gets its own JSON-RPC id, and notifications carry it in `params.requestId`.
- This was an honest gap at the 2026-05-01 demo (gap analysis #11/#22): we had `Server.Broadcast` for push + URL-keyed routing for webhook, neither of which distinguished multiple subs to the same event name with different params. γ-4 + ε closed the gap.

### Step 9: Webhook delivery health: deliveryStatus on subscribe-refresh + suspend transition

Demonstrates ζ-5 (`deliveryStatus` block on subscribe-refresh) and ζ-6 (suspend state machine + auto-PostTerminated control envelope).

- Per spec §"Webhook Delivery Status" L425-460, refresh responses carry `deliveryStatus` when the target has prior delivery attempts. `lastError` is from a **closed categorical set** (`connection_refused`, `timeout`, `tls_error`, `http_3xx_redirect`, `http_4xx`, `http_5xx`, `challenge_failed`); the spec forbids raw response bodies / headers / status lines because the subscribe response is visible to the subscriber and arbitrary receiver responses must not become a data oracle.
- `failedSince` is set on the **first failure of the current run** and preserved across subsequent failures, so subscribers can see how long the receiver has been unreachable.
- Spec §"Webhook Event Delivery" L413+L460: "after repeated failures the server SHOULD set active: false." The library fires this transition after 5 consecutive failures within a 10-min sliding window (configurable via `WithWebhookSuspendThreshold` / `WithWebhookSuspendWindow`). On the `true→false` transition the library auto-Posts a `{type:terminated}` control envelope to the (now-suspended) receiver as a courtesy notification — `webhook-id` prefix is `msg_terminated_<random>` so receivers can distinguish it from event deliveries (which use `evt_<eventId>`).
- A successful refresh of a suspended target reactivates it: clears `failureCount`, resets `lastError` and `failedSince`, flips `active` back to true.

**Fast-mode tip:** with the default `make serve` (`-webhook-suspend-threshold 5`), this step demonstrates ζ-5 (lastError populated, failedSince populated, active still true) — full ζ-6 suspend takes 5 failed deliveries × ~8.5s each. To see ζ-6 fire after ONE failure (~12s total step time), restart the server with `make serve-fast-suspend` (sets `-webhook-suspend-threshold 1`).

### Step 10: Spec validation: empty delivery.secret is rejected

delivery.secret is REQUIRED on every events/subscribe — no server-side fallback per spec.

- Server rejects at subscribe time so a subscription never exists that produces unverifiable deliveries.
- The Go SDK auto-generates a conforming whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty.
- This step makes a raw client.Call to bypass the SDK and demonstrate the server-side validator directly.

### Step 11: Spec validation: malformed delivery.secret is rejected

The validator enforces the full Standard Webhooks format: `whsec_` followed by base64 of 24-64 random bytes.

- A non-prefixed value, a too-short value, or non-base64 garbage all fail with -32602 InvalidParams.
- Catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.

### Step 12: Spec validation: client-supplied id is rejected

Spec §"Subscription Identity" → "Key composition" L363: "There is no client-generated id — a subscription is fully determined by what it listens for, where it delivers, and who asked."

- Server derives the id from (principal, name, params, url) and returns it.
- Old SDKs sending an id field get a loud -32602 instead of a silent mis-keying.

### Step 13: Spec validation: valid whsec_ accepted; response carries server-derived id, no secret

Counter-test: a freshly-generated whsec_ value is accepted.

- Response carries the server-derived id (sub_<base64>) per spec §"Subscription Identity" → "Derived id" L367.
- The id is non-load-bearing for security; surfaced as X-MCP-Subscription-Id on delivery POSTs (γ-4 wires the header).
- Response does NOT echo the secret — the client supplied it. Echoing would risk leaks via proxies / logs / IDE network panes.

### Step 14: Live Discord interaction (typing + message from a real Discord channel)

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
