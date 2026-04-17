# RFC: MCP Service Bindings — Proto & OpenAPI Annotations for MCP Server Generation

**Status:** Proposal  
**Author:** Sri Panyam  
**Date:** 2026-04-13

---

## Problem

Teams with existing gRPC or REST (OpenAPI) services that want to expose functionality to LLM agents via MCP face a cold-start problem:

1. **Manual glue code** — each RPC/endpoint needs ~30-40 lines of hand-written MCP registration, handler wiring, and JSON Schema authoring.
2. **Schema drift** — the proto message (or OpenAPI schema) and the MCP `InputSchema` are maintained separately and diverge silently over time.
3. **Expertise barrier** — every team onboarding a service to MCP needs to understand MCP registration APIs, JSON Schema conventions, and transport wiring.

This is the same class of problem that `grpc-gateway` solved for REST and `protoc-gen-openapi` solved for documentation. The solution pattern is well-established: **annotate the IDL, generate the bridge**.

## Proposal

Define a **shared annotation specification** for marking existing service methods as MCP tools, resources, and prompts. Pair it with **per-runtime code generators** that read these annotations and emit idiomatic MCP server registrations.

### What gets standardized (shared)

- **`annotations.proto`** — proto extensions (`mcp_tool`, `mcp_resource`, `mcp_prompt`, `mcp_service`) with defined field semantics
- **OpenAPI extensions** — `x-mcp-tool`, `x-mcp-resource`, `x-mcp-prompt` with equivalent semantics for REST-first teams
- **Schema mapping rules** — how proto/OpenAPI types map to JSON Schema (scalars, well-known types, oneofs, enums, etc.)
- **Conformance test suite** — given this IDL + these annotations, the generated server MUST behave as specified

### What stays per-runtime (community-owned)

Each MCP SDK gets its own code generator, analogous to how `protoc-gen-go` and `protoc-gen-ts` both read the same `.proto` but emit different code:

| Generator | Target runtime | Community |
|-----------|---------------|-----------|
| `protoc-gen-go-mcp` | Go MCP SDKs | Go |
| `protoc-gen-ts-mcp` | TypeScript MCP SDK | TypeScript |
| `protoc-gen-py-mcp` | Python MCP SDK | Python |
| `protoc-gen-rs-mcp` | Rust MCP SDK | Rust |
| `openapi-gen-mcp` | Any (language-agnostic) | Cross-language |

## Annotation Surface

### Proto annotations

A service author annotates their existing proto — no new messages, no new services, just options on existing methods:

```proto
import "mcp/v1/annotations.proto";

service UserService {
  option (mcp.v1.mcp_service) = { namespace: "users" };

  // Exposed as MCP tool "users_get_user"
  rpc GetUser(GetUserRequest) returns (User) {
    option (mcp.v1.mcp_tool) = {
      description: "Retrieve a user by ID"
      structured_output: true
    };
  }

  // Exposed as MCP resource template "user://{user_id}/profile"
  rpc GetUserProfile(GetUserProfileRequest) returns (UserProfile) {
    option (mcp.v1.mcp_resource) = {
      uri_template: "user://{user_id}/profile"
      name: "User Profile"
      mime_type: "application/json"
    };
  }

  // Exposed as MCP prompt "users_summarize"
  rpc Summarize(SummarizeRequest) returns (SummarizeResponse) {
    option (mcp.v1.mcp_prompt) = {
      description: "Summarize a user's activity"
    };
  }
}
```

The generated code handles:
- JSON Schema derivation from request messages (`InputSchema`)
- Request unmarshaling (JSON → proto)
- Response marshaling (proto → MCP TextResult or StructuredResult)
- Registration with the MCP server

### OpenAPI extensions (equivalent semantics)

```yaml
paths:
  /users/{user_id}:
    get:
      x-mcp-tool:
        name: get_user
        description: Retrieve a user by ID
        structured_output: true
```

## What exists today

A working **proof-of-concept** in Go:

- **`annotations.proto`** — defines `mcp_tool`, `mcp_resource`, `mcp_prompt`, `mcp_service` extensions
- **`protoc-gen-go-mcp`** — generates typed MCP tool registrations from annotated protos
- **Three forwarding modes** from a single annotation:
  - In-process (service impl in same binary)
  - gRPC client forwarding (remote gRPC backend)
  - ConnectRPC client forwarding (remote Connect backend)
- **Proto → JSON Schema** derivation covering scalars, enums, nested messages, oneofs, maps, repeated fields, and well-known types (Timestamp, Duration, Struct, wrappers, Any, FieldMask)
- **Design document** covering annotation semantics, schema mapping rules, and multi-runtime strategy

Source: [github.com/panyam/mcpkit](https://github.com/panyam/mcpkit) (`ext/protogen/`)

## Open design questions

These are areas where community input would be especially valuable:

### 1. Completion annotations

MCP supports auto-complete for resource template parameters and prompt arguments. How should completions be annotated?

**Option A — field-level proto option:**
```proto
extend google.protobuf.FieldOptions {
  MCPCompletionOptions mcp_completion = 51004;
}
```

**Option B — reference a separate completion RPC:**
```proto
option (mcp.v1.mcp_tool) = {
  completion_rpc: "CompleteUserFields"
};
```

### 2. Streaming → progressive output

Server-streaming RPCs naturally map to MCP's `EmitContent()` (progressive tool output). Should this be automatic for any server-streaming method with `mcp_tool`, or require an explicit annotation field?

### 3. Prompt response mapping

An MCP prompt returns `PromptMessage[]`. How should a proto response message map to this?
- Convention: response must contain `repeated PromptMessage messages`?
- Single string field → single user message?
- Explicit annotation field specifying the mapping?

### 4. Field number registry

The current PoC uses field numbers 51001-51010 for proto extensions. If this becomes a community spec, should we request an allocation from the protobuf global extension registry?

### 5. OpenAPI parity

Should the OpenAPI `x-mcp-*` extensions be defined in the same spec, or as a companion document? They share semantics but the authoring experience is quite different.

## Proposed WG scope

| Workstream | Deliverable |
|---|---|
| **Annotation spec** | `annotations.proto` + OpenAPI extension definitions with formal semantics |
| **Schema mapping spec** | Proto/OpenAPI type → JSON Schema rules, formalized as a testable specification |
| **Conformance suite** | Test cases: given IDL + annotations, assert generated server behavior |
| **Reference generators** | At least Go + one other language to validate the spec is runtime-agnostic |
| **Spec proposal** | If the WG reaches consensus, propose as official MCP companion spec |

## What this is NOT

- **Not a protocol change** — generates standard MCP tools, resources, and prompts. No new capabilities or messages.
- **Not a single implementation** — the annotation spec is the shared artifact. Generators are community-maintained per runtime.
- **Not Go-specific** — the PoC is in Go, but the annotation proto and schema mapping rules are language-neutral.
- **Not a new framework** — annotate your existing services, don't rewrite them.

## Call to action

If you maintain an MCP SDK, have services you want to expose to agents, or have opinions on IDL-driven code generation — let's talk. The annotation spec is small enough to stabilize quickly, and each runtime community can build generators independently once the contract is agreed.

Interested? Reply here or DM. Looking for:
- Co-authors for the annotation spec (especially from non-Go runtimes)
- Early adopters willing to try the PoC on real services
- Feedback on the open design questions above
