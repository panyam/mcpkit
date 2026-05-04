# MCP Events Extension — Discord reference walkthrough

Walks through the four delivery modes of the experimental MCP Events extension (events/list, push via SSE, poll, webhook with TTL refresh) plus the cursored vs cursorless source distinction. Webhook subscriber uses the typed Go SDK at experimental/ext/events/clients/go.

## What you'll learn

- **Connect to the events server** — The mcpkit client opens a GET SSE stream so push notifications reach us during later steps. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. A small in-process broker fans out `notifications/events/event` by name so each step can subscribe just to what it cares about.
- **events/list — see the source catalog** — The new `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.
- **Push: inject a message, observe SSE notification** — The /inject endpoint is the demo's stand-in for a real Discord WebSocket handler; in production the bot's MessageCreate handler calls yield(). Either way, the YieldingSource fans out: the library installs an emit hook that broadcasts the event to all SSE subscribers.
- **Poll: events/poll with the cursor we just saw** — Single-subscription per call (PR B removed batching). Polling at the head returns no new events but advances the cursor — the same response shape that would carry events if any had arrived since the last poll.
- **Cursorless: inject a typing event, observe cursor:null on the wire** — WithoutCursors() sources don't buffer and emit cursor:null. Push and webhook fanout still work — there's just nothing to replay. Useful for ephemeral state (typing indicators, presence, current readings).
- **Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh** — clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel). Per spec, the HMAC signing secret is client-supplied — the SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty, and exposes it via Subscription.Secret() so the receiver can verify with the same value. Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape, so the consumer reads `ev.Data.Content` rather than re-parsing JSON.
- **Spec validation: empty delivery.secret is rejected** — Per spec, delivery.secret is REQUIRED on every events/subscribe — there is no server-side fallback. The server rejects at subscribe time so a subscription never exists that produces unverifiable deliveries. The Go SDK auto-generates a conforming whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty; this step makes a raw client.Call to bypass that and demonstrate the validator.
- **Spec validation: malformed delivery.secret is rejected** — The validator enforces the full Standard Webhooks format: whsec_ followed by base64 of 24-64 random bytes. A non-prefixed value, a too-short value, or non-base64 garbage all fail with -32602 InvalidParams. This is what catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.
- **Spec validation: client-supplied id is rejected** — Per spec §"Subscription Identity" → "Key composition" L363: "There is no client-generated id — a subscription is fully determined by what it listens for, where it delivers, and who asked." The server derives the id from (principal, name, params, url) and returns it; old SDKs sending an id field get a loud -32602 instead of a silent mis-keying.
- **Spec validation: valid whsec_ accepted; response carries server-derived id, no secret** — Counter-test: a freshly-generated whsec_ value is accepted. The response carries the server-derived id (sub_<base64>) per spec §"Subscription Identity" → "Derived id" L367 — non-load-bearing for security, surfaced as X-MCP-Subscription-Id on delivery POSTs (γ-4 wires the header). The response does NOT echo the secret because the client already supplied it; echoing would risk leaks via proxies / logs / IDE network panes.
- **Live Discord interaction (typing + message from a real Discord channel)** — Requires the server to be running with -token + the bot invited to a channel you can post in. The TypingStart handler in main.go yields a cursorless discord.typing event; MessageCreate yields the cursored discord.message. Discord's typing indicator fires once when you start (then refires every ~8s if you keep typing), not per keystroke. In --non-interactive mode this step skips the wait so CI runs aren't slowed.

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

    Note over Host,Receiver: Step 3: Push: inject a message, observe SSE notification
    Receiver->>Server: POST /inject (simulated Discord message)
    Server-->>Host: notifications/events/event (via SSE)

    Note over Host,Receiver: Step 4: Poll: events/poll with the cursor we just saw
    Host->>Server: events/poll {subscriptions: [{name: discord.message, cursor: <head>}]}
    Server-->>Host: {events: [], cursor: <head>, hasMore: false}

    Note over Host,Receiver: Step 5: Cursorless: inject a typing event, observe cursor:null on the wire
    Receiver->>Server: POST /inject?event=discord.typing
    Server-->>Host: notifications/events/event { cursor: null }

    Note over Host,Receiver: Step 6: Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh
    Receiver->>Receiver: spin up local httptest receiver on :random
    Host->>Server: events/subscribe { mode: webhook, url, secret: whsec_<client-supplied> }
    Server-->>Host: { id, refreshBefore }   (response does NOT echo secret per spec)
    Receiver->>Server: POST /inject (simulated message)
    Server-->>Receiver: POST <url> + HMAC signature headers (default: webhook-* per Standard Webhooks; opt-in: X-MCP-* via -webhook-header-mode mcp)
    Host-->>Host: background loop: re-subscribe at 0.5 × TTL

    Note over Host,Receiver: Step 7: Spec validation: empty delivery.secret is rejected
    Host->>Server: events/subscribe { delivery: { ... } }   (no secret)
    Server-->>Host: -32602 InvalidParams: delivery.secret is required

    Note over Host,Receiver: Step 8: Spec validation: malformed delivery.secret is rejected
    Host->>Server: events/subscribe { delivery: { secret: 'wrong' } }
    Server-->>Host: -32602 InvalidParams: delivery.secret invalid: must start with the whsec_ prefix

    Note over Host,Receiver: Step 9: Spec validation: client-supplied id is rejected
    Host->>Server: events/subscribe { id: 'mine', ... }
    Server-->>Host: -32602 InvalidParams: client-supplied id is not accepted

    Note over Host,Receiver: Step 10: Spec validation: valid whsec_ accepted; response carries server-derived id, no secret
    Host->>Host: events.GenerateSecret() → whsec_<base64 of 32 bytes>
    Host->>Server: events/subscribe { delivery: { secret: whsec_<valid> } }
    Server-->>Host: { id: sub_<base64-of-16-bytes>, cursor, refreshBefore }   (no secret per spec)

    Note over Host,Receiver: Step 11: Live Discord interaction (typing + message from a real Discord channel)
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

The mcpkit client opens a GET SSE stream so push notifications reach us during later steps. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. A small in-process broker fans out `notifications/events/event` by name so each step can subscribe just to what it cares about.

### Step 2: events/list — see the source catalog

The new `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.

