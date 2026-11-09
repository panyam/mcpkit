# mcpkit

Production-grade MCP (Model Context Protocol) server and client library for Go.

[![Go Reference](https://pkg.go.dev/badge/github.com/panyam/mcpkit.svg)](https://pkg.go.dev/github.com/panyam/mcpkit)
[![Go Report Card](https://goreportcard.com/badge/github.com/panyam/mcpkit)](https://goreportcard.com/report/github.com/panyam/mcpkit)
[![CI](https://github.com/panyam/mcpkit/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/panyam/mcpkit/actions/workflows/test.yml)
[![Conformance](https://img.shields.io/endpoint?url=https%3A%2F%2Fpanyam.github.io%2Fmcpkit%2Fconformance%2Fbadge.json)](https://panyam.github.io/mcpkit/conformance/)
[![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fpanyam.github.io%2Fmcpkit%2Fcoverage%2Fbadge.json)](https://github.com/panyam/mcpkit/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/panyam/mcpkit?sort=semver)](https://github.com/panyam/mcpkit/releases)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/panyam/mcpkit/badge)](https://scorecard.dev/viewer/?uri=github.com/panyam/mcpkit)
[![Docs](https://img.shields.io/badge/docs-panyam.github.io%2Fmcpkit-blue)](https://panyam.github.io/mcpkit/)
[![License](https://img.shields.io/github/license/panyam/mcpkit)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/panyam/mcpkit?style=social)](https://github.com/panyam/mcpkit/stargazers)

**[Docs site](https://panyam.github.io/mcpkit/)** · **[Examples](https://panyam.github.io/mcpkit/examples/)** · **[Conformance report](https://panyam.github.io/mcpkit/conformance/)** · **[Capabilities](https://panyam.github.io/mcpkit/capabilities/)** · **[Changelog](CHANGELOG.md)** · **[Quick Start](#quick-start)**

## Why mcpkit

- **Batteries included.** OAuth and JWT auth, Tasks, Skills, MCP Apps, Events and OpenTelemetry ship beside the core. Each is its own Go module, so the core never pulls in their dependencies.
- **Conformance first.** mcpkit passes every tier-scored scenario in the official [MCP conformance suite](https://github.com/modelcontextprotocol/conformance): server 31/31 and client 21/21. CI regenerates the report on every PR.
- **Both wires.** One server answers session clients on 2025-11-25 and stateless clients on the 2026-07-28 draft, through one request path.
- **Draft SEPs early.** Extensions still in review, such as MCP Events and agent discovery, ship under `experimental/` with conformance scenarios of their own. Their APIs can change between releases.

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

srv.Run(":8787") // Streamable HTTP — blocks
```

`Run` blocks. When you start it in a goroutine, wait for `Ready()` instead of
sleeping. It closes once the listener is bound and the port is accepting:

```go
go srv.Run(":8787")
<-srv.Ready()
// safe to connect; srv.Addr() has the bound address (useful with ":0")
```

A client mirrors it. Every method that talks to the server takes a context:

```go
ctx := context.Background()

c := client.NewClient("http://localhost:8787/mcp",
    core.ClientInfo{Name: "my-client", Version: "0.1.0"},
)
if err := c.Connect(ctx); err != nil { // bounds the handshake, not the session
    log.Fatal(err)
}
defer c.Close()

out, err := c.ToolCall(ctx, "greet", map[string]any{"name": "world"})
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
| **experimental/ext/agents** | `github.com/panyam/mcpkit/experimental/ext/agents` | Separate module: server-declared agent discovery (agents-wg#22), with a Go client |
| **testutil** | `github.com/panyam/mcpkit/testutil` | TestClient wrapper for e2e tests |

## Conformance

mcpkit passes the [official MCP conformance suite](https://github.com/modelcontextprotocol/conformance) for the base protocol, and the scenarios for the newer SEPs it implements:

| Suite | Spec | Result |
|-------|------|--------|
| Server tier-check | MCP 2025-11-25 | **31/31** scenarios, 73 checks |
| Client tier-check | MCP 2025-11-25 | **21/21** scenarios, 581 checks |
| Tasks v2 | SEP-2663 | **44** pass, 1 skip |
| MRTR | SEP-2322 | **36** pass, 1 warn |
| Stateless wire | SEP-2575 | **30/30** |
| Skills | SEP-2640 | **45/45** |
| Events (draft) | SEP-3415 | **130** pass, 11 warn |
| Agent discovery (draft) | agents-wg#22 | **32/32** |

The first six run against upstream `main`. We wrote the Events and agent discovery scenarios ourselves and proposed them upstream, where they are still in review. Authorization, MCP Apps, Tasks v1 and Keycloak interop have test suites of their own, and `make testall` runs everything.

The counts come from [`CONFORMANCE.md`](CONFORMANCE.md). It is generated, and CI fails a PR that leaves it stale, so read numbers there before quoting them.

Three artifacts describe mcpkit's conformance posture at increasing granularity:

- [`CONFORMANCE.md`](CONFORMANCE.md) — auto-generated per-SEP rollup (regenerated on every PR; CI-gated for staleness). [Live site](https://panyam.github.io/mcpkit/conformance/).
- [`conformance/UPSTREAM_AUDIT.md`](conformance/UPSTREAM_AUDIT.md) — per-scenario pass/fail against upstream's full test set (`just testconf-upstream-audit`). [Live site](https://panyam.github.io/mcpkit/conformance/upstream-audit/).
- [`conformance/AUTH_SPEC_COVERAGE.md`](conformance/AUTH_SPEC_COVERAGE.md) — hand-curated per-clause traceability for the auth surface: every MUST/SHOULD → mcpkit impl file:line → test that proves it.

## Testing

`make` is the supported task runner and the only one CI uses, so `make` is
preinstalled everywhere and needs no setup step.

```bash
make test          # Unit tests (200+ across core/server/client)
make testall       # ALL tests + Keycloak + conformance + HTML report
make testconf      # MCP conformance suite
make testconfauth  # Auth conformance
make test-e2e      # E2E tests (auth + apps)
make test-apps-playwright  # ext-apps Playwright suite (needs Node.js)
```

Justfiles mirroring the same target names also exist and are kept as an
experiment. They are not used by CI, and `make` is authoritative when the two
disagree.

## Documentation

| Doc | What |
|-----|------|
| [Examples gallery](https://panyam.github.io/mcpkit/examples/) | Runnable, batteries-included examples (auth, tasks, apps, tracing, events, skills) — each a guided two-terminal walkthrough. Source under [`examples/`](examples/) |
| [CHANGELOG.md](CHANGELOG.md) | Release notes (Keep a Changelog / SemVer); fuller write-ups in [`docs/releases/`](docs/releases/) |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to build, test, and contribute |
| [CLAUDE.md](CLAUDE.md) | Quick reference: commands, package structure, gotchas |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Transport design, type definitions, protocol details |
| [docs/PROMPTS.md](docs/PROMPTS.md) | Prompts: arguments, content types, listing |
| [docs/ELICITATION.md](docs/ELICITATION.md) | Elicitation: schemas, SEP-1034 defaults, enums, URL mode |
| [docs/COMPLETIONS.md](docs/COMPLETIONS.md) | Argument completion for prompt arguments and resource URI templates |
| [ext/auth/docs/DESIGN.md](ext/auth/docs/DESIGN.md) | Auth architecture, spec compliance (C1-C23, X1-X5) |
| [docs/APPS_DESIGN.md](docs/APPS_DESIGN.md) | MCP Apps extension design, protocol flows, conformance strategy |

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

- `servicekit` v0.1.3 — SSE hub, graceful shutdown, HTTP error types
- `oneauth` v0.1.31 — JWT/OIDC (only via `ext/auth` sub-module)

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=panyam/mcpkit&type=Date)](https://star-history.com/#panyam/mcpkit&Date)

If mcpkit is useful to you, starring the repo is the cheapest way to help, since it's the main signal we use to prioritize what to ship next.
