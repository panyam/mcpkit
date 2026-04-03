# MCPKit

Production-grade MCP (Model Context Protocol) server library for Go.

MCPKit handles the transport, protocol negotiation, session management, and auth so you can focus on registering tools, resources, and prompts. Supports both **HTTP+SSE** (MCP 2024-11-05) and **Streamable HTTP** (MCP 2025-03-26) transports.

## Quick Start

```go
package main

import (
    "context"
    "log"
    "time"
    "github.com/panyam/mcpkit"
)

func main() {
    srv := mcpkit.NewServer(
        mcpkit.ServerInfo{Name: "my-server", Version: "0.1.0"},
        mcpkit.WithListen(":8787"),
        mcpkit.WithToolTimeout(30 * time.Second),
    )

    srv.RegisterTool(
        mcpkit.ToolDef{
            Name:        "greet",
            Description: "Say hello",
            InputSchema: map[string]any{
                "type": "object",
                "properties": map[string]any{
                    "name": map[string]any{"type": "string"},
                },
                "required": []string{"name"},
            },
        },
        func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
            var args struct {
                Name string `json:"name"`
            }
            if err := req.Bind(&args); err != nil {
                return mcpkit.ErrorResult(err.Error()), nil
            }
            return mcpkit.TextResult("Hello, " + args.Name + "!"), nil
        },
    )

    // Streamable HTTP (recommended) — responses in HTTP body
    srv.ListenAndServe(mcpkit.WithStreamableHTTP(true))
}
```

## Transports

### Streamable HTTP (MCP 2025-03-26) — recommended

Single endpoint, responses in HTTP body. Session via `Mcp-Session-Id` header.

```go
srv.ListenAndServe(mcpkit.WithStreamableHTTP(true))
```

### HTTP+SSE (MCP 2024-11-05) — legacy

Long-lived SSE stream + POST endpoint. Session tied to SSE connection.

```go
srv.ListenAndServe() // SSE is the default
```

### Both simultaneously

```go
srv.ListenAndServe(mcpkit.WithStreamableHTTP(true), mcpkit.WithSSE(true))
// SSE at /mcp/sse + /mcp/message, Streamable HTTP at /mcp
```

### Transport options

```go
handler := srv.Handler(
    mcpkit.WithPrefix("/custom"),                       // URL prefix (default: /mcp)
    mcpkit.WithPublicURL("https://proxy.example.com"),  // for reverse proxy
    mcpkit.WithMaxSessions(100),                        // limit concurrent sessions
    mcpkit.WithKeepalivePeriod(15 * time.Second),       // SSE keepalive interval
    mcpkit.WithStreamableHTTP(true),                    // enable Streamable HTTP
)
```

## Capabilities

| Capability | Methods |
|-----------|---------|
| **Tools** | `tools/list`, `tools/call` |
| **Resources** | `resources/list`, `resources/read`, `resources/templates/list` |
| **Prompts** | `prompts/list`, `prompts/get` |
| **Cancellation** | `notifications/cancelled` with context propagation |
| **Pagination** | Cursor-based pagination for all list methods |

Capabilities are auto-advertised in the `initialize` response when the corresponding handlers are registered.

## Protocol Support

- MCP **2025-11-25** and **2024-11-05** with automatic version negotiation
- Initialization gating: requests rejected until `initialize` + `notifications/initialized` handshake completes
- Tool error semantics: handler errors → `isError: true` in result (not JSON-RPC errors)

## Testing

```bash
make test         # Unit tests (64 tests)
make testconf     # MCP conformance suite (requires Node.js)
make testall      # Both unit + conformance
make smoke        # Curl-based transport tests (SSE + Streamable HTTP)
make audit        # Security: govulncheck + gosec + gitleaks + race detection
make serve        # Start SSE test server on :8787
make serve-streamable  # Streamable HTTP on :8787
```

### Conformance Suite

Validated against the [official MCP conformance test suite](https://github.com/modelcontextprotocol/conformance). Current status: **18/30 scenarios passing** (remaining require logging, sampling, elicitation, and subscriptions — tracked in `conformance/baseline.yml`).

```bash
bash scripts/conformance-test.sh                    # full suite
bash scripts/conformance-test.sh tools-call-simple-text  # single scenario
```

When a feature's conformance scenario starts passing, **remove it from `conformance/baseline.yml`** — stale entries cause CI failure.

### Manual testing

```bash
# Start test server (Streamable HTTP)
STREAMABLE=1 go run ./cmd/testserver

# MCP Inspector
npx @modelcontextprotocol/inspector
# Point it at http://localhost:8787/mcp
```

## Stack Dependencies

**Core module** (`github.com/panyam/mcpkit`):
- `servicekit` v0.0.14 — SSE connection/hub, graceful shutdown, HTTP middleware

**Sub-module** (`github.com/panyam/mcpkit/auth`) — separate `go.mod`:
- `oneauth` — JWT/OIDC validation (only pulled in when you import this sub-module)
