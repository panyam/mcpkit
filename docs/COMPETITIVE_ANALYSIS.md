# MCPKit vs go-sdk — Competitive Analysis

> **Last updated**: 2026-04-15
> **go-sdk version analyzed**: v1.5.x (main branch, Go 1.25.0)
> **mcpkit version**: 0.2.20

This document compares mcpkit against the official MCP Go SDK (`modelcontextprotocol/go-sdk`). It is intended to be maintained over time as both projects evolve.

---

## Executive Summary

go-sdk is the official, minimal MCP SDK. It ships a single Go module with typed generic handlers and auto-generated schemas from Go structs. It deliberately stays thin — no extensions, no admin, no codegen.

mcpkit is a production-grade MCP library with a modular architecture (core + server + client + 3 extension sub-modules). It targets teams building real services: auth, observability, protobuf codegen, MCP Apps, and test infrastructure are first-class.

**The core question "why not go-sdk?" usually comes down to**: go-sdk gets you a working MCP server in 20 lines. mcpkit gets you a production MCP server you can operate, extend, and evolve.

---

## Feature Comparison

### Protocol Coverage

| Feature | go-sdk | mcpkit | Notes |
|---------|--------|--------|-------|
| Tools (call/list/cancel) | Yes | Yes | |
| Resources (read/list/templates) | Yes | Yes | |
| Prompts (get/list) | Yes | Yes | |
| Sampling (createMessage) | Yes | Yes | |
| Elicitation (create) | Yes | Yes | |
| Completions (complete) | Yes | Yes | |
| Roots (list/changed) | Yes | Yes | |
| Logging (setLevel/message) | Yes | Yes | |
| Progress (notifications) | Yes | Yes | |
| Ping (before init) | Yes | Yes | |
| Cancellation (notifications/cancelled) | Yes | Yes | |
| Keep-alive (periodic ping) | Yes | Yes | |
| Content cardinality tolerance | No | **Yes** | mcpkit accepts both object/array `content` forms (#81) |
| Structured output (OutputSchema) | Yes | Yes | |
| Content chunk streaming | No | **Yes** | EmitContent() for partial results during tool execution |
| Protocol version negotiation | 4 versions | 2 versions | go-sdk: 2024-11-05 through 2025-11-25 |

Both libraries have full core protocol coverage. mcpkit adds defensive content parsing and mid-execution streaming that go-sdk lacks.

### Transports

| Transport | go-sdk Server | go-sdk Client | mcpkit Server | mcpkit Client |
|-----------|:---:|:---:|:---:|:---:|
| Stdio | Yes | Yes (CommandTransport) | Yes | Yes (CommandTransport) |
| SSE (2024-11-05) | Yes | Yes | Yes | Yes |
| Streamable HTTP | Yes | Yes | Yes | Yes |
| In-Process | Yes (InMemory) | Yes (InMemory) | Yes | Yes |
| IO (generic reader/writer) | Yes | Yes | — | — |
| Dual (SSE + Streamable HTTP) | — | — | **Yes** | — |
| Logging transport wrapper | **Yes** | **Yes** | — | — |

**go-sdk edge**: `LoggingTransport` wraps any transport for wire-level debugging. `IOTransport` for arbitrary streams.

**mcpkit edge**: Dual-mode serving (SSE + Streamable HTTP simultaneously). SSE grace period with event replay for brief disconnects.

### Transports — Advanced Features

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| Stateless mode (serverless) | Yes | Yes |
| Session timeout (idle cleanup) | Yes | Yes |
| Stream resumability (EventStore) | Yes (MemoryEventStore) | Yes (WithEventStore) |
| SSE grace period (survive brief disconnects) | No | **Yes** |
| SSE retry hint (server → client) | No | **Yes** |
| DNS rebinding protection | Yes | Yes |
| Max concurrent sessions | No | **Yes** |
| Per-request server selection | **Yes** | No |
| JSON-only response mode | **Yes** | No |

### Handler Design

This is the sharpest divergence between the two libraries.

#### go-sdk: Generic Typed Handlers

```go
type GreetInput struct {
    Name string `json:"name" jsonschema:"the name to greet"`
}

func SayHi(ctx context.Context, req *mcp.CallToolRequest, input GreetInput) (*mcp.CallToolResult, any, error) {
    return mcp.NewResult(mcp.NewTextContent("Hi " + input.Name)), nil, nil
}

mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"}, SayHi)
```

- Schema auto-generated from Go struct via `jsonschema-go`
- Function signature enforces input/output types — impossible to drift
- Handlers receive plain `context.Context`
- To emit logs/progress, must obtain `ServerSession` from context manually

#### mcpkit: Typed Handler Contexts

```go
srv.RegisterTool(
    core.ToolDef{
        Name: "greet", Description: "say hi",
        InputSchema: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "name": map[string]any{"type": "string", "description": "the name to greet"},
            },
            "required": []string{"name"},
        },
    },
    func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
        var args struct{ Name string `json:"name"` }
        req.Bind(&args)
        ctx.EmitLog(core.LogInfo, "greet", "greeting "+args.Name)
        return core.TextResult("Hi " + args.Name), nil
    },
)
```

- Schema is explicit (map literal or protogen-generated)
- `ToolContext` provides `EmitLog`, `EmitProgress`, `Sample`, `Elicit`, `AuthClaims` directly — IDE-discoverable
- Schema and handler struct can drift if using inline maps (mitigated by protogen)

#### mcpkit + Protogen: Best of Both

```proto
rpc SearchBooks(SearchBooksRequest) returns (SearchBooksResponse) {
    option (mcp.v1.mcp_tool) = {
        name: "search"
        description: "Search the book catalog"
    };
}
```

```go
// Generated — developer writes only the implementation
type BookServiceMCPServer interface {
    SearchBooks(ctx core.ToolContext, req *SearchBooksRequest) (*SearchBooksResponse, error)
}
```

- Schema derived from proto message — zero drift
- Typed contexts + typed request/response
- Supports sampling, elicitation, completions via proto annotations
- 3 forwarding variants: in-process, gRPC, ConnectRPC

| Aspect | go-sdk (AddTool) | mcpkit (Register) | mcpkit (TypedTool) | mcpkit (protogen) |
|--------|:---:|:---:|:---:|:---:|
| Schema auto-generated | Yes (struct tags) | No (manual) | **Yes** (struct tags) | Yes (proto message) |
| Type-safe handler params | Yes | No | **Yes** | Yes |
| Schema ↔ handler drift | Impossible | **Possible** | **Impossible** | Impossible |
| `additionalProperties` control | **No** (hardcoded false) | Yes | **Yes** | Yes |
| Full JSON Schema expressiveness | Limited (struct reflection) | Full | High (struct tags) | High (proto mapping) |
| Typed handler context | No (plain ctx) | **Yes** | **Yes** | **Yes** |
| IDE-discoverable MCP methods | No | **Yes** | **Yes** | **Yes** |
| External toolchain required | No | No | **No** | Yes (protoc) |

**Assessment**: With `TypedTool[In, Out]`, mcpkit closes the DX gap with go-sdk while retaining typed contexts and `additionalProperties` control. Three tiers cover all use cases: `TypedTool` for pure Go (zero drift, zero toolchain), `Register` for dynamic/proxy tools, protogen for gRPC/Connect services.

### Schema Generation

| Feature | go-sdk | mcpkit (Register) | mcpkit (TypedTool) | mcpkit (protogen) |
|---------|--------|--------|--------|--------|
| Struct → JSON Schema (reflection) | **Yes** (google/jsonschema-go) | No | **Yes** (invopop/jsonschema) | No |
| Proto → JSON Schema (codegen) | No | No | No | **Yes** (schema.FromMessage) |
| Manual schema (any) | Yes (override Tool.InputSchema) | **Yes** (InputSchema: any) | No | No |
| `additionalProperties` control | **No** (hardcoded false) | Yes | **Yes** (omitted by default) | Yes |
| Required field inference | Struct tags | Manual `required` array | **`omitempty` absence** (Go convention) | Proto `optional` keyword |
| Schema validation at registration | Yes (panic on malformed) | Yes | Yes | Yes |
| Schema validation at call time | Yes (with defaults) | Yes (jsonschema/v6) | Yes (jsonschema/v6) | Yes (jsonschema/v6) |
| Auto OutputSchema from type | **Yes** | No | **Yes** (struct Out) | No |
| SchemaCache (stateless perf) | **Yes** | No | No | No |
| Output schema validation | **Yes** | No | No | No |

**go-sdk edge**: Output schema validation, SchemaCache for stateless deployments.

**mcpkit edge**: Three schema paths for different needs. TypedTool matches go-sdk's zero-config DX while preserving `additionalProperties` control. Protogen eliminates drift for proto-native services. Register gives full manual control for dynamic/proxy tools.

### Authentication & Authorization

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| Bearer token validation | Yes (RequireBearerToken) | Yes (BearerTokenValidator) |
| JWT/OIDC validation | User-provided TokenVerifier | **Yes** (JWTValidator via oneauth) |
| OAuth PRM (RFC 9728) | Yes (handler) | **Yes** (MountAuth auto-wires) |
| OAuth Authorization Code (client) | Yes (experimental) | **Yes** (OAuthTokenSource) |
| Client Credentials flow | Planned | **Yes** (via oneauth) |
| Dynamic Client Registration | Yes | Yes |
| Token refresh with callback | No | **Yes** (#137, OnToken callback) |
| Scope enforcement helpers | No | **Yes** (RequireScope, HasScope) |
| Claims propagation to handlers | Via context | **Yes** (ctx.AuthClaims()) |
| WWW-Authenticate builders | No | **Yes** (WWWAuth401/403) |
| Enterprise auth (OIDC + JWT exchange) | In progress (PR #770) | No |
| Auth conformance | Client conformance passing | **14/14 scenarios (210 checks)** |
| Session hijacking protection | **Yes** (userID check) | No |

**mcpkit edge**: Auth is a first-class sub-module with JWT validation, scope enforcement, PRM auto-wiring, refresh callbacks, and WWW-Authenticate builders. go-sdk gives you hooks but expects you to build the auth layer.

**go-sdk edge**: Session hijacking protection (binding userID to session). Enterprise auth (OIDC + JWT Bearer exchange) in progress.

### Middleware

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| Middleware system | Yes | Yes |
| Abstraction level | JSON-RPC method string | HTTP request/response |
| Sending middleware (outgoing) | **Yes** | No |
| Receiving middleware (incoming) | **Yes** | Yes |
| Client middleware | **Yes** | No |
| Per-tool/per-resource middleware | No | No |
| Built-in logging middleware | Yes | Yes |
| Built-in tool timeout | No (manual) | **Yes** (WithToolTimeout) |
| Built-in roots enforcement | No | **Yes** (WithAllowedRoots) |

**go-sdk edge**: Bidirectional middleware on both server and client. Middleware on outgoing messages enables tracing/logging of server-initiated requests.

**mcpkit edge**: Built-in middleware for common needs (timeouts, roots enforcement) so you don't have to write them.

### Dynamic Registration

Both fully support runtime add/remove of tools, resources, prompts with automatic `list_changed` notifications. Parity here.

### Session Management

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| Multi-session | Yes | Yes |
| Session close (programmatic) | Via session.Close() | **Yes** (Server.CloseSession/CloseAllSessions) |
| Session timeout | Yes | Yes |
| Max concurrent sessions | No | **Yes** |
| Session registry/introspection | No | No |
| Stateless mode | Yes | Yes |

### Client Features

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| Auto-pagination (iterators) | **Yes** (iter.Seq2) | No |
| Reconnection | SSE stream only | **Full session** (WithMaxRetries + re-initialize) |
| Auth retry (401/403) | Yes | Yes |
| ToolCallFull (preserve isError) | Yes | Yes |
| ModifyRequest hook | No | **Yes** (WithModifyRequest) |
| Notification callbacks | Yes | Yes |
| Client logging | No | **Yes** (WithClientLogging) |
| Connect timeout | No | **Yes** (WithConnectTimeout) |
| Client keepalive | **Yes** | **Yes** |

**go-sdk edge**: `iter.Seq2` auto-pagination is elegant — no manual cursor handling.

**mcpkit edge**: Full session reconnection (not just SSE stream), ModifyRequest hook for injecting headers (tracing, tenant IDs), connect timeout, client-side logging.

### Testing Utilities

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| In-memory transport | Yes (InMemoryTransport) | Yes (InProcessTransport) |
| LoggingTransport | **Yes** | No |
| NewTestServer helper | No | **Yes** |
| TestClient wrapper | No | **Yes** |
| ForAllTransports (parametric) | No | **Yes** |
| SchemaCache (stateless testing) | **Yes** | No |

**mcpkit edge**: `ForAllTransports` runs every test against all 4 transports as subtests. `NewTestServer` + `TestClient` reduce test boilerplate significantly.

### Extensions & Ecosystem

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| Extension system | No | **Yes** (Extension/Stability/ExtensionProvider) |
| MCP Apps (ext/ui) | No | **Yes** (21 conformance tests) |
| Protobuf codegen (ext/protogen) | No | **Yes** (buf.build published) |
| App Bridge (JS) | No | **Yes** (TypeScript source + compiled) |
| File serving (file:// URIs) | **Yes** (built-in) | No |
| Sub-module architecture | No (monorepo) | **Yes** (core + 3 sub-modules) |

This is mcpkit's strongest competitive advantage. MCP Apps, protobuf codegen, and the extension system have no go-sdk equivalent and would require significant effort to replicate.

### Observability & Operations

| Feature | go-sdk | mcpkit |
|---------|--------|--------|
| Admin HTTP endpoints | No | No |
| Stats/metrics | No | No |
| Health checks | No | No |
| Error handler interface | No | **Yes** (OnSessionExpire, OnTransportError, OnKeepaliveFailure) |
| Request logging | Via middleware | Yes (WithRequestLogging) |
| slog integration | **Yes** (LoggingHandler) | No |

Neither library has admin/observability endpoints yet. go-sdk has better slog integration; mcpkit has a structured error handler interface.

### Conformance

| Suite | go-sdk | mcpkit |
|-------|--------|--------|
| Server conformance | All pass | 30/30 |
| Client conformance | All pass | Not measured separately |
| Auth conformance | Not measured separately | 14/14 (210 checks) |
| MCP Apps conformance | N/A | 21 tests |
| Full test pipeline | — | 9/9 stages (testall) |

---

## What go-sdk Has That We Don't

These are real features where go-sdk has something mcpkit lacks.

### We Should Add (real gaps)

1. **Auto-pagination iterators** — `iter.Seq2` for `Tools()`, `Resources()`, `Prompts()`. Elegant API that eliminates manual cursor handling. Low effort, high DX value.

2. **Output schema validation** — go-sdk validates tool outputs against OutputSchema at call time. We validate inputs but not outputs. Worth adding for structured output correctness.

3. **Sending/client middleware** — go-sdk supports middleware on outgoing messages and on the client. Useful for tracing, logging server-initiated requests (sampling, elicitation).

4. **Session hijacking protection** — go-sdk binds user identity to session, rejecting requests from different users on the same session. Security hardening we should consider.

5. **slog.Handler integration** — go-sdk's `LoggingHandler` implements `slog.Handler`, letting MCP logging plug into the standard Go logging ecosystem.

### We Could Add (nice-to-have)

6. ~~**Generic typed handlers**~~ — **Done.** `server.TypedTool[In, Out]` + `server.TextTool[In]` shipped in `server/typed_tool.go`.

7. **IO Transport (generic reader/writer)** — Accept any `io.ReadCloser`/`io.WriteCloser` as a transport. Enables Unix domain sockets, named pipes, SSH tunnels, custom framing, and test fixtures without writing a full transport. The composable escape hatch for "batteries included."

8. **LoggingTransport** — Decorator that wraps any transport and logs every JSON-RPC message. The `tcpdump` of MCP — essential for debugging auth failures, schema validation, conformance issues, and audit logging. Zero-cost when not enabled.

9. **Per-request server selection** — go-sdk's `NewStreamableHTTPHandler(func(*http.Request) *Server)` enables multi-tenant routing at the transport level. Interesting for gateway patterns. (#250)

10. **SchemaCache** — Avoid repeated schema compilation in stateless deployments. Only matters at high scale.

### We Won't Add (by design)

10. **Single-package architecture** — go-sdk ships everything in one `mcp` package. We deliberately split into core/server/client/ext/* for modularity and independent versioning. This is a design choice, not a gap.

11. **`additionalProperties: false` by default** — go-sdk hardcodes this on generated schemas. We give developers full control. Their default is a known limitation (go-sdk#892).

12. **`MCPGODEBUG` compat system** — go-sdk uses environment variable flags for backward-compatible behavior changes. We prefer explicit API versioning.

---

## What We Have That go-sdk Doesn't

### Architectural Advantages

1. **Typed handler contexts** — `ToolContext`/`ResourceContext`/`PromptContext` with IDE-discoverable methods. go-sdk uses plain `context.Context`; developers must manually extract `ServerSession` to emit logs/progress/sample/elicit.

2. **Extension system** — Formal `Extension`/`Stability`/`ExtensionProvider` for declaring sub-protocol support in capability negotiation. go-sdk has no extension mechanism.

3. **Sub-module architecture** — Independent Go modules for auth, UI, protogen. Teams import only what they need. go-sdk is monolithic.

4. **Protobuf codegen** — `protoc-gen-go-mcp` generates tool/resource/prompt registrations from proto annotations. Schema derived from proto messages (zero drift). 3 forwarding variants (in-process, gRPC, ConnectRPC). go-sdk has nothing comparable.

5. **MCP Apps** — Full ext/ui implementation with App Bridge (JS), template resources, display modes, CSP, visibility filtering. 21 conformance tests. go-sdk doesn't support MCP Apps.

### Operational Advantages

6. **Full client reconnection** — Automatic session re-initialization on transient errors, not just SSE stream retry.

7. **Auth sub-module** — JWT validation, scope enforcement, PRM auto-wiring, token refresh callbacks, WWW-Authenticate builders. go-sdk provides hooks; we provide the implementation.

8. **Error handler interface** — Structured callbacks for session expiry, transport errors, keepalive failures.

9. **SSE resilience** — Grace period, event replay via Last-Event-ID, SSE retry hints, DetachFromClient for long-running tools.

10. **Testing infrastructure** — `ForAllTransports`, `NewTestServer`, `TestClient` — parametric tests across all transports with minimal boilerplate.

### DX Advantages

11. **Content cardinality tolerance** — Defensive parsing of `content` field (both object and array forms). Eliminates a class of interop bugs.

12. **Dual transport mode** — Serve SSE and Streamable HTTP simultaneously for gradual migration.

13. **Max concurrent sessions** — Built-in session cap without custom middleware.

14. **ModifyRequest hook** — Inject custom headers (tracing, tenant IDs) on client transport.

15. **Content chunk streaming** — `EmitContent()` for partial results during tool execution.

---

## Strategic Assessment

### go-sdk's Strengths

- **Official SDK** — Backed by Anthropic/MCP org. Will be the default recommendation. Conformance is a given.
- **Simplicity** — Single import, 20 lines to a working server. Hard to beat for getting started.
- **Generic typed handlers** — `AddTool[In, Out]` is the best DX for simple tools in pure Go.
- **Ecosystem gravity** — As the official SDK, it will accumulate community examples, blog posts, and integrations.

### mcpkit's Moat

- **Production readiness** — Auth, reconnection, error handling, session management, testing infrastructure. go-sdk gives you primitives; mcpkit gives you production patterns.
- **MCP Apps** — First (and currently only) Go implementation. 21 conformance tests. App Bridge JS. This is a significant lead.
- **Protobuf codegen** — Unique capability. Teams with existing gRPC/Connect services get MCP for free. Schema drift is impossible.
- **Typed contexts** — IDE-discoverable MCP capabilities on the handler context. This is a DX advantage that compounds as the protocol grows.
- **Modular architecture** — Import what you need. Auth, UI, and protogen evolve independently.

### Risks

- **Official SDK momentum** — As go-sdk matures, it will close gaps (better auth, middleware, etc.). Our lead narrows unless we keep shipping.
- **Breaking changes** — go-sdk is pre-1.0 and moves fast. They could adopt typed contexts, extensions, or codegen.
- **Adoption barrier** — Requiring `ext/protogen` toolchain for best DX is a higher bar than `mcp.AddTool`.
- **Single maintainer** — go-sdk has organizational backing; mcpkit needs to demonstrate reliability and responsiveness.

### Strategy

1. **Keep shipping extensions** — MCP Apps, protogen, and auth are the moat. Every new extension feature widens the gap.
2. **Close the easy gaps** — Auto-pagination iterators, IO transport, LoggingTransport, slog integration. These are table stakes.
3. **Generic typed handlers** — `TypedTool[T]()` closes the biggest DX gap vs go-sdk while keeping our typed context advantage. See design below.
4. **Production stories** — Document operational patterns (auth, reconnection, monitoring) that go-sdk doesn't address.

---

## Design: Generic Typed Handlers (`TypedTool[In, Out]`)

### Problem

mcpkit's inline schema approach (`InputSchema: map[string]any{...}`) is verbose and prone to drift — the schema map and the handler's `Bind()` struct can diverge silently. go-sdk solved this with `AddTool[In, Out]` but gave up typed contexts and `additionalProperties` control.

### Proposed API

```go
// The general form — both input and output are typed
func TypedTool[In, Out any](name, description string,
    handler func(ctx core.ToolContext, input In) (Out, error),
    opts ...TypedToolOption,
) server.Tool

// Sugar for the common case (Out = string → text result, no OutputSchema)
func TextTool[In any](name, description string,
    handler func(ctx core.ToolContext, input In) (string, error),
    opts ...TypedToolOption,
) server.Tool {
    return TypedTool[In, string](name, description, handler, opts...)
}
```

### Output Type Behavior

`TypedTool[In, Out]` switches behavior based on `Out` at registration time:

| `Out` type | OutputSchema | Result construction |
|---|---|---|
| `string` | None | `core.TextResult(output)` |
| Any struct | Auto-derived from `Out` | `core.StructuredResult(output)` + OutputSchema on ToolDef |
| `core.ToolResult` | None (full control) | Pass through as-is |

This means `TextTool[In]` is just `TypedTool[In, string]` — one implementation, not a separate code path.

### Usage

```go
// Most common: text output
srv.Register(
    server.TextTool[SearchInput]("search", "Search the catalog",
        func(ctx core.ToolContext, input SearchInput) (string, error) {
            ctx.EmitProgress(ctx.ProgressToken(), 0, 100, "searching...")
            books := searchBooks(input.Query, input.Genre, input.MaxResults)
            return formatBooks(books), nil
        },
    ),
)

// Structured output: OutputSchema auto-derived from SearchOutput
srv.Register(
    server.TypedTool[SearchInput, SearchOutput]("search", "Search the catalog",
        func(ctx core.ToolContext, input SearchInput) (SearchOutput, error) {
            books := searchBooks(input.Query, input.Genre, input.MaxResults)
            return SearchOutput{Results: books, Total: len(books)}, nil
        },
    ),
)

// Escape hatch: full control over content blocks
srv.Register(
    server.TypedTool[SearchInput, core.ToolResult]("search", "Search the catalog",
        func(ctx core.ToolContext, input SearchInput) (core.ToolResult, error) {
            // Return multiple content blocks, images, etc.
            return core.ToolResult{
                Content: []core.Content{
                    {Type: "text", Text: "found 3 books"},
                    {Type: "image", Data: coverImage, MimeType: "image/png"},
                },
            }, nil
        },
    ),
)

type SearchInput struct {
    Query      string `json:"query"      jsonschema:"description=Search query,required"`
    Genre      string `json:"genre"      jsonschema:"description=Filter by genre"`
    MaxResults int    `json:"max_results" jsonschema:"description=Max results,default=10"`
}

type SearchOutput struct {
    Results []Book `json:"results"`
    Total   int    `json:"total"`
}
```

### Design Decisions

**1. `[In, Out]` with `TextTool[In]` sugar**

`TypedTool[In, Out]` is the single primitive. `TextTool[In]` is `TypedTool[In, string]` — the 80% case where tools return text. The `Out` type determines whether an OutputSchema is generated and how the return value is wrapped. One implementation, three behaviors via runtime type check on `Out`.

**2. Free function returning `server.Tool`**

Go doesn't support generic methods on structs. A free function that returns `server.Tool` composes with the existing `srv.Register()` API — no new registration path needed.

**3. Schema generation: `invopop/jsonschema` (not `google/jsonschema-go`)**

go-sdk uses Google's package, which hardcodes `additionalProperties: false`. We'd use `invopop/jsonschema` which:
- Respects struct tags for full schema control
- Doesn't hardcode `additionalProperties`
- Supports `jsonschema:"enum=a|b|c"`, `jsonschema:"required"`, etc.
- Well-maintained, widely used

**4. Layered architecture — TypedTool builds on Register**

`TypedTool[In, Out]` is implemented on top of `Register(Tool{...})`. It generates a `ToolDef` (with auto-schema from `In` and optionally `Out`) and wraps the typed handler into a `ToolHandler` that deserializes the raw JSON arguments.

The foundation layer (`Register`) is not a competing API — it's the substrate:

| Layer | API | Who uses it |
|-------|-----|-------------|
| **Foundation** | `Register(Tool{ToolDef, ToolHandler})` | Dispatch internals, dynamic tools, proxies, protogen codegen |
| **Type-safe** | `TypedTool[In, Out]()` | Pure Go tools — the recommended default |
| **Sugar** | `TextTool[In]()` | Common case (= `TypedTool[In, string]`) |

The foundation layer remains necessary for:
- **Dynamic tools** — schema loaded from config/DB at runtime, unknown at compile time
- **Forwarding/proxy tools** — operate on raw JSON, never deserialize to a Go type
- **Protogen codegen** — has its own schema pipeline (proto descriptors → JSON Schema), more precise than struct reflection for proto-specific types (`oneof` → `oneOf`, `Timestamp` → ISO-8601, `map<K,V>` → `additionalProperties`). Protogen stays on the foundation layer because it already has type-safe interfaces and proto-native binding.

**5. Struct tag conventions**

```go
type MyInput struct {
    // Required string field with description
    Name string `json:"name" jsonschema:"required,description=The user name"`

    // Optional enum field
    Role string `json:"role" jsonschema:"enum=admin|user|guest"`

    // Nested object
    Address Address `json:"address"`

    // Allow extra fields on this struct (our advantage over go-sdk)
    // By default: additionalProperties is NOT set (JSON Schema default = true)
    // To restrict: use jsonschema:"additionalProperties=false" on the struct
}
```

### Comparison

| | go-sdk `AddTool[In,Out]` | mcpkit `Register(Tool{})` | **mcpkit `TypedTool[In,Out]`** | mcpkit protogen |
|---|:---:|:---:|:---:|:---:|
| Lines per tool | ~13 | ~25 | **~12** | ~3 (proto) + impl |
| Schema drift | Impossible | Possible | **Impossible** | Impossible |
| Typed context | No | Yes | **Yes** | Yes |
| `additionalProperties` | Hardcoded false | Full control | **Full control** | Full control |
| External toolchain | No | No | **No** | Yes (protoc) |
| Auto OutputSchema | Yes | No | **Yes** (when Out is struct) | No |
| Dynamic schemas | Yes (override) | Yes | No (use Register) | No (use Register) |
| Proto-native types | No | N/A | No | **Yes** (oneof, Timestamp, etc.) |

---

## Tracking

| Gap | Priority | Status | Issue | Notes |
|-----|----------|--------|-------|-------|
| `TypedTool[In,Out]` + `TextTool[In]` | High | **Done** | — | Shipped: `server/typed_tool.go` |
| Migrate examples to TypedTool | High | Open | #242 | Validate DX with real usage |
| Auto-pagination iterators | High | Open | — | Low effort, high DX value |
| IO Transport (generic reader/writer) | High | Open | — | Composable transport primitive — Unix sockets, pipes, SSH, tests |
| LoggingTransport (decorator) | High | Open | — | Wire-level debugging, audit logging, conformance testing |
| Server sending middleware | Medium | Open | #244 | Trace/log outgoing sampling, elicitation, notifications |
| Client middleware chain | Medium | Open | #245 | Trace/log/retry all client operations |
| Session registry/introspection | Medium | Open | #246 | Admin/observability: list sessions, inspect state |
| File serving (file:// resources) | Medium | Open | #247 | Built-in with root validation (go-sdk has this) |
| slog.Handler integration | Medium | Open | #248 | Standard Go structured logging ecosystem |
| Session hijacking protection | Medium | Open | #249 | Bind user identity to session (may pushdown to oneauth/servicekit) |
| Per-request server selection | Low | Open | #250 | Multi-tenant gateway routing (may pushdown to servicekit) |
| Evaluate TypedPrompt[In] | Low | Open | #243 | Extend typed pattern to prompts if validated by adoption |
| Output schema validation | Low | Open | — | Correctness for structured output |
| SchemaCache | Low | Open | — | Only matters at high scale |
