# MCPKit

Production-grade MCP (Model Context Protocol) server library for Go.

MCPKit handles the transport, authentication, rate limiting, observability, and operational concerns so you can focus on registering tools. It supports both **stdio** (editor-spawned) and **HTTP+SSE** (remote/hosted) transports.

## Quick Start

```go
package main

import (
    "context"
    "time"
    "github.com/panyam/mcpkit"
)

func main() {
    srv := mcpkit.NewServer(
        mcpkit.ServerInfo{Name: "my-server", Version: "0.1.0"},
        mcpkit.WithListen(":8787"),
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

    srv.ListenAndServe(context.Background())
}
```

## Protocol Support

MCPKit supports MCP protocol versions **2025-11-25** and **2024-11-05** with automatic version negotiation during the `initialize` handshake. The server validates the client's requested version and responds with the negotiated version, or rejects with the list of supported versions.

## Features

- **Two transports**: HTTP+SSE (MCP 2024-11-05) and stdio (Content-Length framed)
- **Protocol negotiation**: Supports MCP 2025-11-25 and 2024-11-05 with version handshake
- **Initialization gating**: Enforces the MCP lifecycle — requests are rejected until the full `initialize` / `notifications/initialized` handshake completes
- **Auth**: Constant-time bearer token, JWT/OIDC via oneauth (optional)
- **Rate limiting**: Per-session and per-IP token bucket
- **Observability**: Structured logging (slog), Prometheus metrics, health endpoint
- **Safety**: Server timeouts, body size limits, subprocess timeouts, allowed-roots
- **Graceful shutdown**: SIGTERM → drain SSE → exit
- **Session management**: Concurrency-safe SSE hub with per-session write mutex
- **CORS**: Configurable origins with OPTIONS preflight

## Tool Error Handling

MCPKit follows the MCP spec for error semantics:

- **Tool execution failures** (handler returns `error`) → JSON-RPC success with `isError: true` in the tool result
- **Protocol errors** (bad params, unknown tool) → JSON-RPC error response

This means clients should check `result.isError`, not the JSON-RPC error field, to detect tool-level failures.

## Stack Dependencies

**Core module** (`github.com/panyam/mcpkit`):
- `goutils` — concurrency utilities

**Sub-module** (`github.com/panyam/mcpkit/auth`) — separate `go.mod`:
- `oneauth` — JWT/OIDC validation (only pulled in when you import this sub-module)
