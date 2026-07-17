# mcpkit

Production-grade MCP (Model Context Protocol) server and client library for Go.

[![Go Reference](https://pkg.go.dev/badge/github.com/panyam/mcpkit.svg)](https://pkg.go.dev/github.com/panyam/mcpkit)
[![Go Report Card](https://goreportcard.com/badge/github.com/panyam/mcpkit)](https://goreportcard.com/report/github.com/panyam/mcpkit)
[![CI](https://github.com/panyam/mcpkit/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/panyam/mcpkit/actions/workflows/test.yml)
[![Conformance](https://img.shields.io/badge/MCP%20conformance-passing-success)](https://panyam.github.io/mcpkit/conformance/)
[![Docs](https://img.shields.io/badge/docs-panyam.github.io%2Fmcpkit-blue)](https://panyam.github.io/mcpkit/)
[![License](https://img.shields.io/github/license/panyam/mcpkit)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/panyam/mcpkit?style=social)](https://github.com/panyam/mcpkit/stargazers)

**[Docs site](https://panyam.github.io/mcpkit/)** · **[Examples](https://panyam.github.io/mcpkit/examples/)** · **[Conformance report](https://panyam.github.io/mcpkit/conformance/)** · **[Capabilities](https://panyam.github.io/mcpkit/capabilities/)** · **[Changelog](CHANGELOG.md)** · **[Quick Start](#quick-start)**

## Quick Start

```go
import (
    "time"

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
}, func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
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
| **ext/tasks** | `github.com/panyam/mcpkit/ext/tasks` | Separate module: SEP-2663 v2 tasks (long-running / async tool calls) |
| **ext/otel** | `github.com/panyam/mcpkit/ext/otel` | Separate module: SEP-414 OpenTelemetry tracing adapter |
| **ext/skills** | `github.com/panyam/mcpkit/ext/skills` | Separate module: SEP-2640 skills (data-only, served over resource primitives) |
| **experimental/ext/events** | `github.com/panyam/mcpkit/experimental/ext/events` | Separate module: MCP Events protocol (webhooks, polling, streaming) |
| **testutil** | `github.com/panyam/mcpkit/testutil` | TestClient wrapper for e2e tests |

## Conformance

mcpkit passes the [official MCP conformance suite](https://github.com/modelcontextprotocol/conformance) for the base protocol **and** the conformance scenarios for a long list of draft/recent SEPs — the "batteries" that set it apart from a minimal SDK:

| Suite | Spec | Result |
|-------|------|--------|
| Server (base protocol) | MCP 2025-11-25 | **30/30** scenarios |
| Auth | MCP authorization | **14/14** scenarios |
| MCP Apps | ext-apps | **21** tests |
| Tasks v1 (frozen) | — | **26/27** (1 skipped — SDK-client limitation) |
| Tasks v2 | [SEP-2663](https://github.com/modelcontextprotocol/conformance) | **47/47** (upstream) |
| MRTR | [SEP-2322](https://github.com/modelcontextprotocol/conformance) | **3/3** negative (upstream) |
| Stateless wire | [SEP-2575](https://github.com/modelcontextprotocol/conformance) | **30/30** (upstream) |
| List-TTL | SEP-2549 | **5/5** |
| File-Inputs | SEP-2356 | **7/7** |
| Skills | SEP-2640 | fixture-driven |
| Keycloak interop | — | **12/12** |

Tasks v2, MRTR, and the SEP-2575 stateless wire all run against [`modelcontextprotocol/conformance`](https://github.com/modelcontextprotocol/conformance) `main` directly (merged upstream). The full per-SEP rollup is published at [panyam.github.io/mcpkit/conformance](https://panyam.github.io/mcpkit/conformance/).

Three artifacts describe mcpkit's conformance posture at increasing granularity:

- [`CONFORMANCE.md`](CONFORMANCE.md) — auto-generated per-SEP rollup (regenerated on every PR; CI-gated for staleness). [Live site](https://panyam.github.io/mcpkit/conformance/).
- [`conformance/UPSTREAM_AUDIT.md`](conformance/UPSTREAM_AUDIT.md) — per-scenario pass/fail against upstream's full test set (`just testconf-upstream-audit`). [Live site](https://panyam.github.io/mcpkit/conformance/upstream-audit/).
- [`conformance/AUTH_SPEC_COVERAGE.md`](conformance/AUTH_SPEC_COVERAGE.md) — hand-curated per-clause traceability for the auth surface: every MUST/SHOULD → mcpkit impl file:line → test that proves it.

## Testing

```bash
just test          # Unit tests (200+ across core/server/client)
just testall       # ALL tests + Keycloak + conformance + HTML report
just testconf      # MCP conformance suite
just testconfauth  # Auth conformance
just test-e2e      # E2E tests (auth + apps)
just test-apps-playwright  # ext-apps Playwright suite (needs Node.js)
```

## Documentation

| Doc | What |
|-----|------|
| [Examples gallery](https://panyam.github.io/mcpkit/examples/) | Runnable, batteries-included examples (auth, tasks, apps, tracing, events, skills) — each a guided two-terminal walkthrough. Source under [`examples/`](examples/) |
| [CHANGELOG.md](CHANGELOG.md) | Release notes (Keep a Changelog / SemVer); fuller write-ups in [`docs/releases/`](docs/releases/) |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to build, test, and contribute |
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

## Auth Performance

For best performance, configure your authorization server to use **ES256 (ECDSA P-256)** instead of RS256. ES256 verification is ~10x faster, with smaller keys and tokens. See [auth design doc](ext/auth/docs/DESIGN.md#jwt-algorithm-performance) for details.

## Dependencies

- `servicekit` v0.1.2 — SSE hub, graceful shutdown, HTTP error types
- `oneauth` v0.1.31 — JWT/OIDC (only via `ext/auth` sub-module)

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=panyam/mcpkit&type=Date)](https://star-history.com/#panyam/mcpkit&Date)

If mcpkit is useful to you, starring the repo is the cheapest way to help — it's the main signal we use to prioritize what to ship next.
