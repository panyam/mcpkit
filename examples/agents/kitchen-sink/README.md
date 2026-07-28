# kitchen-sink

Every agent feature wired together in one runnable harness. It reuses
`cmd/agentchat` (the reference agent surface) with all the knobs turned on, so
you can see durable sessions, tool-result offloading, semantic memory,
compaction, tracing, and sub-agents working at the same time against real
backends. It is meant to grow: as new SDK features land, add a knob here and a
line to the walkthrough.

## What it wires

| Feature | How it's turned on | Backend |
|---|---|---|
| Durable sessions (resume/fork) | `--session-store $SESSION_STORE --session $SESSION` | postgres `RunStore` |
| Tool-result offloading | `--offload-threshold $OFFLOAD_THRESHOLD` | postgres blobs (shares the session store) |
| Semantic memory (recall by meaning) | `--memory --memory-inject-recall` + the config's `embedder` role | pgvector `SemanticMemoryStore` |
| History compaction | `--compact-tokens $COMPACT_TOKENS` | in-Runner summarizer |
| Distributed tracing | `--exporter $EXPORTER --otlp-endpoint $OTLP_ENDPOINT` | OTel → Tempo/Grafana |
| Sub-agent personas | `subAgents` in `kitchen-sink.json` | in-process child Runners |
| Tool-call approval | `/approval` slash command in-session | — |
| Skills, eager | `skillsMode: "eager"` on the `runbooks` server | skills-core MCP server (:8789) |
| Skills, catalog | `skillsMode: "catalog"` on the `community` server | skills MCP server (:8790) + `load_skill` |
| Event injection | `events` on the `events` server | events kitchen-sink MCP server (:8791) |
| Runtime-config persistence | `--persist-config` in `run.sh` | `kitchen-sink.local.json` overlay (gitignored) |

The chat model comes from `kitchen-sink.json`'s `connections` block (local
LM Studio by default, offline-friendly). Only the **embedder** is passed by
flag, because it is a separate endpoint from the chat model.

### The four MCP servers

`kitchen-sink.json` connects to four servers, and the host is a **pure client**:
it connects to them by URL, it does not manage them. Their lifecycle is
decoupled from the agent (root `CONSTRAINTS.md` C6) — you start them once with
`just servers-up-bg` and they survive chat restarts. This mirrors an
`.mcp.json`-style connect-list.

| Server | Port | Serves | Why |
|---|---|---|---|
| `demo` | 8788 | `greet`/`report`/`analyze` | offloading (`report` is large) + sub-agent tools |
| `runbooks` | 8789 | one skill, **eager** | full skill body spliced into the system prompt at connect |
| `community` | 8790 | three skills, **catalog** | names + descriptions only; bodies fetched on demand via `load_skill` |
| `events` | 8791 | synthetic `chat.message` + `alert.fired` | the host auto-subscribes and injects occurrences into the turn |

`runbooks` is eager and `community` is catalog on purpose: eager for the small,
trusted skill set, catalog for the fuller one — the trust lever documented in
`agent/host` (catalog gates each skill through the `load_skill` tool, so it can
ride the approval ladder).

Manage the servers independently of the chat:

```bash
just servers-up            # start + tail all four in the FOREGROUND; Ctrl+C brings them down
just servers-up-bg         # start detached, no tail — they survive restarts (stop with servers-down)
just servers-restart       # down then up (foreground + tail) — bounce them in one command
just servers               # status: which are up
just servers-up-bg events  # start just one, detached
just servers-down          # stop all
```

`servers-up` (and `servers-restart`) run the servers in the **foreground** and
tail their logs, so this terminal owns them — Ctrl+C brings them down. Run
`just run` (agentchat) in a **second** terminal (the inline TUI and the
interleaved logs would fight for the same screen). For a fire-and-forget start
that survives restarts and needs no dedicated terminal, use `servers-up-bg`.

`just run` only *checks* these ports and tells you to `just servers-up` if any
are down — it never boots or kills them. Having the agent spawn them from the
config (`ServerConfig.command`, stdio) is a deferred follow-up; the client
already owns stdio subprocess lifecycle, only the config surface is missing.

## Prerequisites

- Docker (for the backend + observability stacks).
- A local chat model on an OpenAI-compatible endpoint (LM Studio / Ollama), or
  a cloud provider — `kitchen-sink.json` ships several models per provider
  (`openai-*`, `gemini-*`, `anthropic-*`; set the matching `*_API_KEY` env
  var). Point `connections.active` at one, or switch between them at runtime
  with `/provider` to compare models. The model ids are examples; edit them
  for what your account has access to.
