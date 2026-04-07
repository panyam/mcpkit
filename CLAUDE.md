# CLAUDE.md — MCPKit

## What This Is

Go library for building production-grade MCP servers and clients. Handles transports (SSE + Streamable HTTP), protocol negotiation, session management, auth. Applications register tools, resources, and prompts. Includes a Go MCP client for agents and testing.

## Quick Commands

```bash
make test         # Unit tests (200+ tests, includes parametric 3-transport variants)
make testconf     # MCP conformance suite (needs Node.js)
make testconfauth # MCP Auth conformance — client OAuth tests (needs mcpkit/auth)
make testall      # ALL tests + Keycloak + HTML report (test-reports/report.html)
make smoke        # Curl-based transport tests
make audit        # govulncheck + gosec + gitleaks + race detection
make serve        # Start SSE test server on :8787
make serve-streamable  # Streamable HTTP on :8787
make serve-both   # Both transports

# Auth tests (separate modules, published oneauth v0.0.64)
make test-auth        # Auth sub-module unit tests
make test-auth-e2e    # 31 E2E auth tests (in-process oneauth AS)
make test-auth-keycloak  # 7 Keycloak interop tests (needs Docker)
make upkcl            # Start Keycloak container
make downkcl          # Stop Keycloak container
```

## Key Files

| File | Purpose |
|------|---------|
| `dispatch.go` | JSON-RPC routing, Dispatcher, version negotiation, init gating, cancellation, logging/setLevel, completion/complete, resources/subscribe, resources/unsubscribe |
| `logging.go` | LogLevel, LogMessage, NotifyFunc, EmitLog, context-based notification delivery |
| `progress.go` | ProgressNotification, EmitProgress for long-running tool reporting |
| `completion.go` | CompletionRef, CompletionArgument, CompletionResult, CompletionHandler |
| `auth.go` | Claims, ClaimsProvider, TokenSource, Extension, Stability, ExtensionProvider, AuthClaims(), HasScope() |
| `server.go` | Server, options, Handler(), ListenAndServe(), transport config, CheckAuth, writeAuthError, RegisterExperimental*, subscriptionRegistry, NotifyResourceUpdated |
| `tool.go` | ToolDef (with Annotations), ToolRequest, ToolResult, Content types |
| `resource.go` | ResourceDef, ResourceTemplate, ResourceHandler, ResourceUpdatedNotification types |
| `prompt.go` | PromptDef, PromptArgument, PromptHandler types |
| `pagination.go` | Generic cursor-based pagination helper |
| `jsonrpc.go` | JSON-RPC 2.0 Request/Response/Error |
| `transport.go` | SSE transport (sseTransport, mcpSSEConn, SSEData) |
| `streamable_transport.go` | Streamable HTTP transport (streamableTransport) |
| `middleware.go` | Server-side Middleware type, WithMiddleware, LoggingMiddleware |
| `request.go` | RequestFunc type, server-to-client request infrastructure (sendServerRequest, routeServerResponse) |
| `sampling.go` | CreateMessageRequest/Result, SamplingMessage, Sample() — server-to-client LLM sampling |
| `elicitation.go` | ElicitationRequest/Result, Elicit() — server-to-client user input |
| `client.go` | MCP client: Connect, ToolCall, ReadResource, ListTools, ListResources, SubscribeResource, UnsubscribeResource, WithClientBearerToken, WithTokenSource, WithSamplingHandler, WithElicitationHandler |
| `client_logging.go` | Client-side loggingTransport, WithClientLogging |
| `client_reconnect.go` | Client reconnection: WithMaxRetries, WithReconnectBackoff, isTransientError |
| `client_memory.go` | In-memory transport: WithInMemoryServer, no HTTP overhead |
| `www_authenticate.go` | ParseWWWAuthenticate in core (client transport needs it, core must not depend on auth/) |
| `auth/discovery.go` | DiscoverMCPAuth: PRM fetch, AS metadata discovery, scope selection (C1-C5, C18) |
| `auth/token_source.go` | OAuthTokenSource (full MCP OAuth flow), ClientCredentialsSource, ValidatePKCES256, ValidateCIMDURL |
| `auth/dcr.go` | RegisterClient: client-side RFC 7591 Dynamic Client Registration |
| `auth/server_auth.go` | MountAuth: serves PRM endpoints (/.well-known/oauth-protected-resource) |
| `docs/AUTH_DESIGN.md` | MCP Auth architecture, sequence diagrams, extension system, oneauth integration map |
| `testutil/testclient.go` | TestClient: wraps Client + httptest.Server + testing.T for e2e tests |
| `cmd/testserver/` | Test server with conformance tools, resources, and prompts |
| `cmd/testclient/` | Headless OAuth conformance client (PKCE, PRM discovery, token exchange) |
| `conformance/baseline.yml` | Expected conformance failures — remove entries as features ship |
| `tests/e2e/` | E2E auth tests: real oneauth AS + mcpkit MCP server (separate Go module) |
| `tests/keycloak/` | Keycloak interop tests (separate Go module, requires Docker) |

