# agentchat

A terminal chat harness over any set of MCP servers: point it at a config (or
a server URL) plus an OpenAI-compatible model, and converse with live tool
calls, streamed output, and in-terminal elicitation prompts. agentchat is the
reference in-process surface for the `agent/` module (see
`docs/AGENT_DESIGN.md`, Surfaces).

## Quick start

Against a local model (lmstudio, vllm, any OpenAI-compatible endpoint) and
one MCP server:

```bash
go run . --model qwen2.5-7b-instruct \
  --base-url http://localhost:1234/v1 \
  --url http://localhost:8080/mcp
```

Or with a config file:

```bash
go run . --config agentchat.json
```

```json
{
  "model": {
    "baseUrl": "http://localhost:1234/v1",
    "model": "qwen2.5-7b-instruct",
    "apiKeyEnv": "MODEL_API_KEY"
  },
  "instructions": "You are a helpful assistant with access to tools.",
  "servers": [
    { "id": "skills", "url": "http://localhost:18099/mcp" },
    { "id": "internal", "url": "https://tools.example.com/mcp",
      "auth": { "type": "bearer", "tokenEnv": "INTERNAL_MCP_TOKEN" },
      "allow": ["search", "lookup"] },
    { "id": "svc", "url": "https://svc.example.com/mcp",
      "auth": { "type": "client-credentials",
                "clientIdEnv": "SVC_CLIENT_ID",
                "clientSecretEnv": "SVC_CLIENT_SECRET",
                "scopes": ["mcp:basic"] } }
  ]
}
```

Secrets are env-indirected (`apiKeyEnv`, `tokenEnv`, `clientIdEnv`,
`clientSecretEnv` name variables, never values), and validation fails at
startup when a named variable is unset. A per-server `allow` list is a
capability boundary (a FilterSource): tools outside it are neither offered to
the model nor callable.

## Telemetry and failover

`--exporter` selects telemetry (`stdout`, `otlp`, `auto`; empty = off, zero
overhead) and `--otlp-endpoint` points at a collector (default
localhost:4317), the same contract the repo's examples use. With an exporter
on, every turn emits `agent.turn` / `agent.step` / `agent.tool` spans
stitched to client dispatch and server spans (one trace end to end), and
operational logs flow through slog to the same collector. The transcript is
UI, not logging: it always renders to the terminal regardless of exporter.

`model.backup` in the config enables failover: a call that fails cleanly on
the primary retries the backup once, the primary is benched for a cooldown,
and transitions are logged. A stream that already produced output is never
silently replayed. `/health` shows the current snapshot.

## Auth modes

`auth.type` selects among the client auth modes MCP supports:

- **`bearer`**: static token from `tokenEnv`.
- **`client-credentials`**: OAuth machine-to-machine via ext/auth. PRM and AS
  discovery, token caching, and refresh are automatic; `scopes` is optional
  (empty inherits the server's PRM scopes_supported); `allowInsecure: true`
  permits an http:// AS for dev setups.
- **`oauth`** (authorization-code browser flow): not implemented yet; the
  config rejects it with a pointer at the tracking issue. Interactive CLI
  login is the natural fit for agentchat and lands with that ticket.

Every flag is env-overridable with the `AGENTCHAT_` prefix (dashes become
underscores: `--base-url` is `AGENTCHAT_BASE_URL`); an explicit flag beats
the env var.

In the REPL: `/tools` lists the merged tool index, `/history` the
conversation, `/health` the failover snapshot, `/quit` exits; Ctrl-C cancels the in-flight turn. During an
elicitation, `/d` declines and `/c` cancels.

## What a session looks like

```
agentchat: 1 server(s), model qwen2.5-7b-instruct. /tools /history /quit; Ctrl-C cancels a turn.
> what tools do you have and what is 2+40?
⚙ add({"a":2,"b":40})
  ✓ add: 42
I have an add tool available. 2 + 40 = 42.
— 2 step(s), 812 in / 64 out tokens
>
```

Elicitation renders inline, schema-driven (enums become numbered choices,
booleans y/n, required fields re-prompt):

```
⚙ log_service({"car":"honda"})

? What was the mileage at service time?
  mileage (integer): 42000
  ✓ log_service: recorded at 42000 miles
```

## Walkthroughs against in-repo examples

Each assumes a local OpenAI-compatible model on `localhost:1234`.

**Skills server** (`examples/skills`, port 18099 per its README): start the
fixture, then `go run . --model <m> --url http://localhost:18099/mcp`. The
skills *tools* surface works today; automatic skill discovery and prompt
injection (fetching `skill://index.json` and honoring SKILL.md) lands with
the skills-consumption ticket in the agent epic and will change what the
model knows, not what it can call.

**Auth example** (`examples/auth`): start its server, export the token it
mints, and connect with `authTokenEnv`. The bearer flows through
`client.WithClientBearerToken`; OAuth token sources are wired the same way in
config once a flow needs them.

**Tasks v2** (`examples/tasks-v2`): connects and lists tools, but calling a
task-returning tool currently fails fast with a task-not-supported dispatch
error fed back to the model. Task-aware dispatch (poll/resume, input pauses
through the same elicitation prompts) is the next agent-epic ticket; this
note is the honest boundary of what agentchat does today.

## Testing

The app core is fully testable offline: every test drives real in-process
mcpkit servers with a scripted `StubProvider` (no network, no model), and the
terminal elicitation UI is exercised with scripted stdin. The live-model
session above is the manual interop check; run it against lmstudio before
releases.
