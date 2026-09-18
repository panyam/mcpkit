# examples/agents

Examples of the **server side** of agent work: MCP servers that advertise a roster of specialist
agents, and a server set broad enough to exercise a host end to end.

- **`kitchen-sink`** — the demo, skills, and events servers wired together so one host can reach
  every surface at once.

## The agent SDK examples moved

`agent-async`, `multi-agent`, and `critic` imported the agent SDK, so they followed it to
[chakra](https://github.com/panyam/chakra) and now live under that repo's `examples/`. So did the
shared `llm.json`, `common.just`, and the `agentchat-multi-agent.json` sub-agent host config, along
with `examples/playground`.

`deep-agent-supervisor` went too, in 2026-09. It was built for issue 1146 as the demo the WG stress
test in `agents-wg#20` benchmarked against LangChain Deep Agents, and it made its argument: the
supervisor's context holds three routing tuples rather than every specialist's tool schemas. What it
could not survive was losing its other half. The roster server it shipped still compiled, but
`run.sh` drove it from `agent/surfaces/chat`, so `demo`, `run`, `chat`, `note` and `web` all pointed
at a tree that is in chakra now. `experimental/ext/agents` keeps its own tests; the demo does not
need to exist for the extension to.

`kitchen-sink` stays because it demonstrates a protocol extension rather than the SDK, and never
imports it.

## Driving these with a host

Both examples ship a host config and expect a client to point at it. The terminal
(`agentchat`) and browser (`agentweb`) surfaces live in chakra now, so install from there:

```bash
go install github.com/panyam/chakra/surfaces/chat@latest   # agentchat
go install github.com/panyam/chakra/surfaces/web/cmd/agentweb@latest
```

Each example's `justfile` still carries `serve`, `run` / `chat`, and `web` recipes; they invoke
whichever binary is on your `PATH`. Nothing here depends on chakra at build time, so `make test`
covers the servers with no agent toolchain present.

## Server lifecycle stays decoupled

`kitchen-sink` is the reference for a host never owning its servers' processes: `servers.sh`
(`just servers-up` / `servers-down` / `servers`) owns them, and the run recipe only *checks* the
ports and points at `servers-up` if any are down. It never boots or kills them. The constraint that
rule belongs to travelled to chakra with the host it constrains.
