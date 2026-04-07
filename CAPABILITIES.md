# MCPKit

## Version
0.0.11

## Provides
- mcp-protocol-negotiation: Version negotiation supporting MCP 2025-11-25 and 2024-11-05
- mcp-initialization-gating: Enforces initialize/initialized handshake before accepting requests
- mcp-tool-error-semantics: Spec-compliant isError tool results (not JSON-RPC errors) for handler failures
- mcp-sse-transport: HTTP+SSE transport (MCP 2024-11-05) with per-session SSE streams
- mcp-streamable-http-transport: Streamable HTTP transport (MCP 2025-03-26) with Mcp-Session-Id header sessions
- mcp-dual-transport: Both SSE and Streamable HTTP simultaneously via WithSSE/WithStreamableHTTP options
- mcp-graceful-shutdown: ListenAndServeGraceful with SSE hub drain on SIGTERM
- mcp-auth-middleware: Bearer token (constant-time), Claims propagation via ClaimsProvider, JWT/OIDC via oneauth sub-module
- mcp-auth-submodule: mcpkit/auth — JWTValidator, MountAuth (PRM), WWW-Authenticate builders, RequireScope, OAuthTokenSource, ClientCredentialsSource
- mcp-extensions: Extension/Stability/ExtensionProvider system — sub-modules declare spec version + stability in initialize
- mcp-annotations: Annotations field on ToolDef/ResourceDef/PromptDef + RegisterExperimental* helpers
- mcp-client-auth: WithClientBearerToken, WithTokenSource — auth header injection on all client requests
- mcp-auth-conformance: 14/14 required auth conformance scenarios passing (210/210 checks)
- mcp-tool-timeout: context.WithTimeout wrapper for tool execution
- mcp-allowed-roots: Restrict tool cwd to allowed directories (option registered, not enforced yet)
- mcp-resources: resources/list, resources/read, resources/templates/list with URI template matching
- mcp-prompts: prompts/list, prompts/get with argument passing
- mcp-pagination: Generic cursor-based pagination for all list methods
- mcp-cancellation: notifications/cancelled with inflight request tracking and context cancellation
- mcp-logging: logging/setLevel + notifications/message via EmitLog() with per-session atomic log level
- mcp-streamable-sse-streaming: Streamable HTTP POST returns SSE stream when Accept: text/event-stream, enabling mid-request notifications
- mcp-progress: notifications/progress via EmitProgress() with _meta.progressToken
- mcp-completion: completion/complete for argument autocompletion
- mcp-dns-rebinding-protection: Origin header validation on Streamable HTTP (WithAllowedOrigins)
- mcp-resource-subscriptions: resources/subscribe, resources/unsubscribe, notifications/resources/updated via WithSubscriptions() + Server.NotifyResourceUpdated()
- mcp-conformance: Official MCP conformance test suite integration (28/30 server passing, 14/14 auth passing)
- mcp-client: Go MCP client for Streamable HTTP — Connect, ToolCall, ReadResource, ListTools, ListResources
- mcp-testutil: TestClient wrapper for e2e testing MCP servers (httptest + testing.T integration)
- mcp-auth-e2e: E2E auth tests with real oneauth AS (31 tests: JWT validation, transport auth, scopes, PRM, WWW-Authenticate, reconnection, middleware)
- mcp-server-middleware: Request/response middleware chain (WithMiddleware, LoggingMiddleware) — intercepts after auth, before dispatch
- mcp-client-logging: Transport debug logging (WithClientLogging) — logs method, latency, errors for every operation
- mcp-client-reconnect: Automatic reconnection with exponential backoff (WithMaxRetries, WithReconnectBackoff) �� re-initializes MCP session on transient errors
- mcp-client-auth-retry: Client transport 401/403 handling — doWithAuthRetry, ScopeAwareTokenSource, ClientAuthError
- mcp-in-memory-transport: WithInMemoryServer — client calls Server.Dispatch directly, no HTTP (for tests/embedded)
- mcp-stateless-mode: WithStateless — no sessions, fresh dispatcher per request (for serverless/CLI)
- mcp-session-management: Server.CloseSession/CloseAllSessions — programmatic session teardown
- mcp-structured-output: StructuredContent + OutputSchema on ToolDef/ToolResult — typed tool output
- mcp-server-run: Server.Run(addr) — simple blocking entry point defaulting to Streamable HTTP
- mcp-error-codes: ErrCodeServerError (-32000) + documented JSON-RPC error code ranges
- mcp-parametric-tests: forAllTransports — core client tests run against all 3 transports as subtests

## Module
github.com/panyam/mcpkit

## Location
newstack/mcpkit/main

## Stack Dependencies

### Core module (github.com/panyam/mcpkit)
- servicekit (github.com/panyam/servicekit) v0.0.14 — SSEConn/SSEHub, ListenAndServeGraceful, StreamableServe

### Sub-module: auth (github.com/panyam/mcpkit/auth)
- oneauth (github.com/panyam/oneauth) v0.0.64 — JWT/OIDC validation, testutil.TestAuthServer; separate go.mod

## Integration

### Go Module
```go
require github.com/panyam/mcpkit v0.0.11
```

### Basic Server (Streamable HTTP)
```go
srv := mcpkit.NewServer(
    mcpkit.ServerInfo{Name: "my-server", Version: "0.1.0"},
    mcpkit.WithBearerToken("secret"),
    mcpkit.WithToolTimeout(30 * time.Second),
)
srv.RegisterTool(def, handler)
srv.Run(":8787")  // defaults to Streamable HTTP
```

### Both Transports
```go
srv.ListenAndServe(mcpkit.WithStreamableHTTP(true), mcpkit.WithSSE(true))
```

## Status
Active

## Conventions
- Functional options pattern for server and transport configuration
- SSE infrastructure from servicekit (SSEConn, SSEHub); MCP-specific middleware in mcpkit
- Transport/protocol separation: dispatch layer shared across SSE and Streamable HTTP
- Per-session Dispatchers via newSession() (tool registry shared by reference)
- SSEData union type for SSE wire format (text for URLs, JSON for responses)
- Conformance suite validates spec compliance via baseline.yml
