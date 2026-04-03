# Architecture

## Overview

MCPKit is a Go library for building production-grade MCP (Model Context Protocol) servers. It provides the transport, middleware, and operational infrastructure so that application code only needs to register tools and handle requests.

```
┌──────────────────────────────────────────────────┐
│                   Application                     │
│         (registers tools, handles calls)          │
├──────────────────────────────────────────────────┤
│                    MCPKit                          │
│  ┌────────────┐  ┌─────────────────┐  ┌──────────┐│
│  │  Transport  │  │ MCP Middleware   │  │ Dispatch ││
│  │ HTTP+SSE    │  │ Auth (bearer)    │  │tools/list││
│  │ stdio       │  │ Tool timeout     │  │tools/call││
│  │ Streamable  │  │ Allowed-roots    │  │initialize││
│  │  HTTP       │  │ Tool authz       │  │resources/││
│  └────────────┘  │ MCP metrics      │  │ prompts/ ││
│                  └─────────────────┘  └──────────┘│
│  ┌─────────────────────────────────────────────┐  │
│  │ servicekit (v0.0.10+)                        │  │
│  │ SSEConn, SSEHub, ListenAndServeGraceful,     │  │
│  │ StreamableServe, CORS, RateLimiter, Logger,  │  │
│  │ Recovery, BodyLimit, Health, RequestID,       │  │
│  │ ServerTimeouts, Guard, OriginChecker         │  │
│  └─────────────────────────────────────────────┘  │
├──────────────────────────────────────────────────┤
│     Sub-module: mcpkit/auth (separate go.mod)     │
│     Imports oneauth (v0.0.51+) for JWT, OIDC      │
└──────────────────────────────────────────────────┘
```

## Design Principles

1. **Transport is not protocol** — HTTP+SSE and stdio are transports. JSON-RPC dispatch is shared. Adding Streamable HTTP (MCP 2025-03-26) means adding a transport, not changing dispatch.

2. **Generic infrastructure from servicekit, MCP-specific here** — SSEConn/SSEHub, graceful shutdown, Streamable HTTP, CORS, rate limiting, request logging, recovery, body limits, health checks, request IDs, and server timeouts come from servicekit (v0.0.10+). mcpkit only implements MCP-specific middleware: tool timeout, allowed-roots, tool authz, MCP metrics.

3. **Sub-module for heavy auth** — The core module ships `BearerTokenValidator` (constant-time compare, zero deps). JWT/OIDC lives in `mcpkit/auth`, a separate Go module with its own `go.mod` that imports oneauth. Apps that don't need JWT never pull in oneauth.

4. **Tools are the app's job** — MCPKit handles transport, security, and operations. The application registers tool handlers that receive validated, authenticated, rate-limited requests.

5. **Safe defaults** — Server timeouts, body size limits, loopback-only binding, and constant-time token comparison are on by default. You opt out, not in.

## Package Structure

```
mcpkit/                     # module: github.com/panyam/mcpkit
├── go.mod                  # core module — no oneauth dependency
├── server.go               # Server type, functional options, ListenAndServe
├── options.go              # WithListen, WithAuth, WithToolTimeout, etc.
├── dispatch.go             # JSON-RPC method routing (initialize, tools/*, resources/*)
├── tool.go                 # Tool registration, ToolHandler interface
├── transport/
│   ├── sse/                # HTTP+SSE transport (MCP 2024-11-05)
│   │   └── handler.go      # SSE + POST handlers (uses servicekit SSEConn/SSEHub)
│   ├── stdio/              # Content-Length framed stdio transport
│   │   └── stdio.go
│   └── streamhttp/         # Streamable HTTP (MCP 2025-03-26)
│       └── handler.go      # Uses servicekit StreamableServe
├── middleware/              # MCP-specific middleware only
│   ├── auth.go             # AuthValidator interface + BearerTokenValidator (constant-time)
│   ├── timeout.go          # Tool execution timeout (context.WithTimeout)
│   ├── roots.go            # Allowed-roots cwd restriction
│   ├── authz.go            # Per-tool authorization (role/scope → tool names)
│   └── metrics.go          # MCP-specific metrics (tool calls, session gauges)
│   # Generic HTTP middleware (CORS, rate limiting, logging, recovery,
│   # body limit, health, request ID, server timeouts) from servicekit
├── jsonrpc/
│   └── types.go            # JSON-RPC 2.0 request/response types
│
└── auth/                   # SEPARATE module: github.com/panyam/mcpkit/auth
    ├── go.mod              # requires mcpkit + oneauth
    └── jwt.go              # JWTValidator, OIDCValidator — implements AuthValidator via oneauth
```

