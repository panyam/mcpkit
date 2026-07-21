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
it connects to them, it does not manage them, so the launcher boots them on the
ports the config expects. This mirrors an `.mcp.json`-style connect-list.

| Server | Port | Serves | Why |
|---|---|---|---|
| `demo` | 8788 | `greet`/`report`/`analyze` | offloading (`report` is large) + sub-agent tools |
| `runbooks` | 8789 | one skill, **eager** | full skill body spliced into the system prompt at connect |
| `community` | 8790 | three skills, **catalog** | names + descriptions only; bodies fetched on demand via `load_skill` |
| `events` | 8791 | synthetic `chat.message` + `alert.fired` | the host auto-subscribes and injects occurrences into the turn |

`runbooks` is eager and `community` is catalog on purpose: eager for the small,
trusted skill set, catalog for the fuller one — the trust lever documented in
`agent/host` (catalog gates each skill through the `load_skill` tool, so it can
ride the approval ladder). Spawning these servers from the config instead of the
launcher is a deferred follow-up (`ServerConfig.command`); today they connect by
URL.

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

```bash
just allup      # postgres+pgvector + observability stacks (or: make allup)
just check      # probe everything; prints how to fix whatever is down
just run        # preflight, boot the four MCP servers, launch agentchat (inline TUI)
just note       # same, but the alt-screen notebook UI (scrollable, foldable cells)
# ... chat ...
just alldown    # tear the stacks back down
```

`just run` fails fast if postgres is down (the config depends on it) and warns
if observability or the embedder are missing (chat still works; those features
degrade).

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
3. **Semantic memory + durability.** Tell it: *"Remember that our prod region is
   us-east-1."* It calls `remember`, which embeds and upserts a pgvector row.
   **Quit and `just run` again** (same `$SESSION`). Ask: *"Where do we run
   prod?"* `--memory-inject-recall` embeds your question, does ANN top-k against
   pgvector, and injects the note — it survived the restart.
4. **Approval.** Type `/approval ask` to require confirmation before tool calls,
   then trigger a tool and approve/deny it.
5. **Traces.** With the observability stack up, open Grafana at
   http://localhost:3000 and find the trace for a turn (service `agentchat`).
6. **Reasoning display.** `/provider local-thinker` switches to a local reasoning
   model (deepseek-r1 via LM Studio on :1234). Its inline `<think>…</think>` is
   re-tagged as reasoning by the connection's `thinkingHint` and streamed dimmed
   under a `· thinking:` line. Cloud OpenAI/Gemini models don't emit inline
   reasoning, so this only shows with a reasoning model + a `thinkingHint`.
7. **Eager skills.** The `runbooks` server's skill is spliced into the system
   prompt at connect. Ask it to do what that skill covers (the skills-core
   `commit-helper` skill formats commit messages): *"Format a commit for a bug
   fix in the auth module."* The model follows the skill's guidance with no tool
   round-trip — the body was already in context.
8. **Catalog skills.** The `community` server is catalog mode, so only skill
   names + descriptions are in the prompt. Ask about one of its skills (*"Use the
   git-workflow skill to help me rebase."*). The model first calls `load_skill`
   to fetch that body (a tool call you'll see in the transcript), then acts on
   it. Turn on `/approval ask` first and you'll be prompted before the skill
   loads — catalog skills ride the tool-approval ladder.
9. **Event injection.** The `events` server emits synthetic `chat.message` and
   `alert.fired` every few seconds; the host subscribes at startup and injects
   them ahead of your next turn. After a short pause, ask: *"Anything happen
   while I was away?"* — the injected occurrences are in context, so the model
   can summarize them.
10. **Config persistence.** `run.sh` passes `--persist-config`. Switch models
    with `/provider openai-5.1` (or set an approval mode with `/approve ask`) and
    those picks are written to `kitchen-sink.local.json` (gitignored). Quit and
    `just run` again: it comes back on your last-picked provider, not the
    config's default. The overlay is a sparse delta merged over
    `kitchen-sink.json` at startup, so it never touches the base file; a launch
    flag still wins (`ACTIVE=anthropic-opus just run` overrides the overlay for
    that run).

## Inspecting state

```bash
just mem        # the durable semantic-memory rows for your sessions
just psql       # a psql shell on the agent DB (agent_runs, agent_memories, ...)
```

## Variables

Override any of these on the CLI (`SESSION=demo2 just run`) or via env. Defaults
live at the top of the `justfile` / `Makefile`.

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
  Grafana `:3000`. The four MCP servers are booted by `run.sh`; their logs go to
  `$LOG_DIR/kitchen-sink-<name>.log` (default `$TMPDIR`) so they don't clobber
  the TUI. `tail -f` one to watch a server.

## Extending

This is the place to demo new features end to end. When one lands: add its flag
to `run.sh`, expose the knob as a variable in the `justfile`/`Makefile`, add a
probe to `preflight.sh` if it needs a new backend, and add a numbered step to
the walkthrough above.
