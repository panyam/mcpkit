# Telegram Events Example

Reference server demonstrating the [MCP Events spec](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) with Telegram as the event source. Built on the [`experimental/ext/events`](../ext/events/) library.

Companion to [Clare Liguori's TypeScript implementation](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/tree/main/telegram-reference-server).

## Quick Start

```bash
# Test mode (no Telegram — use make inject)
make run

# With Telegram bot
TELEGRAM_BOT_TOKEN=your-token make run
```

Get a bot token from [@BotFather](https://t.me/BotFather) (`/newbot`).

## Injecting Messages

```bash
make inject TEXT="hello world"
make inject TEXT="test" SENDER="alice" CHAT_ID=123
```

## Diagnostics

```bash
make diag    # init → tools/list → events/list → events/poll → resources
make test    # 21 Go tests
```

## Testing the Three Delivery Modes

### Poll (`events/poll`)

Client calls `events/poll` with a cursor. Server returns events since that cursor.

```bash
make diag   # Look for the "events/poll" section
```

Cursor flow: poll with `"0"` → get events + cursor `"3"` → poll with `"3"` → nothing new → inject → poll again → get new event.

### Push (SSE broadcast)

Server broadcasts events to all connected SSE clients in real-time.

```bash
# Terminal 1: start SSE listener
SID=$(curl -s -D- -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"sse","version":"1.0"},"capabilities":{}}}' \
  | grep -i Mcp-Session-Id | awk '{print $2}' | tr -d '\r')
curl -s -X POST http://localhost:8080/mcp -H 'Content-Type: application/json' \
  -H "Mcp-Session-Id: $SID" -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
curl -N http://localhost:8080/mcp -H "Mcp-Session-Id: $SID" -H "Accept: text/event-stream"

# Terminal 2: inject a message
make inject TEXT="push me"
```

Event appears instantly in Terminal 1 as `notifications/events/event`.

### Webhook (`events/subscribe`)

Server POSTs HMAC-signed events to a registered callback URL.

```bash
# Terminal 1: simple receiver
nc -l 9999

# Terminal 2: subscribe (use SID from above)
curl -s -N http://localhost:8080/mcp -H 'Content-Type: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"events/subscribe","params":{"id":"wh-1","name":"telegram.message","delivery":{"mode":"webhook","url":"http://localhost:9999","secret":"my-secret"}}}' \
  | grep '^data:' | sed 's/^data: //' | jq .

# Terminal 3: inject
make inject TEXT="webhook me"
```

POST arrives in Terminal 1 with `X-MCP-Signature` and `X-MCP-Timestamp` headers.

## Architecture

```
Telegram Bot (long-poll)  ──or──  POST /inject
                │                       │
                ▼                       ▼
          MessageStore (in-memory ring buffer)
                │
                ├──► events.Emit()              → Push (SSE)
                ├──► srv.NotifyResourceUpdated() → Resource subscribers
                └──► events.EmitToWebhooks()     → Webhook (HMAC POST)
```

The Telegram-specific code is ~200 LoC. Protocol methods, webhook delivery, and HMAC signing come from the [`events` library](../ext/events/).

## Make Targets

| Target | Description |
|--------|-------------|
| `make run` | Start server (with bot if `TELEGRAM_BOT_TOKEN` set) |
| `make test` | 21 Go tests |
| `make diag` | Full diagnostic sequence |
| `make inject TEXT="..."` | Inject a message (optional: `SENDER=`, `CHAT_ID=`) |
