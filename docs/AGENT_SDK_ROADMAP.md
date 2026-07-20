# Competitive Analysis — mcpkit `agent/` vs. the field

**Scope.** A three-way comparison of mcpkit's agent host layer (`agent/`) against (a) general agent
frameworks and (b) real coding-agent loops, plus a grounded build-out roadmap for closing every
material gap. This document treats the current `AGENT_DESIGN.md` exclusions (memory, multi-agent,
workflows, persistence) as **temporary** and folds them back in as build targets: the question here
is not "what is in scope today" but "what would it take to ship a complete, competitive agent SDK."

**Method.** Direct inventory of the `agent/` source (file-cited), a survey of agent frameworks
(Mastra, Eino, Genkit-Go, langchaingo, swarmgo, agno-Go) and coding-agent loops (Claude Code, Cursor
CLI, Gemini CLI, aider, Codex CLI, OpenCode), and an architecture pass mapping each missing capability
onto the existing seams. Verdicts respect `agent/CONSTRAINTS.md` (dependency direction A1,
wire-serializability A2, no global output A4, mechanism-vs-policy layering A6).

---

## 1. Positioning — what `agent/` is today

A clean **four-seam** host loop (`docs/AGENT_DESIGN.md`):

- **Provider** (`agent/provider.go`, `openai_provider.go`) — streaming OpenAI-compatible chat
  completions, no SDK dependency; `StubProvider` for determinism; `FailoverProvider` for
  primary/backup with cooldown.
- **Runner** (`agent/runner.go`) — bounded multi-step loop (`MaxSteps=8`), parallel tool dispatch
  with ordered results, every failure fed back to the model as text (only cancellation / provider
  failure / step cap abort), SEP-414 spans (`agent.turn`/`agent.step`/`agent.tool`).
- **ToolSource** (`agent/toolsource.go`, `client_source.go`, `func_source.go`, `multi_source.go`,
  `filter_source.go`) — aggregation across MCP servers + host-local functions, `FilterSource` as a
  hard capability boundary, per-step `Selector` for context-aware narrowing.
- **Policy hooks** — FIFO **elicitation** (`elicitation.go`), plus **injection** and **trigger**
  policies (`injection.go`, `triggers.go`) that turn server events into injected context or
  proactive turns.

**Distinctive strengths worth defending** (most competitors lack these):

1. **Async control plane.** The model manages its *own* async via host meta-tools —
   `subscribe_events`, `create_trigger`/`list_triggers`, `list_tasks`/`cancel_task`
   (`agent/host/metatools.go`). No surveyed framework exposes standing-behavior installation to
   the model this way.
2. **MCP-native, wire-agnostic.** Client *and* server authoring; stateless / legacy / task input
   wires are absorbed at the client layer, so the agent never branches on wire mode.
3. **Wire-serializable by constraint (A2).** `Message`, `Event`, `Delta`, `IncomingEvent` all
   round-trip as JSON — persistence and web surfaces are cheap to add later.
4. **Zero-overhead observability by default.** SEP-414 tracing stitches agent → client → server into
   one trace; Noop provider means the unconfigured path pays nothing.

---

## 2. Gap table A — agent-framework parity

| Capability | Leaders | mcpkit today | Verdict |
|---|---|---|---|
| Tiered memory: working + **semantic recall** (vector) + compaction | Mastra (best), langchaingo, agno-Go | absent; history is caller-owned `[]Message` | **Build — Phase 2** |
| Durable **suspend/resume** workflows; branch/parallel control flow | Mastra, Eino, agno-Go, Genkit | absent (linear loop only) | **Build — Phase 4 (sibling module)** |
| Multi-agent / handoffs / sub-agents | all five Go frameworks + Mastra | absent | **Build — Phase 3** |
| RAG pipeline (chunk / embed / retrieve / index) | Mastra, Eino, Genkit, agno-Go | absent | Compose from tools + memory recall — ext/example |
| **Eval / scorer framework** | Mastra, Genkit *(rare in Go)* | absent (StubProvider = unit tests) | **Build — Phase 0 (differentiator)** |
| Prompt versioning / templating | Genkit (Dotprompt) | minimal (`Instructions` + skills) | Defer — seam/example |
| Native providers beyond OpenAI-compat | all | OpenAI-compat only | **Build — Phase 0 (Anthropic)** |
| Structured output *inside the loop* | Mastra, Genkit, Eino | `Generate`-only (not in Runner) | **Build — Phase 0** |
| Voice (STT / TTS) | Mastra only | absent | Non-goal (no Go competitor either) |

