# Telegram Events Example

Reference server demonstrating the [MCP Events spec](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) with Telegram as the event source. Built on the [`experimental/ext/events`](../ext/events/) library.

Companion to [Clare Liguori's TypeScript implementation](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/tree/main/telegram-reference-server).

## Quick Start

```bash
# Terminal 1: start server in test mode (no Telegram needed)
make run

# Terminal 2: start SSE listener
make listen

# Terminal 3: inject a message
make inject TEXT="hello world"
# → event appears instantly in Terminal 2
```

With a real Telegram bot:
```bash
TELEGRAM_BOT_TOKEN=your-token make run
```

Get a bot token from [@BotFather](https://t.me/BotFather) (`/newbot`).

## Three Delivery Modes

All three modes work simultaneously from the same server.

### Push — `make listen`

Client opens a long-lived SSE connection. Server broadcasts events in real time.

```mermaid
sequenceDiagram
    participant C as Client (listen)
    participant S as MCP Server
    participant T as Telegram / inject

    C->>S: POST initialize
    S-->>C: 200 + Mcp-Session-Id
    C->>S: POST notifications/initialized
    C->>S: GET /mcp (SSE stream open)
    Note over C,S: Connection held open

    T->>S: Message arrives
    S->>S: yield() → store + library fanout hook
    S-->>C: SSE: notifications/events/event
    T->>S: Another message
    S-->>C: SSE: notifications/events/event
```

### Poll — `make poll`

Client calls `events/poll` on an interval. Cursor-based — never misses events.

```mermaid
sequenceDiagram
    participant C as Client (poll)
    participant S as MCP Server

    C->>S: POST initialize + initialized
    C->>S: events/poll (cursor="0")
    S-->>C: events=[], cursor="5"
    Note over C: sleep(interval)

    C->>S: events/poll (cursor="5")
    S-->>C: events=[msg6, msg7], cursor="7"
    Note over C: process events

    C->>S: events/poll (cursor="7")
    S-->>C: events=[], cursor="7"
    Note over C: nothing new, sleep
```

### Webhook — `make webhook`

Client registers a callback URL. Server POSTs HMAC-signed events to it.

```mermaid
sequenceDiagram
    participant C as Client (webhook receiver)
    participant S as MCP Server
    participant T as Telegram / inject

    C->>C: Start HTTP server on :9999
    C->>S: POST initialize + initialized
    C->>S: events/subscribe (url=localhost:9999, secret=...)
    S-->>C: 200 (id, refreshBefore)

    T->>S: Message arrives
    S->>S: yield() → store + library fanout hook
    S->>C: POST http://localhost:9999<br/>X-MCP-Signature: sha256=...<br/>X-MCP-Timestamp: 1714000000
    C->>C: Verify HMAC, print event

    Note over C,S: WebhookSubscription helper auto-refreshes at<br/>0.5×TTL (60s default) and re-subscribes if<br/>the boundary race produces a "not found"
```

## Architecture

```
Telegram Bot (long-poll)  ──or──  POST /inject
                │                       │
                ▼                       ▼
                yield(TelegramEventData{...})    ← user code: one call
                │
                │  YieldingSource (library):
                │    - assigns cursor + event ID
                │    - stores in bounded ring (1000 max)
                │    - calls library-installed fanout hook
                ▼
                ├──► events.Emit()              → Push (SSE broadcast)
                └──► events.EmitToWebhooks()     → Webhook (HMAC POST)
                                                   ▲
                                              events/poll reads from
                                              the same source's buffer

  Resource handlers read typed payloads via:
    source.Recent(50)         → []TelegramEventData
    source.ByCursor("42")     → (TelegramEventData, true)
```

## Make Targets

| Target | Description |
|--------|-------------|
| `make run` | Start server (with bot if `TELEGRAM_BOT_TOKEN` set) |
| `make test` | Go tests |
| `make inject TEXT="..."` | Inject a message (optional: `SENDER=`, `CHAT_ID=`) |
| `make list` | Show server capabilities: tools, resources, events, sample poll |
| `make listen` | SSE push listener — print events in real time |
| `make webhook` | Webhook receiver — subscribe + auto-refresh, receive HMAC-signed POSTs |
| `make poll` | Polling loop (default 5s interval, override: `INTERVAL=10`) |

All client commands use the shared [`events_client.py`](../ext/events/events_client.py).
