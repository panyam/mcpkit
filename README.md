# MCPKit

Production-grade MCP (Model Context Protocol) server and client library for Go.

## Quick Start

```go
import (
    "context"
    "github.com/panyam/mcpkit/core"
    "github.com/panyam/mcpkit/server"
)

srv := server.NewServer(
    core.ServerInfo{Name: "my-server", Version: "0.1.0"},
    server.WithToolTimeout(30 * time.Second),
)

srv.RegisterTool(core.ToolDef{
    Name: "greet", Description: "Say hello",
    InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}},
}, func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
    var args struct{ Name string `json:"name"` }
    req.Bind(&args)
    return core.TextResult("Hello, " + args.Name + "!"), nil
})

srv.Run(":8787") // Streamable HTTP
```

## Packages

| Package | Import | What |
|---------|--------|------|
| **core** | `github.com/panyam/mcpkit/core` | Protocol types (Request, ToolDef, Content, Claims) + tool-handler APIs (Sample, Elicit, EmitLog) |
| **server** | `github.com/panyam/mcpkit/server` | Server, Dispatcher, transports (SSE + Streamable HTTP), middleware |
| **client** | `github.com/panyam/mcpkit/client` | Client, HTTP/stdio/command transports, reconnection, logging |
| **ext/auth** | `github.com/panyam/mcpkit/ext/auth` | Separate module: JWT, PRM, OAuth discovery, DCR, CIMD |
| **ext/ui** | `github.com/panyam/mcpkit/ext/ui` | Separate module: MCP Apps extension (UIExtension, RegisterAppTool) |
| **testutil** | `github.com/panyam/mcpkit/testutil` | TestClient wrapper for e2e tests |

## Conformance

**30/30** server scenarios, **14/14** auth scenarios, **21** MCP Apps conformance tests passing against the [official MCP conformance suite](https://github.com/modelcontextprotocol/conformance) and internal test suites.

## Testing

```bash
make test          # Unit tests (200+ across core/server/client)
make testall       # ALL tests + Keycloak + conformance + HTML report
make testconf      # MCP conformance suite
make testconfauth  # Auth conformance
make test-e2e      # E2E tests (auth + apps)
make test-apps-playwright  # ext-apps Playwright suite (needs Node.js)
```

## Documentation

| Doc | What |
|-----|------|
| [CLAUDE.md](CLAUDE.md) | Quick reference: commands, package structure, gotchas |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Transport design, type definitions, protocol details |
| [ext/auth/docs/DESIGN.md](ext/auth/docs/DESIGN.md) | Auth architecture, spec compliance (C1-C23, X1-X5) |
| [docs/APPS_DESIGN.md](docs/APPS_DESIGN.md) | MCP Apps extension design, protocol flows, conformance strategy |
| [CAPABILITIES.md](CAPABILITIES.md) | Stack component: all capabilities listed |

## Client Features

### Subprocess MCP Servers

Spawn and manage subprocess MCP servers with `CommandTransport`:

```go
c := client.NewClient("", info,
    client.WithCommandTransport("python", []string{"my_server.py"},
        client.WithEnv("DEBUG=1"),
        client.WithShutdownTimeout(10*time.Second),
    ),
    client.WithMaxRetries(3), // auto-restart on crash
)
c.Connect()
defer c.Close()
```

### Custom Request Headers

Inject headers into all outgoing HTTP requests:

```go
c := client.NewClient(url, info,
    client.WithModifyRequest(func(req *http.Request) {
        req.Header.Set("X-Tenant-ID", "acme")
        req.Header.Set("X-Request-ID", uuid.New().String())
    }),
)
```

## Dependencies

- `servicekit` v0.0.14 — SSE hub, graceful shutdown
- `oneauth` v0.0.64 — JWT/OIDC (only via `ext/auth` sub-module)
