# MCPKit

## Version
0.0.1

## Provides
- mcp-http-transport: Production-grade MCP HTTP+SSE server with session management
- mcp-stdio-transport: Content-Length framed JSON-RPC stdio transport
- mcp-auth-middleware: Bearer token, JWT (via oneauth), API key authentication for MCP endpoints
- mcp-rate-limiter: Per-session and per-IP token-bucket rate limiting
- mcp-request-logger: Structured logging (slog) with method, session, latency, caller
- mcp-metrics: Prometheus-compatible metrics (request count, error rate, active sessions, tool duration)
- mcp-health: /healthz endpoint for load balancer and k8s probes
- mcp-session-hub: Concurrency-safe SSE session manager with per-session mutex
- mcp-tool-timeout: context.WithTimeout wrapper for tool/subprocess execution
- mcp-body-limit: http.MaxBytesReader middleware for request body size limits
- mcp-server-timeouts: Sensible defaults for ReadTimeout, WriteTimeout, IdleTimeout
- mcp-graceful-shutdown: SIGTERM handler with SSE drain and clean exit
- mcp-sse-keepalive: Periodic SSE ping comments to survive proxy idle timeouts
- mcp-allowed-roots: Restrict tool cwd to a set of allowed directories
- mcp-cors: Configurable CORS with OPTIONS preflight handler
- mcp-tool-authz: Per-tool authorization (role/scope to allowed tool names)
- mcp-constant-time-auth: Timing-safe token comparison

## Module
github.com/panyam/mcpkit

## Location
newstack/mcpkit/main

## Stack Dependencies

### Core module (github.com/panyam/mcpkit)
- goutils (github.com/panyam/goutils) — concurrency utilities

### Sub-module: auth (github.com/panyam/mcpkit/auth)
- oneauth (github.com/panyam/oneauth) — JWT/OIDC validation; separate go.mod so core has no oneauth dependency

## Integration

### Go Module
```go
// go.mod
require github.com/panyam/mcpkit v0.0.1

// Local development
replace github.com/panyam/mcpkit => ~/newstack/mcpkit/main
```

### Basic Server
```go
import "github.com/panyam/mcpkit"

srv := mcpkit.NewServer(
    mcpkit.WithListen(":8787"),
    mcpkit.WithBearerToken("secret"),
    mcpkit.WithToolTimeout(30 * time.Second),
    mcpkit.WithHealthCheck(true),
)
srv.RegisterTool("mytool", myHandler)
srv.ListenAndServe(ctx)
```

### With OneAuth JWT
```go
import (
    "github.com/panyam/mcpkit"
    "github.com/panyam/mcpkit/auth"
)

srv := mcpkit.NewServer(
    mcpkit.WithListen(":8787"),
    mcpkit.WithAuth(auth.JWTValidator(keyStore)),
    mcpkit.WithAllowedRoots("/data/decks", "/data/projects"),
    mcpkit.WithMetrics(true),
)
```

## Status
Active

## Conventions
- Functional options pattern for server configuration
- Middleware chain (auth → rate-limit → log → dispatch)
- Transport/protocol separation (HTTP+SSE, stdio share dispatch layer)
- oneauth is an optional dependency — bearer token works without it
- Per-session mutex for SSE write safety
- slog for structured logging (no custom logger interface)
