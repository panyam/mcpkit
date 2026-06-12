# Project-Wide Constraints

These apply across all packages. Package-specific constraints live in each package's own `CONSTRAINTS.md`.

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

**Verify:** the script below catches `require`/`replace` lines pointing at another extension after subtracting (a) the module's own path and (b) any ancestor of it. It walks every `go.mod` under `ext/` and `experimental/ext/`, including nested submodules. Must print nothing.

```bash
bash -c '
for m in $(find ext experimental/ext -name go.mod); do
  mod=$(grep -E "^module " "$m" | awk "{print \$2}")
  refs=$(grep -E "github\.com/panyam/mcpkit/(ext|experimental/ext)/" "$m" \
    | grep -vE "^module " \
    | grep -oE "github\.com/panyam/mcpkit/(ext|experimental/ext)/[a-zA-Z0-9_/-]+" \
    | sort -u)
  for ref in $refs; do
    case "$mod" in
      "$ref"|"$ref"/*) continue;;
    esac
    echo "VIOLATION $m: $ref"
  done
done
'
```
