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
| Semantic memory (recall by meaning) | `--memory --memory-inject-recall --memory-embed-*` | pgvector `SemanticMemoryStore` |
| History compaction | `--compact-tokens $COMPACT_TOKENS` | in-Runner summarizer |
| Distributed tracing | `--exporter $EXPORTER --otlp-endpoint $OTLP_ENDPOINT` | OTel → Tempo/Grafana |
| Sub-agent personas | `subAgents` in `kitchen-sink.json` | in-process child Runners |
| Tool-call approval | `/approval` slash command in-session | — |

The chat model comes from `kitchen-sink.json`'s `connections` block (local
LM Studio by default, offline-friendly). Only the **embedder** is passed by
flag, because it is a separate endpoint from the chat model.

## Prerequisites

- Docker (for the backend + observability stacks).
- A local chat model on an OpenAI-compatible endpoint (LM Studio / Ollama), or
  a cloud provider — `kitchen-sink.json` ships several models per provider
  (`openai-*`, `gemini-*`, `anthropic-*`; set the matching `*_API_KEY` env
  var). Point `connections.active` at one, or switch between them at runtime
  with `/provider` to compare models. The model ids are examples; edit them
  for what your account has access to.
- An OpenAI-compatible **embeddings** endpoint for semantic memory. `just check`
  tells you exactly what to start if it's missing.

## Quick start

```bash
just allup      # postgres+pgvector + observability stacks (or: make allup)
just check      # probe everything; prints how to fix whatever is down
just run        # preflight, boot the demo MCP server, launch agentchat (TUI)
# ... chat ...
just alldown    # tear the stacks back down
```

`just run` fails fast if postgres is down (the config depends on it) and warns
if observability or the embedder are missing (chat still works; those features
degrade).

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
| `SESSION_STORE` | `postgres://postgres:postgres@localhost:5432/agent` | runs + offload blobs |
| `SESSION` | `kitchen-sink` | run id to create/resume |
| `OFFLOAD_THRESHOLD` | `1024` | bytes; 0 disables offloading |
| `EMBED_MODEL` | `text-embedding-nomic-embed-text-v1.5` | must exist on the embedder |
| `EMBED_URL` | `http://localhost:1234/v1` | OpenAI-compatible `/embeddings` |
| `EMBED_DIM` | `768` | **must match the model** — pgvector rejects a mismatch |
| `COMPACT_TOKENS` | `8000` | compact history past this estimate |
| `EXPORTER` | `otlp` | `otlp` / `stdout` / `auto` / empty(off) |
| `OTLP_ENDPOINT` | `localhost:4317` | OTel collector |

## Gotchas

- **`EMBED_DIM` must equal the embedding model's true width** (nomic = 768,
  OpenAI `text-embedding-3-small` = 1536, MiniLM = 384). A mismatch fails the
  insert.
- **The `agent` DB + `vector` extension are created on a fresh postgres volume
  only.** If you started the backends stack before this feature existed,
  `just check` tells you to reset: `cd docker/backends && just down && rm -rf data/postgres && just up`.
- **Ports:** demo server `:8788` (playground owns `:8787`), postgres `:5432`,
  OTLP `:4317`, Grafana `:3000`.

## Extending

This is the place to demo new features end to end. When one lands: add its flag
to `run.sh`, expose the knob as a variable in the `justfile`/`Makefile`, add a
probe to `preflight.sh` if it needs a new backend, and add a numbered step to
the walkthrough above.
