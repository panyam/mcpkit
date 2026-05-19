# List Results TTL Migration Guide

This guide covers SEP-2549 (TTL for List Results), merged Final on the MCP
specification on 2026-05-15. The SEP adds two cache-control fields to result
objects, explains how mcpkit ships them, and walks through what server
authors need to know about cache scope and authorization.

> **Status:** SEP-2549 is merged Final. mcpkit first shipped an implementation
> of an earlier draft (commit `256c243`, 2026-04-30); the field renamed and
> grew a sibling during the spec's final review, so the pre-merge API is a
> breaking step away from what shipped here. See "Migrating from the
> pre-merge implementation" below.

## TL;DR

- `ttlMs` (integer milliseconds) and `cacheScope` (`"public"` / `"private"`)
  are added to five result types: `tools/list`, `prompts/list`,
  `resources/list`, `resources/templates/list`, and `resources/read`.
- Set both at server registration with `server.WithListCacheControl(ttlMs, scope)`
  for the four list endpoints and `server.WithReadResourceCacheControl(ttlMs, scope)`
  for `resources/read`.
- A `resources/read` handler MAY override either hint per-read by setting
  `core.ResourceResult.TTLMs` / `.CacheScope` on its return value.
- Clients read the hints off the typed envelope: `client.ListToolsPage` and
  its three siblings, plus `client.ReadResourceFull`.

## Wire fields

| Type | Go field | JSON tag | Meaning |
|---|---|---|---|
| `core.ToolsListResult` | `TTLMs *int` | `ttlMs,omitempty` | freshness window in ms |
| `core.PromptsListResult` | `TTLMs *int` | `ttlMs,omitempty` | freshness window in ms |
| `core.ResourcesListResult` | `TTLMs *int` | `ttlMs,omitempty` | freshness window in ms |
| `core.ResourceTemplatesListResult` | `TTLMs *int` | `ttlMs,omitempty` | freshness window in ms |
| `core.ResourceResult` (`resources/read`) | `TTLMs *int` | `ttlMs,omitempty` | freshness window in ms |
| all five | `CacheScope string` | `cacheScope,omitempty` | `"public"` or `"private"` |

### `ttlMs` semantics

- **absent** or **`0`** — the response is immediately stale; clients MAY
  re-fetch every time the result is needed. Per the merged spec an absent
  `ttlMs` is treated the same as `0`.
- **`> 0`** — the response is fresh for that many milliseconds from receipt.
- **negative** — clients ignore it and treat it as `0`.

mcpkit keeps `TTLMs` a `*int` even though absent and `0` are
client-equivalent: the pointer lets a server emit an explicit `"ttlMs": 0`
distinct from omitting the field. `core.CacheScopePublic` and
`core.CacheScopePrivate` are the two `cacheScope` constants.

### `cacheScope` semantics

`cacheScope` mirrors HTTP `Cache-Control: public` vs `private`:

- **`"public"`** — the response holds no caller-specific data; any client,
  shared gateway, or caching proxy MAY store it and serve it to any user.
- **`"private"`** — the response holds caller-specific data; a cache MAY be
  reused only within the same authorization context and MUST NOT be shared
  across access tokens.
- **absent** — clients default to `"public"`.

## Server API

```go
// All four list endpoints, ttlMs only:
srv := server.NewServer(info, server.WithListTTLMs(60000))

// All four list endpoints, ttlMs + cacheScope in one call:
srv := server.NewServer(info,
    server.WithListCacheControl(60000, core.CacheScopePublic))

// resources/read default (a read handler may override per-read):
srv := server.NewServer(info,
    server.WithReadResourceCacheControl(30000, core.CacheScopePrivate))
```

A resource or template handler sets the per-read override on its return
value; the `WithReadResourceCacheControl` default fills only the fields the
handler left unset:

```go
srv.RegisterResource(def, func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
    return core.ResourceResult{
        Contents:   contents,
        TTLMs:      core.IntPtr(5000),
        CacheScope: core.CacheScopePrivate,
    }, nil
})
```

## Client API

The plain `client.ListTools()` / `ReadResource()` helpers drop the response
envelope. To read the cache hints, use the typed helpers:

```go
page, _ := c.ListToolsPage("")        // also ListPromptsPage / ListResourcesPage / ListResourceTemplatesPage
if page.TTLMs != nil && *page.TTLMs > 0 {
    expiresAt := time.Now().Add(time.Duration(*page.TTLMs) * time.Millisecond)
    // cache page.Tools until expiresAt; key by access token if page.CacheScope == "private".
}

rr, _ := c.ReadResourceFull("file:///doc")  // typed core.ResourceResult with TTLMs / CacheScope
```

## Security implications

The spec's `caching.mdx` utility doc carries a Security Considerations
section. Two obligations fall on server authors:

1. **`cacheScope` MUST reflect intended visibility.** A `"public"` response
   may be served across authorization contexts even when it came from an
   authenticated endpoint — different access tokens can share the same
   cache entry. Marking a per-user tool list as `"public"` leaks one user's
   primitives to another. Any response whose contents differ per
   authorization context MUST be `"private"`.
2. **Per-primitive access controls are still required.** `cacheScope` is a
   hint to clients, not an enforcement mechanism. Servers MUST apply
   appropriate per-primitive access controls on every request and MUST NOT
   rely on `cacheScope` alone to prevent unauthorized access.

Worked example:

- A single-tenant Calendar server exposes the same tool set to every
  caller. Its `tools/list` response is `"public"`.
- A multi-tenant CRM filters the tool list by the caller's role, so
  different users see different tools. Its `tools/list` response MUST be
  `"private"`, and the server still authorizes every `tools/call`.

## What changed during the spec review cycle

mcpkit's pre-merge implementation tracked an earlier draft. The spec drifted
in three ways before merging Final:

- `ttl` (integer seconds) renamed to `ttlMs` (integer milliseconds). Driven
  by maintainer pushback on units-in-the-name; the SEP author applied it
  2026-05-07.
- `cacheScope` field added 2026-05-07. It was not in the original SEP.
- `resources/read` added to the coverage list mid-cycle (it originally
  covered only the four list endpoints).

A Security Implications section landed in `caching.mdx` on 2026-05-14.

## Migrating from the pre-merge implementation

If you adopted mcpkit's pre-merge SEP-2549 release (roughly 2026-04-30
onward), the rename is a breaking change. It is loud — old code stops
compiling rather than silently changing units.

| Pre-merge | Final |
|---|---|
| `server.WithListTTL(60)` (seconds) | `server.WithListTTLMs(60000)` (milliseconds) |
| `core.ToolsListResult.TTL` (`json:"ttl"`) | `.TTLMs` (`json:"ttlMs"`) |
| three-state model (`nil` / `&0` / `&N`) | two client behaviors: absent-or-`0` (stale) and `> 0` (fresh) |
| (no cache scope) | `CacheScope` field + `WithListCacheControl` |
| (no `resources/read` coverage) | `core.ResourceResult.TTLMs` / `.CacheScope` + `WithReadResourceCacheControl` |

`WithListTTL` was removed rather than reinterpreted: a renamed `WithListTTL`
that silently switched units would have turned `WithListTTL(60)` from "60
seconds" into "60 milliseconds" with no compile error. Grep your codebase
for `WithListTTL` and `.TTL` on list-result types, then multiply
second-valued arguments by 1000.

## References

- SEP-2549 spec PR: <https://github.com/modelcontextprotocol/specification/pull/2549>
- Caching utility doc: `docs/specification/draft/server/utilities/caching.mdx`
- Example: [`examples/list-ttl/`](../examples/list-ttl/)
