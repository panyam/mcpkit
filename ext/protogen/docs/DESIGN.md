# MCP Protocol Bindings for gRPC/OpenAPI Services

**Status:** Draft  
**Authors:** Sri Panyam  
**Last updated:** 2026-04-13

## Problem

Teams with existing gRPC or OpenAPI services must hand-write MCP tool/resource/prompt registrations to expose their APIs to LLM agents. This creates:

1. **Schema drift** — proto message definitions and MCP InputSchema diverge silently
2. **Boilerplate** — each RPC needs ~30-40 lines of registration + handler glue
3. **Adoption friction** — every new service requires MCP expertise to onboard

## Solution

Annotation-driven code generation that produces MCP server (and client) bindings from existing API definitions. Annotate your proto (or OpenAPI spec), run codegen, get a fully typed MCP server that forwards to your existing service.

## Design Principles

### 1. Annotations are semantic, never transport

Proto annotations declare *what* to expose (tool name, description, schema) — never *how* to serve it. Transport (Streamable HTTP, SSE, stdio) is chosen at server startup, completely orthogonal to the annotations.

**Rationale:** A single proto service should work identically across all transports. Transport is a deployment concern, not an API design concern. This matches MCP's own transport-agnostic design.

### 2. Schemas are derived, not duplicated

`InputSchema` and `OutputSchema` are mechanically generated from proto message descriptors. The proto message *is* the schema — no manual JSON Schema authoring, no sync burden.

**Rationale:** Schema drift is the #1 source of bugs in hand-written MCP integrations. If the proto changes, the MCP schema changes automatically.

### 3. Configurable forwarding variants

A single annotation generates registration functions for selected variants, controlled by the `variants` CLI flag:

| Variant | Use case | Call pattern | Import |
|---------|----------|-------------|--------|
| **inprocess** | Service impl in same binary | `impl.Method(ctx, &req)` | none |
| **grpc** | Forward to remote gRPC server | `client.Method(ctx, &req, opts...)` | `google.golang.org/grpc` |
| **connect** | Forward to Connect server | `client.Method(ctx, connect.NewRequest(&req))` | `connectrpc.com/connect` |

Default: `inprocess,grpc`. Connect is opt-in via `--go-mcp_opt=variants=inprocess,grpc,connect`.

Use `variants=inprocess` for the lightest output — zero gRPC/Connect dependencies.

**Rationale:** The annotation describes the API contract. How you reach the backend is a wiring decision made at server construction time, not at proto authoring time. Unused variants add transitive dependencies that bloat go.sum.

### 4. Streaming RPCs map to progressive tool output

Server-streaming RPCs map to `core.EmitContent()` — each streamed response becomes a content chunk, with the final message as the authoritative result.

Client-streaming and bidirectional RPCs are not supported (MCP tools are request-response).

### 5. Business logic stays manual

The codegen generates the *transport bridge* — marshaling, schema, registration. Application-specific concerns (middleware, auth, dependency injection, error mapping) remain in hand-written server setup code.

**Rationale:** grpc-gateway follows the same pattern. The proto defines the mapping; the server author wires interceptors, middleware, and context enrichment.

### 6. Generated code targets mcpkit types directly

Generated code imports `github.com/panyam/mcpkit/core` and `github.com/panyam/mcpkit/server` — it produces `server.Tool`, `server.Resource`, `server.Prompt` structs. No intermediate abstraction layer.

**Rationale:** Tight coupling with the runtime catches breaking changes at compile time. Since the codegen lives in the mcpkit repo, version skew is impossible.

## Annotation Reference

### Service-level: `mcp_service`

```proto
service UserService {
  option (mcp.v1.mcp_service) = { namespace: "users" };
}
```

| Field | Type | Description |
|-------|------|-------------|
| `namespace` | string | Prefix for all tool/prompt names in this service. `namespace="users"` + tool `"get"` → `"users_get"`. |

### Method-level: `mcp_tool`

