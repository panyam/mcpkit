# agent/host

The reusable host application core for the mcpkit agent module. It assembles
`agent/` (Provider, Runner, ToolSource, elicitation, injection/trigger policies)
into a runnable host without committing to any particular UI. A terminal CLI and
a future web-chat surface both build on it; the only thing that differs is the
reader/writer you hand it (stdin/stdout versus a socket).

Nested Go module (`github.com/panyam/mcpkit/agent/host`) under `agent/`, so its
heavier dependencies (ext/auth, ext/skills, ext/tasks, events client, gocurrent)
stay out of the lean `agent/` module.

## What lives here

- **Config loading** (`config.go`): `Config`, `LoadConfig`, and the provider /
  server / auth / event / trigger sub-configs. This is the bulk of the package.
  Where and how to load providers, servers (with per-server auth), tool
  allowlists, skills, events, and triggers.
- **The App** (`app.go`): `NewApp(cfg, out, in, opts...)` builds the Runner, the
  aggregated tool sources, the FanIn event consumption loop, the injection and
  trigger policies, and background-task bookkeeping. `RunTurn` drives one turn;
  `REPL` drives an interactive loop; `Close` tears down.
- **Meta-tools** (`metatools.go`): host-local tools the model calls to manage its
  own async work (`subscribe_events`, `create_trigger`, `list_tasks`,
  `cancel_task`, and friends), installed under source id `host`.
- **Surface seams**: terminal elicitation UI (`elicit.go`) and rendering
  (`render.go`). Swap these for a different surface.
- **Subscriptions and background tasks** (`subscription.go`, `tasks_bg.go`):
  event-stream lifecycle and detach-after-grace task execution.

## Options

- `WithProvider(p)` — supply the LLM Provider (e.g. `StubProvider` for tests,
  `OpenAIProvider` for a real model).
- `WithTracerProvider(tp)` — SEP-414 tracing.
- `WithLogger(l)` — structured logging.
- `WithRunStore(store)` — session persistence. Every completed turn's messages
  and event stream append to a run in the store (`agent.NewInMemoryRunStore`
  for in-process resume/fork; `agent/store/redis` or `agent/store/gorm` —
  Postgres, or a serverless SQLite file — for restart-surviving sessions).
- `WithToolResultStore(store)` — backing store for tool-result offloading
  (`Config.Offload`). Over-threshold results are stored out of band and the
  model gets a compact stub plus a `read_tool_result` tool; omit the option and
  offloading uses an in-memory store, pass a durable one for blobs that survive
  restarts. Durable options: `agent/store/redis`, `agent/store/gorm`, or the
  dependency-free `agent.NewFileToolResultStore(dir)` — the no-server local path
  where blobs are files the agent can also read directly. `Config.Offload`
  (nil = off) sets the byte threshold, preview length, and per-tool overrides. `App.AttachRun` names or resumes a session at startup;
  `App.Resume` and `App.Fork` switch runs mid-session (`/resume <id>`,
  `/fork [id]`, `/session` in the REPL). A failed or cancelled turn persists
  nothing; persistence failures degrade to a rendered warning, never a turn
  failure.

## Testing

Fully offline: real in-process mcpkit servers plus a scripted `StubProvider` plus
a canned reader. See `examples/agent-async` for a golden-transcript scenario and
`cmd/agentchat` for the terminal CLI built on this package.

Run the module's tests with `just test-agent` from the repo root, or
`cd agent/host && go test ./...` directly.