**Notes on the field.** *Eino* (ByteDance, "LangGraph for Go") is the most mature Go competitor:
graph/chain orchestration, a ReAct ADK with sub-agent delegation, interrupt/resume checkpoints.
*Genkit-Go* is the only Go framework with a real eval framework (dataset runner + Developer UI) and
prompt versioning (Dotprompt). *agno-Go* brings teams, snapshot-resumable workflows, and explicit
prompt-injection guardrails. *langchaingo* and *swarmgo* are broad/lightweight respectively but lack
MCP, durable workflows, and evals. **The scarcest capability in the Go ecosystem — and thus the best
differentiation bet — is a proper eval/scorer harness.**

---

## 3. Gap table B — coding-agent-loop UX

Three features are **near-universal across all six** coding agents and absent from `agent/`:

| # | Feature | mcpkit today | Verdict |
|---|---|---|---|
| 1 | **Tiered permission ladder** (read-only → auto-edit → full-auto) + per-tool allow/ask/deny + fast toggle | `FilterSource` is static-only; the Runner auto-dispatches every call | **Build — Phase 0 (the single most consistent competitor feature)** |
| 2 | **Checkpoint / rewind** restoring file *and* conversation state | none | Build — surface (`agent/host`); conversation side in Phase 1 |
| 3 | **Mid-turn interrupt** (Esc cancels the running call, not the session) | ctx-cancel kills the whole turn | **Build — Phase 1** |

