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
| 1 | `capabilities.extensions["io.modelcontextprotocol/agents"]` | "this server has agents" |
| 2 | `agents/list` | agent cards: `name`, `description`, `capabilities`. Paginated. **No tool schemas.** |
| 3 | `agents/get {name}` | one agent's `instructions`, optional `modelRoutingHint`, and its `tools[]` |

Both results carry `resultType: "complete"` and the SEP-2549 `ttlMs` /
`cacheScope` cache hints.

**Invocation is not new wire surface.** The host builds a local subagent from
the `agents/get` result and calls the scoped tools through the existing
`tools/call`.

The extension identifier is the SEP's own, `io.modelcontextprotocol/agents`.
It was `io.mcpkit/agents` until the implementation matched the SEP, and
`NOTES.md` explains why.

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

**Roster changes.** The extension declares `listChanged: true`. `AddAgent` and
`RemoveAgent` broadcast `notifications/agents/list_changed`, which reaches a
2026-07-28 client only on a `subscriptions/listen` stream that set
`agentsListChanged`. A no-op change sends nothing. When what callers see
changes some other way (the `Visible` policy, or a tool an agent names
appearing), call `NotifyListChanged`.

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
| `ResourceMetadataURL` | empty | RFC 9728 metadata URL put in a step-up challenge's `resource_metadata`, below. |
| `TracerProvider` | no-op | SEP-414 spans, below. |

### Step-up

An agent can be listed for a caller who may not open it yet. Set
`AgentDef.ScopeChallenge`, built the same way as on a tool:

```go
agents.AgentDef{
    Name:           "deploy-agent",
    Tools:          []string{"deploy_service"},
    ScopeChallenge: core.RequireScopes("ops:deploy"),
}
```

A caller without `ops:deploy` still sees `deploy-agent` in `agents/list`.
`agents/get` is refused before dispatch with an HTTP 403 and
`WWW-Authenticate: Bearer error="insufficient_scope", scope="ops:deploy"`, the
step-up the SEP allows for "a caller permitted to know an agent exists but
lacking authorization to retrieve it". The challenge applies only to callers
`Visible` admits, so an agent hidden from a caller still answers
`agent_not_found` and never reveals itself through a 403. A challenge callback
that errors refuses the request with no scope named.

Step-up gates *opening* the agent. The tools it returns keep their own
`ScopeChallenge`, enforced on `tools/call` as usual.

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
`IsInvalidAgentDefinition(err)` classifies the server-misconfiguration case,
and `StepUpScopes(err)` returns the scopes a step-up refusal asks for:

```go
if scopes, ok := agentsclient.StepUpScopes(err); ok {
    // obtain a token carrying scopes for the same user, then retry
}
```

### Caching

`Cache` keeps the roster and each agent's details for their `ttlMs`, as the
SEP's caching rules describe, and `WatchListChanged` invalidates it when the
roster changes:

```go
cache := agentsclient.NewCache(ac)
l, _ := agentsclient.WatchListChanged(ctx, mcp, cache.Invalidate)
defer l.Stop()

roster, _ := cache.ListAgents(ctx)       // fresh from cache, or re-listed
details, _ := cache.GetAgent(ctx, name)  // likewise, per agent
```

A result without a positive `ttlMs` is fetched again on every call, which is
what SEP-2549 says. `WithFallbackTTL(d)` keeps such results for `d` instead.
`agent_not_found` drops that agent and the roster, and
`invalid_agent_definition` is never cached. Use one `Cache` per client, since a
client is one authorization context.

### Model routing hints

`RoleTable` maps `modelRoutingHint` roles to the host's own models. It is
generic, so `M` is whatever the host runs (a provider, a model name):

```go
table := agentsclient.RoleTable[string]{
    Roles: map[agents.ModelRole]agentsclient.RoleModel[string]{
        agents.ModelRoleSmall: {Model: "small-model"},
        agents.ModelRoleLarge: {Model: "large-model", Vision: true},
    },
    Default: "supervisor-model",
}
model, how := table.Resolve(details.ModelRoutingHint)
```

`Resolve` uses the hinted role when it has a model (and vision, if the hint
asks for it), otherwise moves up to a larger role, never down, and falls back
to `Default` when nothing fits. `how` reports `hinted`, `escalated` or
`default`. `LargestRole` merges the hints of several agents run together.

## Example

`examples/agents/discovery` runs all of the above against one server as three
callers: listing, scoped tools, running an agent through `tools/call`,
per-caller filtering, step-up and `list_changed`, on both wires.

## Not yet implemented

Tracked in panyam/mcpkit#1393:

- Conformance scenarios for the SEP.

## Tests

```bash
just -f experimental/justfile test-agents             # server library
just -f experimental/justfile test-agents-clients-go  # Go client SDK
```
