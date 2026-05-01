# SEP-2549 List TTL — Cache-Freshness Hint on List Results

Demonstrates the optional `ttl` (seconds) hint that an MCP server attaches
to every paginated list response (`tools/list`, `prompts/list`,
`resources/list`, `resources/templates/list`). Clients use it to cache the
registered surface between `notifications/list_changed` instead of
re-fetching on every poll.

Spec: [SEP-2549](https://github.com/modelcontextprotocol/specification/pull/2549).

## Three-state contract

| State | Wire shape | Semantics |
|-------|-----------|-----------|
| **No guidance** | field absent | client falls back to `list_changed` or its own heuristics |
| **Do not cache** | `"ttl": 0` (explicit) | client SHOULD re-fetch every time the list is needed |
| **Fresh for N** | `"ttl": <positive int>` | client SHOULD cache for N seconds; invalidate on `list_changed` |

The pointer encoding (`*int` + `omitempty`) preserves all three states on
the wire — a naive `int` with `omitempty` would conflate absent with
`&0`.

## Quick Start

```bash
# Terminal 1 — start the MCP server (TTL=60s)
make serve

# Terminal 2 — scripted walkthrough
make demo

# Or for the interactive TUI:
go run . --tui
```

The walkthrough connects to the server, hits all four list endpoints via
the new typed client helpers (`ListToolsPage`, `ListPromptsPage`,
`ListResourcesPage`, `ListResourceTemplatesPage`), and prints the TTL it
sees. See [WALKTHROUGH.md](WALKTHROUGH.md) for the full sequence diagram
and step-by-step description.

## Other states

The default `make serve` runs with `WithListTTL(60)` (the "fresh for 60s"
state). To exercise the other two:

| Command | Mode |
|---------|------|
| `go run . --serve --ttl=0` | explicit "do not cache" — emits `"ttl": 0` |
| `go run . --serve --ttl=-1` | unset — `ttl` field omitted entirely |
| `go run . --serve` *(no `--ttl`)* | unset (same as `--ttl=-1`) |

The `=` form is required because Go's `flag` package treats `--ttl -1` as
`--ttl` followed by an unknown flag `-1` — Makefile recipes spawning this
fixture should always use `--ttl=N` (or omit the flag entirely for the
unset default).

## Conformance fixture

The same binary is reused as the fixture for the
[`conformance/list-ttl/`](../../conformance/list-ttl/) suite, which spawns
three independent processes (one per TTL state) in parallel so all three
wire shapes flow through a real dispatcher in a single test run. Run via
`make testconf-list-ttl` from the repo root.

## Where to look in the code

| What | Where |
|------|-------|
| Server option | [`server.WithListTTL`](../../server/server.go) |
| Wire types | [`core.ToolsListResult.TTL`](../../core/tool.go), `PromptsListResult` ([`core/prompt.go`](../../core/prompt.go)), `ResourcesListResult` / `ResourceTemplatesListResult` ([`core/resource.go`](../../core/resource.go)) |
| Client helpers | [`client.ListToolsPage`](../../client/iterators.go) and the three siblings |
| Conformance | [`conformance/list-ttl/scenarios.test.ts`](../../conformance/list-ttl/scenarios.test.ts) |
| SEP | [SEP-2549 spec PR](https://github.com/modelcontextprotocol/specification/pull/2549) |