### Sub-module pattern

Go has no optional dependencies. `mcpkit/auth` is a **separate Go module** (`go.mod` in `auth/`) that imports both `mcpkit` (for `AuthValidator` interface) and `oneauth` (for JWT/OIDC). Apps that only need bearer token auth import `github.com/panyam/mcpkit` alone — oneauth never enters their dependency tree. Apps that need JWT import `github.com/panyam/mcpkit/auth` as well.

## Key Types

```go
// ToolHandler is what applications implement
type ToolHandler func(ctx context.Context, req ToolRequest) (ToolResult, error)

// Middleware wraps the JSON-RPC dispatch
type Middleware func(next http.Handler) http.Handler

// AuthValidator is the interface for auth strategies
type AuthValidator interface {
    Validate(r *http.Request) error
}

// ServerInfo identifies this MCP server (with optional 2025-11-25 fields)
type ServerInfo struct {
    Name, Version                          string
    Title, Description, Instructions       string // optional
    WebsiteURL                             string // optional
}

// ClientInfo + ClientCapabilities are stored after initialize
type ClientInfo struct { Name, Version string }
type ClientCapabilities struct {
    Sampling, Elicitation *struct{}
    Roots                 *RootsCap
}
```

## Protocol Version Negotiation

The dispatcher supports MCP protocol versions `2025-11-25` and `2024-11-05`. During `initialize`:

1. Client sends `protocolVersion` in params
2. Server checks if the version is in its supported list
3. If supported → responds with that version as `protocolVersion`
4. If not → returns JSON-RPC error `-32602` with `{"supported": [...]}` in error data

## Initialization Lifecycle

The MCP spec requires a strict initialization handshake before the server processes requests:

1. Client sends `initialize` → server responds with capabilities and negotiated version
2. Client sends `notifications/initialized` → server marks session as ready
3. Only after step 2 does the server accept `tools/list`, `tools/call`, etc.
4. `ping` is exempt — allowed at any time as a keepalive

Requests sent before the handshake completes receive a JSON-RPC error `-32600` ("server not initialized").

## Tool Error Semantics

Per the MCP spec, tool execution failures and protocol errors are handled differently:

- **Handler returns `error`** → JSON-RPC success with `isError: true` in the tool result. The error message is included in the content.
- **Protocol failure** (bad params, unknown tool, malformed JSON) → JSON-RPC error response with appropriate error code.

This distinction matters: clients should check `result.isError` for tool-level failures, not the JSON-RPC error field.

## Session Lifecycle (HTTP+SSE)

Uses servicekit's `SSEConn[O]` and `SSEHub[O]`:

1. Client opens `GET /sse` → handler creates `SSEConn`, registers in `SSEHub`, sends `endpoint` event with POST URL
2. Client sends JSON-RPC via `POST /message?session=<id>` → middleware chain → dispatch → response pushed via `SSEHub.Send()`
3. `SSEConn` sends periodic `:ping` keepalive comments automatically (configurable interval)
4. Client disconnects → `r.Context().Done()` fires → `SSEHub.Unregister()` cleans up
5. POST to expired session → `410 Gone`

## Graceful Shutdown

Uses servicekit's `ListenAndServeGraceful`:

1. SIGTERM/SIGINT received → stops accepting new connections
2. `OnShutdown` callback calls `SSEHub.CloseAll()` — notifies active SSE clients
3. Waits for in-flight tool executions (configurable drain timeout)
4. `http.Server.Shutdown()` closes listener
5. Exit
