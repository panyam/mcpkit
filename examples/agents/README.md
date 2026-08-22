# examples/agents

Examples of the **server side** of agent work: MCP servers that advertise a roster of specialist
agents, and a server set broad enough to exercise a host end to end.

- **`deep-agent-supervisor`** — a server advertising a roster of specialist agents over
  `experimental/ext/agents`, the pre-SEP server-declared discovery extension.
- **`kitchen-sink`** — the demo, skills, and events servers wired together so one host can reach
  every surface at once.

## The agent SDK examples moved

`agent-async`, `multi-agent`, and `critic` imported the agent SDK, so they followed it to
[chakra](https://github.com/panyam/chakra) and now live under that repo's `examples/`. So did the
shared `llm.json`, `common.just`, and the `agentchat-multi-agent.json` sub-agent host config, along
with `examples/playground`.

The two examples above stayed because they demonstrate a protocol extension rather than the SDK, and
never import it.

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