Second tier (2–3 agents each): **context compaction/summarization** (steerable, with a
"context-remaining" indicator), **subagents with isolated context**, **session persistence /
resume / fork**, **slash + custom commands**, **lifecycle hooks**, **repo map** (aider's ranked
tree-sitter summary), **LSP-in-loop** (OpenCode), **sandboxing** (Codex/Gemini), and a **soft
tool-call budget gate** (Cursor's "~25 calls then Continue").

**Takeaway for a coding surface:** the non-negotiable trio to reach parity is (1) the permission
ladder, (2) checkpoint/rewind of file *and* conversation state, (3) the fine-grained interrupt.
After that, compaction, isolated-context subagents, and session resume/fork separate a demo from a
production coding agent. Repo map, hooks, and LSP-in-loop are differentiation, not table stakes.

---

## 4. Build-out roadmap — what it would take

Four invariants decide *where each piece lives*:

- **A1** — LLM/provider dependencies live only in `agent/`.
- **A2** — events and messages are wire-serializable; this is the lever that makes persistence cheap.
- **A6** — model-facing thing (returns a `Message`, `ToolResult`, injected context, a proactive
  turn) → `agent/`; a general mechanism a *non-agent* consumer would want → `client/` or a sibling
  module. **Corollary:** anything trafficking in `agent.Message`/`agent.Event` cannot live in the
  root `stores/` package (that would force root→agent). Agent-typed stores get their interface +
  in-memory default *in* `agent/`, with heavy backends in siblings (`agent/store/redis`), mirroring
  the existing `stores/` + `stores/redis` split.
- **The Runner stays stateless over history.** `Run(ctx, history, emit)` clones history and returns
  only appended messages — resume, fork, and checkpoint fall out for free. Persistence and memory
  **wrap** `Run`; they are not baked into it.

### E. Tool-call approval / permission ladder — *do first* (effort: S–M)

New: `ApprovalPolicy` interface + batteries-included `TieredApproval` (per-tool/per-source rules,
read-only auto-allow tier, session-scoped remember-cache) + `RunnerConfig.Approval` (nil = today's
behavior, fully backward compatible), in `agent/approval.go`. **Gate in `Runner.callTool`
(runner.go:269) before `Tools.Call`.** Key reuse: the **"Ask" outcome routes through the existing
`ElicitationCoordinator`** — an approval prompt *is* an elicitation, and its strict FIFO already
solves the "parallel dispatch stacks N dialogs" problem, so no new UI seam is needed. "Deny" feeds a
model-visible `"denied by policy"` result back (error-as-feedback); the turn continues. Tiered modes
(YOLO / ask-once / always-ask / read-only-auto) are pure config. Risk: keep the gate outside the
dispatch emit-mutex; ensure a denied call still yields a well-formed `RoleTool` message.

### G. Anthropic provider — *early* (effort: M)

The `Provider` interface is already correctly shaped; this is a translation layer, not a redesign.
Follow the `openai_provider.go` no-SDK precedent (net/http + servicekit SSE reader). Mapping:
`Instructions`→top-level `system`; `RoleAssistant.ToolCalls`→`tool_use` blocks; `RoleTool` +
`ToolCallID`→`tool_result` blocks (the `ToolCallID` field already carries `tool_use_id`); SSE
`content_block_delta`→`Delta` (`text_delta`/`input_json_delta`/`thinking_delta`/usage — the
`Accumulator` folds these unchanged). Structured output = a forced synthetic tool via the existing
`ToolChoice` machinery. Risks: prompt caching (`cache_control`) and thinking-signature round-tripping
need a **provider-scoped option**, not neutral-`ProviderRequest` pollution. *Consult the `claude-api`
skill for current model IDs, params, and caching semantics before implementing.*

### F. Structured output in the loop (effort: S–M)

Add `RunnerConfig.ResponseSchema` + `TurnResult.Structured`. **Opinionated:** do not attempt tools
and `response_format` simultaneously (many OpenAI-compat servers forbid it; Anthropic can't). Run the
tool loop exactly as today; when the model emits its terminal no-tool-call response, do **one
finalizing `Provider.Generate`** with the schema set to coerce the answer. Portable, reuses
`Generate` + `Accumulator`, costs one extra call per structured turn. Add a bounded retry on invalid
JSON; degrade to schema-in-prompt where unsupported.

### H. Eval / scorer harness — *early, compounding* (effort: M)

New `agent/eval/` sub-package: `Case` / `Scorer` / `Suite`. Reuses `StubProvider` (deterministic
model), `TracerProvider` spans + `TurnResult.Messages` + the captured `emit` stream as the transcript
to score. Ship deterministic scorers first (exact/contains, tool-was-called, step-count, no-error);
put **LLM-as-judge behind the `Provider` seam** as one opt-in scorer, build-tagged off the default CI
path (like the integration tests). Standing this up early gives every later phase regression coverage.

### D. Persistence / durability — *the keystone* (effort: M; L for mid-turn)

`RunStore` interface + in-memory default in `agent/runstore.go`; durable backends as sibling modules
(`agent/store/redis`, `agent/store/sql`). Follows the `stores/` gRPC-style convention
(`Method(ctx, req) (resp, error)`, app-state on the response, error reserved for storage faults):

```go
type RunStore interface {
    CreateRun(ctx, CreateRunRequest) (CreateRunResponse, error) // returns RunID
    AppendMessages(ctx, AppendMessagesRequest) (…, error)
    AppendEvents(ctx, AppendEventsRequest) (…, error)           // optional: replay/audit
    LoadRun(ctx, LoadRunRequest) (Run, error)
    ForkRun(ctx, ForkRunRequest) (RunID, error)                 // copy log, diverge
}
```

**Not in the Runner** — the surface persists `TurnResult.Messages` at the existing
`append(history, result.Messages...)` site (the host's `RunTurn`, `agent/host`); a `PersistingEmit` helper tees
the event stream into `AppendEvents`. **Resume = `LoadRun` → `Run(ctx, run.Messages, emit)`;
Fork = `ForkRun` then diverge** — both essentially free because `Run` is stateless over history. Ship
**per-turn** durability first (fully covers resume/fork/session lifecycle); mid-turn replay requires
tool-call idempotency the module can't guarantee for arbitrary MCP servers, so treat it as a later,
opt-in feature gated on idempotency metadata.

### I. Interrupt granularity + session lifecycle (effort: M)

Give each dispatched call a **child context keyed by `call.ID`** (today `dispatch`, runner.go:241,
runs all calls under one ctx). Add a consolidated `TurnRequest`/`Runner.RunTurn(ctx, TurnRequest)`
carrying a `Control <-chan Control` (existing `Run` delegates to it — avoids a breaking signature
change per C2). A control listener cancels the specific call's child ctx; the cancelled call feeds
`"cancelled by user"` back as its `RoleTool` result and **the turn continues** — the whole point of
the finer grain. Reuses `ClientSource.Call`'s ctx-to-wire threading for *real* MCP-level
cancellation. A `tool-cancelled` event kind may be warranted (respect the A2 round-trip test).
Session lifecycle (resume/fork) rides entirely on D.

### A. Memory (effort: L)

Four sub-capabilities, four homes — memory is a pre/post-turn wrapper around `Run`, not a Runner
change:

1. **Working memory** — a model-managed scratchpad. First-class `MemorySource` (a `FuncSource`
   exposing `remember`/`recall`/`forget`) in `agent/memory.go`, with optional per-turn summary
   injection. Model-facing → `agent/`. (S)
2. **Conversation store** — this *is* D's `RunStore` at session grain; don't build a second thing.
3. **Semantic recall** — new `Embedder` seam (sibling to `Provider`, → `agent/`) + `VectorStore`
   (interface in `agent/`, backends in siblings). Retrieved context enters via the **same pre-turn
   injection path** as `EventInjectionPolicy.Drain` (produces `InjectedContext`/system messages) — reuse
   that shape, don't invent a parallel one. (L)
4. **Compaction** — `Compactor` / `SummarizingCompactor` backed by `Provider.Generate`, run pre-turn
   when a token estimate trips a threshold; keep a verbatim tail window and summarize only the head.
   Definitively `agent/` (needs a model + turn). (M)

Risks: compaction dropping load-bearing detail; recall poisoning context with irrelevant hits;
embedding latency on the turn's critical path (do it async, feeding the injection buffer the way
`AddAsyncFunc` already feeds completions). Depends on D for the conversation store.

### B. Multi-agent (effort: S–M → M–L)

A sub-agent is **a `Runner` exposed to a parent as a tool** — `ToolSource` + `FuncSource` +
`MultiSource` already compose to make this trivial:

- `AgentSource` implements `ToolSource`; `Call(task)` runs the child `Runner.Run` over its **own
  isolated `[]Message`** and returns the child's final `Text`. Isolation is structural (a separate
  slice); nothing in the Runner changes.
- **Supervision** = a coordinator `Runner` whose `Tools` is a `MultiSource` of `AgentSource`s
  (existing aggregation + collision semantics + `Selector` routing).
- **Handoff** (transfer control, don't return) is the one case that doesn't fit agent-as-tool: model
  a small `Team`/`Orchestrator` *above* the Runner that swaps the active Runner on a handoff signal,
  keeping the Runner unaware.
- **Nested event streams** are the main subtlety: a sub-agent's `emit` must surface to the parent's
  surface with a sub-agent scope on the wire envelope (not new `Event` fields — preserve A2).

Guard runaway recursion/cost with depth + aggregate `MaxSteps`/budget caps across the tree.

### C. Workflows — *sibling module, build last* (effort: XL)

By A6, a general graph engine (nodes, edges, branch, parallel, suspend/resume) is a mechanism a
non-agent consumer would want, so it belongs in a **`workflow/` sibling module** (depends on `core/`
+ a state store), with `agent/` providing only a `ModelNode` adapter that wraps a `Runner`. Two
reuses matter and are easy to get wrong:

- **`stages.go` is NOT the workflow engine** — it's synchronous *event transformation inside a node*,
  not control flow. Don't overload it. (Per the settled pushdown decision, those stages graduate
  into gocurrent when a second consumer appears.)
- **The trigger machinery IS the suspend/resume primitive.** A durable `SuspendNode` waiting on an
  external event/approval resumes when a matching `IncomingEvent` arrives — exactly
  `TriggerBinding` + `TriggerPolicy.OnEvent`. "Wait for `task.completed`" and "wait for user
  approval" become the same mechanism.
- **Durability leans entirely on D** — checkpoint node state to the `RunStore` between transitions.

**Opinion:** build a *minimal durable state machine* (linear + branch + one-level parallel +
event-driven suspend), not a maximal DAG product. For Temporal-grade durability, integrate rather
than reimplement.

---

## 5. Sequencing & phased roadmap

```
D (run store) ──┬──> I (session resume/fork)
                ├──> A (conversation store, semantic-recall backend)
                └──> C (durable workflows)   [also reuses TriggerPolicy]

E, G, F, H  ── independent, no upstream deps (fast SDK-credibility wins)
B  ── on Runner + ToolSource (present); better with D + A
I (finer interrupt) ── Runner change, independent of D
```

Each **phase = an epic** merging stacked per-issue PRs into an integration branch (the epic-895
pattern that built `agent/` itself); each bullet = one PR.

**Phase 0 — "feels like a real SDK" (parallel, no deps): ✅ SHIPPED.**
`ApprovalPolicy` gate + `TieredApproval` (E, issue 929) · Anthropic provider (G, issue 930) ·
structured final output (F, issue 931) · `agent/eval` deterministic scorers + LLM-judge (H, issue 932).
All merged. Deferred riders: extract the shared provider SSE loop (issue 952); Anthropic prompt
caching / extended thinking (issue 953, best sequenced with P2 compaction — the cache breakpoint is
the compactor's stable-head boundary).

**Phase 1 — durability & control: ✅ SHIPPED.**
`RunStore` interface + in-memory (D, issue 933) · Redis sibling `agent/store/redis` (issue 934) ·
Postgres/SQLite sibling `agent/store/gorm` (issue 960) · `agent/host` persist/resume/fork + agentchat
`--session-store` / `--session` (issue 935) · per-tool-call cancellation via `TurnRequest`/`Control`
(I, issue 936) · `tool-cancelled` event (issue 937). All merged (PRs 955–959, 961, 965, 967, 968).
Review-driven riders shipped in the same arc: ForkRun atomicity + idempotent-retry contract (issue
963 — `NewRunID` is an idempotency key; each backend uses its database's native atomicity primitive,
transaction vs Lua script, rationale doc'd on both ForkRuns), `Message.Timestamp` stamped at the
store boundary (issue 964), and fork-at-point + `ForkPoint` lineage (issue 962 — the conversation
half of checkpoint/rewind; `ForkPoint` is a message count, never a timestamp). Deferred with a full
design: tool-result offloading (issue 966 — `OffloadingSource` + `ToolResultStore` +
`read_tool_result`; lossless pay-on-demand beats lossy compaction for tool outputs, sequence with
P2 memory).

**Interactive surface — agentchat playground (epic 983): ✅ SHIPPED.** The UX layer that makes P1/P2
usable and delivers the coding-agent-loop rows (Gap table B: checkpoint/rewind conversation side,
finer interrupt). `agent/host` gains named-provider connections + registry (per-session and
mid-session provider switching, issues 984/985), a `CommandRegistry` + `App.Dispatch` slash-command
system (986), `HostEvent`/`Observer` domain-event fan-out replacing io.Writer prints (992 — host
lifecycle events, not "UI" events; `UIPrompt` dropped as it's request/response not fire-and-forget),
and `RunStore.ListRuns` + a `/sessions` picker (986). `cmd/agentchat` is the thin CLI over it: an
**inline** bubbletea TUI (997 — commit-to-scrollback, native scroll/copy/paste/persist; alt-screen
`--ui notebook` deferred to issue 1001) and `make pg` (988). Layering rule: host concerns in
`agent/host` so a web surface reuses everything and swaps only stdin/stdout. Follow-ups: run
retention (999), paged session picker (1000), provider-routing policy (991).

**Phase 2 — memory: ✅ SHIPPED (epic issue 926, closed).** All four tracks landed — see the diagram in
`docs/AGENT_MEMORY_FLOW.md`.
- **Tool-result offloading** (the just-in-time-context opener, lossless/pay-on-demand): `OffloadingSource`
  + `ToolResultStore` + in-memory (966) · durable redis (TTL) + gorm (configurable table, `PruneExpired`)
  (971) · dep-free `FileToolResultStore` (977) · host `Config.Offload` + agentchat `--offload-threshold` /
  `--offload-dir` (972) · turn-end `Result.Messages` redaction (973). PRs 970/975/976/978/981.
- **Working memory** (938, PR 1004): `MemorySource` (leaf ToolSource: remember/recall/forget) over the
  `MemoryStore` seam (in-memory substring default); pluggable via `WithMemoryStore`.
- **Eval harness** (974, PR 1005): multi-turn `eval.Scenario` / `RunScenario` (threads history + a shared
  MemorySource; one Runner reused), `NotContains` scorer, and `agent/eval/longmemeval` `SmokeScenarios()`
  (hand-authored, NOT the dataset — real loader is 1014). `Scenario.NewMemoryStore` /
  `MemCase.NewCompactor` factories grade different impls through the same scenarios.
- **Compaction** (939, PR 1008): `SummarizingCompactor` + `TokenEstimator`/`CharTokenEstimator` +
  `RunnerConfig.Compactor` hook (in the Runner, top of the turn, so the eval harness can grade it);
  `EventCompaction`; host `Config.Compaction` + agentchat `--compact-tokens`/`--compact-keep-recent`.
- **Semantic recall** (940, PRs 1017 + 1025): `Embedder` seam (`Embedding` type + `Cosine` method;
  `OpenAIEmbedder` no-SDK `/embeddings` + `StubEmbedder`) + `InMemorySemanticStore` (brute-force cosine
  behind `MemoryStore`; retrieval stays an impl detail — no separate `VectorStore` public seam) +
  `ScoredMemory` results (`ListMemories(Query, Limit)`) + pre-turn recall auto-injection
  (`RecallRelevant`, `Config.Memory.InjectRecall`/`RecallMinScore` poison-guard). `agent.embed` /
  `agent.memory.recall` spans.
- **Injection correctness/budget** (1010 transient-not-stacked, 1011 recency-budget) + the
  `InjectionPolicy` → `EventInjectionPolicy` rename (memory injection is its own step).

Deferred follow-ups (all filed): pgvector semantic store (1019) · Scorer/Reranker multi-signal (1020) ·
VectorStore for doc-RAG (1021) · distillation write path (1022) · metrics seam (1023) · unified
injection arbiter (1024) · explicit context-assembly pipeline (1026) · faster cosine (1018) ·
durable/session-scoped MemoryStore backends (1003) · LongMemEval loader (1014) + eval adapter seam
(1015) · binary offloading (979) · streaming/handle-based large results (980).

**Phase 3 — composition: ✅ SHIPPED (epic 927, closed).** Design frame: `docs/AGENT_COMPOSITION.md`
(the two-axis model — context via injection, control via tools + signals; observability the third
channel). Multi-agent is not a new engine; it wraps the same stateless `Runner`.
- `AgentSource` (941, PR 1028): agent-as-tool — a child Runner exposed to a parent as a tool over its
  own isolated slice; depth (`MaxDepth`) + ctx-threaded aggregate call budget (`WithAgentCallBudget`)
  guards; supervision falls out via a `MultiSource` of `AgentSource`s.
- `SubAgentEvent` nesting (942, PR 1029): the child's turn-lifecycle emit stream surfaces to the
  parent surface, scoped on an envelope (`Event` stays wire-flat, A2). NOT domain-event
  subscriptions — a sub-agent's `subscribe_events` injects into its OWN context.
- `Team` handoff (943, PR 1030): transfer control (not call-and-return) via `transfer_to_<name>`
  tools + a shared-thread swap loop; static membership; `MaxHandoffs` ping-pong cap; per-Run ctx
  handoff signal. `examples/multi-agent` (PR 1034) demos both modes offline.
Host surface: `Config.SubAgents` personas (agent-as-tool via config) + `HostSubAgentEvent` nested
rendering shipped (1031 part 1, PRs 1040/1041); Team-in-host handoff (1042) + nested per-sub-agent
provider/servers (1043) deferred. Agent examples grouped under `examples/agents/` with a shared
`llm.json` (apiKeyEnv only, no secrets; routers are connections).
Deferred (new scope, filed): aggregate step/token tree budget
(1032) · parallel fan-out (1033) · async sub-agents / Task form (1035) · upward signals +
runner-control meta-tools + interruptible turn (1036) · model-driven dynamic composition + agent
catalog (1038). Handoff-as-injection (per-agent actor context) is the general form of `Team`.

**Phase 4 — orchestration:**
`workflow/` engine (C) · `SuspendNode` via `TriggerPolicy` + `RunStore` checkpoint (C) ·
`ModelNode`/`ToolNode` adapters (C).

**Bottom line.** Phase 0 makes the SDK *look* competitive with zero upstream dependencies — ship it
first and in parallel. Phase 1 (persistence) is the structural keystone that unlocks durable
sessions, memory, and workflows; don't attempt C before it. And resist building C as a maximal DAG
framework — the mcpkit-native move is a minimal durable state machine that reuses the existing
trigger machinery for suspend/resume, keeping the whole system MCP-native and event-driven rather
than importing a second orchestration paradigm.

---

## Appendix — key files

| Area | Files |
|---|---|
| Loop / approval / interrupt / structured | `agent/runner.go` |
| Provider / structured / embedder / Anthropic | `agent/provider.go`, `agent/openai_provider.go` |
| Tool layer (AgentSource/MemorySource hinge) | `agent/toolsource.go`, `client_source.go`, `func_source.go`, `multi_source.go`, `filter_source.go` |
| Approval "ask" reuse | `agent/elicitation.go` |
| Suspend/resume reuse | `agent/triggers.go`, `agent/injection.go`, `agent/incoming_event.go` |
| Event transformation (not control flow) | `agent/stages.go`, `agent/events.go` |
| Surface: persist / resume / recall / compaction | `agent/host/app.go`, `agent/host/metatools.go`, `agent/host/tasks_bg.go` |
| Invariants | `docs/AGENT_DESIGN.md`, `agent/CONSTRAINTS.md` |
