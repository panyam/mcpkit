# agent-async

An agent managing its own asynchronous work **through conversation**, not
config: it subscribes to an event stream, installs a standing "when X happens,
do Y" trigger, runs a task tool, and is later woken by the event to act. The
control is all host meta-tools (`subscribe_events`, `create_trigger`,
`list_tasks`, ...) the model calls like any other tool.

This is the agent module's scriptable-loop story: the whole flow runs
deterministically against a scripted `StubProvider` — no LLM, no network — so
it is both a runnable demo and a golden-transcript test.

## Run it

```bash
just agent          # deterministic scripted run (no model)
just test           # the golden-transcript test
just demo qwen2.5-7b-instruct   # a live model improvising against the same server
```

## What happens

```
> email me whenever a user is created
⚙ subscribe_events({"server":"app","name":"user.created"})
⚙ create_trigger({"event":"user.created","instructions":"send a welcome email...","label":"welcome"})
Set — I'll welcome-email every new user.
> start the quarterly report
⚙ long_report({})
The quarterly report is ready — 42 pages.
(a user signs up...)
· trigger: welcome
⚙ send_email({"to":"ada@example.com"})
Welcomed ada@example.com.
```

The model set up the standing behavior in the first turn; when `user.created`
fired later, the trigger woke the agent and it emailed the new user — no code
path was hardcoded for that.

## How it's built

- `scenario.go` — the app-domain MCP server (`user.created` events, `send_email`,
  a task-backed `long_report`), the scripted model turns, and `runScenario`
  wiring the reusable host (`agent/host`) to it.
- `main.go` — dual-mode entry (stub vs `--model`) plus a concurrency-safe
  transcript writer (the proactive trigger turn writes from the event goroutine).

The host is `github.com/panyam/mcpkit/agent/host` — the same App that backs the
agentchat CLI, imported here to drive it programmatically.

Background-detach on genuinely long tasks (the task runs past its grace window
and notifies via a `task.completed` event) is exercised in the agentchat
tests; here the report finishes fast to keep the demo deterministic.
