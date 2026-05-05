# MCP Events Extension — Discord reference walkthrough

Walks through the four delivery modes of the experimental MCP Events extension (events/list, push via SSE, poll, webhook with TTL refresh) plus the cursored vs cursorless source distinction. Webhook subscriber uses the typed Go SDK at experimental/ext/events/clients/go.

## What you'll learn

- **Connect to the events server** — Plain MCP initialize over Streamable HTTP. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.
- **events/list — see the source catalog** — The new `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.
- **Push: open events/stream, inject a message, observe per-call notifications** — events/stream is a long-lived JSON-RPC request — one per subscription. Spec §"Push-Based Delivery" L223-296.
- **Poll: events/poll with the cursor we just saw** — Single-subscription per call (PR B removed batching). Polling at the head returns no new events but advances the cursor — the same response shape that would carry events if any had arrived since the last poll.
- **Cursorless: open events/stream for typing, observe cursor:null on the wire** — WithoutCursors() sources don't buffer; the wire emits cursor:null.
- **Health signals: source bubbles a transient upstream failure → notifications/events/error** — Sources bubble health via YieldError(err) (transient, stream stays open) and YieldTerminated(err) (terminal, stream closes).
- **Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh** — clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel).
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

    Note over Host,Receiver: Step 8: Spec validation: empty delivery.secret is rejected
    Host->>Server: events/subscribe { delivery: { ... } }   (no secret)
    Server-->>Host: -32602 InvalidParams: delivery.secret is required

    Note over Host,Receiver: Step 9: Spec validation: malformed delivery.secret is rejected
    Host->>Server: events/subscribe { delivery: { secret: 'wrong' } }
    Server-->>Host: -32602 InvalidParams: delivery.secret invalid: must start with the whsec_ prefix

    Note over Host,Receiver: Step 10: Spec validation: client-supplied id is rejected
    Host->>Server: events/subscribe { id: 'mine', ... }
    Server-->>Host: -32602 InvalidParams: client-supplied id is not accepted

    Note over Host,Receiver: Step 11: Spec validation: valid whsec_ accepted; response carries server-derived id, no secret
    Host->>Host: events.GenerateSecret() → whsec_<base64 of 32 bytes>
    Host->>Server: events/subscribe { delivery: { secret: whsec_<valid> } }
    Server-->>Host: { id: sub_<base64-of-16-bytes>, cursor, refreshBefore }   (no secret per spec)

    Note over Host,Receiver: Step 12: Live Discord interaction (typing + message from a real Discord channel)
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

- **events/list** — the source catalog, including the new `cursorless` flag.
- **Push** — long-lived SSE stream; `notifications/events/event` arrives in real time.
- **Poll** — single-subscription `events/poll` (multi-sub batching is not supported).
- **Cursorless source** — typing indicators that wire as `cursor: null`. Subscribers can't replay, only see live events.
- **Webhook + auto-refresh** — `events/subscribe` with the typed `Subscription` + `Receiver[Data]` from `clients/go`.

Identity-mode subscribe and Standard Webhooks header naming are exercised by the unit tests in `experimental/ext/events/` and by `discord-events`'s e2e tests; they require the server to be started with mode flags so they're documented in the README rather than driven from this walkthrough.

### Step 1: Connect to the events server

Plain MCP initialize over Streamable HTTP. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.

### Step 2: events/list — see the source catalog

The new `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.

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

### Step 8: Spec validation: empty delivery.secret is rejected

delivery.secret is REQUIRED on every events/subscribe — no server-side fallback per spec.

- Server rejects at subscribe time so a subscription never exists that produces unverifiable deliveries.
- The Go SDK auto-generates a conforming whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty.
- This step makes a raw client.Call to bypass the SDK and demonstrate the server-side validator directly.

### Step 9: Spec validation: malformed delivery.secret is rejected

The validator enforces the full Standard Webhooks format: `whsec_` followed by base64 of 24-64 random bytes.

- A non-prefixed value, a too-short value, or non-base64 garbage all fail with -32602 InvalidParams.
- Catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.

### Step 10: Spec validation: client-supplied id is rejected

Spec §"Subscription Identity" → "Key composition" L363: "There is no client-generated id — a subscription is fully determined by what it listens for, where it delivers, and who asked."

- Server derives the id from (principal, name, params, url) and returns it.
- Old SDKs sending an id field get a loud -32602 instead of a silent mis-keying.

### Step 11: Spec validation: valid whsec_ accepted; response carries server-derived id, no secret

Counter-test: a freshly-generated whsec_ value is accepted.

- Response carries the server-derived id (sub_<base64>) per spec §"Subscription Identity" → "Derived id" L367.
- The id is non-load-bearing for security; surfaced as X-MCP-Subscription-Id on delivery POSTs (γ-4 wires the header).
- Response does NOT echo the secret — the client supplied it. Echoing would risk leaks via proxies / logs / IDE network panes.

### Step 12: Live Discord interaction (typing + message from a real Discord channel)

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
