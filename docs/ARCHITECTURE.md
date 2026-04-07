# Architecture

## Overview

MCPKit is a Go library for building production-grade MCP (Model Context Protocol) servers. It provides the transport, middleware, and operational infrastructure so that application code only needs to register tools and handle requests.

```
┌──────────────────────────────────────────────────┐
│                   Application                     │
│    (registers tools/resources/prompts, handles)   │
├──────────────────────────────────────────────────┤
│                    MCPKit                          │
│  ┌────────────┐  ┌─────────────────┐  ┌──────────┐│
│  │  Transport  │  │ MCP Middleware   │  │ Dispatch ││
│  │ HTTP+SSE    │  │ Auth (bearer)    │  │tools/list││
│  │ Streamable  │  │ Tool timeout     │  │tools/call││
│  │  HTTP       │  │ Allowed-roots    │  │initialize││
│  │ stdio (tbd) │  │ Tool authz       │  │resources/││
│  └────────────┘  │ MCP metrics      │  │ prompts/ ││
│                  └─────────────────┘  │ logging/ ││
│  ┌────────────────────────────────┐   └──────────┘│
│  │ Notifications (logging.go)     │               │
│  │ NotifyFunc, EmitLog, LogLevel  │               │
│  │ Context-based session injection│               │
│  └────────────────────────────────┘               │
│  ┌─────────────────────────────────────────────┐  │
│  │ servicekit (v0.0.14)                         │  │
│  │ SSEConn, SSEHub, ListenAndServeGraceful,     │  │
│  │ StreamableServe, CORS, RateLimiter, Logger   │  │
│  └─────────────────────────────────────────────┘  │
├──────────────────────────────────────────────────┤
│     Sub-module: mcpkit/auth (separate go.mod)     │
│     Imports oneauth for JWT, OIDC                 │
└──────────────────────────────────────────────────┘
```

## Design Principles

1. **Transport is not protocol** — HTTP+SSE and Streamable HTTP are transports. JSON-RPC dispatch is shared. Adding a transport means adding a handler, not changing dispatch.

2. **Generic infrastructure from servicekit, MCP-specific here** — SSEConn/SSEHub, graceful shutdown, Streamable HTTP, CORS, rate limiting come from servicekit. mcpkit only implements MCP-specific logic: tool timeout, allowed-roots, tool authz.

3. **Sub-module for heavy auth** — The core module ships `BearerTokenValidator` (constant-time compare, zero deps). JWT/OIDC lives in `mcpkit/auth`, a separate Go module that imports oneauth.

4. **Tools are the app's job** — MCPKit handles transport, security, and operations. The application registers tool handlers.

5. **Safe defaults** — Constant-time token comparison, initialization gating, SSE keepalive are on by default.

## Actual Package Structure

