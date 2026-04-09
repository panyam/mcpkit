# CLAUDE.md — MCPKit

## What This Is

Go library for building production-grade MCP servers and clients. Split into three packages:
- **`core/`** — Protocol types (Request, Response, ToolDef, Content, Claims, etc.) and tool-handler APIs (Sample, Elicit, EmitLog)
- **`server/`** — Server, Dispatcher, transports (SSE + Streamable HTTP + Stdio), middleware, subscriptions
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

# Extension sub-module tests (separate Go modules)
make test-auth        # Auth sub-module unit tests (cd ext/auth)
make test-ui          # UI sub-module unit tests (cd ext/ui)
make test-e2e         # All E2E tests (auth + apps, no Docker)
make testkcl          # 7 Keycloak interop tests (needs Docker)
make upkcl            # Start Keycloak container (with event logging)
make downkcl          # Stop Keycloak container
make kcllogs          # View Keycloak logs (shows token events)
```

## Package Structure

```
mcpkit/
├── core/                    ← Protocol types + tool-handler APIs
│   ├── jsonrpc.go             Request, Response, Error, ErrCode*, IsJSONRPCResponse, PingResult
│   ├── tool.go                ToolDef (+_meta), ToolRequest, ToolResult, ToolsListResult, Content, ToolHandler
│   ├── resource.go            ResourceDef, ResourceTemplate, ResourcesListResult, ResourceTemplatesListResult, ResourceReadContent (+_meta), ResourceHandler
│   ├── prompt.go              PromptDef, PromptsListResult, PromptHandler
│   ├── completion.go          CompletionHandler, CompletionRef, CompletionResult, CompletionCompleteResult
│   ├── auth.go                Claims, TokenSource, AuthValidator, AuthError, Extension, RefValidator
│   ├── logging.go             LogLevel, NotifyFunc, EmitLog, NotifyResourcesChanged, ContextWithSession
│   ├── progress.go            EmitProgress
│   ├── sampling.go            CreateMessageRequest/Result, Sample()
│   ├── elicitation.go         ElicitationRequest/Result, Elicit()
│   ├── request.go             RequestFunc, ErrNoRequestFunc
│   ├── protocol.go            ServerInfo, ClientInfo, ClientCapabilities, ServerCapabilities, ToolsCap, ResourcesCap, PromptsCap, InitializeResult, ExtensionCapability
│   ├── interfaces.go          Transport, ServerRequestHandler, NotificationHandler
│   ├── ui.go                  UIMetadata, UICSPConfig, UIVisibility, AppMIMEType, ToolMeta, ResourceContentMeta
│   └── www_authenticate.go    ParseWWWAuthenticate
│
├── server/                  ← Server + Dispatcher + transports
│   ├── server.go              Server, NewServer, options, Handler(), Run(), Broadcast(), Registry()
│   ├── registry.go            Registry, AddTool/RemoveTool, AddResource/RemoveResource, AddPrompt/RemovePrompt, OnChange
│   ├── dispatch.go            Dispatcher, JSON-RPC routing, all method handlers
│   ├── transport.go           SSE transport
│   ├── streamable_transport.go Streamable HTTP transport
│   ├── stdio_transport.go     Stdio transport (Content-Length framed JSON-RPC)
│   ├── memory_transport.go    InProcessTransport (core.Transport impl)
│   ├── request.go             sendServerRequest, routeServerResponse, pending map
│   ├── middleware.go          Middleware, LoggingMiddleware, WithMiddleware
│   └── pagination.go          cursor-based pagination
│
├── client/                  ← Client + all client transports
│   ├── client.go              Client, NewClient, Connect, ToolCall, WithTransport, WithExtension, WithUIExtension, WithGetSSEStream, ServerSupportsExtension, ServerSupportsUI, ListToolsForModel, ResolveEndpointURL, HTTPStatusError, DoWithAuthRetry
│   ├── stdio_transport.go     StdioTransport, NewStdioTransport, WithStdioTransport
│   ├── client_logging.go      loggingTransport, WithClientLogging
│   └── client_reconnect.go    WithMaxRetries, WithReconnectBackoff, IsTransientError
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
├── ext/ui/                 ← Separate Go module (ext/ui/go.mod)
│   └── extension.go          UIExtension (ExtensionProvider + RefValidator), RegisterAppTool, AppToolConfig
│
├── testutil/                ← Test helpers (NewTestServer, ForAllTransports, TestClient)
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
- **SSE endpoint URL resolution**: Client resolves the endpoint event URL against the SSE connection URL via `ResolveEndpointURL` (RFC 3986). Handles absolute URLs, absolute paths, and relative paths.
- **Per-session Dispatchers**: each connection gets its own `Dispatcher` via `newSession()`. The `Registry` is shared by pointer — all sessions see the same tools/resources/prompts.
- **SSE transport sessions** die with the connection. **Streamable HTTP sessions** persist until DELETE, idle timeout, or server restart. **Stdio sessions** last for the process lifetime (1:1 mapping).
- **Session idle timeout**: `WithSessionTimeout(d)` enables automatic cleanup of abandoned Streamable HTTP sessions. Uses `gocurrent.IdleTimer` for ref-counted idle tracking — timer pauses during active requests (Acquire/Release). Default is 0 (no timeout).
- **Stdio transport** uses Content-Length framed JSON-RPC over stdin/stdout (framing via `servicekit/http.WriteFrame`/`ReadFrame`). Server-side: `srv.RunStdio(ctx)`. Client-side: `client.WithStdioTransport(reader, writer)`. No HTTP, no auth — process boundary is the trust boundary. Debug logging goes to stderr.
- **Notification delivery order**: notifications arrive before tool results across all transports.
- **HTTP error classification**: Both transports return `HTTPStatusError` (type alias for `servicekit/http.HTTPStatusError`) for non-2xx responses (excluding 401/403, handled by `DoWithAuthRetry`). `IsTransientError` classifies 5xx as transient (retriable via `WithMaxRetries`), 4xx as terminal. `servicekit/http.IsHTTPTransient` provides the status-code-only classification.
- **SSE reader death**: `call()` uses dual-select on the response channel and the done channel — returns a transient error immediately if the background reader dies, instead of blocking forever.
- **Client GET SSE stream**: Opt-in via `WithGetSSEStream()`. Opens a background `GET /mcp` SSE stream after Connect() for receiving server-initiated notifications outside POST request-response cycles (Streamable HTTP only). Notification callback (`WithNotificationCallback`) must be goroutine-safe when enabled. Re-established automatically on reconnection.
- **Dispatcher.notifyFunc thread safety**: `notifyFunc` is protected by `notifyMu` (RWMutex). Use `SetNotifyFunc()` / `getNotifyFunc()` — never access the field directly.
- **Broadcast vs NotifyResourceUpdated**: `Server.Broadcast(method, params)` fans out to ALL connected sessions unconditionally. `NotifyResourceUpdated(uri)` only targets sessions that called `resources/subscribe`. Broadcast only reaches HTTP transport sessions (SSE + Streamable HTTP), not in-process — consistent with `CloseSession`/`CloseAllSessions`.

