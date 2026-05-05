# MCP Events Extension — Telegram reference walkthrough

A condensed walkthrough showing the same MCP Events extension wired against a Telegram-shaped event source. The protocol exposition lives in the discord walkthrough; this one focuses on the telegram-specific payload (chat_id, user, text) and the cursored vs cursorless distinction.

## What you'll learn

- **Connect to the events server** — Plain MCP initialize over Streamable HTTP. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.
- **Push: open events/stream, inject a telegram message, observe per-call notifications** — events/stream is a long-lived per-subscription POST returning SSE — see the discord walkthrough for the full protocol exposition. Telegram's flat payload (chat_id, user, text) wires through the same Stream() helper as discord's nested one; only the Data shape changes.
- **Cursorless: open events/stream for telegram.typing, observe cursor:null** — Telegram's typing chat-action is ephemeral — no replay value, no buffer. Same WithoutCursors() story as discord.typing. Wire-shape contract per spec L294: cursorless emits cursor:null, never an empty string or absent key.
- **Webhook: subscribe via the typed Go SDK, receive a TelegramEventData** — Same `Subscription` + `Receiver[Data]` pair as the discord webhook step.
- **Live Telegram interaction (real message from a Telegram chat)** — Setup: start the server with a Telegram bot token and open a chat with the bot in the Telegram app.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)
    participant Receiver as Local webhook receiver (this process)

    Note over Host,Receiver: Step 1: Connect to the events server
    Host->>Server: POST /mcp — initialize
    Server-->>Host: serverInfo + capabilities

    Note over Host,Receiver: Step 2: Push: open events/stream, inject a telegram message, observe per-call notifications
    Host->>Server: events/stream { name: telegram.message }
    Server-->>Host: notifications/events/active { requestId, cursor }
    Receiver->>Server: POST /inject (simulated telegram message)
    Server-->>Host: notifications/events/event { requestId, data: {chat_id, user, text, ...} }

    Note over Host,Receiver: Step 3: Cursorless: open events/stream for telegram.typing, observe cursor:null
    Host->>Server: events/stream { name: telegram.typing }
    Server-->>Host: notifications/events/event { cursor: null }

    Note over Host,Receiver: Step 4: Webhook: subscribe via the typed Go SDK, receive a TelegramEventData
    Receiver->>Receiver: spin up local httptest receiver on :random
    Host->>Server: events/subscribe { mode: webhook, url, secret: whsec_<client-supplied>, name: telegram.message }
    Server-->>Host: { id, refreshBefore }   (response does NOT echo secret per spec)
    Receiver->>Server: POST /inject (simulated message)
    Server-->>Receiver: POST <url> + HMAC signature headers (default: webhook-* per Standard Webhooks; opt-in: X-MCP-* via -webhook-header-mode mcp)

    Note over Host,Receiver: Step 5: Live Telegram interaction (real message from a Telegram chat)
    Telegram->>Server: MessageCreate event (when you send a message to the bot)
    Server-->>Host: notifications/events/event { name: telegram.message, cursor: <new> }
```

## Steps

### Setup — two modes

This walkthrough runs against either a test-mode server or a real Telegram bot.

**Option A — Test mode** (no bot token needed). All steps run; the final live-interaction step skips with a 'no token' message. Drive synthetic events from a third terminal via `make inject` / `make inject-typing`.

```
Terminal 1:  make serve                                # server in test mode
Terminal 2:  make demo                                 # this walkthrough
Terminal 3:  make inject TEXT='hello'                  # message event
             make inject-typing                        # typing event (cursorless, demo-only)
```

**Option B — Real bot mode** (requires `TELEGRAM_BOT_TOKEN`). Same walkthrough plus the live step captures real message events from a chat with the bot. Telegram's Bot API doesn't expose user typing events to bots, so the live step is message-only — see the live step's note for details.

```
Terminal 1:  TELEGRAM_BOT_TOKEN=... make serve         # server in bot mode
Terminal 2:  make demo                                 # this walkthrough
             # In Telegram: send a message to the bot. Live step captures it.
```

### Why a separate telegram demo?

The two demos share the same `experimental/ext/events` library and the same wire protocol. The differences are only in the payload shape (telegram has flat `chat_id` / `text`; discord has nested `author` and richer fields) and the bot SDK used to source events (telegram's `tgbotapi` long-poll vs discord's `discordgo` WebSocket).

For the full protocol exposition (events/list, poll, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).

### Step 1: Connect to the events server

Plain MCP initialize over Streamable HTTP. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.

### Step 2: Push: open events/stream, inject a telegram message, observe per-call notifications

events/stream is a long-lived per-subscription POST returning SSE — see the discord walkthrough for the full protocol exposition. Telegram's flat payload (chat_id, user, text) wires through the same Stream() helper as discord's nested one; only the Data shape changes.

### Step 3: Cursorless: open events/stream for telegram.typing, observe cursor:null

Telegram's typing chat-action is ephemeral — no replay value, no buffer. Same WithoutCursors() story as discord.typing. Wire-shape contract per spec L294: cursorless emits cursor:null, never an empty string or absent key.

### Step 4: Webhook: subscribe via the typed Go SDK, receive a TelegramEventData

Same `Subscription` + `Receiver[Data]` pair as the discord webhook step.

- Receiver[TelegramEventData] decodes the wire envelope's Data field directly into TelegramEventData — consumer reads `ev.Data.Text`, no re-parsing JSON.
- The only differences from discord: the type parameter and the payload field names.
- SDK auto-generates a whsec_ secret when SubscribeOptions.Secret is empty (events.GenerateSecret).

### Step 5: Live Telegram interaction (real message from a Telegram chat)

Setup: start the server with a Telegram bot token and open a chat with the bot in the Telegram app.

```
TELEGRAM_BOT_TOKEN=<your-token> make serve
```

Bot setup (BotFather token, chat link) is documented in this demo's README.md.

- No typing parallel here — Telegram's Bot API doesn't expose user typing events to bots (only the bot can send typing chat actions, not the other way).
- Discord does have user-typing events; see ../discord/WALKTHROUGH.md for the live-typing demo.
- --non-interactive mode skips the wait so CI runs aren't slowed.

### More

For the full protocol walkthrough (events/list, poll, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).

Both demos share `experimental/ext/events` (library), `clients/go/` (Go SDK), and `clients/python/events_client.py` (Python SDK).

## Run it

```bash
go run ./examples/events/telegram/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/events/telegram/ --non-interactive
```
