# CLAUDE.md — MCPKit

## What This Is

Go library for building production-grade MCP servers and clients. Handles transports (SSE + Streamable HTTP), protocol negotiation, session management, auth. Applications register tools, resources, and prompts. Includes a Go MCP client for agents and testing.

## Quick Commands

```bash
make test         # Unit tests (160 tests)
make testconf     # MCP conformance suite (needs Node.js)
make testconfauth # MCP Auth conformance — client OAuth tests (needs mcpkit/auth)
make testall      # Both
make smoke        # Curl-based transport tests
make audit        # govulncheck + gosec + gitleaks + race detection
make serve        # Start SSE test server on :8787
make serve-streamable  # Streamable HTTP on :8787
make serve-both   # Both transports
```

## Key Files

| File | Purpose |
|------|---------|
| `dispatch.go` | JSON-RPC routing, Dispatcher, version negotiation, init gating, cancellation, logging/setLevel, completion/complete |
| `logging.go` | LogLevel, LogMessage, NotifyFunc, EmitLog, context-based notification delivery |
| `progress.go` | ProgressNotification, EmitProgress for long-running tool reporting |
| `completion.go` | CompletionRef, CompletionArgument, CompletionResult, CompletionHandler |
| `auth.go` | Claims, ClaimsProvider, TokenSource, Extension, Stability, ExtensionProvider, AuthClaims(), HasScope() |
| `server.go` | Server, options, Handler(), ListenAndServe(), transport config, CheckAuth, writeAuthError, RegisterExperimental* |
| `tool.go` | ToolDef (with Annotations), ToolRequest, ToolResult, Content types |
| `resource.go` | ResourceDef, ResourceTemplate, ResourceHandler types |
| `prompt.go` | PromptDef, PromptArgument, PromptHandler types |
| `pagination.go` | Generic cursor-based pagination helper |
| `jsonrpc.go` | JSON-RPC 2.0 Request/Response/Error |
| `transport.go` | SSE transport (sseTransport, mcpSSEConn, SSEData) |
| `streamable_transport.go` | Streamable HTTP transport (streamableTransport) |
| `client.go` | MCP client: Connect, ToolCall, ReadResource, ListTools, ListResources, WithClientBearerToken, WithTokenSource |
| `docs/AUTH_DESIGN.md` | MCP Auth architecture, sequence diagrams, extension system, oneauth integration map |
| `testutil/testclient.go` | TestClient: wraps Client + httptest.Server + testing.T for e2e tests |
| `cmd/testserver/` | Test server with conformance tools, resources, and prompts |
| `conformance/baseline.yml` | Expected conformance failures — remove entries as features ship |

## Gotchas

- **SSE endpoint event data must be raw text**, not JSON-encoded. Use `SSEText(url)` not `SSEJSON()`. The `sseDataCodec` bypasses `json.Marshal` for this.
- **Per-session Dispatchers**: each SSE/Streamable connection gets its own `Dispatcher` via `newSession()`. All registries (tools, resources, prompts) are shared by reference (read-only after startup). Session state is per-session.
- **`go.mod` must use published servicekit** (not local replace) — CI doesn't have the local source. Currently `v0.0.14`.
- **Conformance baseline**: when a feature passes its conformance test, remove it from `conformance/baseline.yml`. Stale entries cause CI failure.
- **SSE transport sessions** die with the connection (no TTL needed). **Streamable HTTP sessions** persist until DELETE or server restart.
- **Capabilities auto-advertise**: resources/prompts capabilities only appear in initialize response when resources/prompts are actually registered. Logging and completions are always advertised.
- **Server-to-client notifications** (logging, progress) work over both transports. SSE: pushed via hub. Streamable HTTP: POST response switches to SSE streaming (`Content-Type: text/event-stream`) when client sends `Accept: text/event-stream`. Falls back to synchronous JSON if client doesn't accept SSE.
- **Auth checks on ALL endpoints**: SSE `GET /sse`, Streamable HTTP `POST /mcp`, and Streamable `DELETE /mcp` all call `CheckAuth`. The SSE GET was previously unauthenticated — fixed in this auth work.
- **`auth/` is a separate Go module** with its own `go.mod`. Root `go test ./...` does NOT test it. Use `cd auth && go test ./...` or test explicitly. Uses `replace` directives for local oneauth.
- **Extension metadata in initialize**: extensions registered via `WithExtension` appear under `capabilities.extensions` in the initialize response, with `specVersion` and `stability`.
- **Auth spec is 2025-11-25**: See `docs/AUTH_DESIGN.md` for spec compliance checklist. Key: `resource` param (RFC 8707) is MUST, PKCE S256 is MUST, audience validation is MUST.

## Architecture

See `docs/ARCHITECTURE.md` for transport design, type definitions, and protocol details.

## Conformance Status

### Server conformance
24/30 MCP server conformance scenarios passing. Failing scenarios tracked in `conformance/baseline.yml` under `server:`.

### Auth conformance
12/14 required MCP auth conformance scenarios passing (150/153 checks). Remaining 2 blocked on #53 (client transport 401/403 step-up handling). Run via `make testconfauth`.

## What's Not Implemented Yet

- stdio transport (#3)
- Sampling (#22), Elicitation (#23)
- Resource subscriptions (#24)
- Streamable HTTP GET SSE stream (server-initiated notifications without a request)
- MCP Auth client OAuth flow — OAuthTokenSource needs `resource` param (blocked on oneauth #66), PKCE verification (oneauth #65)
- `cmd/testclient` — needed for `make testconfauth` to actually run auth conformance
