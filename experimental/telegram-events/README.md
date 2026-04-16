# Telegram Events Reference Server

Go implementation of the [MCP Events design sketch](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) (triggers-events-wg), demonstrating all three delivery modes with Telegram as the event source.

Companion to [Clare Liguori's TypeScript implementation](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/tree/main/telegram-reference-server).

## Quick Start

```bash
# Start without Telegram (inject messages manually)
make run

# Start with Telegram bot
TELEGRAM_MCPKIT_BOTTOKEN=your-token make run
```

Get a bot token from [@BotFather](https://t.me/BotFather) on Telegram (`/newbot`).

## Testing the Three Delivery Modes

### Setup: Inject a Message

You don't need Telegram to test. Inject messages directly:

```bash
make inject TEXT="hello world"
make inject TEXT="second message" SENDER="alice"
make inject TEXT="third message" SENDER="bob"
```

Server log shows: `[inject] id=1 sender=test-user text="hello world"`

With Telegram running, just message your bot — same effect.

---

### Mode 1: Poll (`events/poll` protocol method)

**What it is:** Client calls `events/poll` with a cursor. Server returns events since that cursor. Stateless — no subscription needed.

**How to test:**

```bash
# Run full diagnostic (includes poll)
make diag
```

Look for the `events/poll` section in the output:

```json
{
  "id": "diag",
  "events": [
    {
      "eventId": "evt_1",
      "name": "telegram.message",
      "data": { "user": "test-user", "text": "hello world" },
      "cursor": "1"
    }
  ],
  "cursor": "1",
  "hasMore": false,
  "nextPollSeconds": 5
}
```

**How cursor works:**
- First poll with `cursor: "0"` → returns all messages, cursor advances to `"3"`
- Next poll with `cursor: "3"` → returns nothing (no new messages)
- Inject another message, poll with `cursor: "3"` → returns just the new one

**Try it manually:**

```bash
# Initialize a session
SID=$(curl -s -D- -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"test","version":"1.0"},"capabilities":{}}}' \
  | grep -i Mcp-Session-Id | awk '{print $2}' | tr -d '\r')

# Send initialized notification
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

# Poll from the beginning
curl -s -N http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"events/poll","params":{"subscriptions":[{"id":"my-sub","name":"telegram.message","cursor":"0"}]}}' \
  | grep '^data:' | sed 's/^data: //' | jq .
```

---

### Mode 2: Push (SSE broadcast)

**What it is:** Server broadcasts `notifications/events/event` to all connected SSE clients in real-time. No polling needed — events arrive instantly.

**How to test:**

Open a terminal and start an SSE listener:

```bash
# Initialize + get session ID (same as above)
SID=$(curl -s -D- -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"sse-client","version":"1.0"},"capabilities":{}}}' \
  | grep -i Mcp-Session-Id | awk '{print $2}' | tr -d '\r')

curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

# Open SSE stream (this blocks — leave it running)
curl -N http://localhost:8080/mcp \
  -H "Mcp-Session-Id: $SID" \
  -H "Accept: text/event-stream"
```

In another terminal, inject a message:

```bash
make inject TEXT="push me"
```

**What you should see** in the SSE terminal:

```
id: 2
event: message
data: {"jsonrpc":"2.0","method":"notifications/events/event","params":{"eventId":"evt_4","name":"telegram.message","data":{"chat_id":"100","user":"test-user","text":"push me"},"cursor":"4"}}
```

The event appears immediately — no polling required.

---

### Mode 3: Webhook (`events/subscribe` protocol method)

**What it is:** Client registers a callback URL via `events/subscribe`. Server POSTs HMAC-SHA256 signed events to that URL.

**How to test:**

Start a simple webhook receiver in one terminal:

```bash
# Listen on port 9999 and print what arrives
nc -l 9999
```

(Or use a tool like https://webhook.site for a public URL.)

In another terminal, subscribe via the protocol method:

```bash
# Use an existing session (SID from above)
curl -s -N http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"events/subscribe","params":{"id":"wh-1","name":"telegram.message","delivery":{"mode":"webhook","url":"http://localhost:9999","secret":"my-secret"}}}' \
  | grep '^data:' | sed 's/^data: //' | jq .
```

Then inject a message:

```bash
make inject TEXT="webhook me"
```

**What you should see** in the nc terminal: an HTTP POST with JSON body and `X-Signature-256` header:

```
POST / HTTP/1.1
Content-Type: application/json
X-Signature-256: sha256=<hex>

{"eventId":"evt_5","name":"telegram.message","data":{"chat_id":"100","user":"test-user","text":"webhook me"},"cursor":"5"}
```

The signature can be verified: `HMAC-SHA256(body, "my-secret")`.

---

### Mode Summary

| Mode | Protocol Method | Trigger | Where Events Appear |
|------|----------------|---------|-------------------|
| **Poll** | `events/poll` | Client calls on a cadence | In the poll response |
| **Push** | N/A (broadcast) | Automatic on new message | On the SSE stream |
| **Webhook** | `events/subscribe` | Automatic on new message | POSTed to callback URL |

---

## Other Protocol Methods

| Method | Description |
|--------|-------------|
| `events/list` | Returns available event types and supported delivery modes |
| `events/unsubscribe` | Removes a webhook subscription |

## MCP Resources

| URI | Description |
|-----|-------------|
| `telegram://messages/recent` | Last 50 messages as JSON |
| `telegram://message/{id}` | Single message by ID |

## MCP Tools

| Tool | Description |
|------|-------------|
| `send_message` | Send a message via the Telegram bot (requires bot token) |

## Make Targets

| Target | Description |
|--------|-------------|
| `make run` | Start server (with bot if `TELEGRAM_MCPKIT_BOTTOKEN` is set) |
| `make test` | Run all 14 Go tests |
| `make diag` | Full diagnostic: init → tools/list → events/poll → resources |
| `make inject TEXT="..."` | Inject a message directly (optional: `SENDER=`, `CHAT_ID=`) |

## Architecture

```
Telegram Bot (long-poll)  ──or──  POST /inject
                │                       │
                ▼                       ▼
          MessageStore (in-memory ring buffer, monotonic IDs)
                │
                ├──► Server.Broadcast()              → Push (SSE)
                ├──► Server.NotifyResourceUpdated()   → Resource subscribers
                └──► WebhookRegistry.Deliver()        → Webhook (HMAC POST)
```

## Spec Alignment

Maps to [Peter Alexander's design sketch](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1):

| Spec Concept | Implementation |
|---|---|
| `events/list` | Custom method via `HandleMethod` (#266) |
| `events/poll` | Custom method — cursor-based, server stateless |
| `events/subscribe` | Custom method — webhook with HMAC secret |
| `events/stream` (push) | `Server.Broadcast` + SSE GET stream (deferred: full `events/stream` request lifecycle) |
| Event envelope | `TelegramEvent{eventId, name, timestamp, data, cursor}` |
| Cursor lifecycle | Monotonic message IDs, client tracks position |
