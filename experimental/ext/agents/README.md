# experimental/ext/agents

> **EXPERIMENTAL** — pre-SEP research surface. Method names, wire shape, and the
> extension ID will change as the MCP Agents WG iterates (`agents-wg#20`). Lives
> under `experimental/ext/` alongside events and protogen; promote to
> `ext/agents` only once a SEP merges.

Go library for the server-declared **agent-definition discovery** primitive: a
server that hosts a fleet of specialist agents advertises them as a small
roster of tuples for routing, instead of flattening every specialist's tools
into one `tools/list`. A supervisor host sees N agent summaries, picks one, and
only then pulls that specialist's instructions and scoped tool schemas.

## Progressive disclosure

Discovery is the only new wire surface. It comes in three levels, the same
shape as two-tier skills loading (#910):

| Level | Wire | Payload |
|-------|------|---------|
| 1 | `capabilities.extensions["io.modelcontextprotocol/agents"]` | "this server has agents" |
| 2 | `agents/list` | the roster — `agentId`, `description`, `capabilities`, `exampleTasks`, `delegateTool`, `tasksEnabled`, `skillUri`. **No tool schemas.** |
| 3 | `agents/get {agentId}` | one agent's `instructions` + scoped `tools[]` |

**Invocation is not new wire surface.** Each agent advertises a `delegateTool`
(e.g. `invoke_workflow_agent`); the host routes to the agent and calls that
tool with the task via the existing `tools/call`.

## Server side

```go
import (
    "github.com/panyam/mcpkit/server"
    "github.com/panyam/mcpkit/experimental/ext/agents"
)

srv := server.NewServer(info)
reg, err := agents.Register(agents.Config{
    Server: srv,
    Agents: []agents.AgentDef{{
        AgentID:      "workflow-agent",
        Description:  "Pipelines, approval gates, run history, connectors",
        Capabilities: []string{"Pipeline catalog and execution", "Approval gates"},
        ExampleTasks: []string{"Show pending pipeline approvals"},
        DelegateTool: "invoke_workflow_agent",
        TasksEnabled: true,
        SkillURI:     "skill://workflow-agent/SKILL.md",
        // detail — returned only by agents/get:
        Instructions: "You operate CI/CD pipelines.",
        Tools:        []core.ToolDef{ /* the specialist's scoped tools */ },
    }},
})
```

`Register` declares the extension and wires the `agents/list` + `agents/get`
handlers. The returned `Registry` supports runtime `AddAgent` / `RemoveAgent`.
`agents/list` preserves insertion order; a duplicate `AgentID` is reported (the
first wins). An unknown `agentId` on `agents/get` is a `-32602` InvalidParams
error.

### Tracing (SEP-414)

Set `Config.TracerProvider` to opt the discovery handlers into spans:
`agents.list` (attribute `agents.count`) and `agents.get` (`mcp.agent.id`,
`agents.found`). Nil or `core.NoopTracerProvider{}` — the default — emits
nothing with zero allocation, and the extension depends only on the core
tracing abstraction, never on `ext/otel`. A resolved specialist's own execution
is traced by the child `Runner` the host builds from an `agents/get` result;
the host also emits an `agents.resolve` span tying delegation to discovery. See
`docs/SEP_414_OTEL.md` § "experimental/ext/agents discovery spans".

## Client side (`clients/go`)

```go
import agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"

ac := agentsclient.New(mcp) // over a connected *client.Client
if !ac.SupportsAgents() {
    return // no agents; fall back to plain tools/list
}
roster, _ := ac.ListAgents(ctx)          // level 2
detail, _ := ac.GetAgent(ctx, chosen)    // level 3
mcp.ToolCall(detail.DelegateTool, map[string]any{"query": task}) // invoke
```

Decoders are deliberately tolerant: an absent or empty roster decodes to an
empty slice, not an error.

## Deliberate non-coupling

`tasksEnabled` ties conceptually to SEP-2663 (an async delegate is a Task) and
`skillUri` to the skills work, but this package couples to neither — they are an
advertised bool and an advertised string. Turning an `agents/get` result into a
Runner-backed `AgentSource` is agent-layer work (#1144), not here: per
`agent/CONSTRAINTS.md` A6 this package traffics only in protocol objects.

## Tests

```bash
just -f experimental/justfile test-agents             # server library
just -f experimental/justfile test-agents-clients-go  # Go client SDK
```
