# agent/ Constraints

Module-specific rules. Project-wide constraints in the root `CONSTRAINTS.md` also apply (notably C1 typed contexts and C2 consolidated entry structs).

## A1: LLM-provider dependencies stay in agent/

No package outside `agent/` (root module, other sub-modules, examples that do not embed the agent) may import an LLM-provider SDK or this module. `agent/` depends downward on `core/`, `client/`, and optionally sibling sub-modules; nothing depends upward on it except applications and examples that embed the host.

**Verify:** `grep -rn "mcpkit/agent" core/ server/ client/ ext/ stores/ experimental/ --include='*.go'` returns nothing.

## A2: Runner events are wire-serializable

Every event type the Runner emits carries JSON tags, a stable `kind` discriminator, and no Go-only payloads (channels, funcs, non-marshalable interfaces). The wire projection used by web surfaces must be a 1:1 mapping, never a translation layer.

**Verify:** the event round-trip test in this module marshals and unmarshals every event kind through encoding/json and compares.

## A3: One vendor `_meta` prefix

All vendor-namespaced `_meta` keys this module reads or writes use `io.github.panyam.mcpkit/` (pinned in `docs/AGENT_DESIGN.md`). No ad-hoc prefixes.

**Verify:** `grep -rn '_meta\|Meta\[' agent/ --include='*.go' | grep -i 'io\.github\|dev\.\|com\.'` shows only the pinned prefix.

## A4: The loop never owns the user interface or process-global output

The Runner exposes callbacks and event streams; it never prints, prompts, or renders. Logging is the same: agent code logs only through an injected *slog.Logger (nil discards), never fmt, os.Stdout/Stderr, log, or slog.Default. Anything user-facing lives in surfaces (agentchat, web hosts) built on the module.

**Verify:** `grep -rn "fmt.Print\|os.Stdout\|os.Stdin\|slog.Default\|log.Print" agent/ --include='*.go' | grep -v _test.go` returns nothing.

## A5: core.RawJSON for JSON-valued public fields

JSON-valued fields in this module's public types use `core.RawJSON` (wire-transparent, parse-once, typed Bind), never bare `json.RawMessage`. JSON-fragment fields (streamed argument pieces in Deltas) stay strings; the Accumulator's fold is the promotion boundary where fragments become a RawJSON value.

**Verify:** `grep -n "json.RawMessage" agent/*.go | grep -v _test | grep -v NewRawJSON` shows only conversion sites, no struct fields.

## A6: Mechanisms in the client, policy in the agent

A primitive belongs in `client/` (or an events/skills SDK) if any non-agent consumer would want it (a script, a service, a poller, `cmd/testclient`); it belongs in `agent/` only if it requires a model and a turn to make sense. The decidable tell is the natural return type: functions returning protocol objects (`core.DetailedTask`, `events.Event`, `core.InputResponses`) are client-layer; functions returning model-facing objects (`core.ToolResult`, injected context, a proactive turn) are agent-layer. When adding a helper to agent code, check this first — task polling, `BackgroundTask`, and event stream consumption were all initially over-kept in the agent and moved to `client/`.

**Verify:** no `agent/` exported type or function returns a value that a non-agent caller could use standalone without also depending on the Runner/policies; conversely, agent public API that returns `core.ToolResult` / injected context stays here.

## A7: Sub-agents get no ambient parent state (memory is not shared)

A sub-agent receives only what crosses the parent-to-child boundary explicitly: the task arguments and injected context. It gets no working memory and no shared handle to the parent's stores. A child's location is not guaranteed — the in-process `AgentSource` is the degenerate co-located case; the general case is a child on another host, provider, or model — so shared parent memory would assume a co-location that A2 wire-serializability forbids (a store pointer can't cross a wire). A child that needs memory owns its own (configured on its own Runner, opaque to the parent, like a stateful MCP tool's database), never a namespace into the parent's store. Hierarchy (parent recall across children) waits on a prefix/hierarchical namespace query the `MemoryStore` seam does not have (exact-match today). Rationale and the full decision: issue 1151, `docs/AGENT_COMPOSITION.md` § Sub-agents and memory.

