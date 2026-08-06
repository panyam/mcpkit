# examples/agents

Agent-focused examples grouped together, sharing a common demo harness.

- **`agent-async`** — an agent managing async work (events + tasks) through chat.
- **`multi-agent`** — Phase 3 composition: sub-agents-as-tools + handoff.
- **`critic`** — a second model watches the primary agent's turns and injects graded steering notes.
- **`deep-agent-supervisor`** — a supervisor over a server-advertised roster of specialist agents.
- **`kitchen-sink`** — every agent feature wired at once.

## One config per example, shared across surfaces

Every example runs off **one host config** that all its surfaces load:

| Recipe | Surface | What it does |
|---|---|---|
| `just agent` | scripted | deterministic StubProviders, no LLM (the golden test) |
| `just demo` | CLI (live) | a live model against the same server; resolves model/endpoint from `llm.json` |
| `just serve` | server | boots the demo MCP server the live surfaces point at |
| `just chat` | CLI (`agentchat`) | the terminal surface, `--config <config>.json` |
| `just web` | browser (`agentweb`) | the browser surface, `--config <config>.json --addr :8090` |

`chat` and `web` are shared recipes in `common.just`, parameterized by `CONFIG`
(default `config.json`) and `ADDR`. The already-config-driven examples
(`deep-agent-supervisor`, `kitchen-sink`, `examples/playground`) carry their own
variable-driven justfiles and gained the same `web` surface.

The three code-driven examples (`agent-async`, `multi-agent`, `critic`) each
ship a `config.json` too. Their deterministic scenario loads that same
`config.json` and keeps only the piece JSON cannot express in code — the
scripted `StubProvider` (and, for `multi-agent`/`critic`, the hand-wired
composition). Each example's README documents its own split.

## `llm.json` — providers, no secrets

`llm.json` lists named connections (local, cloud, a router) in the same shape as
the host `ConnectionsConfig`. It carries **only** endpoint + model + the *name*
of the env var holding the key (`apiKeyEnv`) — **never a key**, so it is safe to
commit. The active connection is a local model, so `just demo` works offline
against a running LM Studio / Ollama with nothing to configure. A model router
(OpenRouter, LiteLLM, a gateway) is just another connection — point `baseURL` at
it. For machine-specific overrides, copy it to `llm.local.json` (gitignored).

## Multi-agent via host config (agentchat)

`agentchat-multi-agent.json` is a sample host config declaring **sub-agent
personas** (`subAgents`): each is a specialist the main agent delegates to as a
tool, running on the same provider over a filtered view of the same server
tools, with its own instructions. Run it:

```bash
agentchat --config examples/agents/agentchat-multi-agent.json   # needs the demo server running
```

The sub-agents' activity renders **nested** under the main agent's turn
(`HostSubAgentEvent`). This is the declarative counterpart to the `multi-agent`
example, which wires the same `AgentSource`/`Team` primitives by hand.
