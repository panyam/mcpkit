# CLAUDE.md — MCPKit

## What This Is

Go library for building production-grade MCP servers and clients. Three packages (`core/`, `server/`, `client/`) plus sub-modules (`ext/auth/`, `ext/ui/`, `ext/protogen/`).

## Quick Commands

```bash
make test             # Core tests (core/server/client/testutil)
make test-auth        # ext/auth sub-module
make test-ui          # ext/ui sub-module
make test-protogen    # ext/protogen sub-module
make test-e2e         # E2E tests (auth + apps)
make testconf         # MCP conformance suite (needs Node.js)
make testconfauth     # Auth conformance (client OAuth)
make testall          # Everything (9 stages) + Keycloak + HTML report
make audit            # govulncheck + gosec + gitleaks + race
make smoke            # Curl-based transport tests
make testkcl          # Keycloak interop (needs Docker)
make upkcl / downkcl  # Keycloak container lifecycle
make tag-push V=vX.Y.Z  # Tag root + all sub-modules and push
make bump-root V=vX.Y.Z # Update mcpkit version in all sub-module go.mods
```

### ext/ui commands
```bash
cd ext/ui
make test             # Go tests + vitest bridge JS tests
make test-bridge      # Bridge JS unit tests only (vitest + jsdom)
make test-bridge-e2e  # Playwright fake-host integration tests
make build-bridge     # Compile mcp-app-bridge.ts → .js (requires pnpm)
```

### ext/protogen commands
```bash
cd ext/protogen
make build            # Build protoc-gen-go-mcp plugin
make install          # Install plugin to $GOPATH/bin
make test             # Unit tests
make test-e2e         # Regenerate bookservice example + run e2e tests
make lint             # buf lint proto files
make generate         # buf generate Go code from protos
make push             # Push proto module to buf.build/mcpkit/protogen
```

### protoc-gen-go-mcp options
- `package_suffix` — Go package suffix (default empty = same package as pb.go). Set to `mcp` for a separate sub-package.
- `variants=inprocess,grpc` — registration variants to emit (default). Add `connect` for ConnectRPC. Use `inprocess` alone for zero external deps.

## Package Layout

- **`core/`** — Protocol types, handler types (`ToolHandler`, `ResourceHandler`, `PromptHandler`), typed handler contexts (`ToolContext`, `ResourceContext`, `PromptContext`), session APIs
- **`server/`** — Server, Dispatcher, transports (SSE, Streamable HTTP, Stdio, InProcess), middleware, registry
- **`client/`** — Client, HTTP/Stdio/Command transports, reconnection, auth retry
- **`ext/auth/`** — Separate Go module: JWT validation, PRM, OAuth token sources. See `ext/auth/docs/DESIGN.md`
- **`ext/ui/`** — Separate Go module: MCP Apps extension + App Bridge (JS). See `docs/APPS_DESIGN.md`
  - `ext/ui/assets/` — TypeScript bridge source, compiled JS, `.d.ts`, vitest tests
  - `ext/ui/tests/playwright/` — Fake-host integration tests
- **`ext/protogen/`** — Separate Go module: proto annotation-driven MCP code generation. Annotations: `mcp_tool`, `mcp_resource`, `mcp_prompt`, `mcp_elicit`, `mcp_sampling`, `mcp_service`. Published to `buf.build/mcpkit/protogen`. See `ext/protogen/docs/DESIGN.md`
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
- **Typed (recommended)**: `server.TypedTool[In, Out](name, desc, handler)` / `server.TextTool[In](name, desc, handler)` — auto-derives InputSchema (and optionally OutputSchema) from Go struct tags via `invopop/jsonschema`. Zero schema drift. `TextTool[In]` is sugar for `TypedTool[In, string]`.
- **Explicit**: `srv.RegisterTool(def, handler)` / `srv.Register(server.Tool{Def, Handler})` — manual schema, full control. Used by protogen, dynamic tools, proxies.
- `ext/ui.RegisterAppTool(reg, cfg)` — registers tool + resource in one call, auto-detects template URIs. With `TemplateHandler`: auto-generates concrete fallback for hosts that don't substitute template vars. Without: manual hybrid path.
- Schema validation panics at registration for malformed schemas

