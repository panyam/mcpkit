# MCPKit Gateway — Design Document

## Overview

MCPKit Gateway extends MCPKit from a library for building MCP servers to a **universal MCP gateway** that exposes any HTTP or gRPC service as MCP tools — without modifying the backend service. The gateway handles MCP protocol, transport, auth, scoping, and audit. Backend services continue serving HTTP/gRPC as before.

This positions MCPKit as a lightweight, embeddable alternative to Kong AI MCP Proxy, IBM ContextForge, and Envoy AI Gateway — purpose-built for MCP rather than bolted onto a general API gateway.

## Problem

Organizations have hundreds of existing services. Making them accessible to AI agents requires MCP. Today the options are:

1. **Rewrite** — add MCP protocol handling to every service (expensive, multi-language)
2. **Kong/Envoy** — heavyweight API gateways with MCP plugins (complex to operate, vendor lock-in)
3. **ContextForge** — federated registry (IBM-specific ecosystem)

MCPKit Gateway offers a fourth path: a single Go binary (or embeddable library) that reads service descriptions and proxies MCP tool calls to existing backends.

## Design Principles

1. **Config-driven by default** — a YAML file is all you need. No codegen, no SDK, no annotations on the backend.
2. **Loader-agnostic** — OpenAPI, gRPC reflection, proto files, and YAML all produce the same internal tool manifest. New loaders are additive.
3. **Backend services are unmodified** — they never see MCP, SMCP, or JSON-RPC. The gateway translates.
4. **Security is layered, not monolithic** — bearer tokens work out of the box. JWT/OIDC, SMCP envelopes, and capability scopes are opt-in layers.
5. **Embeddable and standalone** — use as a Go library or run as `mcpkit-gateway` binary. No Nginx, no Lua, no sidecar mesh required.
6. **Native + proxied tools coexist** — a single gateway can serve tools written in Go (native MCPKit handlers) alongside proxied tools backed by external services.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            MCP Clients                                      │
│                   (Claude, agents, IDE plugins)                              │
└────────────────────────────┬────────────────────────────────────────────────┘
                             │ MCP (SSE / Streamable HTTP / stdio)
                             ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                        MCPKit Gateway                                       │
