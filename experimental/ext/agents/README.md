# experimental/ext/agents

> **EXPERIMENTAL.** Tracks the agents-wg#22 Extensions Track SEP, "MCP Agent
> Discovery Extension", which has not merged. The wire shape may still change.
> Lives under `experimental/ext/` alongside events and protogen, and is promoted
> to `ext/agents` once the SEP merges.

Go library for **server-declared agent discovery**. A server groups its tools
into agents and advertises them as a compact roster, instead of handing a
supervisor host one flat `tools/list`. The host reads the roster, picks an
agent, and only then pulls that agent's instructions and scoped tool schemas.

## Progressive disclosure

Discovery is the only new wire surface. It comes in three levels:

| Level | Wire | Payload |
|-------|------|---------|
| 1 | `capabilities.extensions["io.mcpkit/agents"]` | "this server has agents" |
| 2 | `agents/list` | agent cards: `name`, `description`, `capabilities`. Paginated. **No tool schemas.** |
| 3 | `agents/get {name}` | one agent's `instructions`, optional `modelRoutingHint`, and its `tools[]` |

Both results carry `resultType: "complete"` and the SEP-2549 `ttlMs` /
`cacheScope` cache hints.

**Invocation is not new wire surface.** The host builds a local subagent from
the `agents/get` result and calls the scoped tools through the existing
`tools/call`.

The extension identifier stays `io.mcpkit/agents` until the implementation
fully matches the SEP. See `NOTES.md`.

## Server side

```go
import (
    "github.com/panyam/mcpkit/core"
    "github.com/panyam/mcpkit/server"
    "github.com/panyam/mcpkit/experimental/ext/agents"
)

srv := server.NewServer(info)
srv.RegisterTool(listFailedPipelines, handler) // register tools first

reg, err := agents.Register(agents.Config{
    Server: srv,
    Agents: []agents.AgentDef{{
        Name:             "workflow-agent",
        Description:      "Investigates delivery pipelines and approvals",
        Capabilities:     []string{"pipeline failures", "approval status"},
        // details, returned only by agents/get:
        Instructions:     "Handle delivery readiness and approval workflows.",
        ModelRoutingHint: &agents.ModelRoutingHint{Role: agents.ModelRoleMedium},
        Tools:            []string{"list_failed_pipelines"},
    }},
})
```

`Register` declares the extension and wires the `agents/list` and `agents/get`
handlers. The returned `Registry` supports runtime `AddAgent` and
`RemoveAgent`. `agents/list` preserves insertion order.

**Tools are names, not copies.** `agents/get` looks each name up in the
server's tool registry on every request and applies the same per-client
rewrite `tools/list` does, so every tool it returns matches that caller's
`tools/list` entry field for field, as the SEP requires.

**Errors.** `Register` and `AddAgent` return an advisory error for a duplicate
name (the first wins) or a tool name the server has not registered. On the
wire:

| Case | Code | `data.reason` |
|------|------|---------------|
| Unknown or hidden agent | `-32602` | `agent_not_found` |
| Agent names an unregistered tool | `-32603` | `invalid_agent_definition` |
| Empty name, malformed params, bad cursor | `-32602` | none |

### Config

| Field | Default | Effect |
|-------|---------|--------|
| `PageSize` | `0` | Agents per `agents/list` page. Zero returns the whole roster, matching mcpkit's core list methods. |
| `TTLMs` | nil | SEP-2549 freshness hint on both results. |
| `CacheScope` | `private` | SEP-2549 scope on both results. The SEP asks for `private` unless every caller gets the same response. |
| `Visible` | nil (all visible) | `func(ctx, name) bool`. A hidden agent is missing from `agents/list` and answers `agents/get` exactly as a nonexistent one does. |
| `TracerProvider` | no-op | SEP-414 spans, below. |

### Tracing (SEP-414)

Set `Config.TracerProvider` to opt the discovery handlers into spans:
`agents.list` (attribute `agents.count`) and `agents.get` (`mcp.agent.id`,
`agents.found`). Nil or `core.NoopTracerProvider{}` (the default) emits
nothing with zero allocation, and the extension depends only on the core
tracing abstraction, never on `ext/otel`. See `docs/SEP_414_OTEL.md`
§ "experimental/ext/agents discovery spans".

## Client side (`clients/go`)

```go
import agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"

ac := agentsclient.New(mcp) // over a connected *client.Client
if !ac.SupportsAgents() {
    return // no agents; fall back to plain tools/list
}
roster, _ := ac.ListAgents(ctx)           // level 2, walks every page
details, err := ac.GetAgent(ctx, chosen)  // level 3
if agentsclient.IsAgentNotFound(err) {
    // the roster changed; re-list and route again
}
```

`ListAgentsPage(ctx, cursor)` returns one page with its `nextCursor` and cache
hints, for hosts that cache the roster or page lazily.
`IsInvalidAgentDefinition(err)` classifies the server-misconfiguration case.

## Not yet implemented

Tracked in panyam/mcpkit#1393:

- `notifications/agents/list_changed` via `subscriptions/listen`. The filter key it needs can be registered since #1504.
- Step-up authorization for an agent the caller may know about but not use.
- Switching the identifier to `io.modelcontextprotocol/agents`.

## Tests

```bash
just -f experimental/justfile test-agents             # server library
just -f experimental/justfile test-agents-clients-go  # Go client SDK
```