### Dynamic Registration
- **`Registry`** is the shared, thread-safe registry for tools, resources, prompts, and completions. Access via `srv.Registry()`. All session dispatchers share the same `*Registry` pointer.
- **`Registry.AddTool` / `RemoveTool`** (and Resource, Prompt variants) acquire write lock, modify the registry, then call `OnChange` to broadcast `notifications/*/list_changed` to all sessions.
- **`Registry.OnChange`** is wired by `NewServer` to `Server.Broadcast`. Pre-serve `RegisterTool` calls also trigger OnChange but Broadcast is a no-op with zero sessions.
- **RLock scoping in handlers**: `handleToolsCall` acquires RLock only for the map lookup, releases before executing the handler. Tool execution is never under lock.
- **`listChanged: true`** is always advertised in capabilities for tools, resources, and prompts, regardless of current registry contents.

### Auth
- **Auth spec is 2025-11-25**: See `ext/auth/docs/DESIGN.md` for spec compliance (all C1-C23, X1-X5 requirements Done).
- **Well-known PRM URL**: `scheme://host/.well-known/oauth-protected-resource/path` (NOT `serverURL + "/.well-known/..."`).
- **`OAuthTokenSource` calls `DiscoverMCPAuth`** on first `Token()`, caches result. Passes discovered endpoints explicitly to `LoginWithBrowser`.
- **Client registration priority (C6)**: pre-registered `ClientID` → CIMD `ClientMetadataURL` → DCR (if `EnableDCR`) → error.
- **Keycloak container** runs with `--log-level=INFO,org.keycloak.events:DEBUG` for token event visibility.

