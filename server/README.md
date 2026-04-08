# server

MCP server implementation: Dispatcher, transports, middleware, subscriptions.

## What belongs here

- `Server` struct and options (`NewServer`, `WithBearerToken`, `WithToolTimeout`, etc.)
- `Dispatcher` — JSON-RPC routing, method handlers, session state
- Transports: SSE (`transport.go`), Streamable HTTP (`streamable_transport.go`)
- `InProcessTransport` — `core.Transport` implementation for testing/embedding
- Middleware chain (`WithMiddleware`, `LoggingMiddleware`)
- Server-to-client request infrastructure (`sendServerRequest`, `routeServerResponse`)
- Resource subscriptions (`WithSubscriptions`, `NotifyResourceUpdated`)
- Extension registration (`WithExtension`) — extensions declare capabilities in initialize response
- Startup validation (`validateExtensionRefs`) — calls `RefValidator` on registered extensions to warn about unresolvable resource references

## Dependencies

- `core/` — protocol types (Request, Response, ToolDef, etc.)
- `servicekit` — SSE hub, graceful shutdown
- Does NOT import `client/`

## In-process transport

For testing, use `NewInProcessTransport(srv)` with `client.WithTransport()`:

```go
transport := server.NewInProcessTransport(srv,
    server.WithServerRequestHandler(func(ctx context.Context, req *core.Request) *core.Response {
        return client.HandleServerRequest(req) // for sampling/elicitation
    }),
    server.WithNotificationHandler(func(method string, params []byte) {
        // capture notifications in tests
    }),
)
c := client.NewClient("memory://", info, client.WithTransport(transport))
```

The in-process transport passes `*core.Request`/`*core.Response` directly — no JSON envelope serialization. This catches logic bugs; HTTP transport tests catch wire format bugs.
