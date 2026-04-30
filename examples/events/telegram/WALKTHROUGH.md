# MCP Events Extension — Telegram reference walkthrough

A condensed walkthrough showing the same MCP Events extension wired against a Telegram-shaped event source. The protocol exposition lives in the discord walkthrough; this one focuses on the telegram-specific payload (chat_id, user, text) and the cursored vs cursorless distinction.

## What you'll learn

- **Connect to the events server** — Same connection setup as discord. The notification broker fans `notifications/events/event` out by source name so each step subscribes to just what it cares about.
- **Push: inject a telegram message, observe SSE notification** — Telegram's payload is flat — chat_id, user, text — vs discord's nested author + content. Same library, same wire envelope, different Data shape (auto-derived from TelegramEventData).
- **Cursorless: telegram.typing emits cursor:null** — Telegram's typing chat-action is ephemeral — no replay value, no buffer. Same WithoutCursors() story as discord.typing; the wire payload differs only in shape.
- **Webhook: subscribe via the typed Go SDK, receive a TelegramEventData** — The typed Receiver[TelegramEventData] decodes the wire envelope's Data field directly into TelegramEventData, so the consumer reads `ev.Data.Text` rather than re-parsing JSON. Same `Subscription` + `Receiver[Data]` pair as the discord webhook step — the only differences are the type parameter and the payload field names.
- **Live Telegram interaction (real message from a Telegram chat)** — Requires the server to be running with -token + you having a chat open with the bot. No typing parallel here — Telegram's Bot API doesn't expose user typing events to bots (only the bot can send typing chat actions, not the other way around). Discord does have user-typing events; see ../discord/WALKTHROUGH.md for the live-typing demo. In --non-interactive mode this step skips the wait so CI runs aren't slowed.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)
    participant Receiver as Local webhook receiver (this process)

    Note over Host,Receiver: Step 1: Connect to the events server
    Host->>Server: POST /mcp — initialize
    Server-->>Host: serverInfo + capabilities

    Note over Host,Receiver: Step 2: Push: inject a telegram message, observe SSE notification
    Receiver->>Server: POST /inject (simulated telegram message)
    Server-->>Host: notifications/events/event { data: {chat_id, user, text, ...} }

    Note over Host,Receiver: Step 3: Cursorless: telegram.typing emits cursor:null
    Receiver->>Server: POST /inject?event=telegram.typing
    Server-->>Host: notifications/events/event { cursor: null }

    Note over Host,Receiver: Step 4: Webhook: subscribe via the typed Go SDK, receive a TelegramEventData
    Receiver->>Receiver: spin up local httptest receiver on :random
    Host->>Server: events/subscribe { mode: webhook, url, name: telegram.message }
    Server-->>Host: { id, secret: <server-assigned>, refreshBefore }
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

For the full protocol exposition (events/list, poll, secret modes, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).

### Step 1: Connect to the events server

Same connection setup as discord. The notification broker fans `notifications/events/event` out by source name so each step subscribes to just what it cares about.

### Step 2: Push: inject a telegram message, observe SSE notification

Telegram's payload is flat — chat_id, user, text — vs discord's nested author + content. Same library, same wire envelope, different Data shape (auto-derived from TelegramEventData).

### Step 3: Cursorless: telegram.typing emits cursor:null

Telegram's typing chat-action is ephemeral — no replay value, no buffer. Same WithoutCursors() story as discord.typing; the wire payload differs only in shape.

### Step 4: Webhook: subscribe via the typed Go SDK, receive a TelegramEventData

The typed Receiver[TelegramEventData] decodes the wire envelope's Data field directly into TelegramEventData, so the consumer reads `ev.Data.Text` rather than re-parsing JSON. Same `Subscription` + `Receiver[Data]` pair as the discord webhook step — the only differences are the type parameter and the payload field names.

### Step 5: Live Telegram interaction (real message from a Telegram chat)

Requires the server to be running with -token + you having a chat open with the bot. No typing parallel here — Telegram's Bot API doesn't expose user typing events to bots (only the bot can send typing chat actions, not the other way around). Discord does have user-typing events; see ../discord/WALKTHROUGH.md for the live-typing demo. In --non-interactive mode this step skips the wait so CI runs aren't slowed.

### More

For the full protocol walkthrough (events/list, poll, secret modes, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).

Both demos share `experimental/ext/events` (library), `clients/go/` (Go SDK), and `clients/python/events_client.py` (Python SDK).

## Run it

```bash
go run ./examples/events/telegram/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/events/telegram/ --non-interactive
```
