# MCPKit

## Version
0.1.1

## Provides
- mcp-protocol-negotiation: Version negotiation supporting MCP 2025-11-25 and 2024-11-05
- mcp-initialization-gating: Enforces initialize/initialized handshake before accepting requests
- mcp-tool-error-semantics: Spec-compliant isError tool results (not JSON-RPC errors) for handler failures
- mcp-sse-transport: HTTP+SSE transport (MCP 2024-11-05) with per-session SSE streams
- mcp-streamable-http-transport: Streamable HTTP transport (MCP 2025-03-26) with Mcp-Session-Id header sessions
- mcp-dual-transport: Both SSE and Streamable HTTP simultaneously via WithSSE/WithStreamableHTTP options
- mcp-graceful-shutdown: ListenAndServeGraceful with SSE hub drain on SIGTERM
- mcp-auth-middleware: Bearer token (constant-time), Claims propagation via ClaimsProvider, JWT/OIDC via oneauth sub-module
- mcp-auth-submodule: mcpkit/ext/auth — JWTValidator, MountAuth (PRM), WWW-Authenticate builders, RequireScope, OAuthTokenSource, DiscoverMCPAuth, ValidatePKCES256, DefaultClientRegistration. Generic OAuth (RegisterClient, ClientCredentialsSource, ValidateHTTPS, ValidateCIMDURL) re-exported from oneauth/client via type aliases (#158).
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
- mcp-streamable-sse-streaming: Streamable HTTP POST returns SSE stream when Accept: text/event-stream, enabling mid-request notifications with delivery order guarantees
- mcp-notification-ordering: Client receives notifications (logging, progress) before tool results across all transports; WithNotificationHandler works on Streamable HTTP, SSE, and in-memory
- mcp-progress: notifications/progress via EmitProgress() with _meta.progressToken
- mcp-completion: completion/complete for argument autocompletion
- mcp-dns-rebinding-protection: Origin header validation on Streamable HTTP (WithAllowedOrigins)
- mcp-resource-subscriptions: resources/subscribe, resources/unsubscribe, notifications/resources/updated via WithSubscriptions() + Server.NotifyResourceUpdated()
- mcp-broadcast: Server.Broadcast(method, params) sends notifications to ALL connected sessions unconditionally (no subscription required)
- mcp-sampling: Server-to-client sampling/createMessage via Sample() — server asks client LLM for inference
- mcp-elicitation: Server-to-client elicitation/create via Elicit() — server asks client for user input
- mcp-conformance: Official MCP conformance test suite integration (30/30 server passing, 14/14 auth passing)
- mcp-client: Go MCP client for Streamable HTTP — Connect, ToolCall, ReadResource, ListTools, ListResources
- mcp-testutil: TestClient wrapper for e2e testing MCP servers (httptest + testing.T integration)
- mcp-auth-e2e: E2E auth tests with real oneauth AS (31 tests: JWT validation, transport auth, scopes, PRM, WWW-Authenticate, reconnection, middleware)
- mcp-server-middleware: Request/response middleware chain (WithMiddleware, LoggingMiddleware) — intercepts after auth, before dispatch
- mcp-client-logging: Transport debug logging (WithClientLogging) — logs method, latency, errors for every operation
- mcp-client-reconnect: Automatic reconnection with exponential backoff (WithMaxRetries, WithReconnectBackoff) �� re-initializes MCP session on transient errors
- mcp-client-auth-retry: Client transport 401/403 handling — doWithAuthRetry, ScopeAwareTokenSource, ClientAuthError
- mcp-sub-packages: core/server/client package split — types in core, server+transports in server/, client in client/
- mcp-in-process-transport: server.NewInProcessTransport + client.WithTransport — typed *Request/*Response, no HTTP (for tests/embedded)
- mcp-stdio-transport: Content-Length framed JSON-RPC over stdin/stdout — Server.RunStdio() + client.WithStdioTransport() for editor-spawned MCP servers (Cursor, Claude Desktop)
- mcp-stateless-mode: WithStateless — no sessions, fresh dispatcher per request (for serverless/CLI)
- mcp-session-management: Server.CloseSession/CloseAllSessions — programmatic session teardown
- mcp-structured-output: StructuredContent + OutputSchema on ToolDef/ToolResult — typed tool output
- mcp-server-run: Server.Run(addr) — simple blocking entry point defaulting to Streamable HTTP
- mcp-error-codes: ErrCodeServerError (-32000) + documented JSON-RPC error code ranges
- mcp-parametric-tests: forAllTransports — core client tests run against all 4 transports as subtests (Streamable HTTP, SSE, in-memory, stdio)
- mcp-apps-extension: MCP Apps (io.modelcontextprotocol/ui) extension negotiation — server advertises via WithExtension(UIExtension{}), client detects via ServerSupportsUI()
- mcp-apps-ui-metadata: UIMetadata, UICSPConfig, UIVisibility types on ToolDef._meta.ui and ResourceReadContent._meta.ui
- mcp-apps-resource-serving: ui:// resources with text/html;profile=mcp-app MIME type, template resources for parameterized URIs
- mcp-apps-visibility: Tool visibility filtering — UIVisibilityModel/UIVisibilityApp, client-side ListToolsForModel() excludes app-only tools
- mcp-apps-ref-validation: RefValidator interface — extensions validate tool-to-resource references at server startup
- mcp-apps-resource-notification: NotifyResourcesChanged(ctx) — tool handlers signal resource list changes to clients
- mcp-apps-register-helper: RegisterAppTool (ext/ui) — registers tool + resource pair in one call via ToolResourceRegistrar interface
- mcp-apps-conformance: 21 MCP Apps conformance tests (tool metadata, resources, visibility, fallback, negotiation)
- mcp-dynamic-registration: Registry.AddTool/RemoveTool/AddResource/RemoveResource/AddPrompt/RemovePrompt — thread-safe runtime registration with automatic notifications/*/list_changed broadcast via OnChange callback
- mcp-session-timeout: WithSessionTimeout — idle session cleanup for Streamable HTTP (timer + ref counting to avoid closing mid-execution)
- mcp-sse-resumption: WithSSEGracePeriod — SSE sessions survive brief disconnects with grace timer. Client reconnects via ?sessionId= query param; server replays missed events via Last-Event-ID header. Principal-bound for security.
- mcp-server-capabilities-typed: core.ServerCapabilities, ToolsCap, ResourcesCap, PromptsCap — typed structs for initialize response capabilities
- mcp-command-transport: CommandTransport — spawn subprocess MCP servers, communicate via stdio, graceful SIGTERM/SIGKILL shutdown, stderr capture, env passthrough. WithCommandTransport client option supports reconnection (auto-restart).
- mcp-tool-exec: ToolExec — wrap CLI binaries as MCP tools with structured I/O. ExecConfig supports static/dynamic args, env, dir, timeout. BuildArgs callback maps JSON tool arguments to CLI flags.
- mcp-modify-request: WithModifyRequest — client-side HTTP request hook for injecting custom headers (tracing, tenant IDs). Runs before auth, applies to Streamable HTTP + SSE transports.

## Module
github.com/panyam/mcpkit

## Location
newstack/mcpkit/main

## Stack Dependencies

### Core module (github.com/panyam/mcpkit)
- servicekit (github.com/panyam/servicekit) v0.0.22 — SSEConn/SSEHub, ListenAndServeGraceful, StreamableServe, HTTPStatusError (with Header), MaxErrorBodySize

### Sub-module: ext/auth (github.com/panyam/mcpkit/ext/auth)
- oneauth (github.com/panyam/oneauth) v0.0.64 — JWT/OIDC validation, testutil.TestAuthServer; separate go.mod

## Integration

### Go Module
```go
require github.com/panyam/mcpkit v0.1.0
```

### Basic Server (Streamable HTTP)
```go
import (
    "github.com/panyam/mcpkit/core"
    "github.com/panyam/mcpkit/server"
)

srv := server.NewServer(
    core.ServerInfo{Name: "my-server", Version: "0.1.0"},
    server.WithBearerToken("secret"),
    server.WithToolTimeout(30 * time.Second),
)
srv.RegisterTool(def, handler)
srv.Run(":8787")  // defaults to Streamable HTTP
```

### Client
```go
import (
    "github.com/panyam/mcpkit/client"
    "github.com/panyam/mcpkit/core"
)

c := client.NewClient(url, core.ClientInfo{Name: "my-client", Version: "1.0"})
c.Connect()
result, _ := c.ToolCall("greet", map[string]any{"name": "world"})
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
