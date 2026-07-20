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

## Prompt editing keys (TUI)

The input line supports readline-style editing. `/keys` prints this cheatsheet
in-session.

| Key | Action |
|---|---|
| `←` / `→` | char back / forward |
| `ctrl+←` / `ctrl+→` | word back / forward |
| `ctrl+a` / `ctrl+e` (or `Home` / `End`) | start / end of line |
| `ctrl+w` | delete previous word |
| `ctrl+k` / `ctrl+u` | delete to end / start of line |
| `ctrl+home` / `ctrl+end` | start / end of input |
| `↑` / `↓` | command history · `Tab` complete · `Enter` send · `ctrl+c` quit |

Word navigation is bound to both `ctrl+←/→` and `alt+←/→` (`alt+b`/`alt+f`); the
`alt+*` bindings (and `alt+d` delete-word-forward, `alt+<`/`alt+>` input ends)
need your terminal to send Option as Meta — the `ctrl+*` bindings work without
it.

## Interfaces (`--ui`)

`--ui` picks the surface: `auto` (default — the inline TUI when stdout is a
terminal, else `plain`), `tui` (inline), `notebook` (alt-screen), or `plain`
(a scriptable line REPL for pipes/CI).

- **`tui`** (inline): finished output commits to the terminal's own scrollback,
  so native scroll, copy/paste, and a transcript that survives exit all work.
- **`notebook`** (alt-screen): a managed viewport with its own scroll and a
  transcript of **collapsible cells** (one per turn / command / info line). Two
  modes: **INS** (default) types into the input — `esc` enters **NAV**, where
  `↑↓`/`jk` move a cell cursor, `space` folds/unfolds, `g`/`G` jump to ends, and
  `esc`/`i` return to INS. `pgup`/`pgdn` and the mouse wheel scroll; in INS,
  `↑`/`↓` move within a multi-line prompt first, then recall history, then
  scroll the transcript. **Enter sends; `ctrl+j` inserts a newline** (also
  `shift+enter` / `alt+enter` where the terminal supports them). The cost:
  alt-screen takes the whole screen, breaks native copy/paste + shell scroll,
  and clears on exit — which is why the inline `tui` stays the default.

## Prompt editing keys (TUI)

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

## Agent-managed events and tasks (meta-tools)

With `metaTools` enabled (implied whenever events or triggers are configured),
the model gets host-local tools to manage its own async: `subscribe_events` /
`unsubscribe` / `list_subscriptions`, `create_trigger` / `remove_trigger` /
`list_triggers`, and `list_tasks` / `cancel_task`. So a conversation can set up
standing behavior:

```
> email me whenever a user is created
⚙ subscribe_events({"server":"crm","name":"user.created"})
⚙ create_trigger({"event":"user.created","instructions":"send a welcome email","label":"welcome"})
Done — I'll email new users.
  (later, a user is created)
· trigger: welcome
⚙ send_email({"to":"ada@example.com"})
Sent a welcome email to ada@example.com.
```

`create_trigger` works for task completions too (they're `task.completed`
events), so "notify me when the build finishes" is the same tool.

## Background tasks

A task-backed tool call stays inline for a grace window (`taskGraceSec`,
default 10s); if it is still running when the window expires it detaches:
the model gets a "moved to background" result and finishes its turn, you keep
chatting, and the task keeps running (input pauses still prompt you). On
completion the outcome prints a `· task <id> completed: ...` line and injects
as `task.completed` context on your next turn, so the model can react to it.
Bind a trigger on `task.completed` to have the agent proactively tell you the
moment a job finishes. `/tasks` lists running tasks; `/tasks cancel <id>`
stops one. Set `taskGraceSec` negative to wait inline forever.

## Events, injected context, and triggers

Configure event streams per server and the host consumes them through two
policies (both live in the agent module and are fully pluggable for
embedders):

```json
"servers": [{ "id": "grocery", "url": "...",
  "events": [{ "name": "cart.changed",
    "hint": { "priority": "high",
              "aggregate": { "windowMs": 2000, "strategy": "merge" },
              "template": "the cart now holds {{count}} items" } }] }],
"triggers": [{ "event": "cart.changed",
  "filter": { "count": "2" },
  "instructions": "The cart changed. Offer one recipe suggestion.",
  "label": "recipe-pitch", "cooldownSec": 300 }]
```

Injection: occurrences buffer per hint (merge, last-wins, or debounce
windows), render via the template, and join the conversation as system
messages at the next turn, priority-ordered under a budget; sensitive events
gate on consent. Host config hints override server-advertised ones (the
vendor context-hint on events/list).

Triggers: a matching event starts a proactive turn (marked with its label),
mediated by the anti-nag policy: one firing per binding until the user
engages AND the cooldown passes, with a session budget on top. Servers can
suggest triggers; the host decides.

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

**Tasks v2** (`examples/tasks-v2`): task-returning tools work end to end.
agentchat negotiates the tasks extension, polls task-backed calls to a
terminal state (honoring server poll hints), renders status transitions as
dim `· task <id>: <status>` lines, and routes input_required pauses through
the same terminal elicitation prompts as everything else — one InputHandler
covers ephemeral MRTR and task-backed pauses alike. Try `confirm_delete` or
`multi_input` from the example.

## Testing

The app core is fully testable offline: every test drives real in-process
mcpkit servers with a scripted `StubProvider` (no network, no model), and the
terminal elicitation UI is exercised with scripted stdin. The live-model
session above is the manual interop check; run it against lmstudio before
releases.
