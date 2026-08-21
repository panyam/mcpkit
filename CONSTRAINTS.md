# Project-Wide Constraints

These apply across all packages. Package-specific constraints live in each package's own `CONSTRAINTS.md`.

## About the `Verify` lines

Every constraint carries one, and it must say **whether anything actually runs it**. C4's was a
bash block pasted into this file: correct, and never executed by anything, so the rule was
documented and unenforced for as long as it existed (#1277). A verifier nobody runs is a comment.

Current state, worth knowing before trusting one:

| Constraint | Enforced? |
|---|---|
| C4 | **CI gate** — `make check-ext-isolation`, run by `.github/workflows/test.yml` |
| C1, C2, C3 | manual `grep` recipes; nothing runs them |
| C5 | says so explicitly; no automated check exists |
| C6 | manual `grep` recipe |

Prefer a script in `scripts/` wired into CI over a snippet here. When a snippet is genuinely the
right weight, say plainly that it is manual so nobody mistakes it for a gate.

One lesson from making C4 real: **a gate's precision is a correctness property, not a nicety.** The
published C4 snippet matched every `go.mod` line mentioning another extension, which fired 16 times
on a clean tree once `agent/ext/` was in scope, because `replace` directives and `// indirect`
requires are not dependency edges. A check that cries wolf gets switched off, which returns the
constraint to being a paragraph by a longer route.

## C1: Typed contexts over raw context.Context

When passing domain-specific state through context, use typed context structs (e.g., `ToolContext`, `TaskContext`) instead of plain `context.Context` with `context.Value`. This gives type safety, discoverability, and IDE autocomplete.

Functions that receive a context should accept the most specific typed context they need, not `context.Context`.

**Verify:** `grep -rn 'ctx context.Context' core/ server/ experimental/ --include='*.go' | grep -v '_test.go' | grep -v 'func.*context.WithValue'` — new handler signatures should use typed contexts.

## C2: Consolidated entry structs over parallel maps

When multiple `map[string]X` fields in a struct share the same key space, consolidate into a single entry struct. For example, instead of:

```go
tasks   map[string]*taskEntry
results map[string]json.RawMessage
waiters map[string][]chan struct{}
```

Use:

```go
type taskEntry struct {
    info    core.TaskInfo
    result  json.RawMessage
    waiters []chan struct{}
}
tasks map[string]*taskEntry
```

This makes it easier to add fields later without scattering state across multiple data structures, and ensures consistency (no orphaned keys in one map but not another).

**Verify:** `grep -rn 'map\[string\]' --include='*.go' | grep -v '_test.go'` — check that structs with multiple same-keyed maps have been consolidated.

## C3: No package-level global mutable state

Don't use package-level `var` for mutable state that should be per-instance (e.g., `var activeTasks sync.Map`). Multiple servers in the same process would collide, and it's untestable.

Scope mutable state to the struct/instance created during registration. E.g., the `Register()` function should create a struct that both middleware and handlers close over.

**Verify:** `grep -rn 'var.*sync.Map\|var.*make(map' --include='*.go' | grep -v '_test.go' | grep -v 'func '` — package-level mutable maps should not exist.

## C4: No cross-extension dependencies unless SEP-mandated

Modules under `ext/` and `experimental/ext/` MUST NOT import each other (runtime OR test) unless the coupling is explicitly mandated by an SEP. Extensions are independent surfaces; each consumes only `core/` abstractions (e.g., `core.TracerProvider`, `core.Claims`).

The rule prevents two failure modes:

- **Hidden coupling cascade**: a single test-only import (e.g., `ext/otel` importing `ext/skills` for an e2e) silently inverts the layering. The adapter that other extensions consume now depends on one of those extensions, and version bumps become entangled.
- **Drive-by interop expectations**: when extension A imports extension B, the API of B is implicitly stabilized for A's benefit, even though no SEP says they must interoperate. Future B-only refactors break A.

Real-world example: `ext/otel` is the OTel SDK adapter implementing `core.TracerProvider`. Every extension that wants real spans imports it. If `ext/otel` were to import `ext/skills` (e.g., to ship an e2e test that exercises both), the layering inverts — `ext/skills` can no longer evolve without considering `ext/otel`'s test surface, and the adapter's go.sum drags in skills-specific deps.

Escape hatch for cross-extension e2e tests: put the test in a separate top-level module (e.g., `tests/<ext-a>_<ext-b>_e2e/`) that imports both. Keeps the cross-cut isolated from either extension's go.mod.

If a future SEP explicitly cross-cuts two extensions, document the SEP reference in the importing module's README so the coupling is auditable.

A cross-extension reference is a violation **only** when the referenced module is neither the importing module itself nor an ancestor of it. Nested intra-extension submodules (e.g., `experimental/ext/events/stores/redis` depending on its parent `experimental/ext/events`) are intentional and allowed.

**`agent/ext/` is in scope**, alongside `ext/` and `experimental/ext/`. Those modules all already depend on `agent/` and `agent/host`, which makes them less independent of each other by construction than `ext/*` modules are, and that is an argument about shared *ancestors* rather than a licence for siblings to couple. Both failure modes above are about module layering, and neither becomes benign because the tree sits under `agent/`. Membership is derived from the module path (any path segment named `ext`), so a new extension tree is covered when it appears rather than when someone remembers to add it.

**Only direct requires count.** A `replace` with no matching require is path resolution for a multi-module repo and changes no build, so it is not a dependency edge. An `// indirect` require is transitive: every `agent/ext/*` module pulls `ext/auth` and friends through `agent/host`, and treating that as a violation would report 16 non-problems and teach everyone to ignore the check.

**Verify:** `make check-ext-isolation`, run in CI by the `No cross-extension requires` step in `.github/workflows/test.yml`. The rationale for each rule lives in `scripts/check-ext-isolation.sh`'s header. Must exit 0.

## C5: Multi-replica notification delivery requires explicit Pattern B wiring

At N>1 (multiple server replicas), five notification surfaces silently break without explicit cross-replica relay wiring. Sessions connected to one replica miss notifications emitted on another:

- `notifications/tools/list_changed`
- `notifications/resources/list_changed`
- `notifications/prompts/list_changed`
- `notifications/resources/updated`
- `notifications/events/event`

Adopters deploying mcpkit at N>1 MUST either:

1. **Configure a `NotificationRelay`** for capability + subscription-shaped notifications (`server.WithNotificationRelay(redisstore.NewCapabilityBus(...))`) AND a `redisstore.Bus` for events. The reference wiring is in `docs/MULTI_REPLICA.md` § Configuration recipes.
2. **Use sticky sessions** so each client only ever hits one replica. The notifications stay broken cross-replica but a single client's experience is consistent.
3. **Document the limitation** if they use neither — adopters should not silently ship a broken setup expecting the notifications to work.

The full architecture (Pattern B, NotificationRelay seam, NotificationRelayReceiver routing, per-surface end-to-end flows, scenario walkthroughs) is in `docs/MULTI_REPLICA.md`. Issue 755 tracks the work.

**Verify:** there is no automated check today — the constraint is documented to prevent silent breakage, not enforced at build time. Adopters running N>1 should verify their wiring matches one of the recipes in `docs/MULTI_REPLICA.md` § Configuration recipes.

## C6: MCP server lifecycle is decoupled from the agent

The agent (`agent/host` and the surfaces built on it, e.g. `agent/surfaces/chat`) is a **pure MCP client**. It connects to servers by URL and does not own their process lifecycle: it MUST NOT spawn, supervise, restart, or kill the MCP servers it talks to. Bringing servers up and down is an operator/launcher concern — a `just servers-up` recipe, docker, systemd — not something the agent process does as a side effect of starting or stopping.

The rule prevents two failure modes:

- **Lifetime coupling**: if the launcher boots the servers as children of the agent (and traps-kills them on exit), restarting the chat kills the servers and vice versa. Decoupled, servers survive chat restarts — you can reconnect a fresh agent to already-running servers, which is also how every real MCP client (Claude Code, Cursor) treats remote servers.
- **Boot coupling**: one unreachable server should not take the whole agent down. The target is that the agent connects asynchronously and degrades per-server (a down server shows as failed/paused/needs-login), rather than fail-fast aborting boot.

The reference decoupling is `examples/agents/kitchen-sink`: `servers.sh` (`just servers-up` / `servers-down` / `servers`) owns the server processes; `run.sh` only *checks* the ports and points at `servers-up` if any are down — it never boots or kills them.

Sanctioned exception: the client's stdio transport (`client.CommandTransport`) owns the subprocess it speaks to — that is the standard, opt-in, per-connection ownership every MCP client has for `command`-style servers (the `.mcp.json` shape). It lives in `client/`, is chosen explicitly, and is not the host spawning servers behind the user's back. The host wiring for it (a `ServerConfig.command` surface) is a deferred follow-up, not a violation.

Second sanctioned exception, on the same reasoning: **an extension may spawn the subprocess it
owns.** `ext/lsp` starts a language server from `ServerSpec.Command`, and `agent/ext/exec` runs
the commands in `Config.Commands`. Both come from operator configuration and neither is reachable
from a tool argument, so no model, and no instruction injected into content a model read, can name
the process that starts. That property is what the exception rests on, and it is the whole reason
`exec` refuses to let the model compose a command line. An extension that took a binary path from a
tool argument would be a violation of this constraint whatever it called itself.

The corollary is that extension-owned subprocesses are outside whatever sandbox the exec extension
applies: a wrapper around a `ToolSource` never sees a process an extension spawned for itself, and
a convention asking every extension to route its spawns through a shared helper would be
enforcement in name only. Decided and recorded on issue 1312, with the rationale in
`experimental/agent/ext/exec/README.md`.

**Note:** the async graceful-degrade half of this constraint is a target, not yet implemented — `NewApp` today still connects synchronously and fail-fast. The enforced half is: the host does not manage server *processes*.

**Verify:** the host must not spawn or kill processes. Must print nothing:

```bash
grep -rn 'os/exec\|exec\.Command\|\.Process\b\|syscall\.\(Kill\|Exec\)\|StartProcess' agent/host/ --include='*.go' | grep -v '_test.go'
```
