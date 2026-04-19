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
