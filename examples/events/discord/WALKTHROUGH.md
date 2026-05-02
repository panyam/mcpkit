# MCP Events Extension — Discord reference walkthrough

Walks through the four delivery modes of the experimental MCP Events extension (events/list, push via SSE, poll, webhook with TTL refresh) plus the cursored vs cursorless source distinction. Webhook subscriber uses the typed Go SDK at experimental/ext/events/clients/go.

## What you'll learn

- **Connect to the events server** — The mcpkit client opens a GET SSE stream so push notifications reach us during later steps. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. A small in-process broker fans out `notifications/events/event` by name so each step can subscribe just to what it cares about.
- **events/list — see the source catalog** — The new `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.
- **Push: inject a message, observe SSE notification** — The /inject endpoint is the demo's stand-in for a real Discord WebSocket handler; in production the bot's MessageCreate handler calls yield(). Either way, the YieldingSource fans out: the library installs an emit hook that broadcasts the event to all SSE subscribers.
- **Poll: events/poll with the cursor we just saw** — Single-subscription per call (PR B removed batching). Polling at the head returns no new events but advances the cursor — the same response shape that would carry events if any had arrived since the last poll.
- **Cursorless: inject a typing event, observe cursor:null on the wire** — WithoutCursors() sources don't buffer and emit cursor:null. Push and webhook fanout still work — there's just nothing to replay. Useful for ephemeral state (typing indicators, presence, current readings).
- **Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh** — clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel). Per spec, the HMAC signing secret is client-supplied — the SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty, and exposes it via Subscription.Secret() so the receiver can verify with the same value. Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape, so the consumer reads `ev.Data.Content` rather than re-parsing JSON.
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

    Note over Host,Receiver: Step 7: Live Discord interaction (typing + message from a real Discord channel)
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

### Step 7: Live Discord interaction (typing + message from a real Discord channel)

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
