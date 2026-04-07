# CLAUDE.md — MCPKit

## What This Is

Go library for building production-grade MCP servers and clients. Split into three packages:
- **`core/`** — Protocol types (Request, Response, ToolDef, Content, Claims, etc.) and tool-handler APIs (Sample, Elicit, EmitLog)
- **`server/`** — Server, Dispatcher, transports (SSE + Streamable HTTP), middleware, subscriptions
- **`client/`** — Client, HTTP transports, reconnection, logging
- **`ext/auth/`** — Separate Go module: JWTValidator, MountAuth (PRM), OAuthTokenSource, DCR, CIMD

## Quick Commands

```bash
make test         # Unit tests (200+ tests across core/server/client)
make testconf     # MCP conformance suite (needs Node.js)
make testconfauth # MCP Auth conformance — client OAuth tests
make testall      # ALL tests + Keycloak + HTML report (test-reports/report.html)
make smoke        # Curl-based transport tests
make audit        # govulncheck + gosec + gitleaks + race detection

# Auth tests (ext/auth is a separate Go module)
make test-auth        # Auth sub-module unit tests (cd ext/auth)
make test-auth-e2e    # 31 E2E auth tests (in-process oneauth AS)
make testkcl          # 7 Keycloak interop tests (needs Docker)
make upkcl            # Start Keycloak container (with event logging)
make downkcl          # Stop Keycloak container
make kcllogs          # View Keycloak logs (shows token events)
```

## Package Structure

```
mcpkit/
├── core/                    ← Protocol types + tool-handler APIs
│   ├── jsonrpc.go             Request, Response, Error, ErrCode*, IsJSONRPCResponse
│   ├── tool.go                ToolDef, ToolRequest, ToolResult, Content, ToolHandler
│   ├── resource.go            ResourceDef, ResourceTemplate, ResourceHandler
│   ├── prompt.go              PromptDef, PromptHandler
│   ├── completion.go          CompletionHandler, CompletionRef, CompletionResult
│   ├── auth.go                Claims, TokenSource, AuthValidator, AuthError, Extension
│   ├── logging.go             LogLevel, NotifyFunc, EmitLog, ContextWithSession
│   ├── progress.go            EmitProgress
│   ├── sampling.go            CreateMessageRequest/Result, Sample()
│   ├── elicitation.go         ElicitationRequest/Result, Elicit()
│   ├── request.go             RequestFunc, ErrNoRequestFunc
│   ├── protocol.go            ServerInfo, ClientInfo, ClientCapabilities
│   ├── interfaces.go          Transport, ServerRequestHandler, NotificationHandler
│   └── www_authenticate.go    ParseWWWAuthenticate
│
├── server/                  ← Server + Dispatcher + transports
│   ├── server.go              Server, NewServer, options, Handler(), Run()
│   ├── dispatch.go            Dispatcher, JSON-RPC routing, all method handlers
│   ├── transport.go           SSE transport
│   ├── streamable_transport.go Streamable HTTP transport
│   ├── memory_transport.go    InProcessTransport (core.Transport impl)
│   ├── request.go             sendServerRequest, routeServerResponse, pending map
│   ├── middleware.go          Middleware, LoggingMiddleware, WithMiddleware
│   └── pagination.go          cursor-based pagination
│
├── client/                  ← Client + all client transports
│   ├── client.go              Client, NewClient, Connect, ToolCall, WithTransport
│   ├── client_logging.go      loggingTransport, WithClientLogging
│   └── client_reconnect.go    WithMaxRetries, WithReconnectBackoff
│
├── ext/auth/                ← Separate Go module (ext/auth/go.mod)
│   ├── discovery.go           DiscoverMCPAuth (PRM + AS metadata)
│   ├── token_source.go        OAuthTokenSource, ClientCredentialsSource
│   ├── dcr.go                 RegisterClient (RFC 7591)
│   ├── jwt_validator.go       JWTValidator (JWKS-based)
│   ├── server_auth.go         MountAuth (PRM endpoints)
│   ├── scopes.go              RequireScope
│   └── docs/DESIGN.md         Auth architecture + spec compliance
│
├── testutil/                ← Test helpers
├── cmd/testserver/          ← Conformance test server
├── cmd/testclient/          ← Headless OAuth conformance client
├── conformance/baseline.yml ← Expected conformance failures
├── tests/e2e/               ← E2E auth tests (separate Go module)
└── tests/keycloak/          ← Keycloak interop tests (separate Go module)
```

## Gotchas

### Package Split
- **Three packages, no cycles**: `core ← server`, `core ← client`, `core ← ext/auth`. Server and client never import each other.
- **`ext/auth/` is a separate Go module** with its own `go.mod`. Root `go test ./...` does NOT test it. Use `make test-auth`.
- **`tests/e2e/` and `tests/keycloak/` are separate Go modules** with `replace` directives pointing to `../../` (root) and `../../ext/auth`.
- **In-process transport** uses `core.Transport` interface. Create via `server.NewInProcessTransport(srv)`, pass to client via `client.WithTransport(transport)`. For bidirectional (sampling/elicitation), wire `server.WithServerRequestHandler(client.HandleServerRequest)`.
- **`core.ContextWithSession`** is exported so `server/` can inject session state. Tool handlers use `core.EmitLog`, `core.Sample`, `core.AuthClaims` — they extract from context internally.

### Transports
- **SSE endpoint event data must be raw text**, not JSON-encoded. Use `SSEText(url)` not `SSEJSON()`.
- **Per-session Dispatchers**: each connection gets its own `Dispatcher` via `newSession()`. Registries shared by reference.
- **SSE transport sessions** die with the connection. **Streamable HTTP sessions** persist until DELETE or server restart.
- **Notification delivery order**: notifications arrive before tool results across all transports.

### Auth
- **Auth spec is 2025-11-25**: See `ext/auth/docs/DESIGN.md` for spec compliance (all C1-C23, X1-X5 requirements Done).
- **Well-known PRM URL**: `scheme://host/.well-known/oauth-protected-resource/path` (NOT `serverURL + "/.well-known/..."`).
- **`OAuthTokenSource` calls `DiscoverMCPAuth`** on first `Token()`, caches result. Passes discovered endpoints explicitly to `LoginWithBrowser`.
- **Client registration priority (C6)**: pre-registered `ClientID` → CIMD `ClientMetadataURL` → DCR (if `EnableDCR`) → error.
- **Keycloak container** runs with `--log-level=INFO,org.keycloak.events:DEBUG` for token event visibility.

### Testing
- **`forAllTransports`**: parametric tests run against Streamable HTTP, SSE, and in-memory. Use for any cross-transport test.
- **In-process transport skips JSON envelope serialization** — catches logic bugs. HTTP tests catch wire format bugs. Both needed.
- **Conformance baseline**: when a feature passes, remove from `conformance/baseline.yml`. Stale entries cause CI failure.

## Conformance Status

### Server conformance
30/30 MCP server conformance scenarios passing. All server features implemented.

### Auth conformance
14/14 required MCP auth conformance scenarios passing (210/210 checks). Run via `make testconfauth`.

## What's Not Implemented Yet

- stdio transport (#3)
- Streamable HTTP GET SSE stream (server-initiated notifications without a request)
