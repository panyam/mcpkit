# server

MCP server implementation: Dispatcher, transports, middleware, subscriptions.

## What belongs here

- `Server` struct and options (`NewServer`, `WithBearerToken`, `WithToolTimeout`, etc.)
- `Dispatcher` - JSON-RPC routing, method handlers, session state
- Transports: SSE (`transport.go`), Streamable HTTP (`streamable_transport.go`)
- `InProcessTransport` - `core.Transport` implementation for testing/embedding
- Middleware chain (`WithMiddleware`, `LoggingMiddleware`)
- Server-to-client request infrastructure (`sendServerRequest`, `routeServerResponse`)
- Resource subscriptions (`WithSubscriptions`, `NotifyResourceUpdated`)
- Broadcast notifications (`Broadcast`) - fan out to all connected sessions
- Extension registration (`WithExtension`) - extensions declare capabilities in the initialize response
- Experimental capabilities (`WithExperimentalCapability`) - entries under the spec's `capabilities.experimental`, e.g. Claude Code's `claude/channel`
- Server instructions (`WithInstructions`) - guidance for the model, sent as the top-level `instructions` of the initialize and `server/discover` results
- Startup validation (`validateExtensionRefs`) - calls `RefValidator` on registered extensions to warn about unresolvable resource references

## Dependencies

- `core/` - protocol types (Request, Response, ToolDef, etc.)
- `servicekit` - SSE hub, graceful shutdown
- Does NOT import `client/`

## Session lifecycle

`OnSessionClose` runs a hook once when a session ends, however it ends (DELETE, idle expiry, the
SSE stream closing, `CloseSession`, stdio or in-process close), so an extension can drop
per-session state:

```go
srv.OnSessionClose(func(ctx context.Context, sessionID string) {
    myExt.forget(sessionID)
})
```

Hooks run in order on the goroutine closing the session, so keep them short. The stateless wire has
no sessions and fires none.

## Refusing for lack of scope

Middleware that refuses a request returns `core.InsufficientScope("pages:act")`, which the server
writes as a 403 with `WWW-Authenticate: Bearer error="insufficient_scope", scope="pages:act"`, the
same challenge `ext/auth`'s scope middleware sends. A `*core.ScopeChallenge` converts with
`.AuthError(resourceMetadataURL)`. Return it from middleware rather than a tool handler: middleware
runs before dispatch, while a handler's response may already be streaming with a 200.

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

The in-process transport passes `*core.Request`/`*core.Response` directly, with no JSON envelope serialization. This catches logic bugs; HTTP transport tests catch wire format bugs.
