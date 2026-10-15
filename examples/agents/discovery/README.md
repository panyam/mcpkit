# Agent discovery: listing, scoping, per-caller filtering and step-up

> ⚠ **Experimental.** Implements the agent discovery extension proposed in [agents-wg#22](https://github.com/modelcontextprotocol/agents-wg/pull/22), which is still a draft. The wire shape may change before it merges.

An MCP server can group its tools into **agents**. Each agent has a name, a short description, some instructions and a list of the server's own tools. A host asks for the roster with `agents/list`, opens the agent it wants with `agents/get`, and then runs that agent itself, with its own model, calling the agent's tools through the ordinary `tools/call`. There is no "invoke agent" method. The server only describes the grouping, and the host does the work.

This example runs one such server with three agents and walks a scripted host through it as three different callers, so you can see what each one is allowed to see and open.

## Quick start

```bash
just serve   # terminal 1, the agents server on :8080
just demo    # terminal 2, the scripted walkthrough
```

Add `--wire=stateless` to both commands to run the same walkthrough over the 2026-07-28 stateless wire. Everything in it works on both.

## The roster

| Agent | Tools | Who sees it | Who can open it |
|---|---|---|---|
| `pipeline-agent` | `list_failed_pipelines`, `get_pipeline_log` | everyone | everyone |
| `billing-agent` | `list_invoices` | callers with `billing:read` | callers with `billing:read` |
| `deploy-agent` | `list_failed_pipelines`, `deploy_service` | everyone | callers with `ops:deploy` |

The server takes three demo bearer tokens in place of an authorization server. `alice-read` grants `ops:read`, `alice-deploy` grants `ops:read ops:deploy` (Alice after stepping up), and `bob-billing` grants `ops:read billing:read`. [fine-grained-auth](../../fine-grained-auth/) shows step-up against a real authorization server, if you want that half.

## What the walkthrough shows

- **Discovery.** The server advertises the extension with `listChanged: true`, and `agents/list` returns routing fields only, so even a large roster is cheap to fetch.
- **Scoping.** `agents/get` returns the instructions, an advisory `modelRoutingHint`, and the full schemas of the agent's tools. Each schema matches the caller's `tools/list` entry field for field, which the walkthrough checks.
- **Running an agent.** The host calls the scoped tools through `tools/call`. The walkthrough stands in for the model and calls each tool once.
- **Per-caller filtering.** Bob sees `billing-agent` and Alice does not. When Alice asks for it by name she gets `agent_not_found`, the same answer as for a name that was never registered, so she can't probe for it.
- **Step-up.** Alice can see `deploy-agent` but can't open it with her first token. `agents/get` answers with an HTTP 403 and `WWW-Authenticate: Bearer error="insufficient_scope", scope="ops:deploy"`, the host swaps in a broader token for the same user, and the retry succeeds.
- **Roster changes.** A demo tool publishes a fourth agent, the server sends `notifications/agents/list_changed`, and the host's cache drops its 60-second roster early and picks up the new agent.

## Server side

```go
reg, err := agents.Register(agents.Config{
    Server:  srv,
    Agents:  []agents.AgentDef{{
        Name:           "deploy-agent",
        Description:    "Ships a service to production",
        Tools:          []string{"list_failed_pipelines", "deploy_service"},
        ScopeChallenge: core.RequireScopes("ops:deploy"), // step-up on agents/get
    }},
    TTLMs:   &ttl,
    Visible: func(ctx core.MethodContext, name string) bool {
        return name != "billing-agent" || core.HasScope(ctx, "billing:read")
    },
})
```

`Visible` hides an agent from a caller entirely. `ScopeChallenge` keeps it listed but asks for more authorization before it can be opened. An agent the caller can't see never produces a challenge, so the challenge can't leak its existence.

## Host side

`experimental/ext/agents/clients/go` carries the host helpers the walkthrough uses:

- `agentsclient.Cache` keeps the roster and each agent's details for the server's `ttlMs`, and drops them on `agent_not_found`.
- `agentsclient.WatchListChanged` opens a `subscriptions/listen` stream on the stateless wire. On the session wire the notification arrives on the GET stream instead, through `client.WithNotificationCallback`, which is how [`host.go`](host.go) handles both.
- `agentsclient.RoleTable` maps a `modelRoutingHint` role to one of the host's own models.
- `agentsclient.StepUpScopes` reads the scopes out of a step-up refusal.

See [WALKTHROUGH.md](WALKTHROUGH.md) for the step-by-step sequence (regenerate it with `just readme`).
