# client

MCP client implementation: HTTP transports, auth retry, reconnection, logging.

## What belongs here

- `Client` struct and options (`NewClient`, `WithSSEClient`, `WithTransport`, etc.)
- HTTP transports: Streamable HTTP (`streamableClientTransport`), SSE (`sseClientTransport`)
- Auth retry (`DoWithAuthRetry`) — 401 token refresh, 403 scope step-up
- Reconnection (`WithMaxRetries`, `WithReconnectBackoff`)
- Transport logging (`WithClientLogging`)
- Server-to-client request handling (`HandleServerRequest`, `WithSamplingHandler`, `WithElicitationHandler`)
- Notification callback (`WithNotificationCallback`)

## Dependencies

- `core/` — protocol types (Request, Response, ToolDef, etc.)
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

## Transport interface

The client accepts any `core.Transport` via `WithTransport()`. This enables:
- `server.NewInProcessTransport` for testing (no HTTP)
- Custom transports for stdio, WebSocket, etc.
