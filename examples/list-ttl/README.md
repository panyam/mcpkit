# SEP-2549 List TTL — Cache Hints on List and Read Results

> **Stable** — implements [SEP-2549](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549) (List TTL), merged into the MCP spec.

Demonstrates the SEP-2549 cache hints — `ttlMs` (integer milliseconds) and
`cacheScope` (`public`/`private`) — that an MCP server attaches to every
paginated list response (`tools/list`, `prompts/list`, `resources/list`,
`resources/templates/list`) and to `resources/read`. Clients use them to
cache the registered surface between `notifications/list_changed` instead
of re-fetching on every poll.

Spec: [SEP-2549](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549).
Migration notes: [docs/LIST_TTL_MIGRATION.md](../../docs/LIST_TTL_MIGRATION.md).

## ttlMs contract

| State | Wire shape | Semantics |
|-------|-----------|-----------|
| **Immediately stale** | field absent, or `"ttlMs": 0` | client MAY re-fetch every time the result is needed |
| **Fresh for N** | `"ttlMs": <positive int>` | client SHOULD cache for N milliseconds; invalidate on `list_changed` |

The merged spec treats an absent `ttlMs` the same as `0`. mcpkit still
encodes the field as `*int` + `omitempty` so a server can emit an explicit
`"ttlMs": 0` distinct from omitting it — clients behave identically either
way, but the explicit form states the intent on the wire.

## cacheScope contract

| Value | Wire shape | Semantics |
|-------|-----------|-----------|
| **Public** | `"cacheScope": "public"` | any client or shared proxy MAY cache and serve to any user |
| **Private** | `"cacheScope": "private"` | cache only within one authorization context; never share across access tokens |
| **Absent** | field omitted | clients default to `public` |

A server whose response varies per caller MUST set `private` explicitly —
see the security note in the migration guide.

## Quick Start

```bash
# Terminal 1 — start the MCP server (ttlMs=60000)
make serve

# Terminal 2 — scripted walkthrough
make demo

# Or for the interactive TUI:
go run . --tui
```

## What it demonstrates

- The `ttlMs` and `cacheScope` fields surfacing on every paginated list response and on `resources/read`.
- The `ttlMs` contract on the wire: absent / `"ttlMs": 0` (both immediately stale) and `"ttlMs": <positive>` (fresh for N ms).
- Server-side configuration via `server.WithListCacheControl(ttlMs, scope)` and `server.WithReadResourceCacheControl(ttlMs, scope)` — uniform across endpoints, with negative `ttlMs` omitting the field.
- Client-side typed helpers — `client.ListToolsPage`, `ListPromptsPage`, `ListResourcesPage`, `ListResourceTemplatesPage`, `ReadResourceFull` — that return the result envelope so callers can read `TTLMs` and `CacheScope`.
- Direct raw-JSON inspection to confirm the wire shape (`ttlMs` is a JSON number, `cacheScope` a string).

See [WALKTHROUGH.md](WALKTHROUGH.md) for the full sequence diagram and
step-by-step description.

## Other states

The default `make serve` runs with `ttlMs=60000` (the "fresh for 60s"
state). To exercise the other modes:

| Command | Mode |
|---------|------|
| `go run . --serve --ttl-ms=0` | immediately stale — emits `"ttlMs": 0` |
| `go run . --serve --ttl-ms=-1` | unset — `ttlMs` field omitted entirely |
| `go run . --serve` *(no `--ttl-ms`)* | unset (same as `--ttl-ms=-1`) |
| `go run . --serve --ttl-ms=60000 --cache-scope=private` | fresh for 60s, `cacheScope: private` |

The `=` form is required because Go's `flag` package treats `--ttl-ms -1`
as `--ttl-ms` followed by an unknown flag `-1` — Makefile recipes spawning
this fixture should always use `--ttl-ms=N` (or omit the flag entirely for
the unset default).

## Conformance fixture

The same binary is reused as the fixture for the SEP-2549 conformance
suite on the [panyam/mcpconformance `pending` branch](https://github.com/panyam/mcpconformance/tree/pending/src/scenarios/server/list-ttl).
The runner spawns three independent processes (one per `ttlMs` state) in
parallel so all the wire shapes flow through a real dispatcher in a single
test run. Drive it via `make testconf-list-ttl` from the repo root.

## Where to look in the code

| What | Where |
|------|-------|
| Server options | [`server.WithListTTLMs` / `WithListCacheControl` / `WithReadResourceCacheControl`](../../server/server.go) |
| Wire types | [`core.ToolsListResult`](../../core/tool.go), `PromptsListResult` ([`core/prompt.go`](../../core/prompt.go)), `ResourcesListResult` / `ResourceTemplatesListResult` / `ResourceResult` ([`core/resource.go`](../../core/resource.go)), `CacheScope*` constants ([`core/cache.go`](../../core/cache.go)) |
| Client helpers | [`client.ListToolsPage`](../../client/iterators.go) and siblings, [`client.ReadResourceFull`](../../client/client.go) |
| Migration guide | [`docs/LIST_TTL_MIGRATION.md`](../../docs/LIST_TTL_MIGRATION.md) |
| Conformance | [SEP-2549 scenarios on panyam/mcpconformance `pending`](https://github.com/panyam/mcpconformance/tree/pending/src/scenarios/server/list-ttl) — drive via `make testconf-list-ttl` |
| SEP | [SEP-2549 spec PR](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549) |

## Next steps

- [List TTL migration notes](../../docs/LIST_TTL_MIGRATION.md)
