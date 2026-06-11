# `HTTPSource` — events over HTTP from a remote source manager

`HTTPSource` is the third source pattern in `experimental/ext/events/`, sibling to `YieldingSource` and `TypedSource`. Use it when upstream-integration concerns belong in a **separate process** from MCP serving — for example the push-server tier in `examples/whole-enchilada/events/`, where Discord WebSocket lifecycle + OAuth refresh lives away from the MCP server that fans out to subscribers.

The wire is intentionally simple: JSON-encoded payloads POSTed to `{baseURL}/events/{eventName}/inject` with an optional `Authorization: Bearer <secret>` header. The event-server's `HTTPSource.Handler()` decodes, yields into a library-owned `YieldingSource`, and the rest of the library (push fanout, webhook delivery, `events/poll`, `events/list`) drives it the same way as any in-process source.

## When to pick each pattern

| Pattern | When | Source-side code | Library handles |
|---|---|---|---|
| `YieldingSource` | In-process callback or HTTP handler emits events at you | `yield(data)` | Buffering, cursor assignment, push + webhook fanout |
| `TypedSource` | You own the storage already (DB, log, external queue) | `Poll(cursor, limit) PollResult` | Wire encoding only |
| `HTTPSource` | Upstream integration is a different process | POST to `{base}/events/{name}/inject` | Receive handler, buffering, cursor, fanout |

The single-process demos (`discord/`, `telegram/`) use `YieldingSource` because their upstream integration lives inside the MCP server. The multi-tier demo (`whole-enchilada/`) uses `HTTPSource` because the push-server pushes events over HTTP from a different process.

## Event-server side — constructing and mounting

```go
import (
    "net/http"
    "github.com/panyam/mcpkit/experimental/ext/events"
)

type ChatMessage struct {
    Sender string `json:"sender"`
    Text   string `json:"text"`
}

chatDef := events.EventDef{
    Name:        "chat.message",
    Description: "Chat messages from the push-server",
    Delivery:    []string{"push", "poll", "webhook"},
}

chatSrc := events.NewHTTPSource[ChatMessage](chatDef, events.HTTPSourceConfig{
    Bearer: os.Getenv("EVENT_INJECT_BEARER"),
    YieldingOpts: []events.YieldingOption{
        events.WithMaxSize(1000),
    },
})

// 1. Register with the events library — same as any other source.
events.Register(events.Config{
    Sources:  []events.EventSource{chatSrc},
    Webhooks: webhooks,
    Server:   mcpServer,
})

// 2. Mount the inject handler on whatever mux your server already uses
//    for non-MCP HTTP (admin, healthcheck, etc.).
mux := http.NewServeMux()
mux.Handle(chatSrc.InjectPath(), chatSrc.Handler()) // /events/chat.message/inject
http.ListenAndServe(":8080", mux)
```

`HTTPSource` satisfies the `EventSource` interface, so `events.Register` discovers it the same way it discovers a `YieldingSource`. The fanout hook (push + webhook on yield) is installed automatically via the `emitterAware` path.

For typed reads (powering MCP resources that show recent payloads), the same accessors as `YieldingSource` are exposed:

```go
recent := chatSrc.Recent(50)                  // []ChatMessage
one, ok := chatSrc.ByCursor("42")             // (ChatMessage, bool)
```

## Push-server side — constructing the typed client

```go
import (
    "context"
    eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

pusher := eventsclient.NewPusher(
    "http://event-server:8080",
    os.Getenv("EVENT_INJECT_BEARER"),
)

// Inside your upstream callback:
err := pusher.PushNamed(ctx, "chat.message", ChatMessage{
    Sender: "alice",
    Text:   "hello",
})
if err != nil {
    var pe *eventsclient.PushError
    if errors.As(err, &pe) {
        log.Printf("inject rejected: status=%d body=%s", pe.StatusCode, pe.Body)
    }
    // else transport error
}
```

The wire shape is symmetrical: whatever struct you `PushNamed`, the event-server's `HTTPSource[T]` decodes via `json.Unmarshal` into its `T`. Keep the type definitions shared between the two processes (a shared `wire/` package, copy-paste, or codegen — your call).

## HTTP status codes

| Status | Meaning |
|---|---|
| `202 Accepted` | Event was decoded and yielded. The library's fanout to push / webhook subscribers happens asynchronously after this response. |
| `400 Bad Request` | JSON decode failed. |
| `401 Unauthorized` | `HTTPSourceConfig.Bearer` is set and the request's `Authorization` header is missing or wrong. |
| `405 Method Not Allowed` | Non-POST request. The `Allow` header carries `POST`. |
| `413 Payload Too Large` | Body exceeds `HTTPSourceConfig.MaxBodyBytes` (default 1 MiB). |
| `500 Internal Server Error` | `YieldingSource.yield` rejected the event (terminated source). |

`Pusher` distinguishes status-code rejections from transport errors via the `*PushError` type — use `errors.As` to inspect.

## Mounting many sources on one mux

Each `HTTPSource` exposes `InjectPath()` (default `/events/<name>/inject`). Loop and mount:

```go
sources := []events.EventSource{chatSrc, presenceSrc, alertSrc}
events.Register(events.Config{Sources: sources, Webhooks: wh, Server: srv})

mux := http.NewServeMux()
for _, s := range []interface{ InjectPath() string; Handler() http.Handler }{chatSrc, presenceSrc, alertSrc} {
    mux.Handle(s.InjectPath(), s.Handler())
}
```

(The structural interface in the loop captures the two methods every `HTTPSource[T]` exposes regardless of `T`.)

If your event-server already hosts an HTTP admin surface, mount the inject paths on the same mux — they're namespaced under `/events/` so there's no collision with `/admin/` or `/mcp/`.

## Bearer rotation

For stage-1 deployments, the bearer is a static shared secret. Pass it via environment variable to both the event-server (`HTTPSourceConfig.Bearer`) and the push-server (`NewPusher(..., bearer)`). Rotate by restarting both with a new value.

For production hardening (rotation without downtime, per-tenant bearers, signed envelopes), wrap the `Handler()` in your own authenticating middleware — the bearer check is intentionally minimal and the inject endpoint is just an `http.Handler` that composes like any other.

## In-process yield for hybrid demos

`HTTPSource` exposes a `Yield(data)` method for callers that want to mix HTTP inject with direct in-process yields against the same source. Useful for tests and for demos that seed historical events at startup before opening the inject endpoint to a push-server:

```go
chatSrc := events.NewHTTPSource[ChatMessage](chatDef, events.HTTPSourceConfig{...})
for _, seed := range loadHistoricalMessages() {
    _ = chatSrc.Yield(seed)
}
mux.Handle(chatSrc.InjectPath(), chatSrc.Handler())
```

The internal buffer and cursor sequence are shared — HTTP-injected events are indistinguishable from `Yield`-ed events on the wire and in `Recent` / `ByCursor`.

## What HTTPSource is NOT

- **Not a JSON-RPC method.** The inject endpoint is plain HTTP POST. It does not return JSON-RPC envelopes; errors are plain-text status reasons.
- **Not part of the MCP Events spec.** The spec governs the events that subscribers consume (`events/poll`, `events/subscribe`, `notifications/events/event`, etc.); the push-server-to-event-server wire is internal infrastructure. Your inject endpoint can be replaced with Kafka, NATS, Redis streams, or anything else without touching what MCP clients see.
- **Not authenticated by MCP's auth surface.** The bearer is a separate concern from MCP client auth — these are upstream operators (push-servers), not MCP clients. Wire them through your own infra-auth (mTLS, network policy, bearer rotation).
