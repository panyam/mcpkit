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
│  │ stdio       │  │ Tool authz       │  │resources/││
│  └────────────┘  │ MCP metrics      │  │ prompts/ ││
│                  └─────────────────┘  │ logging/ ││
│  ┌────────────────────────────────┐   └──────────┘│
│  │ Notifications (logging.go)     │               │
│  │ NotifyFunc, EmitLog, LogLevel  │               │
│  │ Context-based session injection│               │
│  └────────────────────────────────┘               │
│  ┌─────────────────────────────────────────────┐  │
│  │ servicekit (v0.0.22)                         │  │
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
├── go.mod                       # servicekit v0.0.22
├── core/                        # Protocol types + tool-handler APIs
│   ├── jsonrpc.go               # Request, Response, Error, ErrCode*
│   ├── tool.go                  # ToolDef, ToolRequest, ToolResult, Content, ToolHandler
│   ├── resource.go              # ResourceDef, ResourceTemplate, ResourceHandler
│   ├── prompt.go                # PromptDef, PromptHandler
│   ├── completion.go            # CompletionHandler, CompletionRef, CompletionResult
│   ├── auth.go                  # Claims, TokenSource, AuthValidator, AuthError, Extension
│   ├── logging.go               # LogLevel, NotifyFunc, EmitLog, ContextWithSession
│   ├── progress.go              # EmitProgress
│   ├── sampling.go              # CreateMessageRequest/Result, Sample()
│   ├── elicitation.go           # ElicitationRequest/Result, Elicit()
│   ├── request.go               # RequestFunc, ErrNoRequestFunc
│   ├── protocol.go              # ServerInfo, ClientInfo, ClientCapabilities
│   ├── interfaces.go            # Transport, ServerRequestHandler, NotificationHandler
│   ├── ui.go                    # UIMetadata, UICSPConfig, UIVisibility, AppMIMEType, ToolMeta, ResourceContentMeta
│   └── www_authenticate.go      # ParseWWWAuthenticate
├── server/                      # Server + Dispatcher + transports
│   ├── server.go                # Server, NewServer, options, Handler(), Run()
│   ├── dispatch.go              # Dispatcher, routing, handlers, subscriptions
│   ├── transport.go             # SSE server transport
│   ├── streamable_transport.go  # Streamable HTTP transport
│   ├── stdio_transport.go       # Stdio transport (Content-Length framed JSON-RPC)
│   ├── memory_transport.go      # InProcessTransport (core.Transport)
│   ├── request.go               # sendServerRequest, routeServerResponse
│   ├── middleware.go            # Middleware, LoggingMiddleware
│   └── pagination.go            # cursor-based pagination
├── client/                      # Client + transports
│   ├── client.go                # Client, NewClient, Connect, ToolCall, WithTransport
│   ├── stdio_transport.go       # StdioTransport, NewStdioTransport, WithStdioTransport
│   ├── client_logging.go        # loggingTransport, WithClientLogging
│   └── client_reconnect.go      # WithMaxRetries, WithReconnectBackoff
├── ext/auth/                    # SEPARATE module (github.com/panyam/mcpkit/ext/auth)
│   ├── go.mod                   # depends on mcpkit + oneauth
│   ├── discovery.go             # DiscoverMCPAuth (PRM + AS metadata)
│   ├── token_source.go          # OAuthTokenSource, ValidatePKCES256 (MCP-specific)
│   ├── dcr.go                   # DefaultClientRegistration (MCP defaults), type aliases for oneauth
│   ├── jwt_validator.go         # JWTValidator (JWKS-based)
│   ├── server_auth.go           # MountAuth (PRM endpoints)
│   ├── scopes.go                # RequireScope
│   └── docs/DESIGN.md           # Auth architecture, spec compliance
│   NOTE: Generic OAuth (RegisterClient, ClientCredentialsSource, ValidateHTTPS,
│         ValidateCIMDURL, mergeScopes) pushed to oneauth (#158). Type aliases preserved.
├── ext/ui/                      # SEPARATE module (github.com/panyam/mcpkit/ext/ui)
│   ├── go.mod                   # depends on mcpkit only (zero external deps)
│   └── extension.go             # UIExtension (ExtensionProvider + RefValidator), RegisterAppTool
├── testutil/testclient.go       # TestClient wrapper for e2e testing
├── cmd/testserver/              # Conformance test server
├── cmd/testclient/              # Headless OAuth conformance client
├── conformance/baseline.yml     # Expected failures: 0 server + 1 auth (warning)
├── scripts/                     # smoke-test.sh, conformance-test.sh
├── docs/                        # ARCHITECTURE.md, APPS_DESIGN.md, GATEWAY_DESIGN.md
├── tests/e2e/                   # E2E auth tests (separate module)
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

1. Transport creates a session dispatcher and sets `dispatcher.SetNotifyFunc()` to a transport-specific sender
2. `Server.dispatchWith` injects the notify func, log level, and auth claims into the context via `contextWithSession`
3. Tool handlers call `EmitLog(ctx, level, logger, data)` which checks the session's log level and calls the notify func
4. SSE transport: `notifyFunc` pushes via `hub.SendEvent` (real-time delivery)
5. Streamable HTTP POST: when client sends `Accept: text/event-stream`, `handlePostSSE` passes a request-scoped notifyFunc via `dispatchWithNotify` (no shared state mutation)
6. Streamable HTTP GET: client opens `GET /mcp` with `Mcp-Session-Id` header, server wires `dispatcher.SetNotifyFunc()` to push via SSEHub. Client enables this via `WithGetSSEStream()` — notifications arrive on the background GET stream

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
- `Server.NotifyResourceUpdated(uri)` iterates subscribers under read lock, copies dispatcher list, then calls each `d.getNotifyFunc()` outside the lock
- `Server.Broadcast(method, params)` fans out to ALL connected sessions across all transports, unconditionally (no subscription required). Uses `sessionBroadcasters` — each transport registers a closure that iterates its session map. Pattern mirrors `CloseAllSessions`.
- Transport `OnClose` / `closeSession` calls `subManager.unsubscribeAll(sessionID)` to clean up

**Why store `*Dispatcher` not `NotifyFunc`:** The `notifyFunc` on a Dispatcher can change — Streamable HTTP wires it when a GET SSE stream opens. Storing the Dispatcher pointer and reading `d.getNotifyFunc()` at notification time handles this correctly. Access to `notifyFunc` is protected by `notifyMu` (RWMutex) to handle concurrent GET SSE stream setup and subscription notifications.

**Session cleanup:** All per-session teardown is centralized in `Dispatcher.Close()`. Transports call it in their disconnect path (SSE `OnClose`, Streamable `handleDelete`/`closeSession`, memory `close`). New per-session state should add its cleanup to `Close()` — not to each transport individually.

## Notification Delivery Order Guarantees

Notifications emitted during a tool call (logging, progress) are delivered to the client **before** the tool result. The ordering guarantee is per-transport:

| Transport | Guarantee | Mechanism |
|-----------|-----------|-----------|
| **Streamable HTTP POST** (SSE streaming) | Notifications arrive on the **same POST response stream** as the tool result, in emission order, before the result. | `handlePostSSE` creates a request-scoped `requestNotify` closure. Notifications and the final response share a mutex-protected `writeSSE` function. The response is written last. |
| **Streamable HTTP GET** (opt-in) | Server-initiated notifications arrive on the background GET SSE stream. Ordering relative to POST responses is not guaranteed. | Client opens `GET /mcp` via `WithGetSSEStream()`. Server wires `dispatcher.notifyFunc` to SSEHub. `backgroundGetReader` dispatches events to `notifyHandler`. |
| **SSE** | Notifications arrive on the shared SSE stream in emission order. The background reader delivers them to `notifyHandler` before routing the response to the pending call channel. | Single `backgroundReader` goroutine processes events sequentially: notification delivery completes before the response is sent to the blocked `call()`. |
| **In-memory** | Fully synchronous — notifications are delivered inline during `call()`, before the response is returned. | `dispatchWithNotifyAndRequest` calls `notifyFunc` synchronously within the tool handler. |

**Cross-request isolation (Streamable HTTP):** Each POST gets its own `requestNotify` closure. Notifications from concurrent tool calls never leak to other requests' response streams.

**Client-side delivery:** `WithNotificationCallback(fn)` works across all transports. The handler receives `(method string, params any)` where params is always `map[string]any` (JSON-roundtripped for consistency, including in-memory). When `WithGetSSEStream()` is enabled, notifications may arrive concurrently from the GET stream and POST SSE responses — the callback must be goroutine-safe.

## Tool Error Semantics

- **Handler returns `error`** → JSON-RPC success with `isError: true` in tool result
- **Protocol failure** (bad params, unknown tool) → JSON-RPC error response

## Graceful Shutdown

Uses servicekit's `ListenAndServeGraceful`:
1. SIGTERM/SIGINT → stops accepting new connections
2. `OnShutdown` callback calls `SSEHub.CloseAll()` for SSE sessions
3. Waits for in-flight requests (configurable drain timeout)
4. Exit

## MCP Apps Extension

MCPKit supports the [MCP Apps extension](https://modelcontextprotocol.io/extensions/apps/overview) (`io.modelcontextprotocol/ui`), enabling servers to return interactive HTML UIs that render inline in host conversations.

**mcpkit's scope:** capability negotiation, `_meta.ui` metadata on tools/resources, `ui://` resource serving, visibility filtering, text-only fallback. The iframe rendering and `postMessage` bridge are the host's responsibility.

**Package split:**
- `core/ui.go` — Protocol types (`UIMetadata`, `UICSPConfig`, `UIVisibility`, `AppMIMEType`, `ToolMeta`, `ResourceContentMeta`) and context helpers (`ClientSupportsUI`, `NotifyResourcesChanged`)
- `ext/ui/` — Extension implementation (`UIExtension`, `RegisterAppTool`, `RefValidator`)
- `client/client.go` — `WithExtension`, `WithUIExtension`, `ServerSupportsUI`, `ListToolsForModel`

See [docs/APPS_DESIGN.md](APPS_DESIGN.md) for the full design: protocol flows, edge cases, conformance strategy, and slyds reference integration.