```proto
rpc GetUser(GetUserRequest) returns (User) {
  option (mcp.v1.mcp_tool) = {
    name: "get_user"
    description: "Retrieve a user by ID"
    timeout: "30s"
    structured_output: true
    result_summary: "User {name} retrieved (id: {id})"
  };
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | snake_case of method name | MCP tool name. Must match `[a-z][a-z0-9_]*`, max 64 chars. |
| `description` | string | Method's leading comment | Human-readable description for LLM consumption. |
| `timeout` | string | Server default | Per-tool execution timeout (Go duration format). Validated at generation time. |
| `structured_output` | bool | false | Emit response as `OutputSchema` + `StructuredResult`. |
| `result_summary` | string | — | Template for human-readable text content. Interpolates `{field}` from response JSON. Implies structured output. |

### Method-level: `mcp_resource`

```proto
rpc GetUserProfile(GetUserProfileRequest) returns (UserProfile) {
  option (mcp.v1.mcp_resource) = {
    uri_template: "user://{user_id}/profile"
    name: "User Profile"
    mime_type: "application/json"
    description: "A user's profile information"
  };
}
```

| Field | Type | Description |
|-------|------|-------------|
| `uri_template` | string | **Required.** Static URI or RFC 6570 URI template. Presence of `{param}` placeholders determines static vs. template resource. |
| `name` | string | Display name. |
| `mime_type` | string | Content MIME type. |
| `description` | string | Human-readable description. |

### Method-level: `mcp_prompt`

```proto
rpc SummarizeDocument(SummarizeRequest) returns (SummarizeResponse) {
  option (mcp.v1.mcp_prompt) = {
    name: "summarize"
    description: "Summarize a document given its content"
  };
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | snake_case of method name | MCP prompt name. |
| `description` | string | Method's leading comment | Human-readable description. |

**Prompt arguments** are auto-derived from the request message fields:
- Each field becomes a `PromptArgument` with `Name` = proto field name
- Non-optional scalar fields are `Required: true`
- Optional, repeated, map, and message fields are `Required: false`
- Field comments become the argument `Description`

## Schema Mapping

### Scalars

| Proto type | JSON Schema |
|-----------|-------------|
| `string` | `{"type": "string"}` |
| `bool` | `{"type": "boolean"}` |
| `int32`, `sint32`, `sfixed32`, `uint32`, `fixed32` | `{"type": "integer"}` |
| `int64`, `sint64`, `sfixed64`, `uint64`, `fixed64` | `{"type": "string", "format": "int64"}` |
| `float`, `double` | `{"type": "number"}` |
| `bytes` | `{"type": "string", "contentEncoding": "base64"}` |
| `enum` | `{"type": "string", "enum": [...values]}` |

### Composite types

| Proto type | JSON Schema |
|-----------|-------------|
| `repeated T` | `{"type": "array", "items": <T schema>}` |
| `map<K,V>` | `{"type": "object", "additionalProperties": <V schema>}` |
| nested `message` | Recursive object schema |
| `oneof` | `{"anyOf": [{"oneOf": [...alternatives]}]}` |

### Well-known types

| Proto type | JSON Schema |
|-----------|-------------|
| `google.protobuf.Timestamp` | `{"type": "string", "format": "date-time"}` |
| `google.protobuf.Duration` | `{"type": "string", "pattern": "^-?[0-9]+(\\.[0-9]+)?s$"}` |
| `google.protobuf.Struct` | `{"type": "object", "additionalProperties": true}` |
| `google.protobuf.Value` | `{"description": "Any JSON value"}` |
| `google.protobuf.ListValue` | `{"type": "array", "items": {}}` |
| `google.protobuf.Any` | `{"type": "object", "properties": {"@type": {"type": "string"}}, "required": ["@type"]}` |
| `google.protobuf.FieldMask` | `{"type": "string"}` |
| Wrapper types (`StringValue`, etc.) | Nullable scalar (e.g., `{"type": ["string", "null"]}`) |

### Required fields

Proto3 scalar fields are required by default. Fields are optional when:
- Marked with `optional` keyword
- Are `repeated` or `map` type
- Are message-typed (inherently optional in proto3)

## Generated Code Shape

For a service `UserService` with tool, resource, and prompt annotations:

```go
// user_service.pb.mcp.go — GENERATED, DO NOT EDIT

// --- Tools (always generated) ---
type UserServiceMCPServer interface { ... }
func RegisterUserServiceMCP(srv *server.Server, impl UserServiceMCPServer)

// --- Resources (only if mcp_resource methods exist) ---
type UserServiceMCPResourceServer interface { ... }
func RegisterUserServiceMCPResources(srv *server.Server, impl UserServiceMCPResourceServer)

// --- Prompts (only if mcp_prompt methods exist) ---
type UserServiceMCPPromptServer interface { ... }
func RegisterUserServiceMCPPrompts(srv *server.Server, impl UserServiceMCPPromptServer)

// --- gRPC forwarding (only with variants=grpc) ---
type UserServiceGRPCClient interface { ... }
func ForwardUserServiceToGRPC(srv *server.Server, client UserServiceGRPCClient)
type UserServiceResourceGRPCClient interface { ... }
func ForwardUserServiceResourcesToGRPC(srv *server.Server, client UserServiceResourceGRPCClient)
type UserServicePromptGRPCClient interface { ... }
func ForwardUserServicePromptsToGRPC(srv *server.Server, client UserServicePromptGRPCClient)

// --- ConnectRPC forwarding (only with variants=connect) ---
// Same pattern with Connect request/response wrappers.
```

Each function calls `srv.Register(...)` with `server.Tool`, `server.Resource`, or `server.Prompt` structs — standard mcpkit registration.

## CLI Options

```
protoc --go-mcp_out=. --go-mcp_opt=<options> service.proto
```

Or with buf:

```yaml
# buf.gen.yaml
plugins:
  - local: protoc-gen-go-mcp
    out: gen
    opt:
      - paths=source_relative
      - variants=inprocess       # only in-process, no grpc/connect deps
```

| Option | Default | Description |
|--------|---------|-------------|
| `package_suffix` | `` (empty) | Appended to the Go package name. Default empty generates into the same package as `pb.go`. Set to `mcp` for a separate sub-package. |
| `variants` | `inprocess,grpc` | Comma-separated list of registration variants. Valid: `inprocess`, `grpc`, `connect`. |

## gRPC Error Mapping

When a forwarded RPC returns a gRPC status error, `runtime.RPCError` extracts the status code, message, and any attached details (proto Any messages) and returns them as a `StructuredError`:

```json
{
  "code": "NotFound",
  "message": "user not found",
  "details": [...]
}
```

This lets LLM agents parse error details programmatically (e.g., version conflict recovery from an `ABORTED` status with conflict details).

## Future Work

- **OpenAPI ingestion** — `openapi-gen-mcp` reads OpenAPI 3.x specs with `x-mcp-tool` extensions
- **Client bindings** — typed MCP client stubs using `client.ToolCallTyped[T]`
- **Server-streaming** — `EmitContent()` mapping for progressive output
- **MCP Apps** — `mcp_app` annotation for `ext/ui.RegisterAppTool` generation
- **Prompt argument schemas** — derive `PromptArgument.Schema` from request message fields

## Multi-Runtime Strategy

The design separates **annotation definitions** (universal) from **code generators** (runtime-specific):

### Universal layer (shared across all MCP runtimes)
- `annotations.proto` — the proto extensions (`mcp_tool`, `mcp_resource`, `mcp_prompt`, `mcp_service`)
- Schema mapping rules — proto scalar to JSON Schema type, well-known types, etc.
- This DESIGN.md — the companion spec document

Any protoc plugin for any language/runtime reads the same annotations.

### Runtime-specific generators
Each target MCP SDK gets its own protoc plugin, analogous to how `protoc-gen-go` and `protoc-gen-ts` both read the same `.proto` but emit different code:

| Plugin | Target runtime | Emits |
|--------|---------------|-------|
| `protoc-gen-go-mcp` | mcpkit (this repo) | `server.Tool` structs, `runtime.BindProto` |
| `protoc-gen-go-mcp-sdk` | mark3labs/mcp-go | `mcp.Tool` + `mcpserver.AddTool` |
| `protoc-gen-ts-mcp` | TypeScript MCP SDK | Tool registration functions |
| `protoc-gen-rs-mcp` | Rust MCP SDK | Trait implementations |

The annotation proto is the **shared contract**. Generator backends are pluggable.

### Path to multi-runtime
1. **Now**: annotations.proto + mcpkit generator in one repo (tight feedback loop)
2. **Next**: extract annotations.proto to a standalone `mcp-proto-annotations` repo (or propose to MCP spec)
3. **Later**: community contributes generators for other runtimes

## Relationship to MCP Spec

This extension is **spec-compatible** — it generates standard MCP tools, resources, and prompts. No protocol changes required. The annotations are a DX layer that could be proposed as an official MCP companion spec for service integration.
