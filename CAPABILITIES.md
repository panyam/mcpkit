# MCPKit

## Version
0.2.40

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
- mcp-allowed-roots: WithAllowedRoots + core.IsPathAllowed — per-session sandbox enforcement using intersection of static server roots and dynamic client roots. Handler-side helper, not automatic middleware. (#197)
- mcp-roots-fetch-timeout: WithRootsFetchTimeout — configurable deadline for server-to-client roots/list requests. Default 30s. (#198)
- mcp-resources: resources/list, resources/read, resources/templates/list with URI template matching
- mcp-prompts: prompts/list, prompts/get with argument passing
- mcp-prompt-argument-schema: PromptArgument.Schema — declarative JSON Schema on prompt arguments (mirrors ToolDef.InputSchema). Clients render typed inputs; server-side validation tracked by #184 (#87)
- mcp-content-cardinality-tolerance: Defensive parsing of `content` field cardinality across PromptMessage/SamplingMessage/ToolResult/ResourceResult/Content.resource. Accepts both single-object and array forms on read; always emits spec-canonical form on write (#81)
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
- mcp-elicitation-url-mode: URL-mode elicitation (SEP-1036) — ElicitURL() sends mode="url" with URL + elicitationId for out-of-band user interaction. ElicitationCap{Form, URL} structured capability. notifications/elicitation/complete for completion signaling. ErrCodeURLElicitationRequired (-32042) with composable error data (FineGrainedAuth-ready). Client mode validation + WithElicitationURLSupport(). 5/5 conformance scenarios.
- mcp-authorization-denial-experimental: EXPERIMENTAL AuthorizationDenial envelope + RemediationHint types for FineGrainedAuth (draft SEP). ScopeStepUpHint helper for UC2 scope escalation. NewAuthorizationDenialError composer. Examples: elicitation/ (UC1 consent), fine-grained-auth/ (UC2 Keycloak scope step-up). UC3 (RAR/PSD2) stubbed pending oneauth RAR.
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
- mcp-typed-handler-contexts: ToolContext, ResourceContext, PromptContext — typed handler contexts with IDE-discoverable methods (EmitLog, EmitProgress, Sample, Elicit, AuthClaims, etc.). BaseContext shared across all handler types. ToolContext adds EmitProgress/EmitContent. Free functions preserved as thin wrappers. (#179)
- mcp-parametric-tests: forAllTransports — core client tests run against all 4 transports as subtests (Streamable HTTP, SSE, in-memory, stdio)
- mcp-apps-extension: MCP Apps (io.modelcontextprotocol/ui) extension negotiation — server advertises via WithExtension(UIExtension{}), client detects via ServerSupportsUI()
- mcp-apps-ui-metadata: UIMetadata, UICSPConfig, UIVisibility types on ToolDef._meta.ui and ResourceReadContent._meta.ui
- mcp-apps-resource-serving: ui:// resources with text/html;profile=mcp-app MIME type, template resources for parameterized URIs
- mcp-apps-visibility: Tool visibility filtering — UIVisibilityModel/UIVisibilityApp, client-side ListToolsForModel() excludes app-only tools
- mcp-apps-ref-validation: RefValidator interface — extensions validate tool-to-resource references at server startup
- mcp-apps-resource-notification: NotifyResourcesChanged(ctx) — tool handlers signal resource list changes to clients
- mcp-apps-register-helper: RegisterAppTool (ext/ui) — registers tool + resource pair in one call via ToolResourceRegistrar interface. Auto-detects template URIs (containing `{`) and routes to RegisterResourceTemplate; concrete URIs use RegisterResource.
- mcp-apps-display-modes: UIMetadata.SupportedDisplayModes — apps declare inline/fullscreen/pip support. RequestDisplayMode(ctx, mode) emits notifications/ui/displayMode. (#185)
- mcp-apps-template-resources: RegisterAppTool auto-detects template URIs and registers resource templates. TemplateHandler field on AppToolConfig for parameterized HTML serving (SSR). (#190)
- mcp-apps-template-fallback: RegisterAppTool auto-generates concrete fallback resource (`ui://{host}/{tool}/latest`) for template URIs when TemplateHandler is provided. Wraps tool handler to capture args, delegates fallback to TemplateHandler with stored params. Transparent to consumers — removed when hosts support template substitution. (#213)
- mcp-uri-template-helpers: core.URITemplateVars/core.IsTemplateURI — RFC 6570 template detection using yosida95/uritemplate (replaces string-based `{` checks)
- mcp-apps-elicitation-meta: ElicitationRequest._meta.ui and CreateMessageRequest._meta.ui — app metadata on server-to-client requests. ElicitWithApp/SampleWithApp helpers in ext/ui. (#191)
- mcp-apps-conformance: 21 MCP Apps conformance tests (tool metadata, resources, visibility, fallback, negotiation)
- mcp-apps-bridge: MCP App Bridge — framework-agnostic JS for iframe postMessage protocol. TypeScript source → compiled JS + .d.ts. Global MCPApp singleton: on/off/once events, callTool, readResource, sendMessage, updateModelContext, openLink, downloadFile, requestDisplayMode. Style utilities (applyTheme, applyStyleVariables, applyFonts). AbortSignal/timeout support. Bidirectional tool calls (oncalltool, onlisttools). Auto ResizeObserver + CustomEvent dispatch for HTMX. Graceful no-op when not hosted. (v0.2.18–v0.2.23)
- mcp-apps-bridge-template: BridgeData + BridgeTemplateDef() — Go html/template integration for explicit bridge inclusion. template.JS for safe unescaped JS. Single `<script type="module">` pattern. (v0.2.19)
- mcp-apps-bridge-serve: ServeBridge() HTTP handler at /_mcpkit/mcp-app-bridge.js — serves bridge JS for external `<script src>` loading
- mcp-apps-bridge-inject: InjectAppBridge(html) + AppShellHTML(title, body) — convenience helpers for inline bridge injection
- mcp-json-no-html-escape: core.MarshalJSON with SetEscapeHTML(false) — JSON-RPC responses preserve literal <, >, & matching Node.js/Python behavior. Fixes HTML content in resource responses for MCP Apps hosts. (v0.2.19)
- mcp-protogen: experimental/ext/protogen — protoc plugin (protoc-gen-go-mcp) generates MCP registrations from proto service definitions. Proto annotations (mcp_tool, mcp_resource, mcp_prompt, mcp_service) with full field support. In-process, gRPC forwarding, and ConnectRPC forwarding variants for all three primitives. JSON Schema derived from proto messages. Uses typed handler contexts. Published to buf.build/mcpkit/protogen. (#211, #216, #217, #218)
- mcp-protogen-tool-annotations: mcp_tool annotation fields: name, description, timeout, structured_output, result_summary. Validated at generation time (invalid names/timeouts are fatal errors). Namespace prefix via mcp_service.namespace. (#216)
- mcp-protogen-resources: mcp_resource annotation → server.Resource (static) or server.ResourceTemplate (parameterized). URI template detection via core.IsTemplateURI. runtime.BindParams delegates to protokit PopulateFromMap for type-coerced field binding. (#217)
- mcp-protogen-prompts: mcp_prompt annotation → server.Prompt with auto-derived PromptArguments from request message fields. runtime.BindPromptArgs and ProtoPromptResult helpers. (#218)
- mcp-protogen-grpc-errors: RPCError extracts gRPC status code, message, and details (proto Any) as StructuredError with {code, message, details} JSON. Agents can parse and recover programmatically. (#224)
- mcp-protogen-result-summary: mcp_tool.result_summary template: "Slide {position} updated (v{version})". runtime.ProtoSummaryStructuredResult renders from response fields. (#224)
- mcp-protogen-embedded-templates: Codegen templates use go:embed (templates/file.go.tmpl) instead of Go string constants
- mcp-protogen-buf: Proto module published to buf.build/mcpkit/protogen. experimental/ext/protogen/Makefile with build, lint, generate, push targets
- mcp-protogen-typed-contexts: Generated in-process server interfaces use typed handler contexts (ToolContext, ResourceContext, PromptContext) instead of context.Context. Gives impls direct access to ctx.Sample(), ctx.Elicit(), ctx.EmitProgress() etc. gRPC/Connect client interfaces unchanged.
- mcp-protogen-sampling: mcp_sampling annotation on tool methods — generates pre-configured SampleForXxx() helper with system_prompt, max_tokens, include_context, model preferences. Service-level default_sampling in mcp_service; method-level overrides.
- mcp-protogen-elicitation: mcp_elicit annotation on tool methods — schema_message references a proto message, JSON Schema auto-derived via schema.FromMessage(). Generates typed ElicitXxx() helper returning (*SchemaMsg, action, error). Uses generic runtime.BindElicitResult[T] for type-safe unmarshaling.
- mcp-protogen-completions: completable_fields on mcp_resource and mcp_prompt annotations. Generates deduplicated Completer interface + RegisterXxxMCPCompletions dispatcher.
- mcp-client-toolcall-full: Client.ToolCallFull returns *core.ToolResult directly — preserves IsError, all Content blocks, and StructuredContent. Tool-level errors returned in result, not as Go errors. (#215)
- mcp-dynamic-registration: Registry.AddTool/RemoveTool/AddResource/RemoveResource/AddPrompt/RemovePrompt — thread-safe runtime registration with automatic notifications/*/list_changed broadcast via OnChange callback
- mcp-session-timeout: WithSessionTimeout — idle session cleanup for Streamable HTTP (timer + ref counting to avoid closing mid-execution)
- mcp-sse-resumption: WithSSEGracePeriod — SSE sessions survive brief disconnects with grace timer. Client reconnects via ?sessionId= query param; server replays missed events via Last-Event-ID header. Principal-bound for security.
- mcp-server-capabilities-typed: core.ServerCapabilities, ToolsCap, ResourcesCap, PromptsCap — typed structs for initialize response capabilities
- mcp-command-transport: CommandTransport — spawn subprocess MCP servers, communicate via stdio, graceful SIGTERM/SIGKILL shutdown, stderr capture, env passthrough. WithCommandTransport client option supports reconnection (auto-restart).
- mcp-tool-exec: ToolExec — wrap CLI binaries as MCP tools with structured I/O. ExecConfig supports static/dynamic args, env, dir, timeout. BuildArgs callback maps JSON tool arguments to CLI flags.
- mcp-modify-request: WithModifyRequest — client-side HTTP request hook for injecting custom headers (tracing, tenant IDs). Runs before auth, applies to Streamable HTTP + SSE transports.
- mcp-sse-retry-hint: core.EmitSSERetry — tool/resource/prompt handlers emit raw SSE `retry:` field to tell clients how long to wait before reconnecting. Both SSE (2024-11-25) and Streamable HTTP transports. Streamable HTTP routes the hint from the POST handler to the session's GET SSE stream. Hint-only (no disconnect). Combines with WithSSEGracePeriod + WithEventStore for drop-and-resume patterns. (#72, #202)
- mcp-tool-context-detach: core.DetachFromClient — tool handlers opt into surviving client disconnect and per-tool timeout. Uses context.WithoutCancel to strip cancellation while preserving session state. Combine with EmitSSERetry + GracePeriod + EventStore for long-running tools with result replay on reconnect. (#203)
- mcp-auth-refresh-callback: OAuthTokenSource.OnToken — optional callback fired after successful refresh_token grant by the underlying oneauth AuthClient. Use for external persistence without implementing CredentialStore. (#137)
- mcp-auth-refresh-flow: OAuthTokenSource.Token() attempts the refresh_token grant before falling back to LoginWithBrowser. Long-running clients (agents, CLI tools) no longer re-prompt for browser consent on every token expiry. Default in-memory cred store when CredStore is nil; TokenForScopes wipes stored credential to force full re-login on scope step-up; scope-coverage check skips refresh when stored credential doesn't cover requested scopes. (#196)
- mcp-typed-tool-registration: core.TypedTool[In, Out] and core.TextTool[In] — generic typed handlers with auto-derived JSON Schema from Go struct tags (via invopop/jsonschema). Zero schema drift between InputSchema and handler parameters. Out type dispatch: string → TextResult (no OutputSchema), struct → StructuredResult (auto OutputSchema), core.ToolResult → passthrough. TextTool[In] is sugar for TypedTool[In, string]. SchemaGenerator interface with default invopop binding, overridable via core.SetSchemaGenerator(). (#242)
- mcp-io-transport: Server.RunIO(ctx, r, w) — Content-Length framed JSON-RPC over arbitrary io.Reader/io.Writer streams. Generalizes stdio transport for Unix sockets, named pipes, SSH tunnels, test fixtures. Client: WithIOTransport(r, w). RunStdio/WithStdioTransport delegate to RunIO/WithIOTransport. (#253)
- mcp-logging-transport: core.LoggingTransport — wire-level JSON-RPC message tracing decorator for any core.Transport. Logs method names, direction (→/←), latency. Optional LogBodies for full JSON output. Complements server middleware (method-level) with transport-level visibility. (#254)
- mcp-auto-pagination: Client iterators c.Tools(ctx), c.Resources(ctx), c.ResourceTemplates(ctx), c.Prompts(ctx) — iter.Seq2 auto-pagination over cursor-based list responses. Generic paginate[T] helper handles cursor threading and context cancellation. (#252)
- mcp-custom-method-handlers: server.HandleMethod / WithMethodHandler — register handlers for custom JSON-RPC methods (e.g., events/poll, events/stream). Dispatched after initialization, participates in middleware. Built-in MCP methods cannot be overridden (panics). Uses core.MethodContext typed context. (#266)
- mcp-method-context: core.MethodContext — typed handler context for custom method handlers. Embeds BaseContext (EmitLog, Sample, Elicit, AuthClaims, Notify, DetachFromClient). Matches ToolContext/ResourceContext pattern.
- mcp-events-library-experimental: experimental/ext/events — EXPERIMENTAL library for MCP Events spec (triggers-events-wg). EventSource interface, TypedSource[Data] with auto-derived payloadSchema, Register() for protocol methods, WebhookRegistry with HMAC-SHA256 signing (ts+"."+body), TTL, retry with backoff, SSRF validation. cursorGap detection for ring buffer wrap. (#264)
- mcp-telegram-events-example: experimental/telegram-events — Reference server implementing MCP Events with Telegram. Three delivery modes: push (Broadcast+SSE), poll (events/poll), webhook (events/subscribe+HMAC POST). 21 tests. Companion to Clare Liguori's TypeScript impl. (#264)
- mcp-slog-handler: core.MCPLogHandler — slog.Handler that routes structured log records through MCP notifications/message. SlogToMCPLevel/MCPToSlogLevel bidirectional mapping. Respects per-session logging/setLevel. (#248)
- mcp-client-setloglevel: Client.SetLogLevel(level) — convenience method for logging/setLevel. Client.ListPrompts() for prompts/list.
- mcp-tasks-experimental: server/task_*.go — MCP Tasks protocol (spec 2025-11-25). Async tool execution with lifecycle tracking. server.RegisterTasks() installs middleware + tasks/get, tasks/result, tasks/list, tasks/cancel handlers. InMemoryTaskStore with channel-based WaitForResult/WaitForUpdate. Client helpers in client/tasks.go.
- mcp-tasks-side-channel: TaskContext.TaskElicit/TaskSample — background tasks can elicit user input or request LLM sampling via the tasks/result side-channel. The tasks/result handler proxies requests through its live POST SSE connection. Status transitions: working → input_required → working.
- mcp-tasks-detach-background: core.DetachForBackground(ctx) — returns a context suitable for background goroutines. Server registers a detach strategy that replaces the dead POST-scoped requestFunc with the session-level persistent push (GET SSE). Aligns with future sub-task spawning (#281).
- mcp-tasks-parent-task: TaskInfo.ParentTaskID — backward-compatible extension field for sub-task trees. Foundation for SpawnTool/WaitForTask threading model (#281).
- mcp-tasks-cancel-propagation: context.WithCancel on background goroutines — Cancel() fires ctx.Done() so tool handlers can exit early. activeTask struct consolidates channel + cancel func.
- mcp-tasks-status-notifications: notifications/tasks/status sent on cancel, completion, and status changes (Option 1 — from live handler context). Queue-based delivery deferred (#288).
- mcp-tasks-atomic-store: StoreTerminalResult — atomic result + status transition with terminal guard. Prevents cancel→completed race and double-completion.
- mcp-tasks-progress-token: _meta.progressToken from tools/call preserved on TaskContext.ProgressToken(). Background goroutine uses client's token for EmitProgress.
- mcp-tasks-client-helpers: client.ToolCallAsTask (variadic TaskCallOptions), client.WaitForTask(ctx), client.IsToolTask, client.GetTask/GetTaskPayload/ListTasks/CancelTask.
- mcp-tasks-post-sse-closure: POST-scoped SSE writer signals closure via atomic flag. Background goroutines get silent no-ops instead of panics on dead ResponseWriter.
- mcp-server-withmux: server.WithMux — TransportOption for registering additional HTTP routes (auth PRM, health checks) on the server's mux while staying on srv.Run()'s graceful shutdown path.
- mcp-sending-middleware: server.NotifyInterceptor + RequestInterceptor — wrap outgoing notifications and server-to-client requests. WithNotifyInterceptor/WithRequestInterceptor options. (#244)
- mcp-client-middleware: client.ClientMiddleware — call-level middleware on Client.Call path. WithClientMiddleware option. Method+params visibility for tracing, logging, metrics. (#245)
- mcp-session-hijack-protection: Streamable HTTP session hijacking prevention — binds Claims.Subject at session creation, verifies on POST/GET/DELETE. Mirrors SSE transport protection. (#249)
- mcp-public-methods: server.WithPublicMethods — bypass auth for specified JSON-RPC methods. Pre-auth capability discovery (tools/list without token). (#76)
- mcp-ctx-progress: ToolContext.Progress(progress, total, message) — token-free progress emission. Dispatch stashes progressToken from _meta.progressToken into ToolContext. ToolContext.ProgressToken() accessor.
- mcp-typed-app-tool: ext/ui.RegisterTypedAppTool[In, Out] — typed variant of RegisterAppTool. Auto-derives InputSchema from In type. Delegates to RegisterAppTool for all app-specific wiring.
- mcp-auth-examples: examples/auth/ — 5 persistent MCP servers demonstrating auth patterns: bearer (:8081), JWT/JWKS (:8082), scopes (:8083), session hijacking (:8084), pre-auth discovery (:8085). Shared common/ module. mcp.json for VS Code.
- mcp-tasks-types: core.TaskStatus (working/input_required/completed/failed/cancelled), TaskInfo, ToolExecution, TasksCap, ClientTasksCap — wire types for MCP Tasks spec 2025-11-25. ToolDef.Execution declares per-tool task support (required/optional/forbidden).
- mcp-tasks-server-plumbing: Server.SetTasksCap(), Server.UseMiddleware(), Registry.ToolDef() — server-side hooks for tasks capability advertisement and middleware injection post-construction.
- mcp-tasks-library: server/task_*.go + server/tasks_experimental.go — MCP Tasks protocol (spec 2025-11-25, wire-format parity with TS SDK). server.RegisterTasks(TasksConfig) installs middleware + method handlers. Side-channel elicitation/sampling via TaskContext.TaskElicit/TaskSample. Client helpers in client/tasks.go (GetTask, GetTaskPayload, ToolCallAsTask, etc.).
- mcp-tasks-callbacks: server.TaskCallbacks — per-tool GetTask/GetResult overrides for the external proxy pattern (Step Functions, CI pipelines). Registered via server.Tool{TaskCallbacks: &TaskCallbacks{...}}. creatorToolForTask map dispatches to callbacks; falls through to TaskStore when callback returns false.
- mcp-tasks-conformance: conformance/tasks/ — 27 scenarios testing full Tasks v1 protocol surface. Uses official TS SDK client against both Go and TS reference servers. Covers lifecycle, errors, TTL, concurrency, session isolation, elicitation, sampling, progress, status notifications, related-task _meta.

## Module
github.com/panyam/mcpkit

## Location
newstack/mcpkit/main

## Stack Dependencies

### Core module (github.com/panyam/mcpkit)
- servicekit (github.com/panyam/servicekit) v0.0.25 — SSEConn/SSEHub, ListenAndServeGraceful, StreamableServe, HTTPStatusError (with Header), MaxErrorBodySize

### Sub-module: ext/auth (github.com/panyam/mcpkit/ext/auth)
- oneauth (github.com/panyam/oneauth) v0.0.71 — JWT/OIDC validation, testutil.TestAuthServer; separate go.mod

### Sub-module: experimental/ext/protogen (github.com/panyam/mcpkit/experimental/ext/protogen)
- protokit (github.com/panyam/protokit) v0.0.5 — proto descriptor test utilities, PopulateFieldFromPath (dot-path field binding with type coercion), wire package (proto wire-format decoding helpers for extension extraction)

## Integration

### Go Module
```go
require github.com/panyam/mcpkit v0.2.9
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