```
mcpkit/                          # module: github.com/panyam/mcpkit
├── go.mod                       # servicekit v0.0.14
├── dispatch.go                  # Dispatcher: JSON-RPC routing, version negotiation, init gating, logging, completion, resources/subscribe, resources/unsubscribe
├── logging.go                   # LogLevel, LogMessage, NotifyFunc, EmitLog, context helpers
├── progress.go                  # ProgressNotification, EmitProgress
├── completion.go                # CompletionRef, CompletionArgument, CompletionResult, CompletionHandler
├── auth.go                      # Claims, ClaimsProvider, TokenSource, Extension, Stability, ExtensionProvider
├── server.go                    # Server, options, Handler(), ListenAndServe(), CheckAuth, writeAuthError, subscriptionRegistry, NotifyResourceUpdated
├── tool.go                      # ToolDef (with Annotations), ToolRequest, ToolResult, Content, ToolHandler
├── resource.go                  # ResourceDef, ResourceTemplate, ResourceHandler, ResourceUpdatedNotification types
├── prompt.go                    # PromptDef (with Annotations), PromptArgument, PromptHandler types
├── pagination.go                # Generic cursor-based pagination helper
├── jsonrpc.go                   # JSON-RPC 2.0 Request/Response/Error types
├── transport.go                 # SSE transport (sseTransport, mcpSSEConn, SSEData)
├── streamable_transport.go      # Streamable HTTP transport (streamableTransport)
├── Makefile                     # test, testconf, smoke, audit, serve, ci targets
├── cmd/testserver/              # Minimal MCP server for testing
│   ├── main.go                  # echo, add, fail tools + transport selection
│   ├── conformance_tools.go     # Tools expected by MCP conformance suite
│   ├── conformance_resources.go # Resources + templates for conformance
│   └── conformance_prompts.go   # Prompts for conformance
├── conformance/
│   └── baseline.yml             # Expected failures: 0 server + 1 auth (with warning)
├── scripts/
│   ├── smoke-test.sh            # Curl-based tests for SSE + Streamable HTTP
│   ├── conformance-test.sh      # Runs @modelcontextprotocol/conformance (server)
│   └── conformance-auth-test.sh # Runs auth conformance suite (client)
├── docs/
│   ├── ARCHITECTURE.md          # This file
│   ├── AUTH_DESIGN.md           # Auth architecture, sequence diagrams, spec compliance
│   └── GATEWAY_DESIGN.md       # MCP Gateway design for proxying HTTP/gRPC backends
├── client.go                    # MCP client: Connect, ToolCall, ReadResource, SubscribeResource, UnsubscribeResource, WithClientBearerToken
├── testutil/testclient.go       # TestClient wrapper for e2e testing
├── auth/                        # SEPARATE module (github.com/panyam/mcpkit/auth)
│   ├── go.mod                   # depends on mcpkit + oneauth v0.0.64
│   ├── jwt_validator.go         # JWTValidator (jwt.Parse + JWKS keyfunc)
│   ├── server_auth.go           # MountAuth (PRM endpoint via oneauth)
│   ├── www_authenticate.go      # MCP-specific WWW-Authenticate builders
│   ├── scopes.go                # RequireScope for tool handlers
│   ├── token_source.go          # OAuthTokenSource, ClientCredentialsSource
│   └── discovery.go             # DiscoverMCPAuth (MCP discovery orchestration)
├── tests/e2e/                   # E2E auth tests (separate module, oneauth v0.0.64)
│   └── (22 tests: JWT, transport, scope, PRM, WWW-Authenticate)
└── tests/keycloak/              # Keycloak interop (separate module, needs Docker)
    └── (7 tests: Keycloak JWT → mcpkit JWTValidator)
```

## Two Transports

### SSE Transport (MCP 2024-11-05) — `transport.go`

- `GET /mcp/sse` → long-lived SSE stream, sends `endpoint` event with POST URL
- `POST /mcp/message?sessionId=<id>` → JSON-RPC dispatch, response pushed on SSE
- Session lifetime = SSE connection lifetime (cleanup on disconnect)
- Uses servicekit `BaseSSEConn[SSEData]` + `SSEHub[SSEData]`
- `SSEData` union type: `SSEText(url)` for raw text, `SSEJSON(bytes)` for JSON-RPC

### Streamable HTTP (MCP 2025-03-26) — `streamable_transport.go`

- `POST /mcp` → JSON-RPC dispatch, response in HTTP body (synchronous)
- `DELETE /mcp` → terminate session
- Session tracked via `Mcp-Session-Id` header (created on initialize)
- No long-lived connections — each request is independent HTTP
- `MCP-Protocol-Version` header validated if present

### Dual Transport Mode

```go
srv.Handler(WithStreamableHTTP(true), WithSSE(true))
// SSE at /mcp/sse + /mcp/message, Streamable HTTP at /mcp
```

## Key Types

```go
type ToolHandler func(ctx context.Context, req ToolRequest) (ToolResult, error)

type AuthValidator interface {
    Validate(r *http.Request) error
}

// Optional: validators that also implement ClaimsProvider
// propagate identity to tool handlers via context.
type ClaimsProvider interface {
    Claims(r *http.Request) *Claims
}

type TokenSource interface {
    Token() (string, error) // for client-side auth
}

type ExtensionProvider interface {
    Extension() Extension // sub-modules declare their extension metadata
}

type ServerInfo struct {
    Name, Version                          string
    Title, Description, Instructions       string // optional
    WebsiteURL                             string // optional
}

type ClientInfo struct { Name, Version string }
type ClientCapabilities struct {
    Sampling, Elicitation *struct{}
    Roots                 *RootsCap
}

type Content struct {
    Type     string           // "text", "image", "audio", "resource"
    Text     string           // for text
    MimeType string           // for image/audio
    Data     string           // base64 for image/audio
    Resource *ResourceContent // for embedded resources
}

type ResourceHandler func(ctx context.Context, req ResourceRequest) (ResourceResult, error)
type TemplateHandler func(ctx context.Context, uri string, params map[string]string) (ResourceResult, error)
type PromptHandler func(ctx context.Context, req PromptRequest) (PromptResult, error)
```

## Protocol Version Negotiation

Supports `2025-11-25` and `2024-11-05`. During `initialize`, server checks if client's version is in supported list. If not → JSON-RPC error `-32602` with `{"supported": [...]}`.

