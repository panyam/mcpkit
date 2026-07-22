# mcpkit Agent SDK — Competitive Status & Gap Analysis

**What this is.** A living assessment of the mcpkit `agent/` host layer against (a) general agent
frameworks (Mastra, Eino, Genkit-Go, langchaingo, swarmgo, agno-Go) and (b) real coding-agent loops
(Claude Code, Cursor, Gemini CLI, aider, Codex, OpenCode). The first edition (below the fold) framed
these as "what would it take to build a complete agent SDK." **Most of that roadmap has now shipped**
— this edition re-baselines to *where we actually stand* and *what is genuinely still missing*,
distinguishing **tracked** gaps (open issues) from **untracked** ones.

**Status snapshot:** `main` as of 2026-07 (post the Phase 0–3 stack; issues #929–#1044). Method:
direct inventory of `agent/` source + open-issue sweep. Verdicts respect `agent/CONSTRAINTS.md`
(A1 dependency direction, A2 wire-serializability, A6 mechanism-vs-policy layering).

---

## 1. Headline: how are we doing?

**Very well.** The four-phase build-out sketched in the first edition landed almost in full over ~140
commits. The agent SDK today is no longer "a minimal loop" — it has approval gating, a native
Anthropic provider, in-loop structured output, an eval harness, durable persistence with fork/rewind,
per-tool-call cancellation, tiered memory (working + semantic + compaction), tool-result offloading,
sub-agents + team handoff, and a real host surface (slash commands, connection registry, TUI). The
distinctive strengths still hold and are now better supported: the **async control plane**
(triggers/events/tasks as model-facing meta-tools), **MCP-native wire-agnosticism**, **A2
wire-serializability**, and **zero-overhead SEP-414 tracing**.

**Four structural phases are now open as epics**, in rough dependency order: **Phase 4 — durable
workflows** (#928), **Phase 5 — provider control & decoding fidelity** (#1050: logprob #1053, grammar
decoding #1054, +caching/thinking #953), **Phase 6 — test-time compute & routing reliability** (#1051:
sampling/vote #1056, confidence-gated cascades #1057; adjacent routing #991), **Phase 7 — safety &
guardrails** (#1052: prompt-injection spotlighting #1058). The items the previous edition listed as
*untracked* (logprob/grammar, guardrails, sampling/vote, cascade trigger, coding-surface) have all been
**promoted to tracked phase children** — see §5a. Nothing identified is left untracked (the two opt-in
Phase-7 extensions are now filed as #1060/#1061). Beyond the phases, a rich refinement backlog on the
shipped primitives remains (mostly tracked).

---

## 2. Status scorecard

Legend: ✅ shipped · 🟡 partial/shipped-with-follow-ups · ⏳ tracked (open issue) · ❌ untracked gap.

| Area | Status | As-built (file) | Remaining |
|---|---|---|---|
| **Tool-call approval / permission ladder** | ✅ #929 | `agent/approval.go` — `ApprovalPolicy`, `TieredApproval`, `ApprovalMode` {AlwaysAsk/ReadOnlyAuto/AlwaysAllow} + per-tool `RuleAsk/Allow/Deny`; "ask" routes through `ElicitationCoordinator.Confirm`; `EventToolDenied`; host `/approve` | — |
| **Anthropic-native provider** | 🟡 #930 | `agent/anthropic_provider.go` — no-SDK, content-block↔Delta, `thinking_delta`→reasoning; structured output via forced synthetic tool | prompt caching + extended-thinking **⏳ #953** |
| **Structured output in the loop** | ✅ #931 | finalizing `Generate` (`runner.go` `finalizeStructured`, retry×2); `RunnerConfig.ResponseSchema` → `TurnResult.Structured` | — |
| **Eval / scorer harness** | 🟡 #974,#932 | `agent/eval/` — `Case`/`Scorer`/`Suite`/`Scenario`; 8 deterministic scorers; `Judge` (build-tagged); LongMemEval *smoke* scenarios | external-benchmark adapter **⏳ #1015**; real LongMemEval loader **⏳ #1014** |
| **Persistence (RunStore) + fork/rewind** | ✅ #960,#962,#963,#986 | `agent/runstore.go` — full interface + `InMemoryRunStore`; redis + gorm (pg/sqlite) backends; `ForkRun{AtMessage}` checkpoint fork; `ListRuns`; `Message.Timestamp` | retention/GC **⏳ #999** |
| **Per-tool-call cancellation / interrupt** | ✅ #936,#937 | `runner.go` `TurnRequest`/`Control` channel (per-`CallID`); `EventToolCancelled` | — |
| **Working memory** | ✅ #938 | `agent/memory.go` — `MemorySource` remember/recall/forget; `Summary`/`RecallRelevant`; `InMemoryMemoryStore` | durable/session-scoped backends **⏳ #1003** |
| **Semantic recall (vector)** | ✅ #940,#1019 | `agent/embedder.go` (`Embedder`, `OpenAIEmbedder`), `agent/semantic_memory.go` (`InMemorySemanticStore`), gorm **pgvector** store; pre-turn recall auto-injection | standalone doc-RAG VectorStore **⏳ #1021**; reranker **⏳ #1020**; auto-distillation **⏳ #1022** |
| **Compaction / summarization** | 🟡 #939,#1011 | `agent/compaction.go` — `SummarizingCompactor`, `TokenEstimator`/`CharTokenEstimator`, `EventCompaction`; pre-loop hook; budgeted summary injection | mid-turn compaction **⏳ #1006**; real tokenizer **⏳ #1007** |
| **Tool-result offloading (context mgmt)** | ✅ #966,#971,#972 | `agent/offloading_source.go` + `ToolResultStore` (mem/redis/gorm); `read_tool_result` (offset/limit/grep) | streaming/handle-based very-large results **⏳ #980,#979** |
| **Sub-agents (agent-as-tool)** | ✅ #941,#942,#943,#1031 | `agent/agent_source.go` (`AgentSource`, depth+budget caps), `SubAgentEvent` nesting, `agent/team.go` (`Team` handoff), declarative host personas | richer composition (structured I/O, parallel fan-out, async/task sub-agents, dynamic catalog) **⏳ #1032,#1033,#1035,#1036,#1038,#1043** |
| **Host surface** | ✅ #984–#992 | slash-command registry, `ConnectionRegistry` + runtime `/provider`, `HostEvent`/`Observer` render seam, notebook renderer (#1001), interactive `/mcp` + `/sessions` overlay (#1095, `focusLayer`/`modalHost` seam + `client.Group.Reconnect`), bubbletea TUI, playground | context-assembly pipeline **⏳ #1024,#1026**; full focus-stack (base+overlay as peer layers) **⏳ #1063 C4**; per-server login/tools overlay actions **⏳ #1116,#1117** |
| **Observability** | 🟡 | SEP-414 tracing (`agent.turn/step/tool`, `agent.memory.recall`) | OTel **metrics** seam (counters/histograms) **⏳ #1023** |
| **Durable workflows / graphs (Phase 4)** | ⏳ #928 | — | engine **#944**, `SuspendNode` via TriggerPolicy+RunStore **#945**, `ModelNode`/`ToolNode` **#946** |
| **Provider routing / cascades** | ⏳ #991 | only `FailoverProvider` (failure+cooldown) today | per-turn/per-role routing **#991**, router presets **#1044**, confidence-gated cascade **#1057** (Phase 6) |
| **Provider control & decoding fidelity (Phase 5)** | ⏳ #1050 | structured output via finalizing `Generate` only | logprob exposure **#1053**, grammar/guided decoding **#1054**, Anthropic caching/thinking **#953** |
| **Test-time compute (Phase 6)** | ⏳ #1051 | achievable by host loop; `#1033` covers sub-agent fan-out only | sampling/vote helper **#1056**, `FailoverProvider` quality trigger **#1057** |
| **Safety & guardrails (Phase 7)** | ⏳ #1052 | approval ladder (#929); event stages exist (`stages.go`) but no shipped guardrail Transform | prompt-injection spotlighting **#1058**; opt-in extensions (AgentDojo eval, constitutional gate) unfiled |
| **Coding-surface: sandboxing, hooks, repo map, LSP** | ⏳ #1059 | — | scope decision (agent/ vs coding agent built on it) **#1059** |

**Bottom line:** Phases 0–3 are effectively **done**. Four structural phases are open and fully scoped
in issues: **Phase 4** (workflows, the last big engine piece) plus **Phases 5–7** (provider decoding
control, test-time compute, safety) which promote the former §5b "untracked" gaps into tracked epics.
Everything else open is refinement of shipped primitives.

---

## 3. Gap table A — agent-framework parity (updated)

| Capability | Leaders | mcpkit status |
|---|---|---|
| Tiered memory: working + semantic recall + compaction | Mastra, langchaingo, agno-Go | ✅ **at parity** (#938/#940/#939) + pgvector (#1019); reranker/distillation tracked (#1020/#1022) |
| Multi-agent / handoffs / sub-agents | all five + Mastra | ✅ **at parity** (AgentSource #941, Team #943); richer composition tracked (#1032–#1038) |
| Eval / scorer framework | Mastra, Genkit *(rare in Go)* | ✅ **shipped** (#974) — a **differentiator vs the Go field**; external-suite adapter tracked (#1015) |
| Native providers beyond OpenAI-compat | all | ✅ Anthropic (#930); caching tracked (#953) |
| Structured output *inside the loop* | Mastra, Genkit, Eino | ✅ **shipped** (#931) |
| Durable suspend/resume workflows; branch/parallel | Mastra, Eino, agno-Go, Genkit | ⏳ **the remaining parity gap** — Phase 4 (#928/#944/#945/#946) |
| RAG pipeline (chunk/embed/retrieve/index) | Mastra, Eino, Genkit, agno-Go | 🟡 recall path shipped; standalone doc-RAG VectorStore tracked (#1021) |
| Prompt versioning / templating | Genkit (Dotprompt) | ❌ still minimal (`Instructions` + skills) — no issue |
| Voice (STT/TTS) | Mastra only | ❌ non-goal (no Go competitor either) |

**Net:** mcpkit has moved from "behind on memory/multi-agent/evals" to **at or ahead of the Go field**
on those, with a proper eval harness that only Genkit matches. Durable workflows are the one place the
TS/heavier frameworks still lead, and that gap is scoped and scheduled.

## 4. Gap table B — coding-agent-loop UX (updated)

The three once-universal gaps are now mostly closed:

| # | Feature | mcpkit status |
|---|---|---|
| 1 | **Tiered permission ladder** | ✅ shipped (#929) — modes + per-tool rules + runtime `/approve` |
| 2 | **Checkpoint / rewind** | 🟡 **conversation-state** side shipped (fork-at-point #962, resume/`ListRuns` #986); **file-state** rewind is a coding-surface concern, not in the general SDK |
| 3 | **Mid-turn interrupt** | ✅ shipped (#936/#937) — cancel one call, turn continues |

Second-tier: context compaction ✅ (#939), isolated-context subagents ✅ (#941), session
persistence/resume/fork ✅ (#960/#962), slash + custom commands ✅ (#985). **Still absent (untracked):**
lifecycle **hooks** (PreToolUse/PostToolUse), **sandboxing** of tool execution, **repo map**,
**LSP-in-loop**, and a soft **tool-call budget gate**. These are coding-*surface* features; whether
they belong in `agent/` or in a coding-agent built on it is a scoping decision (see §5).

---

## 5. What's still missing

### 5a. Tracked (open issues — planned work)

**The four structural phases (epics):**
- **Phase 4 — durable workflows:** epic #928 → engine #944, `SuspendNode` (reuses TriggerPolicy +
  RunStore) #945, `ModelNode`/`ToolNode` adapters #946. *The last structural engine gap.*
- **Phase 5 — provider control & decoding fidelity:** epic #1050 → logprob/token-confidence on the
  `Provider` seam #1053, grammar-constrained/guided decoding passthrough #1054; +Anthropic prompt
  caching & extended thinking #953. Capability-optional fields, nil = today's behavior (A2/CONSTRAINTS).
- **Phase 6 — test-time compute & routing reliability:** epic #1051 → sampling+aggregate helper
  (self-consistency / Best-of-N + verifier rerank) #1056, `FailoverProvider` quality-score trigger for
  confidence-gated cascades #1057. Complements upfront routing #991 (selection vs escalation).
- **Phase 7 — safety & guardrails:** epic #1052 → prompt-injection spotlighting/datamarking `Transform`
  stage for untrusted tool output #1058; opt-in extensions AgentDojo eval suite #1060 and constitutional
  pre-dispatch critique gate #1061. The coding-surface scope decision (sandboxing/hooks/repo map/LSP)
  is #1059.

**Refinement backlog on shipped primitives:**
- **Provider routing / cascades:** #991 (per-turn/per-role model selection over `ConnectionRegistry`),
  #1044 (openrouter/litellm presets). Today only `FailoverProvider` (failure-triggered).
- **Prompt caching + extended thinking:** #953 (Anthropic, provider-scoped) — note the documented
  cache-vs-`Selector` prefix-stability tension.
- **Memory depth:** standalone doc-RAG `VectorStore` #1021, Scorer/Reranker seam #1020, auto-distillation
  write-path #1022, durable/session-scoped MemoryStore #1003, faster cosine #1018.
- **Compaction depth:** mid-turn compaction #1006, token-accurate estimator #1007.
- **Sub-agent composition:** aggregate tree budget #1032, structured I/O + parallel fan-out #1033,
  async/task sub-agents #1035, upward signals / runner-control meta-tools #1036, dynamic agent catalog
  + transfer graph #1038, full nested per-agent config #1043, Team config in the App loop #1042.
- **Context assembly:** explicit pre-turn pipeline #1026, unified injection budget with a final arbiter
  #1024.
- **Eval:** external eval/conformance adapter seam #1015 (see §6), real LongMemEval loader #1014.
- **Ops:** OTel metrics seam #1023, RunStore retention/GC #999, large/binary tool results #980/#979,
  thinking-hint stream parser #989, session-picker paging #1000.

### 5b. Untracked (no issue yet)

Nothing. The six gaps this section previously listed — logprob exposure, grammar/guided decoding, the
sampling/vote helper, the prompt-injection guardrail, the `FailoverProvider` quality-score trigger, and
the coding-surface scoping call — are now tracked phase children (Phases 5–7 and #1059; see §5a), and
the two Phase-7 extensions (AgentDojo eval suite #1060, constitutional critique gate #1061) have been
filed. Everything identified is tracked; new gaps get filed as they surface.

---

## 6. Validation & benchmarks

Unchanged in substance — and issue **#1015** now tracks exactly the recommendation below (external
eval/conformance adapter seam). The competitor projects still ship no reusable suite (Eino none; Mastra
a TS judge lib; Genkit flow-coupled; aider's harness aider-coupled but its polyglot problem set is
reusable data). MCP's own conformance is wire-level only — **no first-party agent-level MCP conformance
suite exists.** Practical stand-ins, reachable via the existing OpenAI-compatible endpoint / an HTTP
shim:

| Suite | Validates | Integration cost |
|---|---|---|
| **BFCL v3/v4** | tool-calling fidelity | lowest — OpenAI-compatible `--skip-server-setup` |
| **τ²-bench** | multi-turn tool-agent-*user* loop | one thin Python `HalfDuplexAgent` proxy → our Runner |
| **SWE-bench Verified** | end coding-task resolution | near-zero (predictions JSONL); adopt when a coding agent exists |
| **AgentDojo** | prompt-injection security | optional; pairs with the §5b spotlighting gap |

**Shape for the eval harness:** the internal `agent/eval` harness (StubProvider + spans, ✅ shipped) is
the CI gate; the **external-benchmark adapter (#1015)** — starting with BFCL — is the missing second
layer. mcpkit's own `agent/eval` scorer harness is itself a differentiator: only Genkit matches it in
the Go field.

---

## 7. Advanced techniques enabled by our primitives

The technique catalog (research through 2025, mapped to primitives) is unchanged in its analysis — but
the shipped memory + sub-agent + structured-output work has **moved most of it from "needs a roadmap
primitive" to "buildable today."** Updated status:

### Now buildable on shipped primitives
- **Sleep-time compute / Generative-Agents reflection / A-MEM evolution** — trigger/injection async
  control plane + `SummarizingCompactor` + `MemorySource` (all ✅). ([2504.13171](https://arxiv.org/abs/2504.13171),
  [2304.03442](https://arxiv.org/abs/2304.03442), [2502.12110](https://arxiv.org/abs/2502.12110))
- **Semantic recall / MemGPT paging** — `Embedder` + `InMemorySemanticStore`/pgvector + injection (✅).
  ([2310.08560](https://arxiv.org/abs/2310.08560))
- **Multi-agent debate / Mixture-of-Agents / CAMEL / supervisor** — `AgentSource` + `MultiSource` +
  `Team` (✅). ([2305.14325](https://arxiv.org/abs/2305.14325), [2406.04692](https://arxiv.org/abs/2406.04692))
- **Best-of-N + verifier / CRITIC / judge panel** — `ToolChoice` forced-tool + `ResponseSchema` +
  `FuncSource` verifiers + eval scorers (✅). ([2110.14168](https://arxiv.org/abs/2110.14168),
  [2305.11738](https://arxiv.org/abs/2305.11738), [2404.18796](https://arxiv.org/abs/2404.18796))
- **Tool retrieval via `Selector`** — narrow a big `MultiSource` per step (✅; relevance needs an
  embedder, now present). ([2410.14594](https://arxiv.org/abs/2410.14594))
- **CodeAct** — `execute_code` `FuncSource` + forced-tool + error-feedback (✅).
  ([2402.01030](https://arxiv.org/abs/2402.01030))

### Still gated on missing infra (from §5b)
- **Grammar-guided decoding** ([2307.09702](https://arxiv.org/abs/2307.09702)) — gated on §5b #2.
- **Calibrated uncertainty / abstention** ([2407.16221](https://arxiv.org/abs/2407.16221)) — gated on
  logprob exposure §5b #1.
- **Model cascades (FrugalGPT)** ([2305.05176](https://arxiv.org/abs/2305.05176)) — gated on §5b #5 /
  routing #991.
- **Spotlighting injection defense** ([2403.14720](https://arxiv.org/abs/2403.14720)) — gated on §5b #4.
- **Tree search (ToT/LATS/MCTS)** — sub-agents exist, but no search/fork controller helper (§5b #3
  neighborhood).

**Demo shortlist, re-ranked for what ships today:** (1) tool-retrieval-via-`Selector`, (2) Best-of-N
with a `FuncSource` verifier, (3) sleep-time compute (the async-control-plane showcase), (4) judge
panel (seeds the eval harness), (5) Mixture-of-Agents / debate on `AgentSource`+`Team`. All five now
build on shipped primitives — the "experiment with SOTA agent strategies on mcpkit" story is available
*now*, not after Phase 4.

---

## Appendix — key files (current)

| Area | Files |
|---|---|
| Loop / approval / cancel / structured | `agent/runner.go`, `agent/approval.go` |
| Providers | `agent/provider.go`, `agent/anthropic_provider.go`, `agent/openai_provider.go`, `agent/failover.go` |
| Memory | `agent/memory.go`, `agent/semantic_memory.go`, `agent/embedder.go`, `agent/compaction.go` |
| Persistence / offloading | `agent/runstore.go`, `agent/toolresultstore.go`, `agent/offloading_source.go`, `agent/store/{redis,gorm}` |
| Multi-agent | `agent/agent_source.go`, `agent/team.go` |
| Eval | `agent/eval/` (`eval.go`, `scorer.go`, `judge.go`, `suite.go`, `scenario.go`, `longmemeval/`) |
| Tool layer / events / policies | `agent/toolsource.go`, `agent/multi_source.go`, `agent/filter_source.go`, `agent/events.go`, `agent/injection.go`, `agent/triggers.go`, `agent/stages.go` |
| Host surface | `agent/host/` (`commands.go`, `connections.go`, `render.go`, `hostevent.go`, `memory.go`, `subagents.go`, `persistence.go`), `cmd/agentchat/` |
| Invariants | `docs/AGENT_DESIGN.md`, `agent/CONSTRAINTS.md` |

---

<details>
<summary><b>First edition (2026-07, pre-implementation): "What it would take to build a solid Agent SDK"</b> — retained for provenance; superseded by the status above.</summary>

The original build-out roadmap (Phases 0–4, the E/G/F/H/D/I/A/B/C area designs, and the effort/sequencing
analysis) is preserved in git history at the first revisions of this file. Its designs were implemented
substantially as written — see the git log for issues #929–#1044 and the scorecard in §2 for the
as-built mapping. The competitor surveys and technique catalog it introduced live on in §3, §4, §6, §7
above, updated to current status.

</details>
