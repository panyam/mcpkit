# client

MCP client implementation: HTTP transports, auth retry, reconnection, logging.

## What belongs here

- `Client` struct and options (`NewClient`, `WithSSEClient`, `WithTransport`, etc.)
- HTTP transports: Streamable HTTP (`streamableClientTransport`), SSE (`sseClientTransport`)
- Auth retry (`DoWithAuthRetry`) - 401 token refresh, 403 scope step-up
- Reconnection (`WithMaxRetries`, `WithReconnectBackoff`)
- Transport logging (`WithClientLogging`)
- Server-to-client request handling (`HandleServerRequest`, `WithSamplingHandler`, `WithElicitationHandler`)
- Notification callback (`WithNotificationCallback`)

## Dependencies

- `core/` - protocol types (Request, Response, ToolDef, etc.)
- Does NOT import `server/`

## Usage

```go
import (
    "github.com/panyam/mcpkit/client"
    "github.com/panyam/mcpkit/core"
)

c := client.NewClient("http://localhost:8787/mcp",
    core.ClientInfo{Name: "my-client", Version: "1.0"},
    client.WithSamplingHandler(mySamplingHandler),
)
c.Connect()
result, _ := c.ToolCall("greet", map[string]any{"name": "world"})
```

## Extension support

```go
c := client.NewClient(url, info,
    client.WithUIExtension(),  // advertise MCP Apps support
)
c.Connect()

if c.ServerSupportsUI() { /* server can serve app UIs */ }

modelTools, _ := c.ListToolsForModel() // excludes app-only tools
```

- `WithExtension(id, cap)` - general extension advertisement
- `WithUIExtension()` - convenience for MCP Apps
- `ServerSupportsExtension(id)` / `ServerSupportsUI()` - detect server support
- `ListToolsForModel()` - filters out tools with visibility `["app"]` only

## Listening for notifications (stateless wire)

On the 2026-07-28 wire, server notifications arrive on a `subscriptions/listen` stream. One
`Listen` call carries every filter key the host wants, including keys that extensions define, and
routes each notification by method:

```go
l, err := c.Listen(ctx, client.ListenOptions{
    Notifications: core.ListChangedFilter("toolsListChanged", "eventsListChanged"),
    Handlers: map[string]func(json.RawMessage){
        "notifications/tools/list_changed":  func(json.RawMessage) { refreshTools() },
        "notifications/events/list_changed": func(json.RawMessage) { refreshEvents() },
    },
})
defer l.Stop()
l.Honored() // the keys the server agreed to, from its acknowledgement
```

`Listen` returns once the server has acknowledged the stream. Handlers run one at a time on the
stream's reader goroutine, so keep them short. On the legacy wire it returns
`ErrListenNeedsStatelessWire`.

## Cancelling a call, and progress

Cancelling a call's context returns from the call and sends `notifications/cancelled` with its
request id, so the server can stop the work. It is not sent for `initialize`, or on the 2026-07-28
stateless wire, where aborting the request is the cancellation.

`ToolCallWith` takes per-call options. `WithProgress` asks the server for progress and hands each
report for this call to a callback:

```go
res, err := c.ToolCallWith(ctx, "reindex", args, client.WithProgress(func(p core.ProgressNotification) {
    fmt.Printf("%.0f/%.0f %s\n", p.Progress, p.Total, p.Message)
}))
```

The callback runs on the transport's reader goroutine, so keep it short. Reports that arrive after
the call returns are dropped.

## When the server wants more scope

A 403 the client cannot step up from (a static token, say) comes back as a
`*client.ClientAuthError` carrying the scopes the server asked for:

```go
_, err := c.ToolCall(ctx, "edit", args)
var ae *client.ClientAuthError
if errors.As(err, &ae) && ae.StatusCode == 403 {
    fmt.Println("ask for:", ae.RequiredScopes) // from WWW-Authenticate scope="..."
}
```

A `core.ScopeAwareTokenSource` gets those scopes first and the call is retried once.

## Transport interface

The client accepts any `core.Transport` via `WithTransport()`. This enables:
- `server.NewInProcessTransport` for testing (no HTTP)
- Custom transports for stdio, WebSocket, etc.
