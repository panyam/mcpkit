# Architecture

## Overview

MCPKit is a Go library for building production-grade MCP (Model Context Protocol) servers. It provides the transport, middleware, and operational infrastructure so that application code only needs to register tools and handle requests.

```
┌──────────────────────────────────────────────────┐
│                   Application                     │
│         (registers tools, handles calls)          │
├──────────────────────────────────────────────────┤
│                    MCPKit                          │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐  │
│  │  Transport  │  │ Middleware  │  │  Dispatch   │  │
│  │ HTTP+SSE    │  │ Auth       │  │ tools/list  │  │
│  │ stdio       │  │ RateLimit  │  │ tools/call  │  │
│  │ (future:    │  │ Logger     │  │ initialize  │  │
│  │  Streamable │  │ Metrics    │  │ resources/* │  │
│  │  HTTP)      │  │ BodyLimit  │  │ prompts/*   │  │
│  └────────────┘  │ CORS       │  └────────────┘  │
│                  │ Timeout    │                    │
│                  └────────────┘                    │
│  ┌────────────┐  ┌────────────┐                   │
│  │ Session Hub │  │  Health /  │                   │
│  │ (SSE mgmt)  │  │  Metrics   │                   │
│  └────────────┘  └────────────┘                   │
├──────────────────────────────────────────────────┤
│     Sub-module: mcpkit/auth (separate go.mod)     │
│     Imports oneauth for JWT, OIDC, API keys       │
└──────────────────────────────────────────────────┘
```

## Design Principles

1. **Transport is not protocol** — HTTP+SSE and stdio are transports. JSON-RPC dispatch is shared. Adding Streamable HTTP (MCP 2025-03-26) means adding a transport, not changing dispatch.

2. **Middleware chain, not monolith** — Each cross-cutting concern (auth, rate limiting, logging, metrics) is a separate middleware. Applications compose what they need via functional options.

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
│   │   ├── handler.go      # SSE + POST handlers
│   │   └── hub.go          # Session hub with per-session mutex
│   ├── stdio/              # Content-Length framed stdio transport
│   │   └── stdio.go
│   └── streamhttp/         # (future) Streamable HTTP (MCP 2025-03-26)
├── middleware/
│   ├── auth.go             # AuthValidator interface + BearerTokenValidator (constant-time)
│   ├── ratelimit.go        # Token-bucket per-session/IP
│   ├── logger.go           # Structured request logging (slog)
│   ├── metrics.go          # Prometheus counters/histograms
│   ├── bodylimit.go        # MaxBytesReader
│   ├── cors.go             # CORS + OPTIONS preflight
│   ├── timeout.go          # Tool execution timeout
│   └── roots.go            # Allowed-roots cwd restriction
├── health/
│   └── health.go           # /healthz handler
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
    Validate(r *http.Request) (Claims, error)
}
```

## Session Lifecycle (HTTP+SSE)

1. Client opens `GET /sse` → server creates session, sends `endpoint` event with POST URL
2. Client sends JSON-RPC via `POST /message?session=<id>` → middleware chain → dispatch → response via SSE `message` event
3. Server sends periodic `:ping` SSE comments to keep connection alive
4. Client disconnects → `r.Context().Done()` fires → session cleanup
5. POST to expired session → `410 Gone`

## Graceful Shutdown

1. SIGTERM received → stop accepting new SSE connections
2. Send SSE close event to all active sessions
3. Wait for in-flight tool executions (up to drain timeout)
4. Close HTTP listener
5. Exit