- An **embeddings** endpoint for semantic memory. By default the config's
  `embedder` role points at OpenAI (`text-embedding-3-small`), so just set
  `OPENAI_API_KEY` — no local embedder needed. Switch it to `gemini-embed`, or
  override with a local endpoint via `EMBED_MODEL`/`EMBED_URL`/`EMBED_DIM`.
  `just check` tells you exactly what's missing.

## Quick start

Three layers, brought up separately (backends → MCP servers → agent):

```bash
just allup        # postgres+pgvector + observability stacks
just servers-up-bg # the four MCP servers, detached (independent of the chat)
just check        # probe backends; prints how to fix whatever is down
just run          # preflight, verify servers are up, launch agentchat (inline TUI)
just note         # same, but the alt-screen notebook UI (scrollable, foldable cells)
# ... chat, quit, `just run` again — the servers are still up ...
just servers-down  # stop the MCP servers
just alldown       # tear the stacks back down
```

`servers-up-bg` here starts the servers detached so this same terminal can go on
to `just run`. Prefer `just servers-up` in a dedicated terminal when you want to
watch the server logs live (Ctrl+C there brings the servers down).

`just run` fails fast if postgres is down (the config depends on it) or if any
MCP server is unreachable (it tells you to `just servers-up`), and warns if
observability or the embedder are missing (chat still works; those degrade).

### Run against real providers

Chat and the embedder are both connections in `kitchen-sink.json`. Pick them
without editing the file:

```bash
# Chat on Anthropic, embeddings on OpenAI (the default embedder role)
ACTIVE=anthropic-opus just run          # needs ANTHROPIC_API_KEY + OPENAI_API_KEY

# All OpenAI
ACTIVE=openai-5.1 just run              # needs OPENAI_API_KEY (chat + embeddings)
```

`ACTIVE` overrides the active chat connection; the embedder comes from the
config's `embedder` role (switch it to `gemini-embed` in the file to embed with
Gemini). You can also swap chat models mid-session with `/provider`.

## Guided walkthrough (exercise each feature)

1. **Tools + offloading.** Ask: *"Write a report on distributed caching."* The
   model calls the `report` tool, whose output is large — over
   `OFFLOAD_THRESHOLD` it is stored and the model gets a stub plus a
   `read_tool_result` handle instead of the full text in context.
2. **Sub-agents.** Ask: *"Have the analyst summarize these numbers: 3, 7, 7, 19, 2."*
   The main agent delegates to the `analyst` persona (its own child Runner that
   only sees the `analyze` tool), which returns the stats.