**Verify:** host personas are built over the server-only `serverTools`, never the memory-bearing aggregate; guarded by `TestSubAgentCannotReachParentMemory` in `agent/host` (a persona's `remember` hits an unknown tool and the parent store stays empty).

## A8: No in-repo workflow engine — orchestration is model-driven or integrated

mcpkit does not ship a code-driven workflow / state-machine engine. Orchestration is either **model-driven** (the agent Runner loop, sub-agents, the async control plane: triggers / injection / events) or **delegated** to a dedicated external engine (Temporal, Step Functions, and the like) that the application integrates. A workflow engine has no AI in it — it is a commodity state machine, the dual of the agent loop, not an extension of it. The canonical workflow patterns (prompt chaining, routing, parallelization, orchestrator-workers, evaluator-optimizer) already build on shipped primitives (`AgentSource` / `Team` / `FanOut` + `TriggerPolicy` / injection). Do not accept "Mastra / LangGraph / Eino ships one" (parity) as a reason to build one; require a concrete use case the shipped primitives cannot express, and even then prefer integration over reimplementation. Decision + full rationale (2026-08-04): the former Phase 4 epic (issue 928, closed not-planned) and `docs/AGENT_SDK_ROADMAP.md` § Phase 4.

**Verify:** no `workflow/` engine module exists — `grep -rn '^package workflow' --include='*.go' .` returns nothing.

## A9: The provider seam exposes loop-visible capabilities; loop-invisible optimizations stay out

The dividing test for any provider-specific behavior is **does the agent loop act on it?**

- **Loop-invisible optimizations** — prompt caching, extended/interleaved thinking with signed-block preservation, fast mode, task budgets. The Runner receives the same `Delta` stream regardless (caching = cheaper, thinking = reasoning we already parse), so these change nothing above the seam. They are **not** SDK concerns. The no-SDK native providers (`agent/anthropic_provider.go`, `agent/openai_provider.go`) do not implement them; if a concrete need ever appears, wrap the vendor's **official SDK** behind the `Provider` seam rather than hand-maintaining no-SDK wire code that chases the vendor's API drift forever.
- **Loop-visible capabilities** — token-confidence / logprobs (feeds routing, abstention, confidence-gated cascades) and grammar-constrained decoding (a stronger structured-output guarantee the loop consumes). These feed agent decisions and **are** agent-relevant. Expose them **capability-optionally** through the `Provider` seam (nil / zero value = the provider does not support it, loop degrades gracefully) — not by reimplementing a vendor's full API surface, and not gated on any single vendor (logprobs are an OpenAI-wire / local-inference capability; Anthropic does not expose them).

Never thread provider-specific opaque state (e.g. Anthropic signed thinking blocks) through the neutral `agent.Message` or the Runner — that violates A2 wire-serializability and the provider-agnostic core, regardless of which side of the test the feature falls on. The SDK's job is the Provider *seam*, not provider parity. Rationale (2026-08-04): issue 953 (caching/thinking, loop-invisible) closed not-planned; logprob #1053 kept as a loop-visible capability. See `docs/AGENT_SDK_ROADMAP.md` § Phase 5.

**Verify:** `agent.Message` carries no provider-specific reasoning/signature field; the no-SDK providers send no `cache_control`, `thinking`, fast-mode, or task-budget request fields. (Loop-visible capabilities like logprobs are permitted as capability-optional seam additions.)

## A10: Dependency weight decides module boundaries; seam interfaces stay with the Runner

Two rules, and the second is the one that gets broken by accident.

**Seam interfaces live in `agent/`, with the Runner that consumes them.** `Provider`, `ToolSource`, `MemoryStore`, `RunStore`, `ToolResultStore`, `Embedder`, and `Compactor` are all defined where they are used, per the Go convention that an interface belongs to its consumer rather than its implementors. There is no `agent/api` or interface-only package, and there should not be: it would separate every seam from the only code that depends on it, and implementations satisfy these structurally without importing anything.

**A satellite module is for dependency weight, not for layering.** An implementation lives beside its interface in `agent/` when it costs no third-party dependency, and moves to its own module when it needs a driver or SDK. That is why `InMemoryRunStore`, `InMemoryMemoryStore`, `InMemorySemanticStore`, `FileToolResultStore`, and the no-SDK `OpenAIProvider` / `AnthropicProvider` all sit next to their interfaces, while redis, gorm, and pgvector implementations are `agent/store/redis` and `agent/store/gorm` with their own `go.mod`.

The property this protects is that `agent/` is cheap to embed: it pulls mcpkit's own modules and nothing else, so depending on the agent SDK never drags in a database driver or a vendor SDK a consumer does not use. A1 governs the *outward* direction (nothing outside `agent/` may import an LLM SDK or this module); this governs the inward one. A9 depends on it too — "wrap the vendor's official SDK behind the seam" means in a satellite module, not here.

Adding a dependency-free implementation beside its interface is always fine. Adding one that carries a driver is the violation, and the fix is a new satellite module rather than a new direct require.

**Verify:** `cd agent && go mod edit -json | jq -r '.Require[] | select(.Indirect|not) | .Path'` lists only `github.com/panyam/mcpkit` and `github.com/panyam/servicekit`. Any other entry means a dependency landed in the core agent module: justify it as genuinely dep-free infrastructure, or move the code to a satellite module.