## Gotchas

- **SSE endpoint event data must be raw text**, not JSON-encoded. Use `SSEText(url)` not `SSEJSON()`. The `sseDataCodec` bypasses `json.Marshal` for this.
- **Per-session Dispatchers**: each SSE/Streamable connection gets its own `Dispatcher` via `newSession()`. All registries (tools, resources, prompts) are shared by reference (read-only after startup). Session state is per-session.
- **`go.mod` must use published servicekit** (not local replace) — CI doesn't have the local source. Currently `v0.0.14`.
- **Conformance baseline**: when a feature passes its conformance test, remove it from `conformance/baseline.yml`. Stale entries cause CI failure.
- **SSE transport sessions** die with the connection (no TTL needed). **Streamable HTTP sessions** persist until DELETE or server restart.
- **Capabilities auto-advertise**: resources/prompts capabilities only appear in initialize response when resources/prompts are actually registered. Logging and completions are always advertised.
- **Server-to-client notifications** (logging, progress) work over both transports. SSE: pushed via hub. Streamable HTTP: POST response switches to SSE streaming (`Content-Type: text/event-stream`) when client sends `Accept: text/event-stream`. Falls back to synchronous JSON if client doesn't accept SSE.
- **Auth checks on ALL endpoints**: SSE `GET /sse`, Streamable HTTP `POST /mcp`, and Streamable `DELETE /mcp` all call `CheckAuth`. The SSE GET was previously unauthenticated — fixed in this auth work.
- **`auth/` is a separate Go module** with its own `go.mod`. Root `go test ./...` does NOT test it. Use `cd auth && go test ./...` or `make test-auth`. Uses published `oneauth v0.0.64`; only `mcpkit` itself uses a `replace` directive (same-repo reference).
- **Extension metadata in initialize**: extensions registered via `WithExtension` appear under `capabilities.extensions` in the initialize response, with `specVersion` and `stability`.
- **Auth spec is 2025-11-25**: See `docs/AUTH_DESIGN.md` for spec compliance checklist. Key: `resource` param (RFC 8707) is MUST, PKCE S256 is MUST, audience validation is MUST.
- **JWTValidator uses direct jwt.Parse with JWKS keyfunc**, NOT `APIAuth.ValidateAccessTokenFull` (which doesn't support kid-based JWKS lookup). The custom `jwksKeyFunc` method on `JWTValidator` resolves keys via `JWKSKeyStore.GetKeyByKid`.
- **`tests/e2e/` and `tests/keycloak/` are separate Go modules**. They use published `oneauth v0.0.64` and `replace` directives only for same-repo mcpkit references. NOT tested by root `go test ./...`. Run via `make test-auth-e2e` or `make test-auth-keycloak`.
- **Client transport retries on 401/403**: `doWithAuthRetry` handles token refresh (401) and scope step-up (403 via `ScopeAwareTokenSource.TokenForScopes`). Max 1 retry per status code. Static tokens (`WithClientBearerToken`) cannot refresh — 401 returns `ClientAuthError` immediately. `ParseWWWAuthenticate` lives in core (not `auth/`) so the transport can parse scope hints without depending on the auth sub-module.
- **Server middleware** runs after auth but before dispatch. `WithMiddleware(mw...)` — first registered = outermost. Tool timeout is now the innermost handler in the middleware chain. Middleware sees claims via `AuthClaims(ctx)`.
- **Client reconnection** (`WithMaxRetries`, `WithReconnectBackoff`) — on transient transport errors (EOF, connection reset), client tears down, re-creates transport, re-initializes MCP session, and retries. Auth errors (401/403) are NOT transient — handled by `doWithAuthRetry` instead.
- **Client logging** (`WithClientLogging(logger)`) wraps the transport decorator pattern. Logs method name, latency, errors for every connect/call/notify/close.
- **Parametric tests via `forAllTransports`**: Core client tests run against all 3 transports (Streamable HTTP, SSE, in-memory) as subtests. Use this pattern for any test that should work across transports.
- **In-memory transport** (`WithInMemoryServer(srv)`) calls `Server.Dispatch()` directly — no HTTP, no serialization. Use for fast tests and embedded scenarios. `Connect()` skips transport creation when one is already set (by `WithInMemoryServer`).
- **Stateless mode** (`WithStateless(true)`) auto-initializes a fresh dispatcher per request — no sessions, no `Mcp-Session-Id`. For serverless, CLI wrappers, single-shot APIs.
- **Don't brute-force failing tests** — when a test is hard to debug, check if there are open issues for dev-ex improvements (logging, in-memory transport, middleware) that would make the code more testable. In this project, #69 (WithClientLogging) and #68 (in-memory transport) directly unblocked the scope-step-up conformance fix.
- **Resource subscriptions** require `WithSubscriptions()` server option. Subscription registry lives on `Server`; per-session subscription state on `Dispatcher`. `Server.NotifyResourceUpdated(uri)` fans out to all subscribed sessions. Transports clean up subscriptions on session close.
- **`WithNotificationHandler(fn)`** works across all 3 transports (Streamable HTTP, SSE, in-memory). Notifications emitted during a tool call are delivered to the handler before the tool result is returned. Params are always `map[string]any` (JSON-roundtripped for consistency). See `docs/ARCHITECTURE.md` "Notification Delivery Order Guarantees" for per-transport semantics.
- **Server-to-client requests** (sampling, elicitation) use `RequestFunc` — analogous to `NotifyFunc` but blocks for a response. The flow: tool handler calls `Sample(ctx, req)` or `Elicit(ctx, req)` → extracts `RequestFunc` from session context → `sendServerRequest` generates `"srv-N"` ID, registers pending channel, pushes JSON-RPC request to client stream, waits on channel with context timeout → client handles request, POSTs response back → transport detects response (no `method` field via `isJSONRPCResponse`), routes via `Dispatcher.RouteResponse()` → pending channel unblocks.
- **Client-side handler registration**: `WithSamplingHandler(h)` and `WithElicitationHandler(h)` register client-side handlers. When set, the client advertises `sampling`/`elicitation` capabilities during `initialize`. Server checks `clientCaps` before sending requests — returns `ErrSamplingNotSupported`/`ErrElicitationNotSupported` if capability not declared.
- **SSE client background reader**: The SSE client transport uses a background goroutine (started in `connect()`) that demuxes all SSE events — responses to pending calls by ID, server requests to handler. The old synchronous `call()` → `readSSEEvent()` pattern is replaced with `pendingCalls sync.Map` + channel per request.
- **Streamable HTTP client handles server requests inline**: During `readSSEResponse`, server requests (events with `method` field) are handled and responses POSTed back before continuing to read the final tool result. No background goroutine needed — server requests arrive on the same SSE stream as the tool response.
- **In-memory transport bidirectional support**: `memoryTransport.connect()` wires `dispatcher.pushRequest` to call `client.handleServerRequest` directly. Server-to-client requests are dispatched synchronously in-process — no channels, no HTTP.
- **`DiscoverMCPAuth`** performs the full MCP OAuth discovery chain: probe server → 401 → parse WWW-Authenticate → fetch PRM (path-based then root fallback) → extract authorization_servers → discover AS metadata via oneauth `client.DiscoverAS`. Returns `MCPAuthInfo` with PRM, AS metadata, and scopes. Accepts `WithHTTPClient` option for testability.
- **`OAuthTokenSource` discovery flow**: Calls `DiscoverMCPAuth` on first `Token()` call, caches result. Passes discovered endpoints explicitly to `LoginWithBrowser` — bypasses oneauth's internal re-discovery which would incorrectly treat the MCP server URL as an OAuth issuer. PKCE S256 pre-flight validation (C11/C12) and HTTPS enforcement (X1) run before login.
- **Client registration priority (C6)**: `OAuthTokenSource.resolveClientID()` follows: pre-registered `ClientID` → CIMD `ClientMetadataURL` → DCR (if `EnableDCR` and AS has `registration_endpoint`) → error. CIMD URL validated per C8 (https, has path). DCR via `RegisterClient()` in `auth/dcr.go`.
- **Well-known PRM URL construction**: `scheme://host/.well-known/oauth-protected-resource` + path (NOT `serverURL + "/.well-known/..."`). E.g., for `http://host/mcp` → `http://host/.well-known/oauth-protected-resource/mcp`.
- **oneauth/testutil.TestAuthServer** provides the in-process auth server for E2E tests. It generates RSA keys, serves JWKS, and mints tokens. Set audience after creation via `AS.APIAuth.JWTAudience` (the `WithAudience` option is set at creation time, before server URL is known).

## Architecture

See `docs/ARCHITECTURE.md` for transport design, type definitions, and protocol details.

## Conformance Status

### Server conformance
30/30 MCP server conformance scenarios passing. All server features implemented.

### Auth conformance
14/14 required MCP auth conformance scenarios passing (210/210 checks). 1 warning on basic-cimd (CIMD not implemented). Run via `make testconfauth`.

## What's Not Implemented Yet

- stdio transport (#3)
- Streamable HTTP GET SSE stream (server-initiated notifications without a request)