3. **Parallel fan-out.** Ask: *"Have the review team look at this plan: migrate
   the session store to Redis with a 24h TTL."* The model calls `review_team`
   **once**, which broadcasts the task to three reviewer sub-agents (security,
   performance, cost) that run **concurrently** — in `--ui notebook` you see
   their events interleave under one fan-out call in the sub-agent tree — and
   returns their assessments combined in one result.
   - **Async (background) sub-agent.** Ask: *"Kick off deep research on
     event-driven architectures — I'll keep working."* The model calls
     `deep_researcher`, which **acks immediately** ("started in the background")
     so the turn finishes without waiting. Keep chatting; after the child
     finishes you'll see *"sub-agent deep_researcher finished"*, and its report
     is injected as context on your **next** turn (ask *"what did the research
     find?"*). Contrast the synchronous `researcher` (step 2), which blocks the
     turn until it answers — async is the spawn-and-continue Task form for work
     you don't need this instant.
4. **Semantic memory + durability.** Tell it: *"Remember that our prod region is
   us-east-1."* It calls `remember`, which embeds and upserts a pgvector row.
   **Quit and `just run` again** (same `$SESSION`). Ask: *"Where do we run
   prod?"* `--memory-inject-recall` embeds your question, does ANN top-k against
   pgvector, and injects the note — it survived the restart.
5. **Approval.** Type `/approval ask` to require confirmation before tool calls,
   then trigger a tool and approve/deny it.
6. **Traces.** With the observability stack up, open Grafana at
   http://localhost:3000 and find the trace for a turn (service `agentchat`).
7. **Reasoning display.** `/provider local-thinker` switches to a local reasoning
   model (deepseek-r1 via LM Studio on :1234). Its inline `<think>…</think>` is
   re-tagged as reasoning by the connection's `thinkingHint` and streamed dimmed
   under a `· thinking:` line. Cloud OpenAI/Gemini models don't emit inline
   reasoning, so this only shows with a reasoning model + a `thinkingHint`.
8. **Eager skills.** The `runbooks` server's skill is spliced into the system
   prompt at connect. Ask it to do what that skill covers (the skills-core
   `commit-helper` skill formats commit messages): *"Format a commit for a bug
   fix in the auth module."* The model follows the skill's guidance with no tool
   round-trip — the body was already in context.
9. **Catalog skills.** The `community` server is catalog mode, so only skill
   names + descriptions are in the prompt. Ask about one of its skills (*"Use the
   git-workflow skill to help me rebase."*). The model first calls `load_skill`
   to fetch that body (a tool call you'll see in the transcript), then acts on
   it. Turn on `/approval ask` first and you'll be prompted before the skill
   loads — catalog skills ride the tool-approval ladder.
10. **Event injection.** The `events` server emits synthetic `chat.message` and
   `alert.fired` every few seconds; the host subscribes at startup and injects
   them ahead of your next turn. After a short pause, ask: *"Anything happen
   while I was away?"* — the injected occurrences are in context, so the model
   can summarize them.
11. **Config persistence.** `run.sh` passes `--persist-config`. Switch models
    with `/provider openai-5.1` (or set an approval mode with `/approve ask`) and
    those picks are written to `kitchen-sink.local.json` (gitignored). Quit and
    `just run` again: it comes back on your last-picked provider, not the
    config's default. The overlay is a sparse delta merged over
    `kitchen-sink.json` at startup, so it never touches the base file; a launch
    flag still wins (`ACTIVE=anthropic-opus just run` overrides the overlay for
    that run).

## Handoff team (a second topology)

The walkthrough above runs **one** main agent with sub-agents, fan-out, and
memory attached to it. **Handoff** is the other multi-agent shape: control
*transfers* between agents instead of one agent delegating. Because a team
replaces the single main agent, it is a separate config (`kitchen-sink-team.json`)
and its own recipe:

```bash
just team       # triage router -> billing / technical specialists (notebook UI)
```

Ask a billing question (*"I was double charged, can I get a refund?"*). The
**triage** agent transfers you to **billing** — you'll see the `→ handed off to
billing` line — and billing answers. Now ask a technical follow-up (*"also, the
export button throws a 500"*); billing transfers you to **technical**. The
**active agent persists across turns**: your next message goes straight to
whoever holds the conversation, not back through triage. `MaxHandoffs` bounds
ping-pong. (Team mode is mutually exclusive with the main agent's memory /
sub-agents / fan-out — those attach to a single agent; agentchat errors if you
combine them.)

## Inspecting state

```bash
just mem        # the durable semantic-memory rows for your sessions
just psql       # a psql shell on the agent DB (agent_runs, agent_memories, ...)
```

## Variables

Override any of these on the CLI (`SESSION=demo2 just run`) or via env. Defaults
live at the top of the `justfile`.

| Variable | Default | Notes |
|---|---|---|
| `ACTIVE` | *(config's active)* | override the active chat connection (e.g. `anthropic-opus`) |
| `SESSION_STORE` | `postgres://postgres:postgres@localhost:5432/agent` | runs + offload blobs |
| `SESSION` | `kitchen-sink` | run id to create/resume |
| `OFFLOAD_THRESHOLD` | `1024` | bytes; 0 disables offloading |
| `EMBED_MODEL` | *(empty)* | empty = use the config's `embedder` role; set to override with an explicit endpoint |
| `EMBED_URL` | `http://localhost:1234/v1` | with `EMBED_MODEL`: OpenAI-compatible `/embeddings` |
| `EMBED_DIM` | *(empty)* | with `EMBED_MODEL`: **must match the model** — pgvector rejects a mismatch |
| `COMPACT_TOKENS` | `8000` | compact history past this estimate |
| `EXPORTER` | `otlp` | `otlp` / `stdout` / `auto` / empty(off) |
| `OTLP_ENDPOINT` | `localhost:4317` | OTel collector |

## Gotchas

- **The embedding dimension must equal the model's true width** — the `dim` on
  the `embedder` connection (or `EMBED_DIM` when overriding by flag). OpenAI
  `text-embedding-3-small` = 1536, Gemini `text-embedding-004` = 768, nomic =
  768, MiniLM = 384. A mismatch fails the pgvector insert.
- **The `agent` DB + `vector` extension are created on a fresh postgres volume
  only.** If you started the backends stack before this feature existed,
  `just check` tells you to reset: `cd docker/backends && just down && rm -rf data/postgres && just up`.
- **Ports:** demo `:8788` (playground owns `:8787`), skills-core/eager `:8789`,
  skills/catalog `:8790`, events `:8791`, postgres `:5432`, OTLP `:4317`,
  Grafana `:3000`. The four MCP servers are managed by `just servers-up` /
  `servers.sh` (not `run.sh`); their binaries, PID files, and logs live under
  `.servers/` (gitignored). `tail -f .servers/<name>.log` to watch a server.

## Extending

This is the place to demo new features end to end. When one lands: add its flag
to `run.sh`, expose the knob as a variable in the `justfile`, add a probe to
`preflight.sh` if it needs a new backend, and add a numbered step to the
walkthrough above.