│                                                                             │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │  Transport   │  │    Auth     │  │   Scope      │  │     Audit        │  │
│  │  SSE         │  │  Bearer     │  │   Evaluator  │  │   Structured log │  │
│  │  Streamable  │  │  JWT/OIDC   │  │   (deny-by-  │  │   Non-repud.     │  │
│  │  stdio       │  │  SMCP (opt) │  │    default)  │  │   (optional)     │  │
│  └──────┬──────┘  └──────┬──────┘  └──────┬───────┘  └──────┬───────────┘  │
│         │                │                │                  │              │
│         ▼                ▼                ▼                  ▼              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                        MCP Dispatch (existing)                       │   │
│  │          tools/list, tools/call, resources/*, prompts/*              │   │
│  └──────────────────────────────────┬──────────────────────────────────┘   │
│                                     │                                      │
│  ┌──────────────────────────────────▼──────────────────────────────────┐   │
│  │                      Tool Registry (merged)                         │   │
│  │                                                                     │   │
│  │   ┌──────────────┐  ┌───────────────┐  ┌────────────────────────┐  │   │
│  │   │ Native tools │  │ Proxied tools │  │  Proxied tools         │  │   │
│  │   │ (Go handlers)│  │ (HTTP backends│  │  (gRPC backends)       │  │   │
│  │   └──────────────┘  └───────┬───────┘  └───────────┬────────────┘  │   │
│  │                             │                      │               │   │
│  └─────────────────────────────┼──────────────────────┼───────────────┘   │
│                                │                      │                    │
│  ┌─────────────────────────────▼──────────────────────▼───────────────┐   │
│  │                     Proxy Dispatch Layer                            │   │
│  │         (tool call → backend HTTP/gRPC request → MCP result)       │   │
│  │                                                                     │   │
│  │   ┌───────────────┐  ┌──────────────┐  ┌───────────────────────┐  │   │
│  │   │ Request Map   │  │ Auth Forward │  │ Response Map          │  │   │
│  │   │ (args→body/   │  │ (passthru /  │  │ (response→            │  │   │
│  │   │  path/query)  │  │  exchange /  │  │  ToolResult content)  │  │   │
│  │   │               │  │  static)     │  │                       │  │   │
│  │   └───────────────┘  └──────────────┘  └───────────────────────┘  │   │
│  └────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         Loaders                                      │   │
│  │   ┌────────┐  ┌──────────┐  ┌───────────┐  ┌─────────────────────┐ │   │
│  │   │  YAML  │  │ OpenAPI  │  │   gRPC    │  │  Runtime (SDK)      │ │   │
│  │   │ config │  │ spec     │  │ reflection│  │  registration       │ │   │
│  │   └────┬───┘  └────┬─────┘  └─────┬─────┘  └──────────┬──────────┘│   │
│  │        │           │              │                    │           │   │
│  │        └───────────┴──────────────┴────────────────────┘           │   │
│  │                            │                                       │   │
│  │                    []ProxyToolDef                                   │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
                             │                      │
                             ▼                      ▼
                    ┌────────────────┐     ┌────────────────┐
                    │  HTTP/REST     │     │  gRPC          │
                    │  backends      │     │  backends      │
                    └────────────────┘     └────────────────┘
```

## Internal Representation: ProxyToolDef

All loaders produce the same type. This is the convergence point.

```go
// ProxyToolDef is a tool backed by an external service.
type ProxyToolDef struct {
    mcpkit.ToolDef                    // name, description, inputSchema, annotations

    Backend     BackendConfig         // how to reach the service
    Scope       *ScopeConstraints     // optional authorization constraints
}

// BackendConfig describes how the gateway calls the backend.
type BackendConfig struct {
    Kind        BackendKind           // "http" or "grpc"
    Endpoint    string                // "https://api.example.com" or "localhost:9090"
    Method      string                // "POST /users/{id}" or "user.v1.UserService/GetUser"

    // Request mapping: MCP tool arguments → backend request
    RequestMap  RequestMapping

    // Response mapping: backend response → MCP ToolResult
    ResponseMap ResponseMapping

    // Auth: how to authenticate with the backend
    AuthForward AuthForwardConfig

    // Timeouts
    Timeout     time.Duration         // per-call timeout (default 30s)
}

type BackendKind string
const (
    BackendHTTP BackendKind = "http"
    BackendGRPC BackendKind = "grpc"
)
```

### Request Mapping

```go
type RequestMapping struct {
    // Body determines how tool arguments become the request body.
    // "full" (default): entire arguments JSON is the body.
    // "field:<name>": a single field from arguments is the body.
    // "template": use BodyTemplate.
    Body        string

    // BodyTemplate is a Go template for custom body construction.
    // Available: {{.Args}}, {{.Args.fieldName}}, {{.Claims.Subject}}
    BodyTemplate string

    // PathParams maps tool argument fields to URL path parameters.
    // e.g., {"id": "user_id"} means /users/{id} is filled from args.user_id
    PathParams  map[string]string

    // QueryParams maps tool argument fields to query parameters.
    QueryParams map[string]string

    // Headers are static or templated headers added to the backend request.
    Headers     map[string]string
}
```

### Response Mapping

```go
type ResponseMapping struct {
    // Mode determines how the backend response becomes a ToolResult.
    // "text" (default): response body as text content.
    // "json": pretty-printed JSON as text content.
    // "field:<path>": extract a specific JSON field (dot-notation).
    // "template": use ResponseTemplate.
    Mode             string

    // ResponseTemplate is a Go template for custom response formatting.
    ResponseTemplate string

    // ErrorField is the JSON path to check for backend-level errors.
    // If present and non-null, the ToolResult is marked isError.
    ErrorField       string
}
```

### Auth Forwarding

```go
type AuthForwardConfig struct {
    // Mode determines how the gateway authenticates with the backend.
    Mode AuthForwardMode

    // Static token (for mode=static)
    Token string

    // Header name override (default: Authorization)
    Header string

    // Token exchange config (for mode=token_exchange)
    Exchange *TokenExchangeConfig
}

type AuthForwardMode string
const (
    AuthForwardNone          AuthForwardMode = "none"
    AuthForwardPassThrough   AuthForwardMode = "pass_through"   // forward MCP client's token
    AuthForwardStatic        AuthForwardMode = "static"         // fixed token/API key
    AuthForwardTokenExchange AuthForwardMode = "token_exchange"  // exchange for backend-scoped token
)

type TokenExchangeConfig struct {
    TokenEndpoint string
    ClientID      string
    ClientSecret  string
    Scopes        []string
    Audience      string   // backend's expected audience
}
```

## Security Model

### Trust Boundaries

```
                    UNTRUSTED                    TRUSTED                      SEMI-TRUSTED
               ┌─────────────────┐     ┌──────────────────────┐     ┌──────────────────────┐
               │   MCP Client    │     │    MCPKit Gateway     │     │   Backend Services   │
               │                 │     │                       │     │                       │
               │ • May be        │     │ • Root of trust       │     │ • Don't speak MCP     │
               │   compromised   │────>│ • Validates all auth  │────>│ • Trust gateway       │
               │   (prompt       │ TLS │ • Enforces scopes     │mTLS│   (or validate        │
               │    injection)   │     │ • Signs audit log     │    │    forwarded identity) │
               │ • Ephemeral     │     │ • Translates protocol │     │ • May be third-party   │
               │   identity      │     │                       │     │                       │
               └─────────────────┘     └──────────────────────┘     └──────────────────────┘
```

### Auth Layers (Progressive Enhancement)

```
Layer 0: No auth (dev/local)
  └── Gateway accepts all MCP calls, proxies directly

Layer 1: Bearer token (simple)
  └── mcpkit.WithBearerToken("secret")
  └── Existing MCPKit functionality, unchanged

Layer 2: JWT/OIDC (enterprise)
  └── mcpkit/auth JWTValidator + external IdP
  └── Claims propagated to scope evaluator and audit log
  └── Federated trust: multiple gateways share IdP

Layer 3: Capability scopes (fine-grained)
  └── SMCP-compatible Security Scopes
  └── Tool-level ACLs with argument constraints
  └── Deny-by-default evaluation

Layer 4: SMCP envelope (non-repudiation)
  └── Optional Ed25519 signature validation
  └── Cryptographic audit trail
  └── Attestation handshake for workload identity
```

### Security Scopes (SMCP-Compatible)

Adopt SMCP's Security Scope structure as the authorization model. Defined in the gateway config, evaluated on every tool call.

```yaml
scopes:
  read-only-research:
    description: "Safe read-only access to search and retrieval tools"
    capabilities:
      - tool_pattern: "search_*"
        constraints:
          domain_allowlist: ["*.internal.corp"]
          rate_limit: { calls: 30, per_seconds: 60 }
      - tool_pattern: "get_*"
        constraints:
          rate_limit: { calls: 60, per_seconds: 60 }
    deny_list: ["delete_*", "admin_*"]

  code-assistant:
    description: "Read/write source code, run build tools"
    capabilities:
      - tool_pattern: "*"
        constraints:
          rate_limit: { calls: 100, per_seconds: 60 }
    deny_list: ["admin_*", "deploy_*"]
```

```go
// ScopeConstraints is an SMCP-compatible capability definition.
type ScopeConstraints struct {
    ToolPattern     string            // glob pattern, e.g. "get_*", "user.v1.*"
    PathAllowlist   []string          // restrict file/URL path arguments
    CommandAllowlist []string         // restrict command arguments
    DomainAllowlist []string          // restrict domain arguments
    RateLimit       *RateLimit        // per-tool rate limiting
    MaxResponseSize int               // bytes
}

type RateLimit struct {
    Calls      int
    PerSeconds int
}

// ScopeEvaluator checks whether a tool call is allowed under the client's scope.
type ScopeEvaluator interface {
    Evaluate(claims *mcpkit.Claims, toolName string, args json.RawMessage) error
}
```

### Scope Evaluation Flow

```
MCP Client                  Gateway Auth               Scope Evaluator            Proxy Dispatch
  │                             │                           │                          │
  │  tools/call "delete_user"   │                           │                          │
  │  Authorization: Bearer <jwt>│                           │                          │
  │────────────────────────────>│                           │                          │
  │                             │  Validate JWT             │                          │
  │                             │  Extract claims:          │                          │
  │                             │    sub: "agent-7"         │                          │
  │                             │    scope: "read-only-     │                          │
  │                             │            research"      │                          │
  │                             │                           │                          │
  │                             │  Evaluate("read-only-     │                          │
  │                             │    research",             │                          │
  │                             │    "delete_user", args)   │                          │
  │                             │──────────────────────────>│                          │
  │                             │                           │                          │
  │                             │                           │  1. Check deny_list:     │
  │                             │                           │     "delete_*" matches   │
  │                             │                           │     → DENY               │
  │                             │                           │                          │
  │                             │  PolicyDenied{            │                          │
  │                             │    tool: "delete_user",   │                          │
  │                             │    reason: "denied by     │                          │
  │                             │     deny_list pattern     │                          │
  │                             │     'delete_*'"}          │                          │
  │                             │<──────────────────────────│                          │
  │                             │                           │                          │
  │  JSON-RPC error             │                           │                          │
  │  {code: -32600,             │                           │                          │
  │   message: "policy denied"} │                           │                          │
  │<────────────────────────────│                           │                          │
```

### Audit Trail

```go
// AuditEvent is emitted for every tool call (allowed or denied).
type AuditEvent struct {
    Timestamp   time.Time       `json:"ts"`
    RequestID   string          `json:"request_id"`
    ClientID    string          `json:"client_id"`    // from Claims.Subject
    Tool        string          `json:"tool"`
    Arguments   json.RawMessage `json:"arguments"`    // redactable
    Scope       string          `json:"scope"`
    Action      string          `json:"action"`       // "allowed", "denied", "error"
    Reason      string          `json:"reason,omitempty"`
    Backend     string          `json:"backend,omitempty"`     // which backend was called
    BackendCode int             `json:"backend_code,omitempty"` // HTTP status / gRPC code
    Latency     time.Duration   `json:"latency_ms"`
    Signature   string          `json:"signature,omitempty"`   // SMCP: client's Ed25519 sig
}

// AuditSink receives audit events. Implementations: stdout, file, SIEM, etc.
type AuditSink interface {
    Emit(ctx context.Context, event AuditEvent) error
}
```

## Loaders

### Loader Interface

```go
// Loader produces proxy tool definitions from a service description.
type Loader interface {
    Load(ctx context.Context) ([]ProxyToolDef, error)
}
```

### YAML Loader (Phase 1)

Reads a config file. No discovery, no codegen. Immediate value.

```yaml
# gateway.yaml
server:
  name: "my-gateway"
  version: "1.0.0"
  port: 8787
  transport: streamable  # or "sse", "both"

auth:
  # Layer 1: simple bearer
  bearer_token: "${MCP_TOKEN}"

  # Layer 2: JWT/OIDC (optional, overrides bearer)
  # jwt:
  #   jwks_url: "https://auth.corp.com/.well-known/jwks.json"
  #   issuer: "https://auth.corp.com"
  #   audience: "https://gateway.corp.com"

scopes:
  default:
    capabilities:
      - tool_pattern: "*"
        constraints:
          rate_limit: { calls: 60, per_seconds: 60 }

tools:
  - name: get_user
    description: "Fetch a user by ID"
    inputSchema:
      type: object
      properties:
        user_id:
          type: string
          description: "The user's unique identifier"
      required: [user_id]
    backend:
      kind: http
      endpoint: https://user-service.internal
      method: "GET /api/users/{user_id}"
      request:
        path_params:
          user_id: user_id    # tool arg → URL path param
      response:
        mode: json
      auth_forward:
        mode: pass_through
      timeout: 5s

  - name: create_order
    description: "Create a new order"
    inputSchema:
      type: object
      properties:
        item: { type: string }
        quantity: { type: integer }
      required: [item, quantity]
    backend:
      kind: grpc
      endpoint: order-service.internal:9090
      method: "order.v1.OrderService/CreateOrder"
      auth_forward:
        mode: static
        token: "${ORDER_SERVICE_TOKEN}"
```

### OpenAPI Loader (Phase 2)

Auto-generates tool definitions from an OpenAPI spec. Each operation becomes a tool.

```yaml
sources:
  - type: openapi
    spec: https://petstore.swagger.io/v3/openapi.json
    # Or a local file:
    # spec: ./specs/petstore.yaml

    # Filter which operations to expose
    include:
      - "GET /pets/{petId}"
      - "POST /pets"
    exclude:
      - "/admin/*"

    # Tool naming strategy
    naming: operation_id      # uses operationId from spec (default)
    # naming: method_path     # e.g., "get_pets_petid"

    # Override backend endpoint (if different from spec's servers[0])
    endpoint: https://petstore.internal

    # Auth for the backend
    auth_forward:
      mode: static
      token: "${PETSTORE_API_KEY}"
      header: "X-API-Key"
```

**Mapping rules:**

| OpenAPI concept | MCP tool mapping |
|---|---|
| `operationId` | Tool name (snake_cased) |
| `summary` / `description` | Tool description |
| Path params + query params + request body schema | Tool `inputSchema` (merged object) |
| `200` response schema | Response mapping |
| `tags[0]` | Tool annotation (`category`) |

### gRPC Loader (Phase 3)

Two sub-modes: reflection (no proto files needed) and proto file parsing.

```yaml
sources:
  - type: grpc
    endpoint: user-service.internal:9090

    # Option A: server reflection (zero config)
    reflection: true

    # Option B: proto files
    # proto_files: ["user/v1/user.proto"]
    # proto_path: ["./proto", "./third_party"]

    # Filter services/methods
    include:
      - "user.v1.UserService/*"
      - "order.v1.OrderService/GetOrder"
    exclude:
      - "*.Health/*"

    # Tool naming
    naming: method_name       # "GetUser" → "get_user" (default)
    # naming: full_method     # "user_v1_get_user"

    auth_forward:
      mode: token_exchange
      exchange:
        token_endpoint: "https://auth.corp.com/token"
        client_id: "gateway-service"
        client_secret: "${GATEWAY_CLIENT_SECRET}"
        audience: "user-service.internal"
```

**Mapping rules:**

| Proto concept | MCP tool mapping |
|---|---|
| RPC method name | Tool name (snake_cased) |
| Method comment / leading comment | Tool description |
| Request message descriptor | Tool `inputSchema` (JSON Schema from proto) |
| Response message | JSON-serialized as text content |
| Service name | Tool annotation (`service`) |

**gRPC reflection flow:**

```
Gateway startup                   gRPC Backend
  │                                    │
  │  grpc.reflection.v1.ServerReflection │
  │  ListServices()                    │
  │───────────────────────────────────>│
  │  ["user.v1.UserService", ...]      │
  │<───────────────────────────────────│
  │                                    │
  │  FileDescriptorBySymbol(           │
  │    "user.v1.UserService")          │
  │───────────────────────────────────>│
  │  FileDescriptorProto (binary)      │
  │<───────────────────────────────────│
  │                                    │
  │  Parse descriptors:                │
  │  ├─ Extract methods                │
  │  ├─ Build JSON Schema from         │
  │  │  message descriptors            │
  │  └─ Register as ProxyToolDefs      │
  │                                    │
  │  (Ready to serve MCP clients)      │
```

### Runtime SDK Loader (Phase 4)

For services that want to self-register. Thin HTTP contract — no MCP awareness needed.

```
Backend Service                     Gateway
  │                                    │
  │  POST /gateway/register            │
  │  {                                 │
  │    "tools": [{                     │
  │      "name": "analyze",            │
  │      "description": "...",         │
  │      "inputSchema": {...},         │
  │      "backend": {                  │
  │        "kind": "http",             │
  │        "endpoint": "http://me:8080"│
  │        "method": "POST /analyze"   │
  │      }                             │
  │    }]                              │
  │  }                                 │
  │───────────────────────────────────>│
  │                                    │  Register tools
  │  200 OK                            │  Send tools/list_changed
  │<───────────────────────────────────│  notification to MCP clients
  │                                    │
  │  (service health checks)           │
  │<──────────────── GET /health ──────│
```

## Proxy Dispatch: End-to-End Flow

### HTTP Backend

```
MCP Client                Gateway                         HTTP Backend
  │                          │                                 │
  │  tools/call              │                                 │
  │  name: "get_user"        │                                 │
  │  args: {"user_id":"123"} │                                 │
  │─────────────────────────>│                                 │
  │                          │                                 │
  │                          │  1. Auth check (JWT/bearer)     │
  │                          │  2. Scope evaluation            │
  │                          │  3. Audit log (pre-call)        │
  │                          │                                 │
  │                          │  4. Build HTTP request:         │
  │                          │     GET /api/users/123          │
  │                          │     Authorization: Bearer <fwd> │
  │                          │────────────────────────────────>│
  │                          │                                 │
  │                          │     200 OK                      │
  │                          │     {"id":"123","name":"Alice"}  │
  │                          │<────────────────────────────────│
  │                          │                                 │
  │                          │  5. Response mapping → ToolResult│
  │                          │  6. Audit log (post-call)       │
  │                          │                                 │
  │  ToolResult:             │                                 │
  │  content: [{type:"text", │                                 │
  │    text: '{"id":"123",   │                                 │
  │            "name":"Alice" │                                 │
  │           }'}]            │                                 │
  │<─────────────────────────│                                 │
```

### gRPC Backend

```
MCP Client                Gateway                         gRPC Backend
  │                          │                                 │
  │  tools/call              │                                 │
  │  name: "create_order"    │                                 │
  │  args: {"item":"book",   │                                 │
  │         "quantity": 2}   │                                 │
  │─────────────────────────>│                                 │
  │                          │                                 │
  │                          │  1. Auth + scope + audit        │
  │                          │                                 │
  │                          │  2. Build gRPC request:         │
  │                          │     Marshal JSON args to proto  │
  │                          │     using dynamic message       │
  │                          │     (from file descriptor)      │
  │                          │                                 │
  │                          │  order.v1.OrderService/         │
  │                          │    CreateOrder                  │
  │                          │  {item:"book", quantity:2}      │
  │                          │────────────────────────────────>│
  │                          │                                 │
  │                          │  CreateOrderResponse            │
  │                          │  {order_id:"ord-456",           │
  │                          │   status:"created"}             │
  │                          │<────────────────────────────────│
  │                          │                                 │
  │                          │  3. Marshal proto → JSON        │
  │                          │  4. Wrap as ToolResult          │
  │                          │                                 │
  │  ToolResult:             │                                 │
  │  content: [{type:"text", │                                 │
  │    text: '{"order_id":   │                                 │
  │     "ord-456",...}'}]    │                                 │
  │<─────────────────────────│                                 │
```

## Competitive Comparison

| Capability | MCPKit Gateway | Kong AI MCP Proxy | IBM ContextForge | Envoy AI Gateway |
|---|---|---|---|---|
| **Deployment** | Single Go binary or library | Kong Gateway + plugin | Docker compose | Envoy + WASM filter |
| **Backend: REST/HTTP** | Yes (Phase 1) | Yes | Yes | Yes |
| **Backend: gRPC** | Yes (Phase 3) | No | Partial | Via Envoy filters |
| **OpenAPI auto-import** | Yes (Phase 2) | Yes (Kong services) | Yes | No |
| **gRPC reflection** | Yes (Phase 3) | No | No | No |
| **Embeddable as library** | Yes (Go) | No | No | No |
| **Native MCP tools** | Yes (coexist with proxied) | No | No | No |
| **Auth: Bearer** | Yes | Yes (Kong auth plugins) | Yes | Yes |
| **Auth: JWT/OIDC** | Yes (mcpkit/auth) | Yes (Kong + third-party) | Partial | Yes |
| **Auth: SMCP envelopes** | Yes (optional, Phase 3) | No | No | No |
| **Capability scopes** | Yes (SMCP-compatible) | ACL per-tool | Basic RBAC | No |
| **Audit trail** | Structured + crypto sig | Kong logging plugins | Basic logging | Access logs |
| **Hot reload** | Yes (Phase 4) | Yes | Yes | Yes |
| **Transport: SSE** | Yes | Yes | Yes | Yes |
| **Transport: Streamable HTTP** | Yes | Partial | Yes | No |
| **Transport: stdio** | Planned | No | No | No |
| **License** | Open source | Enterprise license | Apache 2.0 | Apache 2.0 |
| **Dependencies** | Go stdlib + grpc | Nginx + Lua + Kong | Node.js | Envoy + WASM |

### Our differentiators

1. **Lightweight** — single binary, no Nginx/Lua/Node.js runtime. Starts in milliseconds. Embeddable as a Go library.
2. **gRPC-native** — reflection-based discovery and proto→JSON Schema. No other MCP gateway does this.
3. **Hybrid native + proxied** — write some tools in Go, proxy others to existing services. Single server, single port.
4. **SMCP-aligned security** — progressive auth layers culminating in cryptographic non-repudiation. Not just ACLs.
5. **Protocol-complete** — full MCP spec: logging, progress, completions, pagination. Not just tools/call.

## SMCP Integration Path

The gateway is the natural enforcement point for SMCP. Integration is layered:

### Phase 2: Security Scopes (no crypto)

Evaluate scopes from JWT claims. Deny-by-default. Audit log.

### Phase 3: SMCP Envelope Validation (optional crypto)

```
MCP Client                       Gateway                            Backend
  │                                 │                                  │
  │  Security Envelope:             │                                  │
  │  {                              │                                  │
  │    protocol: "smcp/v1",         │                                  │
  │    security_token: "<JWT>",     │                                  │
  │    signature: "<Ed25519>",      │                                  │
  │    payload: {tools/call ...},   │                                  │
  │    timestamp: "2026-..."        │                                  │
  │  }                              │                                  │
  │────────────────────────────────>│                                  │
  │                                 │  1. Verify JWT signature         │
  │                                 │  2. Load client public key       │
  │                                 │  3. Verify Ed25519 signature     │
  │                                 │     over canonical message       │
  │                                 │  4. Check timestamp (30s window) │
  │                                 │  5. Evaluate Security Scope      │
  │                                 │  6. Store signature in audit log │
  │                                 │                                  │
  │                                 │  7. Unwrap: extract payload      │
  │                                 │     (standard MCP JSON-RPC)      │
  │                                 │                                  │
  │                                 │  8. Proxy dispatch (normal flow) │
  │                                 │  GET /api/users/123              │
  │                                 │─────────────────────────────────>│
  │                                 │  200 OK {"name":"Alice"}         │
  │                                 │<─────────────────────────────────│
  │                                 │                                  │
  │  ToolResult (standard MCP)      │                                  │
  │<────────────────────────────────│                                  │
```

**Key design choice**: SMCP is an auth middleware layer, not a separate code path. Clients that don't send SMCP envelopes use standard bearer/JWT auth. The proxy dispatch layer downstream is identical in both cases.

### Addressing Gateway Model Gaps

| Gap | Mitigation in Gateway |
|---|---|
| No non-repudiation | SMCP envelope middleware stores client signatures in audit log |
| Gateway compromise blast radius | Token exchange to backends; backends validate forwarded identity |
| No response integrity | Gateway response signing key; mTLS to backends |
| Can't bypass gateway | Native MCPKit servers coexist for latency-sensitive paths |
| No federated trust | External IdP (OIDC); gateway validates, never issues tokens |
| Bespoke scope format | SMCP-compatible Security Scope structure from day one |

## Configuration: Full Example

```yaml
# gateway.yaml — complete configuration

server:
  name: "acme-mcp-gateway"
  version: "2.0.0"
  port: 8787
  transport: both            # SSE + Streamable HTTP

auth:
  jwt:
    jwks_url: "https://auth.acme.com/.well-known/jwks.json"
    issuer: "https://auth.acme.com"
    audience: "https://mcp.acme.com"
  smcp:
    enabled: false           # Phase 3: flip to true
    require: false           # allow non-SMCP clients during migration

scopes:
  analyst:
    description: "Read-only data access for analysis agents"
    capabilities:
      - tool_pattern: "get_*"
        constraints:
          rate_limit: { calls: 100, per_seconds: 60 }
      - tool_pattern: "search_*"
        constraints:
          domain_allowlist: ["*.acme.internal"]
          rate_limit: { calls: 30, per_seconds: 60 }
    deny_list: ["delete_*", "admin_*", "create_*", "update_*"]

  developer:
    description: "Full CRUD for development agents"
    capabilities:
      - tool_pattern: "*"
        constraints:
          rate_limit: { calls: 200, per_seconds: 60 }
    deny_list: ["admin_*"]

  admin:
    description: "Unrestricted (human oversight required)"
    capabilities:
      - tool_pattern: "*"

audit:
  sink: stdout               # or "file", "siem"
  # file:
  #   path: /var/log/mcpkit-gateway/audit.jsonl
  redact_arguments: false    # set true to omit args from logs

# Inline tool definitions
tools:
  - name: get_user
    description: "Fetch user profile by ID"
    inputSchema:
      type: object
      properties:
        user_id: { type: string, description: "User ID" }
      required: [user_id]
    backend:
      kind: http
      endpoint: https://user-service.acme.internal
      method: "GET /api/v2/users/{user_id}"
      request:
        path_params: { user_id: user_id }
      response:
        mode: json
      auth_forward:
        mode: token_exchange
        exchange:
          token_endpoint: "https://auth.acme.com/token"
          client_id: "mcp-gateway"
          client_secret: "${GATEWAY_SECRET}"
          audience: "user-service"
      timeout: 5s

# Import from service descriptions
sources:
  - type: openapi
    spec: https://order-service.acme.internal/openapi.json
    include: ["POST /orders", "GET /orders/{id}"]
    naming: operation_id
    auth_forward:
      mode: pass_through

  - type: grpc
    endpoint: analytics-service.acme.internal:9090
    reflection: true
    include: ["analytics.v1.AnalyticsService/*"]
    exclude: ["*.Health/*"]
    auth_forward:
      mode: token_exchange
      exchange:
        token_endpoint: "https://auth.acme.com/token"
        client_id: "mcp-gateway"
        client_secret: "${GATEWAY_SECRET}"
        audience: "analytics-service"
```

## Go API: Embeddable Usage

```go
package main

import (
    "github.com/panyam/mcpkit"
    "github.com/panyam/mcpkit/gateway"
)

func main() {
    // Load config
    cfg, _ := gateway.LoadConfig("gateway.yaml")

    // Create gateway (loads all tools from config + sources)
    gw, _ := gateway.New(cfg)

    // Optionally add native tools alongside proxied ones
    gw.RegisterNativeTool(mcpkit.ToolDef{
        Name:        "gateway_health",
        Description: "Check gateway status and loaded tools",
        InputSchema: map[string]any{"type": "object"},
    }, func(ctx context.Context, req mcpkit.ToolRequest) (mcpkit.ToolResult, error) {
        return mcpkit.TextResult(fmt.Sprintf("%d tools loaded", gw.ToolCount())), nil
    })

    // Create MCP server with gateway's tools
    srv := mcpkit.NewServer(
        mcpkit.ServerInfo{Name: cfg.Server.Name, Version: cfg.Server.Version},
        mcpkit.WithAuth(gw.AuthValidator()),
    )
    gw.RegisterAll(srv)

    // Serve
    srv.ListenAndServe(fmt.Sprintf(":%d", cfg.Server.Port),
        mcpkit.WithStreamableHTTP(true),
        mcpkit.WithSSE(true),
    )
}
```

## File Layout

```
mcpkit/
  gateway/                          # new sub-package (same module as mcpkit)
  ├── gateway.go                    # Gateway type, New(), RegisterAll(), ToolCount()
  ├── config.go                     # Config types, LoadConfig(), env var expansion
  ├── proxy.go                      # ProxyToolDef, BackendConfig, proxy ToolHandler
  ├── proxy_http.go                 # HTTP backend: build request, execute, map response
  ├── proxy_grpc.go                 # gRPC backend: dynamic message, invoke, marshal
  ├── request_map.go                # RequestMapping: args → path/query/body
  ├── response_map.go               # ResponseMapping: response → ToolResult
  ├── auth_forward.go               # AuthForwardConfig, token exchange
  ├── scope.go                      # ScopeConstraints, ScopeEvaluator, deny-by-default
  ├── audit.go                      # AuditEvent, AuditSink, stdout/file sinks
  ├── loader.go                     # Loader interface
  ├── loader_yaml.go                # YAML loader
  ├── loader_openapi.go             # OpenAPI loader
  ├── loader_grpc.go                # gRPC reflection + proto file loader
  ├── loader_sdk.go                 # Runtime registration endpoint
  ├── smcp.go                       # SMCP envelope validation middleware (Phase 3)
  ├── gateway_test.go               # Integration tests
  └── testdata/                     # Test configs, mock OpenAPI specs
  cmd/
    mcpkit-gateway/                 # Standalone binary
    └── main.go                     # Load config, create gateway, serve
```

## Implementation Phases

### Phase 1: Config-Driven HTTP Proxy

**Goal**: Ship a working gateway that proxies MCP tool calls to HTTP backends via YAML config.

- `gateway.go`, `config.go`, `proxy.go`, `proxy_http.go`
- `request_map.go`, `response_map.go`
- `auth_forward.go` (pass_through + static modes)
- `loader_yaml.go`
- `scope.go` (basic: tool-name deny list only, no argument constraints)
- `audit.go` (stdout sink)
- `cmd/mcpkit-gateway/main.go`
- Tests: e2e with httptest backends

### Phase 2: OpenAPI Loader + Full Scopes

**Goal**: Auto-generate tools from OpenAPI specs. Full SMCP-compatible scope evaluation.

- `loader_openapi.go` (parse OpenAPI 3.x, generate ProxyToolDefs)
- `scope.go` (full: argument constraints, rate limiting, domain/path allowlists)
- `auth_forward.go` (token_exchange mode)
- `audit.go` (file + SIEM sinks)

### Phase 3: gRPC Loader + SMCP

**Goal**: gRPC backend support. Optional SMCP envelope validation.

- `proxy_grpc.go` (dynamic message construction from descriptors)
- `loader_grpc.go` (reflection + proto file parsing)
- `smcp.go` (Ed25519 signature validation, attestation endpoint)
- `audit.go` (cryptographic signatures in audit events)

### Phase 4: Hot Reload + SDK Registration

**Goal**: Runtime service discovery. Config changes without restart.

- `loader_sdk.go` (registration HTTP endpoint)
- Config file watching + tool registry reload
- `tools/list_changed` notifications to connected MCP clients
- Health checks for registered backends

## Prior Art & References

- [Kong AI MCP Proxy](https://developer.konghq.com/plugins/ai-mcp-proxy/) — REST→MCP conversion via API gateway plugin
- [Kong: Autogenerate MCP tools from REST APIs](https://developer.konghq.com/mcp/autogenerate-mcp-tools/) — OpenAPI auto-import pattern
- [IBM ContextForge](https://ibm.github.io/mcp-context-forge/) — federated MCP/A2A/REST/gRPC registry and proxy
- [Envoy AI Gateway](https://aigateway.envoyproxy.io/blog/mcp-implementation/) — MCP proxy via Envoy extension
- [SMCP v1.0 RFC](https://github.com/orgs/modelcontextprotocol/discussions/689) — Security envelope, capability scopes, attestation
- [SEP-1763: Interceptors](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1763) — standardized middleware/sidecar pattern
- [Discussion #94: Proxy guidance](https://github.com/modelcontextprotocol/specification/discussions/94) — MCP aggregator patterns
- [Discussion #804: Gateway auth model](https://github.com/modelcontextprotocol/modelcontextprotocol/discussions/804) — gateway as identity-aware proxy
- [MCP 2026 Roadmap](http://blog.modelcontextprotocol.io/posts/2026-mcp-roadmap/) — enterprise concerns: audit, SSO, gateway behavior
- [MCP Spec 2025-11-25](https://modelcontextprotocol.io/specification/2025-11-25) — current protocol specification
- [protoc-gen-go-mcp](https://github.com/redpanda-data/protoc-gen-go-mcp) — proto→MCP codegen (compile-time, not runtime)
