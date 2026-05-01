# SEP-2549 TTL for List Results — Conformance

Verifies that an MCP server correctly emits the optional `ttl` (seconds)
cache-freshness hint on every paginated list response, with the
three-state contract from [SEP-2549][sep-2549] preserved on the wire:

| State | Wire shape | Semantics |
|-------|-----------|-----------|
| **No guidance** | field absent | client falls back to `list_changed` or its own heuristics |
| **Do not cache** | `"ttl": 0` (explicit) | client SHOULD re-fetch every time |
| **Fresh for N** | `"ttl": <positive int>` | client SHOULD cache for N seconds |

[sep-2549]: https://github.com/modelcontextprotocol/specification/pull/2549

## Why three server processes?

The TTL config is a server-level option (`WithListTTL`); a single server
process can only demonstrate ONE of the three wire shapes. The conformance
suite spawns three independent servers — one per state — so all three wire
shapes flow through the actual server dispatcher in a single test run.

The Makefile target `testconf-list-ttl` handles spawn + tear-down for you;
manual runs need three `SERVER_URL_*` env vars.

## Server fixture

[`examples/list-ttl/`](../../examples/list-ttl/) registers one tool, one
resource, one resource template, and one prompt — just enough surface for
all four list endpoints to return a non-empty page. The `--ttl` flag
selects the mode:

| Flag | Mode |
|------|------|
| `--ttl 60` (or any positive value) | emit `"ttl": <value>` |
| `--ttl 0` | emit `"ttl": 0` |
| `--ttl -1` (or omitted) | emit no ttl field |

## Scenarios

| ID | What it tests | Server |
|----|---------------|--------|
| `list-ttl-01` | positive TTL surfaces on all four list endpoints | `--ttl 60` |
| `list-ttl-02` | explicit zero is preserved (not silently omitted) | `--ttl 0` |
| `list-ttl-03` | ttl is absent when no TTL is configured | `--ttl -1` |
| `list-ttl-04` | ttl coexists with the list payload arrays + cursor | `--ttl 60` |
| `list-ttl-05` | ttl wire type is JSON number (integer) | `--ttl 60` |

## Running

```bash
# from repo root — handles build + spawn × 3 + tear-down
make testconf-list-ttl

# or manually against three already-running servers
cd conformance && npm install
SERVER_URL_POSITIVE=http://localhost:18094/mcp \
SERVER_URL_ZERO=http://localhost:18095/mcp \
SERVER_URL_UNSET=http://localhost:18096/mcp \
  npx tsx --test list-ttl/scenarios.test.ts
```