## Initialization Lifecycle

1. Client sends `initialize` → server responds with capabilities and negotiated version
2. Client sends `notifications/initialized` → server marks session as ready
3. Only after step 2 does the server accept `tools/list`, `tools/call`, etc.
4. `ping` is exempt — allowed at any time

## Server-to-Client Notifications

MCPKit supports server-initiated notifications via `NotifyFunc`, a generic `func(method string, params any)` callback. This is the foundation for logging, progress, and list-changed notifications.

### How it works

1. Transport creates a session dispatcher and sets `dispatcher.notifyFunc` to a transport-specific sender
2. `Server.dispatchWith` injects the notify func, log level, and auth claims into the context via `contextWithSession`
3. Tool handlers call `EmitLog(ctx, level, logger, data)` which checks the session's log level and calls the notify func
4. SSE transport: `notifyFunc` pushes via `hub.SendEvent` (real-time delivery)
5. Streamable HTTP: when client sends `Accept: text/event-stream`, `handlePostSSE` passes a request-scoped notifyFunc via `dispatchWithNotify` (no shared state mutation)

### Logging

- Client sends `logging/setLevel` → dispatcher stores minimum level per-session via `atomic.Pointer[LogLevel]`
- Tool handler calls `EmitLog(ctx, LogInfo, "logger", "message")` → filtered by level → pushed as `notifications/message`
- Thread-safe: atomic read of log level from tool goroutines while `logging/setLevel` writes

### Resource Subscriptions

Clients can subscribe to resource URIs and receive `notifications/resources/updated` when the resource changes. Requires `WithSubscriptions()` server option.

**Architecture:**
- `subscriptionRegistry` on `Server` maps URI → sessionID → `*Dispatcher`
- Per-session `Dispatcher` holds `sessionID` and `subManager` (pointer to Server's registry)
- `resources/subscribe` handler registers the session's Dispatcher under the URI
- `resources/unsubscribe` handler removes it
- `Server.NotifyResourceUpdated(uri)` iterates subscribers under read lock, copies dispatcher list, then calls each `d.notifyFunc` outside the lock
- Transport `OnClose` / `closeSession` calls `subManager.unsubscribeAll(sessionID)` to clean up

**Why store `*Dispatcher` not `NotifyFunc`:** The `notifyFunc` on a Dispatcher can change — Streamable HTTP wires it when a GET SSE stream opens. Storing the Dispatcher pointer and reading `d.notifyFunc` at notification time handles this correctly.

**Session cleanup:** All per-session teardown is centralized in `Dispatcher.Close()`. Transports call it in their disconnect path (SSE `OnClose`, Streamable `handleDelete`/`closeSession`, memory `close`). New per-session state should add its cleanup to `Close()` — not to each transport individually.

## Notification Delivery Order Guarantees

Notifications emitted during a tool call (logging, progress) are delivered to the client **before** the tool result. The ordering guarantee is per-transport:

| Transport | Guarantee | Mechanism |
|-----------|-----------|-----------|
| **Streamable HTTP** (SSE streaming) | Notifications arrive on the **same POST response stream** as the tool result, in emission order, before the result. | `handlePostSSE` creates a request-scoped `requestNotify` closure. Notifications and the final response share a mutex-protected `writeSSE` function. The response is written last. |
| **SSE** | Notifications arrive on the shared SSE stream in emission order. The background reader delivers them to `notifyHandler` before routing the response to the pending call channel. | Single `backgroundReader` goroutine processes events sequentially: notification delivery completes before the response is sent to the blocked `call()`. |
| **In-memory** | Fully synchronous — notifications are delivered inline during `call()`, before the response is returned. | `dispatchWithNotifyAndRequest` calls `notifyFunc` synchronously within the tool handler. |

**Cross-request isolation (Streamable HTTP):** Each POST gets its own `requestNotify` closure. Notifications from concurrent tool calls never leak to other requests' response streams.

**Client-side delivery:** `WithNotificationHandler(fn)` works across all three transports. The handler receives `(method string, params any)` where params is always `map[string]any` (JSON-roundtripped for consistency, including in-memory).

## Tool Error Semantics

- **Handler returns `error`** → JSON-RPC success with `isError: true` in tool result
- **Protocol failure** (bad params, unknown tool) → JSON-RPC error response

## Graceful Shutdown

Uses servicekit's `ListenAndServeGraceful`:
1. SIGTERM/SIGINT → stops accepting new connections
2. `OnShutdown` callback calls `SSEHub.CloseAll()` for SSE sessions
3. Waits for in-flight requests (configurable drain timeout)
4. Exit
