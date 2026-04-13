# CLAUDE.md — MCPKit

## What This Is

Go library for building production-grade MCP servers and clients. Three packages (`core/`, `server/`, `client/`) plus sub-modules (`ext/auth/`, `ext/ui/`, `ext/protogen/`).

## Quick Commands

```bash
make test             # Core tests (core/server/client/testutil)
make test-auth        # ext/auth sub-module
make test-ui          # ext/ui sub-module
make test-e2e         # E2E tests (auth + apps)
make testconf         # MCP conformance suite (needs Node.js)
make testconfauth     # Auth conformance (client OAuth)
make testall          # Everything + Keycloak + HTML report
make audit            # govulncheck + gosec + gitleaks + race
make smoke            # Curl-based transport tests
make testkcl          # Keycloak interop (needs Docker)
make upkcl / downkcl  # Keycloak container lifecycle
```

## Package Layout

- **`core/`** — Protocol types, handler types (`ToolHandler`, `ResourceHandler`, `PromptHandler`), typed handler contexts (`ToolContext`, `ResourceContext`, `PromptContext`), session APIs
- **`server/`** — Server, Dispatcher, transports (SSE, Streamable HTTP, Stdio, InProcess), middleware, registry
- **`client/`** — Client, HTTP/Stdio/Command transports, reconnection, auth retry
- **`ext/auth/`** — Separate Go module: JWT validation, PRM, OAuth token sources. See `ext/auth/docs/DESIGN.md`
- **`ext/ui/`** — Separate Go module: MCP Apps extension. See `docs/APPS_DESIGN.md`
- **`ext/protogen/`** — Separate Go module: proto annotation-driven MCP code generation. See `ext/protogen/docs/DESIGN.md`
- **`testutil/`** — `NewTestServer`, `ForAllTransports`, `TestClient`
- **`cmd/testserver/`** — Conformance test server
- **`tests/e2e/`, `tests/keycloak/`** — Separate Go modules with `replace` directives

## Key Patterns

### Typed Handler Contexts (#179)
Handlers receive typed contexts instead of `context.Context`:
```go
func myTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
    ctx.EmitLog(core.LogInfo, "tool", "starting")
    ctx.EmitProgress(req.ProgressToken, 50, 100, "halfway")
}
```
`BaseContext` → shared methods (EmitLog, Sample, Elicit, AuthClaims, Notify, etc.). `ToolContext` adds `EmitProgress`/`EmitContent`. Free functions (`core.EmitLog(ctx, ...)`) still work. Use `ctx.DetachFromClient()` not `core.DetachFromClient(ctx)` inside typed handlers.

### Registration
- Two-arg: `srv.RegisterTool(def, handler)` / Single-struct: `srv.Register(server.Tool{Def, Handler})`
- `ext/ui.RegisterAppTool(reg, cfg)` — registers tool + resource in one call, auto-detects template URIs. With `TemplateHandler`: auto-generates concrete fallback for hosts that don't substitute template vars. Without: manual hybrid path.
- Schema validation panics at registration for malformed schemas

### Sub-Modules
- `ext/auth/`, `ext/ui/`, and `ext/protogen/` have separate `go.mod` — `make test` does NOT cover them
- Release order: tag root → `make bump-root V=vX.Y.Z` → tag sub-modules (`ext/auth/vX.Y.Z`, `ext/ui/vX.Y.Z`, `ext/protogen/vX.Y.Z`). Don't retag published versions.
- `scripts/verify-submodule-deps.sh` catches `v0.0.0` placeholder bugs (wired into pre-push hook)
- `SUB_MODS_ALL` in Makefile lists all sub-modules for `tidy-all` and `bump-root`
- **New core deps propagate**: adding imports to `core/` (e.g., `uritemplate`) requires `go mod tidy` in every sub-module to update their `go.sum`

## Gotchas

- **Import cycles**: `server/` white-box tests can't import `testutil` (circular). Use local helpers.
- **SSE endpoint data**: Must be raw text (`SSEText(url)`), not JSON-encoded.
- **Content cardinality**: Read path tolerates both single-object and array-form `content`. Write path emits spec-canonical form. See `core/cardinality.go`.
- **Ping before initialize**: Always handled regardless of init state.
- **Error codes**: App errors use -31xxx range, JSON-RPC reserved range is -32xxx.
- **`GH_TOKEN="$GH_PERSONAL_TOKEN"`**: Use personal token for GitHub operations (EMU account can't access personal repos).
- **Template URI detection**: Use `core.IsTemplateURI()` (RFC 6570 parsing), not `strings.Contains("{")`.
- **Sub-module go.sum drift**: Adding a new import in `core/` breaks sub-module builds until `make tidy-all` runs. Always run `make testall` after touching core imports.

## Deeper Documentation

| Topic | Where |
|-------|-------|
| Architecture | `docs/ARCHITECTURE.md` |
| Auth design & spec compliance | `ext/auth/docs/DESIGN.md` |
| MCP Apps design | `docs/APPS_DESIGN.md` |
| Protogen design | `ext/protogen/docs/DESIGN.md` |
| Capabilities list | `CAPABILITIES.md` |
| Constraints | `core/CONSTRAINTS.md`, `server/CONSTRAINTS.md`, `client/CONSTRAINTS.md` |
| Conformance baseline | `conformance/baseline.yml` |

## Conformance Status

- Server: 30/30 scenarios passing
- Auth: 14/14 scenarios (210/210 checks)
- Apps: 21 conformance tests passing
