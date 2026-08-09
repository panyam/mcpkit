# mcpkit Agent SDK — Competitive Status & Gap Analysis

**What this is.** A living assessment of the mcpkit `agent/` host layer against (a) general agent
frameworks (Mastra, Eino, Genkit-Go, langchaingo, swarmgo, agno-Go) and (b) real coding-agent loops
(Claude Code, Cursor, Gemini CLI, aider, Codex, OpenCode, and the Pi / oh-my-pi line). The first edition (below the fold) framed
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

**Structural phases.** ~~Phase 4 — durable workflows (#928)~~ **was evaluated and dropped (2026-08-04,
not-planned).** A code-driven workflow engine has no AI in it, is a commodity (Temporal / Step Functions
territory), is the *dual* of the model-driven agent loop rather than an extension of it, and the
canonical workflow patterns already build on shipped primitives (§7). The decision is recorded as
constraint **A8** in `agent/CONSTRAINTS.md`. The remaining phases stay open as epics, with **Phase 5
re-scoped by constraint A9** (its Anthropic caching/thinking child #953 was dropped as loop-invisible;
logprob #1053 stays as a loop-visible agent capability; grammar #1054 is a marginal deferred enhancement):
**Phase 5 — provider control & decoding fidelity** (#1050: logprob #1053, grammar decoding #1054), **Phase 6 —
test-time compute & routing reliability** (#1051: sampling/vote #1056, confidence-gated cascades #1057;
adjacent routing #991), **Phase 7 — safety & guardrails** (#1052: prompt-injection spotlighting #1058). The items the previous edition listed as
*untracked* (logprob/grammar, guardrails, sampling/vote, cascade trigger, coding-surface) have all been
**promoted to tracked phase children** — see §5a. Nothing identified is left untracked (the two opt-in
Phase-7 extensions are now filed as #1060/#1061). Beyond the phases, a rich refinement backlog on the
shipped primitives remains (mostly tracked).

**Coding-agent re-survey (2026-07).** A pass over the strongest current open coding agent, oh-my-pi
(`can1357/oh-my-pi`, a TS+Rust batteries-included fork of Pi), confirms the picture and surfaces two
genuinely new **agent-layer** primitives we lack — **mid-turn stream-rule steering** (#1147) and a
**critic/observer model role** (#1148) — now filed. The rest of that product's moat (hash-anchored edit
format, LSP-in-writes, DAP, a ~55k-LoC in-process Rust tool core, browser) is the **coding-surface**
layer we deliberately scope to a coding agent built *on* `agent/`, not `agent/` itself (#1059). It is
the clearest evidence yet that #1059 is a real decision, and that harness/tool quality is a first-order
differentiator. See §4.

---

## 2. Status scorecard

Legend: ✅ shipped · 🟡 partial/shipped-with-follow-ups · ⏳ tracked (open issue) · ❌ untracked gap.

> **Marking a row ✅ requires checking that the loop can reach the primitive, not that the seam
> declares it.** §7 credited `ToolChoice` forced-tool as shipped for Best-of-N and judge panels;
> `ProviderRequest` did declare it and both providers rendered it onto the wire, but the Runner's
> step loop never populated it, so no caller could set it on a turn (fixed in #1239). The failure
> mode is specific and worth naming: a seam can look complete while nothing drives it, and reading
> the type definition confirms the wrong half. The cost was not the doc line — #1056 was scoped and
> sized against that row, and the estimate was wrong by the size of a missing contract decision.
> When a row's evidence is a type declaration, grep for a non-test assignment before marking it.

| Area | Status | As-built (file) | Remaining |
|---|---|---|---|
| **Tool-call interception (hooks) + permission ladder** | ✅ #929 | `agent/toolhook.go` — `ToolHook` (`BeforeTool` may rewrite args or deny, `AfterTool` may rewrite the result), `RunnerConfig.ToolHooks`, ordered, deny short-circuits. `agent/approval.go` — `TieredApproval` is a hook, not a parallel seam: `ApprovalMode` {AlwaysAsk/ReadOnlyAuto/AlwaysAllow} + per-tool `RuleAsk/Allow/Deny`; "ask" routes through `ElicitationCoordinator.Confirm`; `EventToolDenied`; host `/approve`, appended last | — |
| **Anthropic-native provider** | ✅ #930 | `agent/anthropic_provider.go` — no-SDK, content-block↔Delta, `thinking_delta`→reasoning; structured output via forced synthetic tool | Deliberately **minimal** (constraint A9). Caching/extended-thinking **dropped (not-planned, #953)** — provider-client features below the loop; wrap the official SDK if ever needed. |
| **Structured output in the loop** | ✅ #931 | finalizing `Generate` (`runner.go` `finalizeStructured`, retry×2); `RunnerConfig.ResponseSchema` → `TurnResult.Structured` | — |
| **Eval / scorer harness** | 🟡 #974,#932 | `agent/eval/` — `Case`/`Scorer`/`Suite`/`Scenario`; 8 deterministic scorers; `Judge` (build-tagged); LongMemEval *smoke* scenarios | external-benchmark adapter **⏳ #1015**; real LongMemEval loader **⏳ #1014** |
| **Persistence (RunStore) + fork/rewind** | ✅ #960,#962,#963,#986 | `agent/runstore.go` — full interface + `InMemoryRunStore`; redis + gorm (pg/sqlite) backends; `ForkRun{AtMessage}` checkpoint fork; `ListRuns`; `Message.Timestamp` | retention/GC **⏳ #999** |
| **Per-tool-call cancellation / interrupt** | ✅ #936,#937 | `runner.go` `TurnRequest`/`Control` channel (per-`CallID`); `EventToolCancelled` | — |
| **Working memory** | ✅ #938,#1003,#1140 | `agent/memory.go` — `MemorySource` remember/recall/forget; `Summary`/`RecallRelevant`; `InMemoryMemoryStore`; durable + per-request `Namespace` backends (redis/gorm, #1003); session-scoping by run id (#1140) | sub-agent memory model (injection over shared store) **#1151** |
| **Semantic recall (vector)** | ✅ #940,#1019 | `agent/embedder.go` (`Embedder`, `OpenAIEmbedder`), `agent/semantic_memory.go` (`InMemorySemanticStore`), gorm **pgvector** store; pre-turn recall auto-injection | standalone doc-RAG VectorStore **⏳ #1021**; reranker **⏳ #1020**; auto-distillation **⏳ #1022** |
| **Compaction / summarization** | 🟡 #939,#1011 | `agent/compaction.go` — `SummarizingCompactor`, `TokenEstimator`/`CharTokenEstimator`, `EventCompaction`; pre-loop hook; budgeted summary injection | mid-turn compaction **⏳ #1006**; real tokenizer **⏳ #1007** |
| **Tool-result offloading (context mgmt)** | ✅ #966,#971,#972 | `agent/offloading_source.go` + `ToolResultStore` (mem/redis/gorm); `read_tool_result` (offset/limit/grep) | streaming/handle-based very-large results **⏳ #980,#979** |
| **Sub-agents (agent-as-tool)** | ✅ #941,#942,#943,#1031,#1032,#1033,#1035,#1036,#1042 | `agent/agent_source.go` (`AgentSource`, depth+budget caps, structured I/O), `agent/async_agent_source.go` (`AsyncAgentSource` Task form, #1035), `agent/fanout_source.go` (`FanOutSource`, #1033), `agent/team.go` (`Team` handoff + host wiring + tagging, #1042), `agent/tree_budget.go` (`TreeBudget` aggregate cap, #1032), `agent/signal.go` + `agent/agent_pool.go` (upward signals + runner-control pool + interruptible turn, #1036), `SubAgentEvent` nesting, declarative host personas | dynamic catalog #1038, nested config #1043, interaction mediator #1157, map-style fan-out (unfiled) |
| **Host surface** | ✅ #984–#992 | slash-command registry, `ConnectionRegistry` + runtime `/provider`, `HostEvent`/`Observer` render seam, notebook renderer (#1001), interactive `/mcp` + `/sessions` overlay (#1095, `focusLayer`/`modalHost` seam + `client.Group.Reconnect`), per-server tool view (#1117) + oauth login action / authorization-code auth type (#1116, #907), dialog stack for nested-overlay back-nav (#1124), color accessibility (#1125), bubbletea TUI, playground | context-assembly pipeline **⏳ #1024,#1026**; remaining TUI-track items (dimmed-base compositing, grapheme width, `WindowSizeMsg` fan-out) **⏳ #1063** |
| **Observability** | ✅ | SEP-414 tracing (`agent.turn/step/tool`, `agent.memory.recall`) + OTel **metrics** (#1023 — turn/step/token/tool counters + duration histograms via `RunnerConfig.MeterProvider`, `host.WithMeterProvider`, agentchat `SetupMeter`, `mcpkit-agent` Grafana dashboard) | — |
| **Durable workflows / graphs (Phase 4)** | ❌ **dropped (not-planned, 2026-08-04)** | — | Not building an engine (constraint A8). Workflow patterns build on shipped primitives (§7); durable orchestration → integrate a dedicated engine. #928/#944/#945/#946 closed not-planned. |
| **Provider routing / cascades** | ⏳ #991 | only `FailoverProvider` (failure+cooldown) today | per-turn/per-role routing **#991**, router presets **#1044**, confidence-gated cascade **#1057** (Phase 6) |
| **Provider control & decoding fidelity (Phase 5)** | ⏳ #1050 (re-scoped by A9) | structured output via finalizing `Generate`; generation parameters reachable from a turn via `RunnerConfig.Generation` / `TurnRequest.Generation` (#1239 — temperature, token cap, tool choice; before it, three of the four `ProviderRequest` parameters could not be set on a Runner at all) | logprob/token-confidence **#1053** — **loop-visible** (routing/abstention/cascade), kept as agent-SDK work; OpenAI-wire/local, not Anthropic. Grammar/guided decoding **#1054** — deferred (marginal). Anthropic caching/thinking **dropped (#953, loop-invisible)**. |
| **Test-time compute (Phase 6)** | ⏳ #1051 | `#1033` `FanOutSource` already gives N concurrent runs, member ordering, failure isolation, and a pluggable `Aggregate` hook | sampling/vote **#1056** — re-scoped down to two aggregators over that hook (see §5a); `FailoverProvider` quality trigger **#1057**, blocked on #1053 |
| **Safety & guardrails (Phase 7)** | ⏳ #1052 | approval ladder (#929); `ToolHook` is the interception seam guardrails attach to — `AfterTool` sees every tool result, `BeforeTool` can rewrite args or deny | prompt-injection spotlighting **#1058** is now an `AfterTool` hook to write rather than a mechanism to build; opt-in extensions (AgentDojo eval, constitutional gate) unfiled |
| **Coding-surface: sandboxing, hooks, repo map, LSP** | ⏳ #1059 | — | scope decision (agent/ vs coding agent built on it) **#1059** |

**Bottom line:** Phases 0–3 are effectively **done**. Phase 4 (workflows) was evaluated and **dropped**
— not a gap, a deliberate non-goal (constraint A8). Three structural phases stay open and fully scoped
in issues: **Phases 5–7** (provider decoding control, test-time compute, safety), which promote the
former §5b "untracked" gaps into tracked epics. Everything else open is refinement of shipped
primitives.

---

## 3. Gap table A — agent-framework parity (updated)

| Capability | Leaders | mcpkit status |
|---|---|---|
| Tiered memory: working + semantic recall + compaction | Mastra, langchaingo, agno-Go | ✅ **at parity** (#938/#940/#939) + pgvector (#1019); reranker/distillation tracked (#1020/#1022) |
| Multi-agent / handoffs / sub-agents | all five + Mastra | ✅ **at parity** (AgentSource #941, Team #943); richer composition tracked (#1032–#1038) |
| Eval / scorer framework | Mastra, Genkit *(rare in Go)* | ✅ **shipped** (#974) — a **differentiator vs the Go field**; external-suite adapter tracked (#1015) |
| Native providers beyond OpenAI-compat | all | ✅ Anthropic (#930), kept minimal (A9); caching/thinking **dropped (#953)** — provider-agnostic is the thesis, OpenAI-wire is the common case |
| Structured output *inside the loop* | Mastra, Genkit, Eino | ✅ **shipped** (#931) |
| Durable suspend/resume workflows; branch/parallel | Mastra, Eino, agno-Go, Genkit | ❌ **deliberate non-goal** (A8) — not a parity gap; patterns build on shipped primitives (§7), durable orchestration is integrated, not reimplemented |
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

### 4a. The coding-agent field, re-surveyed against oh-my-pi

Two related things share the name. **Pi** (pi.dev, `@earendil-works/pi-coding-agent`, `badlogic/pi-mono`,
Mario Zechner / Earendil Inc.) is the *minimal upstream* harness — "many agent harnesses but this one is
yours," four modes (TUI/print/RPC/SDK), tree-structured history, mid-session steering — and it
**deliberately omits MCP, sub-agents, permission popups, plan mode, todos, and background bash**, leaving
them as extension points. **oh-my-pi** (`can1357/oh-my-pi`, Can Bölük) is a TS+Rust, batteries-included
**fork** that *adds* exactly what Pi omits — MCP, 32 tools, subagents, LSP/DAP, the hash-anchored edit
format, a ~55k-LoC Rust core — and is currently the most feature-complete *open* coding-agent surface.

The pairing is instructive for us in two ways. First, **Pi is the closer philosophical peer to
`agent/`** (a lean, embeddable, extension-first harness with the same four surfaces), while oh-my-pi is
precisely the *coding agent built on top* — the #1059 shape. Second, **mcpkit is already ahead of
upstream Pi on MCP-native operation and sub-agents**: Pi omits both by design; the fork had to bolt them
on, and MCP is one integration among many rather than the native fabric. So the comparison below is
really against oh-my-pi *the product*, on the coding-agent axis (Claude Code / Cursor), not against a
Mastra/Eino-style library.

oh-my-pi's explicit thesis — *the harness, not the model, decides* — is worth taking seriously: it
reports multi-fold first-attempt-edit and token-efficiency lifts on the *same weights* purely from a
better edit format and tool ergonomics. That validates our tool-layer investment, but it also draws the
line sharply: **`agent/` ships no tool implementations at all** (it is the SDK substrate), so most of
what makes that product good is the coding-*surface* layer we scope to #1059.

Mapping its surface onto our status and the A6 layering line:

| oh-my-pi capability | Nature | mcpkit `agent/` status |
|---|---|---|
| Hash-anchored edit format (content-hash anchors, stale-anchor reject) | coding-surface | out of SDK scope → **#1059** (the SDK has no edit tool; a coding agent built on it would) |
| LSP-in-writes (rename via `willRenameFiles`), DAP debugger, AST edits | coding-surface | out of scope → **#1059** |
| ~55k-LoC in-process Rust core (ripgrep/glob/shell/PTY/AST, no fork-exec) | coding-surface / perf | out of scope → **#1059**; `agent/` is provider+loop, transport-agnostic by design |
| Browser / computer-use, web_search, GitHub-as-filesystem, `://` resource schemes | coding-surface | out of scope → tool authoring on top of the SDK |
| **Mid-turn stream-rule steering** (regex/AST over the *output* stream → abort → inject rule → retry within the turn; fired-state survives compaction) | **agent-layer** | **not shipped → newly filed #1147.** Our injection is turn-boundary only; the delta scanner (#989) is the hook |
| **Critic/observer model** (second model on its own context reviews each turn, injects graded steering: aside/concern/blocker; cannot approve/deny) | **agent-layer** | **not shipped → newly filed #1148.** Composable on Observer #992 + AgentSource #941 + Control #936, but not a role yet |
| Deterministic image-frame compaction ("snapcompact" — renders discarded history to per-model-sized PNG frames to exploit image-token billing) | agent-layer (novel) | we have `SummarizingCompactor` (#939); this is an exotic provider-billing optimization, noted under compaction depth #1006/#1007, not a parity gap |
| Subagents: agent-as-tool with **schema-validated structured results** (3-level schema precedence), per-agent model/tool/depth, peer **hub** for inter-agent messaging | agent-layer | **at rough parity behind tracked gaps** — AgentSource #941, per-Runner provider, MaxDepth/budget; structured I/O + fan-out #1033; peer messaging ≈ upward-signals #1036 |
| Memory: pluggable SQLite backends, first-turn recall injection, periodic retain, **offline cross-session consolidation into a "mental model" loaded on turn 1**, polyphonic (vector/graph/fact/temporal) recall | agent-layer | **at rough parity behind tracked gaps** — MemorySource #938, semantic recall #940/#1019, injection #1011; consolidation = auto-distillation #1022; multi-signal recall = reranker #1020; durable backends #1003 |
| Provider routing: per-role models, path-scoped model sets, fallback chains, round-robin credentials | agent-layer | **they ship what we have tracked** — only `FailoverProvider` today; per-role/path routing is #991, presets #1044 |
| ACP surface (editor-drivable), RPC (NDJSON stdio), collab live-share | surface | ACP is a notable coding-surface integration we do not have; collab is out of scope |

**Reading of it.** On the *loop* primitives — memory, compaction, multi-agent, structured output — we
are at or behind-by-a-tracked-issue, not behind in kind. Two real agent-layer gaps came out of the pass
(#1147, #1148) and are filed. Everything else that makes the product impressive is coding-surface or
provider-routing-we-have-scheduled. The strategic signal is not "we are behind on the SDK"; it is that
**the coding-surface layer (#1059) is where a large share of end-user value lives**, and a product-grade
coding agent on top of `agent/` is the thing that would actually compete — the SDK is necessary but not
sufficient.

---

## 5. What's still missing

### 5a. Tracked (open issues — planned work)

**The structural phases (epics):**
- **Phase 4 — durable workflows: DROPPED (not-planned, 2026-08-04; constraint A8).** #928/#944/#945/#946
  closed. A workflow engine is code-driven orchestration with no AI in it — the commodity dual of the
  model-driven agent loop, not an extension of it. The canonical workflow patterns already build on
  shipped primitives (§7); when real durable orchestration is needed, integrate a dedicated engine
  (Temporal, Step Functions) rather than reimplement one. #945's one durable idea (the trigger machinery
  *is* the suspend/resume primitive) is already realized by `TriggerPolicy` + `IncomingEvent`.
- **Phase 5 — provider control & decoding fidelity:** re-scoped by constraint A9 (loop-visible capability
  vs loop-invisible optimization):
  - **logprob / token-confidence #1053 — KEPT as agent-SDK work.** Loop-visible: token confidence feeds
    routing, abstention, and the Phase 6 confidence-gated cascade (#1057). It's an OpenAI-wire /
    local-inference capability (Anthropic doesn't expose logprobs), so it serves the common providers, not
    a niche vendor. Exposed capability-optionally on the `Provider` seam (nil = unsupported).
  - **grammar-constrained / guided decoding #1054 — deferred.** Loop-visible but marginal: a stronger
    guarantee than the forced-tool structured-output path already shipped, gated on provider support
    (vLLM/SGLang). Pick up on a concrete need.
  - **Anthropic prompt caching & extended thinking #953 — dropped (not-planned).** Loop-invisible
    optimization; the API-drift treadmill (its `budget_tokens` premise already 400s on current models) is
    the cautionary case for growing the no-SDK client.
  - The phase's original "provider control" framing was too provider-flavored; A9 is the durable line.
    Capability-optional fields, nil = today's behavior (A2/CONSTRAINTS).
- **Phase 6 — test-time compute & routing reliability:** epic #1051 → sampling+aggregate helper
  #1056, `FailoverProvider` quality-score trigger for confidence-gated cascades #1057. Complements
  upfront routing #991 (selection vs escalation).
  - **#1056 was re-scoped down (2026-08-07)** after a design pass, and the reasoning generalizes.
    Most of its mechanism already ships: `FanOutConfig` provides N concurrent runs, stable member
    ordering, per-member failure isolation, and a pluggable `Aggregate` reduce hook, so the delta is
    two aggregator functions rather than a new primitive. Its premise was also not portable —
    self-consistency is defined as sampling at `Temperature>0`, and current Anthropic models reject
    sampling parameters outright, so identical-member diversity is the one unportable case. The
    portable source of diversity is per-member providers, which multi-model already gives us free.
    And no user was identifiable: the issue traces to the §5b competitive sweep, not a request.
    **Worth applying the same three questions to the other phase children** — what already ships,
    does the premise hold on every provider, and who asked.
  - #1057 is blocked on #1053: a confidence-gated cascade needs a confidence signal to gate on.
- **Phase 7 — safety & guardrails:** epic #1052 → prompt-injection spotlighting/datamarking `Transform`
  stage for untrusted tool output #1058; opt-in extensions AgentDojo eval suite #1060 and constitutional
  pre-dispatch critique gate #1061. The coding-surface scope decision (sandboxing/hooks/repo map/LSP)
  is #1059.

**Refinement backlog on shipped primitives:**
- **Provider routing / cascades:** #991 (per-turn/per-role model selection over `ConnectionRegistry`),
  #1044 (openrouter/litellm presets). Today only `FailoverProvider` (failure-triggered).
- **Prompt caching + extended thinking:** ~~#953~~ **dropped (not-planned, constraint A9)** — provider-
  client features below the agent loop; the native provider stays minimal, deep provider features wrap the
  official SDK behind the `Provider` seam only when a concrete need appears.
- **Memory depth:** standalone doc-RAG `VectorStore` #1021, Scorer/Reranker seam #1020, auto-distillation
  write-path #1022, sub-agent memory model #1151, faster cosine #1018. (Durable/session-scoped MemoryStore #1003/#1140 shipped.)
- **Compaction depth:** mid-turn compaction #1006, token-accurate estimator #1007.
- **Sub-agent composition:** upward signals / runner-control meta-tools + interruptible turn #1036 **✅ SHIPPED**
  (A #1165/PR 1168, B #1166/PR 1169, C #1167/PR 1170); dynamic agent catalog
  + transfer graph #1038, full nested per-agent config #1043, cross-agent interaction mediator #1157.
- **Context assembly:** explicit pre-turn pipeline #1026, unified injection budget with a final arbiter
  #1024.
- **Eval:** external eval/conformance adapter seam #1015 (see §6), real LongMemEval loader #1014.
- **Ops:** RunStore retention/GC #999, large/binary tool results #980/#979,
  thinking-hint stream parser #989, session-picker paging #1000. (OTel metrics seam #1023 shipped.)
- **Mid-turn steering & critique (from the coding-agent re-survey, §4a):** stream-rule steering —
  scan the output stream, abort/inject/retry within the turn #1147 (builds on the #989 scanner); a
  first-class critic/observer model role that injects graded steering #1148 (composable on Observer
  #992 + AgentSource #941 + Control #936). Both are agent-layer, A6-clean.

### 5b. Untracked (no issue yet)

Nothing. The six gaps this section previously listed — logprob exposure, grammar/guided decoding, the
sampling/vote helper, the prompt-injection guardrail, the `FailoverProvider` quality-score trigger, and
the coding-surface scoping call — are now tracked phase children (Phases 5–7 and #1059; see §5a), and
the two Phase-7 extensions (AgentDojo eval suite #1060, constitutional critique gate #1061) have been
filed. The 2026-07 coding-agent re-survey (§4a) surfaced two further agent-layer gaps — mid-turn
stream-rule steering and a critic/observer model role — and they were filed on discovery as #1147 and
#1148. Everything identified is tracked; new gaps get filed as they surface.

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
  `FuncSource` verifiers + eval scorers (✅ as of #1239). ([2110.14168](https://arxiv.org/abs/2110.14168),
  [2305.11738](https://arxiv.org/abs/2305.11738), [2404.18796](https://arxiv.org/abs/2404.18796))
  *Correction: this row read ✅ before #1239, but `ToolChoice` was unreachable from a turn — the
  Runner's step loop sent only `{Instructions, Messages, Tools}`, so `Temperature`, `MaxTokens`, and
  `ToolChoice` could not be set on a Runner at all. #1239 added `RunnerConfig.Generation` /
  `TurnRequest.Generation` and the row is now accurate. The stale claim is why #1056 was sized S–M;
  see that issue for the re-scope.*
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
| Host surface | `agent/host/` (`commands.go`, `connections.go`, `render.go`, `hostevent.go`, `memory.go`, `subagents.go`, `persistence.go`), `agent/surfaces/chat/` |
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
