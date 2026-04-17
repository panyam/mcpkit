# experimental/ext/events

> **EXPERIMENTAL** — this package will change as the triggers-events-wg iterates on the spec.

Go library for adding [MCP Events](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1) to an mcpkit server. Implements the protocol methods (`events/list`, `events/poll`, `events/subscribe`, `events/unsubscribe`) and webhook delivery so you only write the event source.

## Usage

```go
import "github.com/panyam/mcpkit/experimental/ext/events"
```

### 1. Define your event data type

```go
type AlertData struct {
    Severity string `json:"severity" jsonschema:"enum=P1,enum=P2,enum=P3"`
    Service  string `json:"service"`
    Message  string `json:"message"`
}
```

### 2. Create a typed event source

`payloadSchema` is auto-derived from your struct (same as `core.TypedTool`):

```go
source := events.TypedSource[AlertData](events.EventDef{
    Name:        "incident.created",
    Description: "Fires when a new incident is created",
    Delivery:    []string{"push", "poll", "webhook"},
}, func(cursor string, limit int) events.PollResult {
    // Your cursor-based retrieval logic here.
    // Cursor is an opaque string — you define the format.
    return events.PollResult{
        Events: myEvents,
        Cursor: nextCursor,
    }
})
```

### 3. Register on the server

```go
webhooks := events.NewWebhookRegistry()

events.Register(events.Config{
    Sources:  []events.EventSource{source},
    Webhooks: webhooks, // nil to disable webhook delivery
    Server:   srv,
})
```

This registers four protocol methods:

| Method | Description |
|--------|-------------|
| `events/list` | Returns event definitions with auto-derived `payloadSchema` |
| `events/poll` | Cursor-based polling, `hasMore` + `cursorGap` signals |
| `events/subscribe` | Webhook registration with HMAC secret + TTL + `refreshBefore` |
| `events/unsubscribe` | Webhook removal by `(url, id)` |

### 4. Wire push + webhook fan-out

```go
store.OnNewEvent = func(data AlertData, id, cursor string, ts time.Time) {
    event := events.MakeEvent[AlertData]("incident.created", id, cursor, ts, data)
    events.Emit(srv, event)                   // push to SSE clients
    events.EmitToWebhooks(webhooks, event)    // POST to webhook subscribers
}
```

## Key Types

```go
// Event is the wire-format envelope.
type Event struct {
    EventID   string          `json:"eventId"`
    Name      string          `json:"name"`
    Timestamp string          `json:"timestamp"`
    Data      json.RawMessage `json:"data"`
    Cursor    string          `json:"cursor"`
}

// EventSource is what you implement.
type EventSource interface {
    Def() EventDef
    Poll(cursor string, limit int) PollResult
}

// PollResult includes CursorGap — true when the client's cursor
// points to evicted events (ring buffer wrapped). Not in Peter's
// spec — an mcpkit extension to signal silent event loss.
type PollResult struct {
    Events    []Event
    Cursor    string
    CursorGap bool
}
```

## Webhook Delivery

- HMAC-SHA256 signing: `HMAC(secret, timestamp + "." + body)`
- Headers: `X-MCP-Signature`, `X-MCP-Timestamp`
- TTL-based soft state with automatic expiry
- Keyed by `(url, id)` per spec (unauthenticated servers)
- Retry with exponential backoff on 5xx (no retry on 4xx)
- Basic SSRF validation on callback URLs

## Spec Alignment

Based on [Peter Alexander's design sketch](https://github.com/modelcontextprotocol/experimental-ext-triggers-events/pull/1). Notable choices:

| Topic | Our Approach |
|-------|-------------|
| **Cursors** | Opaque strings — source defines format |
| **`cursorGap`** | mcpkit extension (not in spec) — signals events lost to buffer wrap |
| **`nextPollSeconds`** | Per-subscription (follows spec schema); client SDK coalesces |
| **`events/stream`** | Deferred — push uses `Server.Broadcast` for now |
| **Typed contexts** | Handlers receive `core.MethodContext` (EmitLog, AuthClaims, etc.) |