### Step 3: Push: inject a message, observe SSE notification

The /inject endpoint is the demo's stand-in for a real Discord WebSocket handler; in production the bot's MessageCreate handler calls yield(). Either way, the YieldingSource fans out: the library installs an emit hook that broadcasts the event to all SSE subscribers.

### Step 4: Poll: events/poll with the cursor we just saw

Single-subscription per call (PR B removed batching). Polling at the head returns no new events but advances the cursor — the same response shape that would carry events if any had arrived since the last poll.

### Step 5: Cursorless: inject a typing event, observe cursor:null on the wire

WithoutCursors() sources don't buffer and emit cursor:null. Push and webhook fanout still work — there's just nothing to replay. Useful for ephemeral state (typing indicators, presence, current readings).

### Step 6: Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh

clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel). Per spec, the HMAC signing secret is client-supplied — the SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty, and exposes it via Subscription.Secret() so the receiver can verify with the same value. Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape, so the consumer reads `ev.Data.Content` rather than re-parsing JSON.

### Step 7: Spec validation: empty delivery.secret is rejected

Per spec, delivery.secret is REQUIRED on every events/subscribe — there is no server-side fallback. The server rejects at subscribe time so a subscription never exists that produces unverifiable deliveries. The Go SDK auto-generates a conforming whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty; this step makes a raw client.Call to bypass that and demonstrate the validator.

### Step 8: Spec validation: malformed delivery.secret is rejected

The validator enforces the full Standard Webhooks format: whsec_ followed by base64 of 24-64 random bytes. A non-prefixed value, a too-short value, or non-base64 garbage all fail with -32602 InvalidParams. This is what catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.

### Step 9: Spec validation: client-supplied id is rejected

Per spec §"Subscription Identity" → "Key composition" L363: "There is no client-generated id — a subscription is fully determined by what it listens for, where it delivers, and who asked." The server derives the id from (principal, name, params, url) and returns it; old SDKs sending an id field get a loud -32602 instead of a silent mis-keying.

### Step 10: Spec validation: valid whsec_ accepted; response carries server-derived id, no secret

Counter-test: a freshly-generated whsec_ value is accepted. The response carries the server-derived id (sub_<base64>) per spec §"Subscription Identity" → "Derived id" L367 — non-load-bearing for security, surfaced as X-MCP-Subscription-Id on delivery POSTs (γ-4 wires the header). The response does NOT echo the secret because the client already supplied it; echoing would risk leaks via proxies / logs / IDE network panes.

### Step 11: Live Discord interaction (typing + message from a real Discord channel)

Requires the server to be running with -token + the bot invited to a channel you can post in. The TypingStart handler in main.go yields a cursorless discord.typing event; MessageCreate yields the cursored discord.message. Discord's typing indicator fires once when you start (then refires every ~8s if you keep typing), not per keystroke. In --non-interactive mode this step skips the wait so CI runs aren't slowed.

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
