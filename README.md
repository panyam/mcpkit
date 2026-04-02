# MCPKit

Production-grade MCP (Model Context Protocol) server library for Go.

MCPKit handles the transport, authentication, rate limiting, observability, and operational concerns so you can focus on registering tools. It supports both **stdio** (editor-spawned) and **HTTP+SSE** (remote/hosted) transports.

## Quick Start

```go
package main

import (
    "context"
    "github.com/panyam/mcpkit"
)

func main() {
    srv := mcpkit.NewServer(
        mcpkit.WithListen(":8787"),
        mcpkit.WithBearerToken("my-secret"),
        mcpkit.WithToolTimeout(30 * time.Second),
    )

    srv.RegisterTool("greet", mcpkit.ToolDef{
        Description: "Say hello",
        InputSchema: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "name": map[string]any{"type": "string"},
            },
        },
    }, func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
        name := req.StringArg("name")
        return mcpkit.TextResult("Hello, " + name + "!"), nil
    })

    srv.ListenAndServe(context.Background())
}
```

## Features

- **Two transports**: HTTP+SSE (MCP 2024-11-05) and stdio (Content-Length framed)
- **Auth**: Constant-time bearer token, JWT/OIDC via oneauth (optional)
- **Rate limiting**: Per-session and per-IP token bucket
- **Observability**: Structured logging (slog), Prometheus metrics, health endpoint
- **Safety**: Server timeouts, body size limits, subprocess timeouts, allowed-roots
- **Graceful shutdown**: SIGTERM → drain SSE → exit
- **Session management**: Concurrency-safe SSE hub with per-session write mutex
- **CORS**: Configurable origins with OPTIONS preflight

## Stack Dependencies

**Core module** (`github.com/panyam/mcpkit`):
- `goutils` — concurrency utilities

**Sub-module** (`github.com/panyam/mcpkit/auth`) — separate `go.mod`:
- `oneauth` — JWT/OIDC validation (only pulled in when you import this sub-module)