### Sub-Modules
- `ext/auth/`, `ext/ui/`, and `ext/protogen/` have separate `go.mod` — `make test` does NOT cover them
- Release order: tag root → `make bump-root V=vX.Y.Z` → tag sub-modules. Use `make tag-push V=vX.Y.Z` to do it in one step. Don't retag published versions.
- `scripts/verify-submodule-deps.sh` catches `v0.0.0` placeholder bugs (wired into pre-push hook)
- `SUB_MODS_ALL` (tidy-all, bump-root) and `SUB_MODS_TO_TAG` (tag, tag-push) in Makefile — both must include ext/protogen
- Pre-push hook runs root + ext/auth + ext/ui + ext/protogen tests
- **New core deps propagate**: adding imports to `core/` (e.g., `uritemplate`) requires `make tidy-all` to update all sub-module `go.sum` files

## Gotchas

- **Import cycles**: `server/` white-box tests can't import `testutil` (circular). Use local helpers.
- **SSE endpoint data**: Must be raw text (`SSEText(url)`), not JSON-encoded.
- **Content cardinality**: Read path tolerates both single-object and array-form `content`. Write path emits spec-canonical form. See `core/cardinality.go`.
- **Ping before initialize**: Always handled regardless of init state.
- **Error codes**: App errors use -31xxx range, JSON-RPC reserved range is -32xxx.
- **`GH_TOKEN="$GH_PERSONAL_TOKEN"`**: Use personal token for GitHub operations (EMU account can't access personal repos).
- **Template URI detection**: Use `core.IsTemplateURI()` (RFC 6570 parsing), not `strings.Contains("{")`.
- **Sub-module go.sum drift**: Adding a new import in `core/` breaks sub-module builds until `make tidy-all` runs. Always run `make testall` after touching core imports.
- **Protogen templates**: Use embedded `.tmpl` files (`go:embed templates/*.tmpl`), not Go string constants. Enables syntax highlighting and avoids backtick escaping.
- **Protogen wire helpers**: `mcpv1/helpers.go` uses `protokit/wire` for raw proto extension decoding. When adding new annotation fields, update both the proto message AND the corresponding `Get*Options` helper in `helpers.go`.
- **No `</script>` in embeddable JS**: HTML parser closes `<script>` tags even inside JS comments. Never include literal `</script>` in JS that gets inlined via `go:embed` or templates. Use `<\/script>` if needed in strings.
- **JSON HTML escaping**: `core.MarshalJSON()` uses `SetEscapeHTML(false)` — JSON-RPC responses must NOT escape `<`/`>` to `\u003c`/`\u003e`. Go is the only language that does this by default; other SDKs (Node/Python) don't, and some hosts don't unescape before parsing HTML.
- **MCP App Bridge templates**: Use `html/template` with `template.JS` type for the bridge script (prevents escaping). Use `text/template` only if you control all inputs. See `ext/ui/bridge.go` for the `BridgeData` type.
- **Single `<script type="module">` for MCP Apps**: MCPJam and some hosts extract inline scripts and re-serve them. Use `type="module"` (not plain `<script>`) and prefer a single script block matching the upstream Vite-bundled pattern.
- **Request vs notification in bridge**: `openLink`, `downloadFile`, `sendMessage`, `callTool`, `readResource`, `updateModelContext`, `requestDisplayMode` are **requests** (host responds). Only `log`, `requestTeardown`, `size-changed`, `initialized` are **notifications** (fire-and-forget). Getting this wrong means the host silently ignores the call.

## Deeper Documentation

| Topic | Where |
|-------|-------|
| Architecture | `docs/ARCHITECTURE.md` |
| Auth design & spec compliance | `ext/auth/docs/DESIGN.md` |
| MCP Apps design | `docs/APPS_DESIGN.md` |
| MCP App Bridge | `ext/ui/bridge.go`, `ext/ui/assets/mcp-app-bridge.ts` |
| Protogen design | `ext/protogen/docs/DESIGN.md` |
| Competitive analysis (vs go-sdk) | `docs/COMPETITIVE_ANALYSIS.md` |
| Capabilities list | `CAPABILITIES.md` |
| Constraints | `core/CONSTRAINTS.md`, `server/CONSTRAINTS.md`, `client/CONSTRAINTS.md` |
| Conformance baseline | `conformance/baseline.yml` |

## Conformance Status

- Server: 30/30 scenarios passing
- Auth: 14/14 scenarios (210/210 checks)
- Apps: 21 conformance tests passing
- testall: 9/9 stages (unit+coverage, race, auth, ui, protogen, e2e, conformance, auth-conformance, keycloak)
