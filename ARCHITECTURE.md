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
├── dispatch.go                  # Dispatcher: JSON-RPC routing, version negotiation, init gating, logging, completion
├── logging.go                   # LogLevel, LogMessage, NotifyFunc, EmitLog, context helpers
├── progress.go                  # ProgressNotification, EmitProgress
├── completion.go                # CompletionRef, CompletionArgument, CompletionResult, CompletionHandler
├── auth.go                      # Claims, ClaimsProvider, TokenSource, Extension, Stability, ExtensionProvider
├── server.go                    # Server, options, Handler(), ListenAndServe(), CheckAuth, writeAuthError
├── tool.go                      # ToolDef (with Annotations), ToolRequest, ToolResult, Content, ToolHandler
├── resource.go                  # ResourceDef (with Annotations), ResourceTemplate, ResourceHandler types
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
│   └── baseline.yml             # Expected failures: 7 server + 22 auth (north star)
├── scripts/
│   ├── smoke-test.sh            # Curl-based tests for SSE + Streamable HTTP
│   ├── conformance-test.sh      # Runs @modelcontextprotocol/conformance (server)
│   └── conformance-auth-test.sh # Runs auth conformance suite (client)
├── AUTH_DESIGN.md               # Auth architecture, sequence diagrams, spec compliance
└── auth/                        # SEPARATE module (github.com/panyam/mcpkit/auth)
    ├── go.mod                   # depends on mcpkit + oneauth
    ├── extension.go             # AuthExtension (ExtensionProvider)
    ├── jwt_validator.go         # JWTValidator (AuthValidator + ClaimsProvider)
    ├── server_auth.go           # MountAuth (PRM endpoint via oneauth)
    ├── www_authenticate.go      # MCP-specific WWW-Authenticate builders
    ├── scopes.go                # RequireScope for tool handlers
    ├── token_source.go          # OAuthTokenSource, ClientCredentialsSource
    └── discovery.go             # DiscoverMCPAuth (MCP discovery orchestration)
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
5. Streamable HTTP: `notifyFunc` is nil (notifications silently dropped until GET SSE stream ships)

### Logging

- Client sends `logging/setLevel` → dispatcher stores minimum level per-session via `atomic.Pointer[LogLevel]`
- Tool handler calls `EmitLog(ctx, LogInfo, "logger", "message")` → filtered by level → pushed as `notifications/message`
- Thread-safe: atomic read of log level from tool goroutines while `logging/setLevel` writes

## Tool Error Semantics

- **Handler returns `error`** → JSON-RPC success with `isError: true` in tool result
- **Protocol failure** (bad params, unknown tool) → JSON-RPC error response

## Graceful Shutdown

Uses servicekit's `ListenAndServeGraceful`:
1. SIGTERM/SIGINT → stops accepting new connections
2. `OnShutdown` callback calls `SSEHub.CloseAll()` for SSE sessions
3. Waits for in-flight requests (configurable drain timeout)
4. Exit