### MCP Apps (io.modelcontextprotocol/ui)
- **"Apps" = feature name, "ui" = extension ID**. The spec repo is `ext-apps`, the wire ID is `io.modelcontextprotocol/ui`. Our package is `ext/ui/` to match the ID.
- **`ext/ui/` is a separate Go module** — tested via `make test-ui`, not by root `go test ./...`.
- **`UIExtensionID`** constant in `core/ui.go` — use this instead of hardcoding the string.
- **Server-side detection**: `core.ClientSupportsUI(ctx)` in tool handlers checks if client declared UI extension support.
- **Client-side detection**: `client.ServerSupportsUI()` checks if server advertised the extension.
- **`NotifyResourcesChanged(ctx)`** — call from tool handlers after mutating state so clients know to re-fetch resources.
- **`RegisterAppTool`** lives in `ext/ui/`, takes a `ToolResourceRegistrar` interface (not `*server.Server`) to avoid cross-module import.
- **`RefValidator`** interface on `ExtensionProvider` — `UIExtension` validates `_meta.ui.resourceUri` refs at `Handler()` startup. Warnings only, no errors.
- **`PrefersBorder`** is `*bool` tri-state: nil (host decides), true (border), false (no border).
- **`ListToolsForModel()`** is client-side filtering — server always returns all tools including app-only. Visibility is a presentation hint, not access control.
- **Playwright tests**: `make test-apps-playwright` runs the upstream ext-apps Playwright suite against our testserver. Not in `testall` — run manually when needed.
- **Design doc**: see `docs/APPS_DESIGN.md` for full architecture, protocol flows, and conformance strategy.

### Testing
- **`testutil.NewTestServer()`**: standard test server with echo, fail, resource, and template fixtures. Use as the base for all test servers; add custom tools after creation.
- **`testutil.ForAllTransports(t, srv, fn)`**: parametric test runner for all 4 transports (Streamable HTTP, SSE, in-memory, stdio). Use for any transport-agnostic test. Exported from `testutil/` so it's reusable across `client_test` and `server_test` packages.
- **`testutil.InitHandshake(d)`**: performs initialize + notifications/initialized handshake on any `Dispatch`-compatible type. Use for raw Dispatcher/Server tests that don't go through a client.
- **`testutil.NewTestClient(t, srv)`**: wraps `client.Client` with `t.Fatal` error handling. Currently Streamable HTTP only.
- **Import cycle constraint**: `server/` package white-box tests (`package server`) cannot import `testutil` because `testutil` imports `server`. These tests keep local handshake helpers (`initDispatcher`, `initServer`) and local server factories. Only black-box tests (`package server_test`, `package client_test`) can use `testutil`.
- **In-process transport skips JSON envelope serialization** — catches logic bugs. HTTP tests catch wire format bugs. Stdio tests catch Content-Length framing bugs. All needed.
- **Conformance baseline**: when a feature passes, remove from `conformance/baseline.yml`. Stale entries cause CI failure.

## Conformance Status

### Server conformance
30/30 MCP server conformance scenarios passing. All server features implemented.

### Auth conformance
14/14 required MCP auth conformance scenarios passing (210/210 checks). Run via `make testconfauth`.

### Apps conformance
21 MCP Apps conformance tests passing (tool metadata, resources, visibility, fallback, negotiation). Run via `make test-e2e`.

## What's Not Implemented Yet

(none — both stdio and GET SSE stream are now implemented)
