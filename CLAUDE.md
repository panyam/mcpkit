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
│   ├── pagination.go          cursor-based pagination
│   └── exec.go                ToolExec: wrap CLI binaries as MCP tools
│
├── client/                  ← Client + all client transports
│   ├── client.go              Client, NewClient, Connect, ToolCall, ToolCallTyped, WithTransport, WithExtension, WithUIExtension, WithGetSSEStream, WithModifyRequest, WithCommandTransport, ServerSupportsExtension, ServerSupportsUI, ListToolsForModel, ResolveEndpointURL, HTTPStatusError, DoWithAuthRetry
│   ├── stdio_transport.go     StdioTransport, NewStdioTransport, WithStdioTransport
│   ├── command_transport.go   CommandTransport, NewCommandTransport, WithEnv, WithDir, WithShutdownTimeout, WithStderr
│   ├── client_logging.go      loggingTransport, WithClientLogging
│   └── client_reconnect.go    WithMaxRetries, WithReconnectBackoff, IsTransientError
│
├── ext/auth/                ← Separate Go module (ext/auth/go.mod)
│   ├── discovery.go           DiscoverMCPAuth (PRM + AS metadata)
│   ├── token_source.go        OAuthTokenSource, ValidatePKCES256
│   ├── dcr.go                 DefaultClientRegistration (MCP defaults), type aliases for client.RegisterClient/types
│   ├── jwt_validator.go       JWTValidator (JWKS-based)
│   ├── server_auth.go         MountAuth (PRM endpoints)
│   ├── scopes.go              RequireScope
│   └── docs/DESIGN.md         Auth architecture + spec compliance
│   NOTE: Generic OAuth code moved to oneauth (#158): RegisterClient,
│         ValidateHTTPS, ValidateCIMDURL, ClientCredentialsSource → oneauth/client;
│         mergeScopes → core.UnionScopes. Type aliases preserved for compat.
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

### JSON-RPC Protocol Compliance
- **JSON-RPC batching**: Both transports accept batch requests (JSON arrays). Each element is dispatched sequentially, responses collected as JSON array. Notifications produce no response entry. Empty batch → Invalid Request error. Streamable HTTP returns JSON array body; SSE pushes individual response events.
- **Content-Type enforcement**: POST requests must have `Content-Type: application/json`. Non-conforming requests are rejected with 415 Unsupported Media Type (CSRF defense-in-depth against cross-origin form submissions).
- **Ping before initialize**: `ping` is always handled, regardless of initialization state. It's in the pre-init switch block alongside `initialize`, `notifications/initialized`, and `notifications/cancelled`.
- **MCP error codes**: Application errors use codes outside JSON-RPC reserved ranges: `ErrCodeToolExecutionError` (-31000), `ErrCodeResourceError` (-31001), `ErrCodePromptError` (-31002), `ErrCodeCompletionError` (-31003). Standard JSON-RPC codes (-32700, -32600 to -32603) are used only for protocol errors.
- **ID generation decoupled**: `sendServerRequest` uses `gohttp.IDGen` interface (servicekit) instead of `*atomic.Int64`. Both `eventIDs` and `requestIDs` on Dispatcher use the interface.
- **Content cardinality tolerance (#81)**: Peers in the wild disagree on whether `content` is an array or a single object. `PromptMessage`, `SamplingMessage`, `CreateMessageResult`, and `Content.Resource` accept both forms on the read path — array-form decodes to the first element (empty array → zero value). `ToolResult.Content` and `ResourceResult.Contents` accept a single object as well, wrapping it into a 1-element slice. The write path always emits spec-canonical form (single for prompt/sampling, array for tool/resource). Logic lives in `core/cardinality.go`; migrating to multi-element (e.g., #141 widening SamplingMessage) is a one-line helper swap at the UnmarshalJSON call site.

### Transports
- **SSE endpoint event data must be raw text**, not JSON-encoded. Use `SSEText(url)` not `SSEJSON()`.
- **SSE endpoint URL resolution**: Client resolves the endpoint event URL against the SSE connection URL via `ResolveEndpointURL` (RFC 3986). Handles absolute URLs, absolute paths, and relative paths.
- **Per-session Dispatchers**: each connection gets its own `Dispatcher` via `newSession()`. The `Registry` is shared by pointer — all sessions see the same tools/resources/prompts.
- **SSE transport sessions** die with the connection. **Streamable HTTP sessions** persist until DELETE, idle timeout, or server restart. **Stdio sessions** last for the process lifetime (1:1 mapping).
- **Session idle timeout**: `WithSessionTimeout(d)` enables automatic cleanup of abandoned Streamable HTTP sessions. Uses `gocurrent.IdleTimer` for ref-counted idle tracking — timer pauses during active requests (Acquire/Release). Default is 0 (no timeout).
- **Stdio transport** uses Content-Length framed JSON-RPC over stdin/stdout (framing via `servicekit/http.WriteFrame`/`ReadFrame`). Server-side: `srv.RunStdio(ctx)`. Client-side: `client.WithStdioTransport(reader, writer)`. No HTTP, no auth — process boundary is the trust boundary. Debug logging goes to stderr.
- **Notification delivery order**: notifications arrive before tool results across all transports.
- **HTTP error classification**: Both transports return `HTTPStatusError` (alias for `servicekit/http.HTTPStatusError`) for non-2xx responses (excluding 401/403, handled by `DoWithAuthRetry`). `IsTransientError` classifies 5xx as transient (retriable via `WithMaxRetries`), 4xx as terminal. `servicekit/http.IsHTTPTransient` provides the status-code-only classification. `HTTPStatusError.Header` contains the cloned response headers (e.g., `Retry-After`, `X-Request-Id`) for programmatic inspection. Error response bodies are capped at `servicekit/http.MaxErrorBodySize` (16 KB) to prevent memory exhaustion.
- **Auth retry**: `DoWithAuthRetry` wraps `core.TokenSource` into servicekit's callback-based `http.DoWithAuthRetry` (401 refresh + 403 scope step-up). `ClientAuthError` is an alias for `servicekit/http.AuthRetryError`.
- **Origin validation**: `streamableTransport` uses servicekit's `middleware.OriginChecker.CheckRequest()` for DNS rebinding protection. Defaults to localhost-only when no `WithAllowedOrigins` configured. Falls back to Host header when Origin is absent.
- **SSE reader death**: `call()` uses dual-select on the response channel and the done channel — returns a transient error immediately if the background reader dies, instead of blocking forever.
- **Client GET SSE stream**: Opt-in via `WithGetSSEStream()`. Opens a background `GET /mcp` SSE stream after Connect() for receiving server-initiated notifications outside POST request-response cycles (Streamable HTTP only). Notification callback (`WithNotificationCallback`) must be goroutine-safe when enabled. Re-established automatically on reconnection.
- **Dispatcher.notifyFunc thread safety**: `notifyFunc` is protected by `notifyMu` (RWMutex). Use `SetNotifyFunc()` / `getNotifyFunc()` — never access the field directly.
- **Broadcast vs NotifyResourceUpdated**: `Server.Broadcast(method, params)` fans out to ALL connected sessions unconditionally. `NotifyResourceUpdated(uri)` only targets sessions that called `resources/subscribe`. Broadcast only reaches HTTP transport sessions (SSE + Streamable HTTP), not in-process — consistent with `CloseSession`/`CloseAllSessions`.

### SSE Event IDs and Stream Resumption
- **All SSE events have IDs**: Both transports assign unique event IDs via `emitSSEEvent()` (server/event_ids.go). IDs are opaque strings generated by `gohttp.IDGen` (per-session `AtomicIDGen`).
- **`WithEventStore(store)`**: Optional `gohttp.EventStore` for SSE event persistence. When configured, all emitted events are stored with their IDs. Use `gohttp.NewMemoryEventStore(maxPerStream)` for in-memory.
- **Streamable HTTP resumption**: Client GET SSE stream sends `Last-Event-ID` header on reconnect. Server replays missed events from the store before resuming live delivery.
- **SSE stream resumption**: `WithSSEGracePeriod(d)` keeps sessions alive for `d` after disconnect. Client reconnects with `?sessionId=<prev>` query param to resume; server replays missed events via `Last-Event-ID` header. Replayed events are sent after the endpoint event (so the client knows where to POST). Session ID is reused as the hub connection ID on reconnect. Security: reconnection requires the same auth principal; mismatches return 403. Default is 0 (no grace period — backward compat, sessions die with connection).
- **`emitSSEEvent()`**: Central function for all SSE event emission — generates ID, sends via callback, stores if configured. All transports use this instead of raw hub calls.
- **Session cleanup trims store**: `expireSession`, `handleDelete`, and `closeSession` all call `store.Trim(sessionID)` to prevent unbounded memory growth.
- **Client tracks `lastEventID`**: `atomic.Value` on `Client`, updated by background SSE readers. Survives transport recreation during reconnection.

### Single-Struct Registration (#41)
- **`server.Register(items ...any)`**: Accepts `server.Tool`, `server.Resource`, `server.ResourceTemplate`, `server.Prompt` — bundles def + handler in one struct.
- **Backward compatible**: Existing two-arg `RegisterTool(def, handler)` methods remain.

### Error Handler (#136)
- **`WithErrorHandler(h ErrorHandler)`**: Receives out-of-band errors (session lifecycle, transport, keepalive).
- **`ErrorHandler` interface**: `OnSessionExpire`, `OnTransportError`, `OnKeepaliveFailure`.
- **`BaseErrorHandler`**: Embed for no-op defaults, override only what you need.

### Client-Suggested Session IDs (#88)
- **`_suggestedSessionId`** in initialize params: client suggests a session ID, server validates (<=128 chars, alphanumeric/-/_/., unique) and uses it or falls back to server-generated.

### Cursor-Based Pagination (#85)
- **All list handlers** support `cursor` param. `NextCursor` in all list results + `ToolResultMeta.NextCursor` for tool output.
- **`defaultPageSize = 0`** — returns all by default (backward compatible).

### Roots Lifecycle (#26)
- **`notifications/roots/list_changed`** handled — marks stale, calls `WithOnRootsChanged(fn)` callback.
- **`core.Root`** type. Server-to-client `roots/list` fetch deferred.

### Concurrent Request Handling (#86)
- **Duplicate request IDs rejected** via `LoadOrStore` on inflight map.

### URI Template Matching (#143)
- **RFC 6570 Level 4**: Uses `yosida95/uritemplate/v3` for proper URI template matching. Replaces naive segment-based matcher.

### Streaming Tool Results (#82)
- **`core.EmitContent(ctx, requestID, content)`**: Emits a partial content block during tool execution. Delivered as SSE event on streaming transports, silently dropped on JSON path.
- **Default method**: `notifications/tools/content_chunk`. Override via `server.WithContentChunkMethod(method)`.
- **Configurable via context**: `core.WithContentChunkMethod(ctx, method)` for per-request override.
- **Client handler**: `client.WithContentChunkHandler(fn)` receives chunks. If not set, chunks are ignored and client uses final ToolResult only.
- **No transport changes**: Uses existing notify infrastructure. All transports automatically support streaming.
- **Final result is authoritative**: Streaming chunks are a preview for responsive UX.

### Prompt Argument Schemas (#87)
- **`PromptArgument.Schema any`**: optional JSON Schema describing the expected shape of a prompt argument. Mirrors `ToolDef.InputSchema` — typically a `map[string]any` with `"type"`, `"enum"`, etc. Arbitrary JSON Schema keywords (`$ref`, `$defs`, `additionalProperties`) are preserved verbatim through registration and serialization.
- **Declarative today**: clients use the schema to render typed inputs (number pickers, dropdowns). The dispatcher does NOT validate incoming argument values against the schema — handlers are responsible for validation. Server-side validation is tracked by #184 and will cover tools and prompts together.
- **`PromptRequest.Arguments map[string]any`** (changed from `map[string]string`): prompt handlers receive pre-decoded JSON values. Strings stay strings, numbers become `float64`, booleans stay `bool`, objects become `map[string]any`. Handlers type-assert as needed: `name, _ := req.Arguments["name"].(string)`. Widening was required so schema'd non-string args (integers, booleans) reach handlers meaningfully.

### Per-Handler Timeout
- **`ToolDef.Timeout`**, **`ResourceDef.Timeout`**, **`ResourceTemplate.Timeout`**, **`PromptDef.Timeout`**: Per-handler execution timeout. When set, overrides the server-wide `WithToolTimeout` for that specific handler. `json:"-"` — not serialized to clients.
- **Fallback chain**: per-handler `Timeout` → server-wide `WithToolTimeout` (tools only) → no timeout.
- **Applied in Dispatcher**: `handleToolsCall`, `handleResourcesRead` (both direct and template), `handlePromptsGet`.

### Client Typed Tool Calls
- **`ToolCallTyped[T](c, name, args)`**: Generic function that calls a tool and unmarshals `structuredContent` into T. For tools with `OutputSchema`. Returns error if no structured content.
- **Complements `ToolCall`**: `ToolCall` returns text, `ToolCallTyped` returns typed structs.

### ToolExec (CLI Binary Wrapper)
- **`server.ToolExec(ExecConfig)`**: Creates a `server.Tool` that wraps a CLI binary as an MCP tool. Returns a single-struct `Tool` compatible with `srv.Register()`.
- **`ExecConfig`**: `Name`, `Command`, `Args` (static), `BuildArgs` (dynamic from JSON), `Env`, `Dir`, `Timeout`, `InputSchema`.
- **Handler**: runs `exec.CommandContext`, returns `TextResult(stdout)` on success, `ErrorResult(output + exit code)` on failure. Non-zero exit is a tool error, not a transport error.
- **Use case**: wrapping existing CLI tools (build systems, linters, code generators) as MCP tools without reimplementing their logic.

### CommandTransport (Subprocess MCP Servers)
- **`NewCommandTransport(name, args, opts...)`**: Spawns a subprocess and communicates via Content-Length framed JSON-RPC over stdin/stdout. Wraps `StdioTransport` for the wire protocol.
- **Options**: `WithEnv(env...)` appends env vars, `WithDir(dir)` sets working directory, `WithShutdownTimeout(d)` controls SIGTERM→SIGKILL escalation (default 5s), `WithStderr(w)` tees stderr to a writer.
- **Lifecycle**: Process starts on `Connect()`, shuts down on `Close()` (stdin EOF → SIGTERM → SIGKILL after timeout). Stderr captured in internal buffer, accessible via `Stderr()`.
- **`WithCommandTransport(name, args, opts...)`**: Client option that stores command config; creates a fresh `CommandTransport` on each `Connect()` and `reconnect()`. Supports `WithMaxRetries` for automatic process restart on failure.
- **`WithTransport(NewCommandTransport(...))`** also works but does NOT support reconnection (the transport is not recreated).

### ModifyRequest Hook
- **`WithModifyRequest(fn func(*http.Request))`**: Client option. Callback invoked on every outgoing HTTP request inside `buildReq`, before `DoWithAuthRetry` applies the `Authorization` header. Cannot accidentally clobber auth.
- **Applies to HTTP transports only** (Streamable HTTP and SSE). Ignored for stdio and in-process. Survives reconnection.
- **8 call sites**: 4 in `streamableClientTransport` (call, notify, postResponse, openGetSSEStream) + 4 in `sseClientTransport` (connect, call, notify, postResponse).

### Application-Level Keepalive
- **`WithKeepalive(interval, maxFailures)`**: Server-side option. Sends JSON-RPC `ping` requests to clients via GET SSE stream at the configured interval. After `maxFailures` consecutive timeouts, the session is expired.
- **`WithClientKeepalive(interval, maxFailures)`**: Client-side option. Periodically sends `ping` to the server. On max failures, triggers reconnection (if retries configured) or closes transport.
- **Keepalive uses existing `ping` method**: Already defined in MCP spec, already handled by Dispatcher. No new protocol methods.
- **Keepalive goroutine lifecycle**: Started in `OnStart` (GET SSE stream), stopped in `OnClose`. Server keepalive uses `makeRequestFunc` with a push function that writes to the SSE hub.

### Dynamic Registration
- **`Registry`** is the shared, thread-safe registry for tools, resources, prompts, and completions. Access via `srv.Registry()`. All session dispatchers share the same `*Registry` pointer.
- **`Registry.AddTool` / `RemoveTool`** (and Resource, Prompt variants) acquire write lock, modify the registry, then call `OnChange` to broadcast `notifications/*/list_changed` to all sessions.
- **`Registry.OnChange`** is wired by `NewServer` to `Server.Broadcast`. Pre-serve `RegisterTool` calls also trigger OnChange but Broadcast is a no-op with zero sessions.
- **RLock scoping in handlers**: `handleToolsCall` acquires RLock only for the map lookup, releases before executing the handler. Tool execution is never under lock.
- **Overwrite semantics**: `AddTool`, `AddResource`, `AddResourceTemplate`, and `AddPrompt` use identity keys (tool name, resource URI, URI template string, prompt name). Re-registering with the same key overwrites the entry and handler without creating duplicates in the ordering slice.
- **`listChanged: true`** is always advertised in capabilities for tools, resources, and prompts, regardless of current registry contents.

### Auth
- **Auth spec is 2025-11-25**: See `ext/auth/docs/DESIGN.md` for spec compliance (all C1-C23, X1-X5 requirements Done).
- **Well-known PRM URL**: `scheme://host/.well-known/oauth-protected-resource/path` (NOT `serverURL + "/.well-known/..."`).
- **`OAuthTokenSource` calls `DiscoverMCPAuth`** on first `Token()`, caches result. Passes discovered endpoints explicitly to `LoginWithBrowser`.
- **Client registration priority (C6)**: pre-registered `ClientID` → CIMD `ClientMetadataURL` → DCR (if `EnableDCR`) → error.
- **Keycloak container** runs with `--log-level=INFO,org.keycloak.events:DEBUG` for token event visibility.
- **JWT validated-token cache**: `JWTValidator.CacheTTL` enables SHA-256-keyed TTL cache. Avoids redundant JWT signature verification during LLM agent loops with rapid sequential tool calls. Lazy eviction, bounded by `CacheMaxSize` (default 1000). Future: consider `hashicorp/golang-lru` for LRU eviction.
- **RFC 9207 issuer validation**: `JWTValidator.Validate()` checks `iss` claim against configured issuer on every request (line 127-131). Prevents OAuth mixup attacks.
- **Generic OAuth pushed to oneauth (#158)**: `RegisterClient`, `ClientRegistrationRequest/Response`, `ValidateHTTPS`, `IsLocalhost`, `ValidateCIMDURL`, `ClientCredentialsSource`, `mergeScopes` (now `core.UnionScopes`) all live in `oneauth/client` and `oneauth/core`. Type aliases in `ext/auth/` preserve backward compat. Only `DefaultClientRegistration()` (MCP-specific defaults) and `ValidatePKCES256` (MCP requirement C11/C12) remain local.
- **AS metadata cache (#47)**: `OAuthTokenSource.ASMetadataStore` (optional) enables caching of authorization server metadata across `DiscoverMCPAuth` calls. Share a single `client.NewMemoryASMetadataStore(ttl)` across multiple token sources to avoid redundant discovery fetches when N resource servers share one AS. Cache infrastructure pushed down to oneauth alongside existing stores (TokenStore, CredentialStore, etc.).
- **Proactive token refresh (#48)**: oneauth's `ClientCredentialsSource.Refresher` enables background token refresh before expiry. `ProactiveRefresher{Buffer: 30*time.Second}` starts a goroutine on first `Token()` call. `Client.Close()` automatically stops the refresh goroutine via `io.Closer` delegation (the client checks if its `tokenSource` implements `io.Closer`). For long-running M2M agents with short-lived tokens — avoids latency spikes on the hot path.
- **AllScopes auto-wiring (#50)**: `MountAuth` now auto-populates `Validator.AllScopes` from `ScopesSupported` (if unset), so 401 `WWW-Authenticate` responses advertise all supported scopes upfront. Reduces scope step-up round-trips for LLM clients. Callers can still set `AllScopes` explicitly on the validator to override.
- **Scope accumulation (#138)**: `TokenForScopes` uses `core.UnionScopes` — scopes accumulate across step-up calls, never replaced. Edge-case verified: concurrent calls accumulate correctly via mutex, empty slice leaves scopes unchanged, cache is invalidated to force refetch with broader scope set.

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

### Releasing Sub-Modules (#189)
- **`ext/auth` and `ext/ui` are independently tagged Go modules** with their own `go.mod`. Their `go.mod` files contain `replace github.com/panyam/mcpkit => ../../` so local dev works against unreleased root changes, but the `require github.com/panyam/mcpkit vX.Y.Z` line must point to a **real, released root tag** — Go ignores `replace` directives in non-main (dependency) modules, so downstream consumers need a resolvable version.
- **The v0.0.0 placeholder bug**: before #189, sub-module `go.mod` files said `require github.com/panyam/mcpkit v0.0.0`. This worked locally but broke any downstream `go get github.com/panyam/mcpkit/ext/auth@vX` because `v0.0.0` has never been tagged. Caught by `scripts/verify-submodule-deps.sh` (wired into `make ci`, `make audit`, and the pre-push hook).
- **Release order** when cutting a new root tag:
  1. Commit root-only changes.
  2. Tag root: `git tag -a v0.1.N -m "v0.1.N"`.
  3. Push the root tag: `git push origin v0.1.N`.
  4. In each sub-module's `go.mod`, bump `require github.com/panyam/mcpkit vX.Y.Z` to the new root tag. Keep the `replace` line.
  5. Run `go mod tidy` in each sub-module.
  6. Commit the sub-module bumps: `chore: bump sub-modules to v0.1.N`.
  7. Tag sub-modules: `git tag -a ext/auth/v0.1.M -m "ext/auth/v0.1.M"`, same for `ext/ui`. Note that sub-module tag numbers drift from root — tag only when the sub-module actually changes.
  8. Push the sub-module tags.
- **Don't retag published sub-module versions.** Go module proxies cache aggressively; retagging `ext/auth/v0.1.15` after the fact is a known footgun. Ship a new version (`v0.1.16`) instead.
- **Test modules are not tagged**: `tests/e2e` and `tests/keycloak` also have their own `go.mod` with replace directives, but they are never published or imported externally — no tagging needed.

## Conformance Status

### Server conformance
30/30 MCP server conformance scenarios passing. All server features implemented.

### Auth conformance
14/14 required MCP auth conformance scenarios passing (210/210 checks). Run via `make testconfauth`.

### Apps conformance
21 MCP Apps conformance tests passing (tool metadata, resources, visibility, fallback, negotiation). Run via `make test-e2e`.

## What's Not Implemented Yet

(none — both stdio and GET SSE stream are now implemented)
