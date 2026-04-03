# CLAUDE.md — MCPKit

## What This Is

Go library for building production-grade MCP servers. Handles transports (SSE + Streamable HTTP), protocol negotiation, session management, auth. Applications just register tools.

## Quick Commands

```bash
make test         # Unit tests (50 tests)
make testconf     # MCP conformance suite (needs Node.js)
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
| `dispatch.go` | JSON-RPC routing, Dispatcher, version negotiation, init gating |
| `server.go` | Server, options, Handler(), ListenAndServe(), transport config |
| `tool.go` | ToolDef, ToolRequest, ToolResult, Content types |
| `jsonrpc.go` | JSON-RPC 2.0 Request/Response/Error |
| `transport.go` | SSE transport (sseTransport, mcpSSEConn, SSEData) |
| `streamable_transport.go` | Streamable HTTP transport (streamableTransport) |
| `cmd/testserver/` | Test server with conformance tools |
| `conformance/baseline.yml` | Expected conformance failures — remove entries as features ship |

## Gotchas

- **SSE endpoint event data must be raw text**, not JSON-encoded. Use `SSEText(url)` not `SSEJSON()`. The `sseDataCodec` bypasses `json.Marshal` for this.
- **Per-session Dispatchers**: each SSE/Streamable connection gets its own `Dispatcher` via `newSession()`. Tool registry is shared by reference (read-only after startup). Session state (initialized, negotiatedVersion) is per-session.
- **`go.mod` must use published servicekit** (not local replace) — CI doesn't have the local source. Currently `v0.0.14`.
- **Conformance baseline**: when a feature passes its conformance test, remove it from `conformance/baseline.yml`. Stale entries cause CI failure.
- **SSE transport sessions** die with the connection (no TTL needed). **Streamable HTTP sessions** persist until DELETE or server restart.

## Architecture

See `ARCHITECTURE.md` for transport design, type definitions, and protocol details.

## Conformance Status

9/30 MCP conformance scenarios passing. Failing scenarios are tracked in `conformance/baseline.yml` with issue references. See `README.md` for testing instructions.

## What's Not Implemented Yet

- Resources capability (#13)
- Prompts capability (#14)
- stdio transport (#3)
- Logging (#19), Progress (#18), Cancellation (#16)
- Sampling (#22), Elicitation (#23)
- Streamable HTTP GET SSE stream (server-initiated notifications)
- mcpkit/auth sub-module (JWT/OIDC via oneauth)
