# MCPKit

Production-grade MCP (Model Context Protocol) server library for Go.

MCPKit handles the transport, authentication, rate limiting, observability, and operational concerns so you can focus on registering tools. It supports both **stdio** (editor-spawned) and **HTTP+SSE** (remote/hosted) transports.

## Quick Start

```go
package main

import (
    "context"
    "log"
    "net/http"
    "time"
    "github.com/panyam/mcpkit"
)

func main() {
    srv := mcpkit.NewServer(
        mcpkit.ServerInfo{Name: "my-server", Version: "0.1.0"},
        mcpkit.WithBearerToken("my-secret"),
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

    handler := srv.Handler() // serves GET /mcp/sse + POST /mcp/message
    log.Println("MCP server on :8787")
    http.ListenAndServe(":8787", handler)
}
```

## HTTP+SSE Transport

The `srv.Handler()` method returns an `http.Handler` implementing the MCP HTTP+SSE transport (2024-11-05 spec):

- **`GET /mcp/sse`** — Opens an SSE stream. The server sends an `endpoint` event with the POST URL.
- **`POST /mcp/message?sessionId=<id>`** — Receives JSON-RPC requests. Responses are pushed on the SSE stream as `message` events.

Each SSE connection is an independent MCP session with its own initialization state.

### Transport options

```go
handler := srv.Handler(
    mcpkit.WithPrefix("/custom"),                    // URL prefix (default: /mcp)
    mcpkit.WithPublicURL("https://proxy.example.com"), // for reverse proxy
    mcpkit.WithMaxSessions(100),                     // limit concurrent sessions
    mcpkit.WithKeepalivePeriod(15 * time.Second),    // SSE keepalive interval
)
```

### Manual testing

```bash
# Terminal 1: Start test server
go run ./cmd/testserver

# Terminal 2: Open SSE stream
curl -N http://localhost:8787/mcp/sse

# Terminal 3: Send initialize (use sessionId from Terminal 2)
curl -X POST 'http://localhost:8787/mcp/message?sessionId=<id>' \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"1.0"}}}'
```

Or use the MCP Inspector:
```bash
npx @modelcontextprotocol/inspector
# Point it at http://localhost:8787/mcp in the web UI
```

## Protocol Support

MCPKit supports MCP protocol versions **2025-11-25** and **2024-11-05** with automatic version negotiation during the `initialize` handshake. The server validates the client's requested version and responds with the negotiated version, or rejects with the list of supported versions.

## Features

- **HTTP+SSE transport**: Per-session SSE streams with session management, keepalive, and graceful cleanup
- **Protocol negotiation**: Supports MCP 2025-11-25 and 2024-11-05 with version handshake
- **Initialization gating**: Enforces the MCP lifecycle — requests are rejected until the full `initialize` / `notifications/initialized` handshake completes
- **Auth**: Constant-time bearer token, JWT/OIDC via oneauth (optional)
- **Safety**: Tool execution timeouts, configurable session limits
- **Session management**: Concurrency-safe SSE hub via servicekit, per-session dispatchers

## Tool Error Handling

MCPKit follows the MCP spec for error semantics:

- **Tool execution failures** (handler returns `error`) → JSON-RPC success with `isError: true` in the tool result
- **Protocol errors** (bad params, unknown tool) → JSON-RPC error response

This means clients should check `result.isError`, not the JSON-RPC error field, to detect tool-level failures.

## Testing

```bash
make test        # Unit tests (50 tests)
make testconf    # MCP conformance suite (requires Node.js)
make testall     # Both unit + conformance
make smoke       # Curl-based smoke tests (both transports)
make audit       # Security: govulncheck + gosec + gitleaks + race detection
```

### Conformance Suite

MCPKit is validated against the [official MCP conformance test suite](https://github.com/modelcontextprotocol/conformance). Current status: **9/30 scenarios passing** (remaining require resources, prompts, logging, and other unimplemented capabilities — tracked in `conformance/baseline.yml`).

Run a single scenario:
```bash
bash scripts/conformance-test.sh tools-call-simple-text
```

When you implement a new feature and its conformance scenario starts passing, **remove it from `conformance/baseline.yml`** — leaving stale entries causes CI to fail.

## Stack Dependencies

**Core module** (`github.com/panyam/mcpkit`):
- `servicekit` — SSE connection/hub infrastructure, HTTP middleware

**Sub-module** (`github.com/panyam/mcpkit/auth`) — separate `go.mod`:
- `oneauth` — JWT/OIDC validation (only pulled in when you import this sub-module)
