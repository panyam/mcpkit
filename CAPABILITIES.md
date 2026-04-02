# MCPKit

## Version
0.0.1

## Provides
- mcp-http-transport: Production-grade MCP HTTP+SSE server with session management
- mcp-stdio-transport: Content-Length framed JSON-RPC stdio transport
- mcp-auth-middleware: Bearer token, JWT (via oneauth), API key authentication for MCP endpoints
- mcp-tool-timeout: context.WithTimeout wrapper for tool/subprocess execution
- mcp-allowed-roots: Restrict tool cwd to a set of allowed directories
- mcp-tool-authz: Per-tool authorization (role/scope to allowed tool names)
- mcp-constant-time-auth: Timing-safe token comparison
- mcp-metrics: MCP-specific metrics (tool call counters, session gauges, tool execution duration)

## Module
github.com/panyam/mcpkit

## Location
newstack/mcpkit/main

## Stack Dependencies

### Core module (github.com/panyam/mcpkit)
- servicekit (github.com/panyam/servicekit) v0.0.10+ — HTTP middleware, SSEConn/SSEHub, ListenAndServeGraceful, StreamableServe
- goutils (github.com/panyam/goutils) — concurrency utilities

### Sub-module: auth (github.com/panyam/mcpkit/auth)
- oneauth (github.com/panyam/oneauth) v0.0.51+ — JWT/OIDC validation; separate go.mod so core has no oneauth dependency

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
- Generic HTTP middleware + SSE infrastructure from servicekit (CORS, rate limiting, logging, health, body limit, request ID, server timeouts, SSEConn, SSEHub, graceful shutdown, Streamable HTTP); MCP-specific middleware in mcpkit (tool timeout, allowed-roots, tool authz)
- Transport/protocol separation (HTTP+SSE, stdio share dispatch layer)
- oneauth is an optional dependency via sub-module — bearer token works without it
- Per-session mutex for SSE write safety
- slog for structured logging (no custom logger interface)
