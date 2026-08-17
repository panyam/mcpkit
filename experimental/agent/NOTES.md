# agent/ — implementation notes

Why the agent layer is shaped the way it is, and what bit us building it. Written for
whoever touches this code next, not for someone learning to use it.

- **How to use it:** `agent/host/README.md`, `agent/surfaces/chat/README.md`
- **Invariants:** `agent/CONSTRAINTS.md` (A1–A9)
- **Design frames:** `docs/AGENT_DESIGN.md`, `docs/AGENT_COMPOSITION.md`, `docs/AGENT_MEMORY_FLOW.md`
- **Roadmap:** `docs/AGENT_SDK_ROADMAP.md`
- **Terminal surface lore:** `agent/surfaces/chat/NOTES.md`

Phases 0–3 of the SDK roadmap have shipped. Phase 4 (durable workflows) was dropped as a
non-goal, recorded as constraint A8. Phases 5–7 remain open as epics 1050 / 1051 / 1052.

---

## The layering rule (A6)

Put a primitive in `client/` (or an events/skills SDK) if any non-agent consumer would want
it: a script, a service, a dashboard poller, `cmd/testclient`. Put it in `agent/` only if it
needs a model and a turn to make sense.

The tell is the natural return type. A protocol object (`core.DetailedTask`, `events.Event`,
`InputResponses`) is client-layer. A model-facing object (`core.ToolResult`, injected context,
a proactive turn) is agent-layer.

This is why task polling, `BackgroundTask`, and `StreamChan` live in `client/` while
`EventInjectionPolicy`, `TriggerPolicy`, and the Runner live here. It is also why `RunStore`,
`ToolResultStore`, and `MemoryStore` live in `agent/` rather than a root `stores/`: they
traffic in `agent.Message` / `core.ToolResult` / `MemoryItem`, so hoisting them would force a
root→agent dependency.

## Background goroutines

Use `core.DetachForBackground(ctx)`, never `context.WithoutCancel`. The former replaces the
dead POST-scoped requestFunc/notifyFunc with the session-level persistent push. Anything that
outlives a turn *and* calls MCP server tools needs it: async sub-agents, the agent pool, task
dispatch.

---

## Runner, control, and cancellation

`Runner.RunTurn(ctx, TurnRequest)` is the real entry point; `Run` is a shim. The Runner is a
deterministic fan-out-then-join pure function over history. It is stateless, so one Runner can
be reused across turns, and that is what makes resume / fork / eval / compaction composable.

**Per-tool-call cancellation** rides `TurnRequest.Control <-chan Control`. `Control{}` with an
empty CallID cancels every in-flight call in one send; `Control{CallID}` targets one. A
cancelled call feeds `"cancelled by user"` back to the model and **the turn continues** —
cancelling `RunTurn`'s own ctx is still the way to abort outright.

The gotcha that bit: cancellation must be checked on all three outcome shapes. Transports
return an error, but `FuncSource` converts a handler's `ctx.Err()` into an `IsError` result,
and a success can race the cancel. User cancel wins in the race. `EventToolCancelled` carries
`Reason`, not `Error`, so error-keyed eval scorers do not miscount interrupts.

`Control` is the extensible steering envelope; future verbs are additive `Kind` values.
Mid-turn *content* ("/btw") is deliberately not a Control — it routes through injection so it
lands in history.

### Upward signals (#1165, PR 1168)

A child raises a **non-referential** `Signal` (`SignalKind` ∈ `escalate`, `custom`, `preempt`)
to its parent by calling the `signal_parent` tool. The child reports only its own state and
never names a sibling: under A7 it cannot know what its siblings are doing. The parent holds
the fan-out inventory and its own policy, so the parent decides what to do about the others.

`cancel_siblings` was deliberately dropped as a kind. It implies sibling-awareness, and it
no-ops anyway — siblings have already joined by the time a non-interruptible parent reads at
the join.

**Two-key ctx sink**, and a single-key design was a real bug caught before tests:
`dispatchSinkKey` is a dispatch's sink for its own children; `parentSinkKey` is snapshotted by
`AgentSource.Call` and is the spawner's sink. `RaiseSignal` reads `parentSinkKey` so a child
raises to its spawner rather than to its own dispatch sink, which would shadow it. A grandchild
therefore reaches its immediate parent. Guarded by
`TestUpwardSignal_NestedReachesImmediateParent`.

The parent reads drained signals at the dispatch join and reacts via `RunnerConfig.SignalPolicy`
(built-in `AbortOnEscalate`) or by injecting a `RoleSystem` note into the next step.
`EventSignal` surfaces it.

### Interruptible turn (#1167, PR 1170)

`RunnerConfig.Interruptible` (default off) lets `dispatch` break the fan-out join barrier on
the first mid-flight signal: cancel the remaining calls, return partial results, and let the
existing `RunTurn` loop re-plan. There is no `RunTurn` change — re-entry falls out of the
existing post-dispatch loop.

**The parent-vs-callBase ctx split is the load-bearing detail.** A fan cancel must leave the
*step* ctx (`parent`) live so cancelled siblings read "cancelled by user" rather than the turn
aborting. `callTool`'s `cancelled()` is `call-ctx-done && parent-live`. Calls run under a
cancellable `callBase = WithCancel(parent)`, and in non-interruptible mode `callBase == parent`
so the default path is byte-identical. Still `wg.Wait()` after the cancel so every result slot
is filled — providers require a result per tool call. `dispatch` returns `([]Message, []Signal)`.

### preempt is parent-granted, not child-authoritative (#1176, PR 1178)

A `custom` FYI signal never breaks the barrier. `escalate` always breaks (must-handle
contract). A `preempt` breaks and cancels the in-flight losers **only when
`RunnerConfig.PreemptGrant` honors it**. Nil, the default, means injected-only: the model
decides on re-plan, so a rogue or prompt-injected child cannot unilaterally kill its siblings.

The design crux: under A7 a child cannot know global sufficiency, so its preempt is advisory.
Mechanism is `signalSink.breakOn` (set to `Runner.shouldBreakOn`) gating `raise`'s notify.
`agent.GrantAllPreempts` is the trust-all grant; host `Config.AllowPreempt` (default false).

Letting the grant select *which* in-flight calls to cancel via the #936 registry is #1177.

### Tree budget (#1032, PR 1162)

`agent.TreeBudget{MaxSteps, MaxTokens}` is a ctx-threaded shared counter capping total model
steps and tokens across a turn's whole tree: parent, sub-agents, fan-out members, handoff
rounds. It is the aggregate rail complementing per-Runner `MaxSteps`, `WithAgentCallBudget`
(call count), and `Team.MaxHandoffs`.

The top-level Runner installs it (install-if-absent, `RunnerConfig.TreeBudget`) and every child
inherits the same live counter through ctx — sharing falls out of existing ctx propagation, no
plumbing. Steps are pre-decremented at the step-loop top; tokens are post-hoc from provider
`Usage`, so a turn can overshoot `MaxTokens` by at most one step. Exhaustion raises
`ErrTreeBudget`, which `AgentSource` softens to an `IsError` result for a sub-agent.

---

## Memory

Design map: `docs/AGENT_MEMORY_FLOW.md`. Phase 2 (epic 926) is complete.

### The MemoryStore seam

`agent/memory.go`: `PutMemory` / `ListMemories(Query, Limit)` / `DeleteMemory`, gRPC-style
req/resp, unknown-key delete is app state rather than an error.

**`ListMemories` returns `[]ScoredMemory`, not `[]MemoryItem`.** `Score` is per-query — it
lives on the result, not the stored item, so a reranker can add signals without touching the
fact.

**Retrieval lives behind this one interface**: substring, O(n) cosine, or pgvector ANN all wear
the same `ListMemories(Query, Limit=k)` signature. There is deliberately no separate public
`VectorStore` seam; #1021 revisits that only if doc-RAG needs one.

Implementations: `InMemoryMemoryStore` (substring, Score 1), `InMemorySemanticStore` (Embedder
plus brute-force cosine — renamed from `SemanticMemoryStore` because "semantic" is a property
of a MemoryStore impl, not a separate interface), `redisstore.MemoryStore` (durable substring),
and `gormstore.SemanticMemoryStore` (durable pgvector ANN).

### Namespace (#1003) and session scoping (#1140)

`Namespace` is a **per-request field** on `PutMemoryRequest` / `ListMemoriesRequest` /
`DeleteMemoryRequest`: one independent scratchpad per session or user, empty meaning the shared
default, and recall never crosses namespaces. Every backend honors it. The in-memory stores
partition their maps; `gormstore.SemanticMemoryStore` reads `req.Namespace` falling back to
`WithMemoryNamespace`; `redisstore.MemoryStore` stores each namespace as one Redis hash
(`<prefix>:<namespace>` → key→JSON, `DefaultMemoryKeyPrefix = "mcpkit.agent.memory"`) with
CreatedAt preserved on update for stable order and an optional per-namespace TTL
(`WithMemoryTTL`) to GC abandoned sessions.

`MemorySource` threads it via `WithMemoryNamespaceFunc(func() string)`; nil means shared.

**The deadlock trap.** `MemoryConfig.SessionScoped` makes `registerMemory` pass
`WithMemoryNamespaceFunc(a.currentRunID)`. The namespace func runs *inside a turn while
`turnMu` is held* — both from the memory tools and from summary/recall injection — so
`App.RunID()`, which takes `turnMu`, would re-enter and deadlock. The fix is a lock-free
`currentRunID()` backed by an `atomic.Value` mirror of `runID`, with all four writers
(`AttachRun`, `resumeLocked`, `Fork`, `ensureRunLocked`) routed through one `setRunID` so the
mirror cannot drift.

A durable *semantic* Redis backend is still a follow-up; durable semantic recall is the
pgvector store's job.

### pgvector store (#1019, PR 1047)

Postgres plus pgvector only, no SQLite path — the vector type, the `<=>` cosine-distance
operator, and the HNSW index are pgvector features. The `vector` extension is a prerequisite
the store does **not** install, because `CREATE EXTENSION` needs privileges.
`docker/backends`' `init/01-agent.sql` creates it on a fresh volume only. Tests are PG-only and
skip when it is unavailable, so the sqlite-first RunStore and ToolResult tests are unaffected.

`PutMemory` embeds `key+" "+value` client-side and upserts `(namespace, key, body JSON,
embedding, created_at)`; **on conflict `created_at` is preserved** for stable listing position.
`ListMemories` embeds the query, orders by `embedding <=> $q LIMIT k`, and reports
**Score = 1 − cosine distance** to match `Embedding.Cosine`. An empty query gives oldest-first
with Score 0. Dimension is `WithVectorDimensions` (default 1536) and **must match the
Embedder** or pgvector rejects the write.

**The table name is validated to a plain identifier at construction** (`^[A-Za-z_][A-Za-z0-9_]*$`)
because it is composed into raw DDL/DML and identifiers cannot be bound. `%q` is Go escaping,
not Postgres identifier quoting, so validation is the right tool rather than quoting.

### MemorySource and injection

`MemorySource` is a leaf ToolSource like `FuncSource`: `remember` / `recall` / `forget` tools,
plus `Summary(SummaryOptions)` (ambient, recency-budgeted) and
`RecallRelevant(query, RecallOptions{TopK, MinScore})` (relevant to this turn). Tool names are
exported constants and deliberately fixed.

**Memory injection is two transient, self-capping producers** woven into the per-turn slice
before the user message, **never into `a.history`**. That is the #1010 fix: a snapshot
re-appended every turn stacks up in both history and the RunStore log. Contrast events, which
do persist, because each is drained once.

- `Summary` (InjectSummary, recency-budgeted, #1011)
- `Recall` (InjectRecall, top-K over `RecallMinScore`) — the `MinScore` floor is the **poison
  guard**, since a semantic store scores *every* note.

Each is its own step and is **not routed through `EventInjectionPolicy`**, which is exactly why
that type was renamed from `InjectionPolicy` (PR 1016): recall is a query result, not an event
with a name, hint, and window. A unified budget across events + summary + recall is the
deferred arbiter (#1024); the whole pre-turn transform ordering is implicit and accreted
(inject-then-compact falls out of compaction living in the Runner), and an explicit context
pipeline is #1026.

### Compaction

`agent/compaction.go`: `SummarizingCompactor` plus a `TokenEstimator` / `CharTokenEstimator`
heuristic, hooked as `RunnerConfig.Compactor`. It runs **in the Runner** at the top of the turn
on the cloned history, not in the host, **so the eval harness can grade it** — the harness
builds a RunnerConfig. `EventCompaction` fires only on a real shrink. An error aborts the turn,
mirroring Selector.

### Embedder

`agent/embedder.go` is a sibling seam to Provider: `Embed([]string) ([]Embedding, error)`.
**`Embedding` is a defined type `[]float32` with a `Cosine` method**, which gives a future
SIMD/normalize kernel a home (#1018). `OpenAIEmbedder` is no-SDK net/http against `/embeddings`;
`StubEmbedder` is deterministic bag-of-words hashing so the semantic path is CI-testable.

### Sub-agents have no working memory (A7, #1151, PR 1153)

Enforced, not conventional. The memory source lives on the main `multi`; personas get a
filtered `serverTools` view and `AgentSource.Call` bypasses the injection. Guarded by
`TestSubAgentCannotReachParentMemory`, which was proven to fail if personas are handed `multi`.

The principle: a child's location is not guaranteed. In-process `AgentSource` is the degenerate
co-located case, so shared parent memory assumes a co-location that A2 already forbids. A child
that needs memory **owns its own setup entirely** — its store, its namespace, configured by
whoever builds the child, the same encapsulation as a stateful MCP tool owning its DB. Never
point a `WithMemoryNamespaceFunc(a.currentRunID)` at the *parent's* namespace.

Parent→child transfer is params plus injection. Hierarchical recall across children is deferred
behind a prefix/hierarchical namespace query the seam lacks today (it is exact-match).

---

## Tool-result offloading

The just-in-time-context layer: lossless, pay-on-lookup, complementary to compaction rather
than a replacement (#966/#971/#972/#973/#977).

`OffloadingSource` wraps a `ToolSource` like `FilterSource` does. An over-threshold *successful*
result is stored and replaced by a **stub** — a normal `ToolResult` whose text is a preview plus
a ref — so the RoleTool message, the `tool-end` event, and the persisted log all carry the stub.
The log stays faithful to what the model actually saw, and the Runner needs no change.

Wrap the **aggregate** MultiSource, which the host does when `Config.Offload != nil`, so one
`read_tool_result` and one store cover every server. **`IsError` stays inline** — truncating an
error is worse than the size. A per-tool threshold of `0` pins a tool inline. Size is measured
on `toolResultText`, **text only**, so a large base64 blob will not trip it yet (#979).

`ToolResultStore` (`agent/toolresultstore.go`) is `Put`/`Get` with an in-memory default.
**Retention is deliberately not in the interface.** The graceful unknown-ref read
(`read_tool_result` → "no longer available", `IsError:false`) is what makes any backend eviction
safe, so redis uses native TTL and gorm a caller-driven `PruneExpired`. The dep-free
`FileToolResultStore` lives in `agent/` itself — stdlib only, one JSON file per ref,
temp-then-rename, injective ref→filename escaping — and is the natural no-server store for a
local coding agent, where blobs are files the agent can read directly. The gorm backend supports
bring-your-own-table via `WithToolResultTableName`, resolved through `db.Table`.

`read_tool_result` takes `{ref, offset?, limit?, pattern?}`: a char window or a regex grep *into*
the blob. Query, don't page. Text-only today.

Deferred: binary offloading (#979), and streaming/handle-based results for hundreds of MB via
MCP resource links plus a ranged store (#980 — `Put` is not the bottleneck, upstream
materialization is).

**A new tool must not reinvent this** (#1284). Writing `list_files` and `search_files`, the obvious
instinct is to cap the returned bytes so a big repo cannot blow the context window. That is already
solved, one layer up, for every tool at once. Two truncation mechanisms then disagree about the same
output, and the tool's cap is the one that also loses information the store would have kept.

The line is A6. A **result-count** limit belongs to the tool: it bounds the work actually done and
means something to a caller ("the first 100 matches"). **Context economics** belong to the agent
layer, where `OffloadingSource` wraps the aggregate and already owns them. So those tools take a
`limit`, report what it dropped, and do no byte-level truncation at all.

---

## Safety, approval, and reversal

### Annotation defaults are not symmetric (#1260, PR 1265)

`toolHints` in `agent/runner.go` resolves `readOnlyHint`, `destructiveHint`, and `idempotentHint`
in one place, and the reason it is one function rather than three reads is that their spec defaults
disagree with Go's zero value in different directions. Absent `readOnlyHint` means not read-only
and absent `idempotentHint` means not idempotent, so both fall to `false`. **Absent
`destructiveHint` means destructive.** A caller that read the annotation map directly and took the
zero value would treat every unannotated tool as safe to run unattended.

`readOnlyHint` wins over the other two, which the spec says are meaningful only when a tool writes,
so a read-only tool reports non-destructive and idempotent whatever else it declared. Passing a
server's self-contradiction through to policy is worse than normalizing it.

The cost of the chosen shape (plain bools resolved by the Runner) is that a hand-built
`ToolCallInfo{}` reads as non-destructive. That is documented on the field and pinned by testing
the resolution path rather than hand-set values. A tri-state was considered and rejected: it adds a
type to a struct heading for the 1.0 freeze.

`ModeReversibleAuto` keys on the result. Against servers that skip `destructiveHint` entirely it
behaves exactly like `ModeAlwaysAsk`, which is the intended failure direction. Note the host's
`"auto-edit"` alias moved from `ModeReadOnlyAuto` (where it never auto-allowed an edit) to this
mode.

### Spotlight provenance: only `operator` exempts (#1262, PR 1266)

`SpotlightConfig.Classify` returns one of four labels instead of a bool. The tempting mistake is to
exempt `server` as well as `operator`: **trusting the relay is not trusting what it relayed.** A
server the operator runs that returns a third-party document is a courier, not an author. So only
`operator` — output the process computed rather than passed along — reaches the model unfenced.

`resolveProvenance` closes the label set *before* the exemption check, so `"Operator"`,
`"oprator"`, and `""` all fence rather than exempt. The set is deliberately closed rather than
pass-through: a finer taxonomy is a plausible want, but opening it later is additive while closing
it later would break callers.

The default fence names the origin in prose ("fetched from outside this system") rather than
emitting the raw label, so the sentence reads to a model that was never told what `world` means.

### Provenance is derived from the source, not restated in config (#1268, PR 1274)

`spotlight.tools` shipped in #1262 as the only input, which is correct exactly as long as someone
maintains it: connect a server and every tool it advertises silently defaults to `world`, so the
labels stop distinguishing anything and the differentiated fencing never gets used. The host
already knows what each source is, because it chose the id when it called `Add`.

The derivation table lives in `agent/host/provenance.go`:

| source id | label |
|---|---|
| a connected MCP server (`sc.ID`, recorded at Add time) | `server` |
| `subagent:*`, `fanout:*`, `serveragents:*` | `agent` |
| `host`, `runner-control` | `operator` |
| an extension, or anything unmatched | none → `world` |

**`operator` is a closed allowlist and must stay one.** An extension is arbitrary code that may
shell out or fetch, so the host has no standing to vouch for it. Deriving `operator` there is the
one mistake that silently *disables* the mitigation instead of making it noisy. Everything
unmatched falls through to `world`, which marks.

Two things that shaped the implementation:

- **You cannot map a tool name back to its source by matching the name.** A colliding name is
  offered to the model as the qualified `sourceID/name` form, so the classifier sees either shape.
  `MultiSource.OwnerOf` shares `resolveLocked` with `Call` — `resolveLocked` returns the source id
  rather than the source for exactly this reason. A classifier that disagreed with the dispatcher
  would attribute output to a source that never served the call, which is worse than not labelling
  it. `TestMultiSourceOwnerOfMatchesCall` pins the two against each other through a `Resolver`.
- **An ambiguous name gets no label rather than a guess.** Picking the first claimant would be a
  plausible answer that `Call` would not have acted on.

The classifier is bounded by a 2s timeout because `SpotlightConfig.Classify` takes no context and
an index miss makes `MultiSource` re-list every source, which reaches the network. An unreachable
server must not stall tool dispatch.

Known wart: an extension's tools get the `world` fence, which reads "fetched from outside this
system" about code that ran in-process and fetched nothing. Right marking decision, false sentence.
Options and the decision are #1273; it was deliberately not fixed by widening the enum.

Also corrected in that PR: three comments in `agent/multi_source.go` described the qualified form
as `sourceID_name` while the code has always joined with `/` — and `Add` rejects `/` in a source id
*because* it is the separator. The stale docs cost a red test to discover.

### Reversal: restore and compensate are different operations (#1267, PRs 1270/1271/1272)

`agent/ext/checkpoint` holds the seam. The design mistake it was rewritten to avoid: putting undo
in the host is only possible if you first restrict undo to files, which assumes the conclusion. The
host cannot know how to undo `create_issue`; the tool author can. Files are the first
implementation, not the definition.

The load-bearing split, and the reason `Reversal` has two fields:

- **Restore** is a true inverse. Local, order-independent, idempotent, unaffected by intervening
  work, near-certain to succeed. The harness runs it unattended.
- **Compensate** is a new action that partially offsets an old one. Not an inverse (notifications
  fired), order-dependent, fails on permissions the original never needed, and breaks once
  something depends on the effect. **Never auto-run.**

`Reversal.Reversible()` requires a `Restore`. Counting a compensation would let a tool auto-approve
under `ModeReversibleAuto` on the strength of an offset nobody verified. Chaining compensations
automatically would be a saga orchestrator, which A8 rules out.

Three further things that bit or nearly bit:

- **Restore is idempotent, not atomic.** POSIX has no multi-file rename. Everything stages to temp
  files beside their destinations first (so a missing blob or unwritable directory fails before the
  tree is touched), then renames. A failure during the rename phase leaves a mix, and the fix is to
  run it again — the manifest is never consumed. Claiming atomicity would read as a guarantee while
  providing none.
- **Capture runs outside the permission gate**, because extension middleware is applied before it.
  A call the gate then denies is still captured. Harmless (restoring an unchanged file is a no-op),
  and the alternative is capturing after the pre-state is gone.
- **`/undo` reports what it could not undo.** Without that, a turn that edits three files and files
  an issue reports "3 files restored" and the issue goes unmentioned. A safety net with an
  unreported hole is worse than none, because it stops being checked. Read-only and denied calls
  are excluded — neither had anything to undo, and a padded list is one people learn to skip.

### The undo proposer runs in a fresh context (#1267, PR 1272)

`ModelProposer` builds its conversation from the gap list alone; the turn's history has no path to
it. This is a security property, not tidiness: if the turn went wrong through prompt injection,
running the cleanup inside that context asks the attacker to write the cleanup. The gap list is
itself untrusted (it carries arguments the model chose, possibly under influence), which is the
other half of why a proposal reaches a human rather than the dispatcher.

Open tension for the freeze: `Reversal.Compensate` and the proposer tier both produce a suggested
call a human approves, differing only in who authored the suggestion. If `Compensate` never gets a
producer, they collapse into one mechanism with two entry points.

### Confining a path is not a check, it is a handle (#1275, PR 1278)

`agent/ext/files` shipped an escape and the fix is worth keeping, because the reasoning generalises
to anything that takes a path from tool arguments.

The first version resolved symlinks on a path's **parent directory** and then appended the basename
unresolved, so a symlink as the *final* component walked straight out: `workspace/innocent.md ->
/etc/passwd` passed containment and was then followed by the open. The test meant to cover this used
a symlinked *parent* (`link/secret`), which is exactly the case parent-resolution catches, so the
suite was green while the property did not hold. A test that exercises the one member of a class
that already works reads as coverage of the class.

The missing case was not the fault. **Checking a path and opening it by name are two operations, and
every way out of a directory lives in the gap between them.** A symlink is followed at open time, not
at check time, so a check on the path text describes a name rather than the file that name will
reach. And the filesystem can change in between, so even a correct check is a statement about the
past. Neither is fixable inside a `resolve` function, because both follow from the check being
separate from the open.

So confinement is an `os.Root` handle. It resolves each component at open time against the directory
it holds and refuses anything leaving it, collapsing check and use into one act and closing the
TOCTOU window along with the symlink escape. `files.rel` survives only to produce a readable refusal
before any syscall and its doc comment says it is not the enforcement, so nobody audits it for
exhaustiveness it was never meant to have.

A follow-on: the temp file's mode is set with `fchmod` on the descriptor, because chmod by name
re-resolves that name and is documented as racy even through `os.Root`.

The same handle covers traversal, which is the part that is easy to build separately and get wrong
(#1284). `fs.WalkDir` over `root.FS()` cannot leave the root, and it does **not** descend into a
symlinked directory, so neither an escape nor a link cycle is reachable without writing any
traversal logic. A symlink stays visible in a listing as a name, which is useful; what is refused is
reading *through* it. Reaching for `filepath.WalkDir` over a joined path is the version that needs
its own containment argument.

One design note that belongs with the rest: `write_file` has **no create-or-overwrite mode**, and
the absence is the feature. The presence of `expect_hash` alone distinguishes create (refused if the
path exists) from replace (refused if the hash is stale), so no argument combination lands content
on a file nobody looked at. A convenience flag would be the escape hatch that made the anchored
editor decorative, and it is exactly what a model reaches for the moment anchoring feels awkward.

### The same shape in checkpoint wanted a different answer (#1281, PR 1283)

`checkpoint.Restore` had the same path-then-open fault and did **not** get the same fix, which is
the more interesting half.

It renamed and removed at whatever absolute paths the manifest held, without looking at what was
there. Those paths come from `WriteSpec.Paths(args)`, so they originate in tool arguments, and the
gap here is not microseconds: capture happens at the top of a turn, the restore happens whenever
someone types `/undo`. A path has minutes or hours to become a symlink, and then the rename writes
captured content to the link's target, while a path captured as absent is undone with `os.Remove`,
which deletes that target outright.

An `os.Root` handle was the wrong instrument, because it answers *containment* and checkpoint has
no root to be contained by. A checkpointed tool may legitimately write to a cache or temp directory
outside any one tree, so imposing a workspace root would break honest callers to stop a dishonest
one. Confining paths stays the **tool's** job; `agent/ext/files` is the worked example.

What checkpoint gained instead is an *integrity* check, which needs no root: the manifest records
what `Add` found (`kindRegular` / `kindAbsent` / `kindUnsupported`, via `Lstat` so a link is
recorded rather than followed), and `Restore` compares that against what is there now. Restoring
through something that is not what you captured is not something any caller wants, whatever their
layout, so refusing costs no legitimate use.

Three consequences worth keeping:

- **Refusals are per path.** One tampered file should not cost the rest of the turn, and the
  staging phase already gives a clean all-or-nothing abort for real errors.
- **`Restore` returns a `RestoreResult`.** Under per-path refusal a partial restore is a normal
  outcome, so `error` alone cannot express it and a caller seeing nil would conclude the turn was
  fully reversed. That is A11's corollary exactly.
- **Through the `Reversal` seam a refusal is an error**, because that caller is the harness running
  unattended with nowhere to put detail. `/undo` calls `Restore` directly and prints each one.

The capture-side and restore-side `Lstat` catch different cases. Restore-side stops writing through
a link that appeared since. Capture-side stops the blob store acquiring a link target's content,
which matters if the link is later replaced by an ordinary file: restore would then see something
perfectly normal and write the wrong content into it.

### Two extensions compose at the wiring layer, not by importing each other (#1275)

`agent/ext/files` is checkpointable and contains no reversal code, because
`checkpoint.WriteSpec{Tool, Paths}` is a declaration keyed by **tool name**, supplied by whoever
builds the host. `files.EditPaths` is deliberately a plain `func(map[string]any) []string` rather
than a `checkpoint.WriteSpec`, so neither module imports the other.

The alternative, an edit tool declaring its own `checkpoint.Reverser`, is the obvious thing to reach
for and is what C4 exists to prevent: checkpoint's API would become implicitly stabilized for the
edit tool's benefit with no design decision saying the two must interoperate. This is now enforced
for `agent/ext/` as well, by `make check-ext-isolation` in CI (#1277); before that, C4's verifier
was a snippet inside `CONSTRAINTS.md` that walked two trees and that nothing ran.

---

## Persistence

`RunStore` lives in `agent/runstore.go` per the A6 corollary above. gRPC-style req/resp per
`stores/STORAGE_SEAMS.md`; an unknown RunID is app state (`Found=false`), never an error.

**Atomicity is a contract on the interface**: explicit-ID `CreateRun` and `ForkRun` are
all-or-nothing, and `NewRunID` is a retry idempotency key. Backends implement it with a native
primitive each — redis with one atomic Lua script (meta-write last, cluster caveat documented),
gorm with one transaction (sqlite and Postgres; `make testpg` boots a container on **5435**,
since the events gorm store owns 5434). The rationale for "native atomicity primitive per
backend, never hand-rolled claim-last ordering" is documented on both ForkRun implementations.

**Runs are flat copy-on-fork lists.** Messages carry no IDs or parent pointers; `Run.ParentID`
and `ForkPoint` are lineage metadata only and nothing reconstructs history by walking.
**`ForkPoint` is a message count, not a timestamp** — renamed from the issue's `ForkedAt` for
exactly that ambiguity. `ForkRunRequest.AtMessage` forks from an earlier point, which is the
conversation half of checkpoint/rewind. **A partial fork copies no events**, because the audit
stream is not sliceable by message index. Stored blobs being immutable is what will make #966
offloading fork-free.

**Stores stamp `Message.Timestamp` on AppendMessages**: zero gets the store clock, caller-set
wins, the same caller-preserves rule as trace context. The field uses **`omitzero`, not
`omitempty`** — omitempty does not omit a zero `time.Time`. Providers never map it into request
bodies. The stamp lives inside the stored JSON body, so there is no schema column and forks copy
it for free.

Host wiring is `agent/host/persistence.go`: `WithRunStore` plus `AttachRun` (create-or-resume),
`Resume` (must-exist), and `Fork`. It persists at the `RunTurn` append site, and `PersistingEmit`
buffers the event stream into one `AppendEvents` per turn. Failed turns persist nothing;
persistence failures render a warning and never fail the turn.

Retention and GC are still open (#999).

---

## Composition

Design frame: `docs/AGENT_COMPOSITION.md`. Epic 927 is complete.

**The two-axis model.** Multi-agent is not a new engine — it wraps the same stateless Runner.
Context in is injection (server events, memory recall, handoff context: one seam). Control is
tools and signals. Observability is the emit stream, which `SubAgentEvent` nests.

### AgentSource

Agent-as-tool: a child `*Runner` exposed as a `ToolSource`. `Call` runs the child over its own
isolated slice, seeded with `{task}`, and returns its final text. Isolation is structural, so
the Runner never changes, and **supervision falls out of a `MultiSource` of `AgentSource`s**
via the existing aggregation, collision handling, and Selector.

Guards: per-source `MaxDepth` (ctx-threaded, default 3) and an optional ctx-threaded aggregate
`WithAgentCallBudget` across the whole tree. A refused or failed child is an `IsError` result so
the parent's turn continues; only an unknown name is a dispatch error.

**The LLM decides to call a sub-agent**, by the tool's name and description — the parent code
does not route. The Runner is a dumb dispatcher and parallelizes tool calls within one turn, so
N sub-agents invoked in one turn run concurrently with ordered results.

**Structured I/O** (#1033, PR 1159): `AgentSourceConfig.InputSchema json.RawMessage` makes the
tool advertise that schema instead of `{task}`, and `Call` seeds the child with the raw args
JSON. Structured *output* is orthogonal and needs no new field: `Call` returns
`result.Structured` when non-empty (the child ran with a `RunnerConfig.ResponseSchema`), else
`result.Text`.

**Colocation is contained, not removed.** `AgentSource` is the in-process implementation of a
location-independent `ToolSource` contract; a remote sub-agent is a *sibling* implementation,
largely "a sub-agent published as an MCP server, reached via the existing server-tools
ToolSource". What in-process adds is carrying composition metadata (depth, budget, cancellation,
nested event stream) for free; carrying that over the wire is the surface of #1035 / #1036 /
#1038. Lifecycle is decomposed rather than centralized — there is no `SubagentManager`, and a
real lifecycle owner only emerges in the async/Task form.

### SubAgentEvent

`SubAgentEvent{Scope, Depth, Event}` forwards the child's turn-lifecycle emit stream to the
parent's surface via `AgentSourceConfig.OnEvent`. Scope and depth live on the **envelope** so
`Event` stays wire-flat (A2). Scope is ctx-threaded, so nested sub-agents compose the path
(`outer/inner`).

This is **not** MCP domain-event subscription. A sub-agent's `subscribe_events` injects into its
own context, never the parent's. The parent sees the child's *activity*, not its *inputs*.

### Async sub-agents (#1035, PR 1161)

`AsyncAgentSource.Call` acks immediately ("sub-agent X started") and runs the child on a
`core.DetachForBackground` goroutine — not `context.WithoutCancel`, because it outlives the turn
*and* calls MCP server tools, so it needs the session push. On finish, `OnComplete(name,
*TurnResult, err)` delivers the result. Depth and budget are checked at spawn.

Host `onAsyncComplete` mirrors `tasks_bg.go`: ingest a `subagent.completed` `IncomingEvent` into
the injection policy, plus `triggers.OnEvent` and a `HostMessage` notice, so the result is
drained into the next turn's context or fires a proactive turn.

**It is not an MCP task**: no wire, no model-visible poll or cancel, and the ephemeral goroutine
dies with the process. For poll/cancel/durability use a real `ext/tasks` task. The
Tool-vs-Task-vs-real-Task ladder is in `docs/AGENT_COMPOSITION.md`.

### FanOutSource (#1033, PR 1155)

A leaf ToolSource whose one tool broadcasts a task to N member `*AgentSource`s concurrently
(goroutine per member, per-index result slots aggregated in member order) and returns one
combined result. Because it reuses AgentSource, per-member `MaxDepth`, `WithAgentCallBudget`,
and scope all apply. A member failure is **isolated** into the aggregate and marked, never a
dispatch error.

This is broadcast-one-task-to-all (ensemble). Map-style distribution of distinct subtasks was
not part of #1033 and remains unfiled. It is the substrate for sampling/vote (#1056).

Host: `Config.FanOut []FanOutGroupConfig` → `registerFanOut`, added under `fanout:<name>`, with
id underscores turned into dashes because the source id bans `_` while the tool name keeps it.

### Team (handoff)

`Team` is the one mode that does not fit agent-as-tool: handoff transfers control rather than
returning. `NewTeam(TeamConfig{Members, Start, MaxHandoffs, OnHandoff})`; each
`TeamMember{Name, Config, HandoffTo}` gets `transfer_to_<name>` tools for its allowed targets
only, and terminal agents get none.

`Run` loops: the active agent runs; if a `transfer_to` fired (a per-Run ctx signal), swap the
active Runner and continue over the **same shared history**; else return the final answer.
Membership is static — dynamic model-driven composition is #1038. Ping-pong is bounded by
`MaxHandoffs` (`ErrMaxHandoffs`).

Handoff is inject-context plus schedule. The shared-thread swap is one implementation;
per-agent-context plus injection (the actor form) is the general one.

`Team.RunTurn(history, active)` is history-aware and returns all-hops messages plus the ending
active agent. `Team.Run` delegates to it.

**Per-agent event tagging** (#1033, PR 1159): `TeamConfig.OnEvent` forwards each member's events
tagged `{Scope: activeAgentName, Depth: 1}` and **replaces** the raw emit — no double-fire; the
raw emit is the nil-OnEvent fallback.

**The persistence gotcha**: attributed events cannot tee back through the render path or they
double-render, so team mode builds a buffer-only `PersistingEmit` — `NewPersistingEmit(store,
id, nil)`, nil next meaning persist without rendering — exposed via a per-turn `a.teamEventSink`
that the `OnEvent` closure feeds.

### AgentPool and runner-control meta-tools (#1166, PR 1169)

`agent.AgentPool` runs named personas in the background on a `core.DetachForBackground`
goroutine and returns a handle; `NewSpawnSource` exposes `spawn_agent` / `await_agent` /
`cancel_agent` / `list_agents`. **Pull-based `await_agent` is the distinct value** over the
auto-injecting `AsyncAgentSource`.

The pool is agent-layer, not host, even though the issue first sketched it as host-layer:
background runs need the unexported `withAgentDepth` / `withAgentScope` / `DetachForBackground`
plumbing, so it belongs beside `AgentSource`. Host is thin wiring (`Config.RunnerControl`), and
the Runner is unchanged. This is where "an agent that knows and targets its children" is
legitimate, in contrast to A's non-referential child. `transfer_to` stays Team's and is not
duplicated here.

### Multi-model falls out

Each `Runner` / `AgentSource` / `TeamMember` carries its own Provider, so a heavy supervisor
delegating to cheap sub-agents needs no special machinery. Within one conversation:
`ConnectionRegistry` plus `providerSwitch` (`/provider`), `FailoverProvider` (primary/backup),
and the deferred routing policy (#991).

### Composition axes (design discussion → #1157)

"Who responds to the user" decomposes into three orthogonal seams, none coupled to the agent:
the **scheduler** (which agent acts next over shared context — that is Team; the
user-interaction framing is the co-located projection), the **surface** (channel/mode —
Observer/HostEvent), and the **interaction mediator** (order/batch/rewrite/route user-facing
asks — generalize the `ElicitationCoordinator` FIFO). Team's durable value is scheduling
who-acts-next, not owning the user; in the distributed limit it decouples into the actor form
(inject and wake).

---

## Providers

### Generation parameters: how they reach a turn (#1239)

`GenerationParams` (`agent/provider.go`) carries `Temperature`, `MaxTokens`, and `ToolChoice`.
Defaults live on `RunnerConfig.Generation`; `TurnRequest.Generation` overrides per turn, **field by
field, where a zero field inherits**. The corollary is documented on the type: a turn cannot un-set a
config default back to the provider's own, it can only set a different value.

`ResponseSchema` deliberately stays on `RunnerConfig` and out of the struct — it selects a different
*turn shape* (a finalizing `Generate`) rather than tuning the calls a turn already makes.

**The trap in the finalizing call.** `finalizeStructured` offers no tools, so a `ToolChoice` carried
from the turn's params would ask the provider to force a call it has no tool for, which OpenAI
rejects. `applyTo` is shared with the step loop, so the drop happens at the finalize call site, not
inside `applyTo`. `TestGenerationToolChoiceDroppedOnFinalize` pins it.

**How this was ever missing.** Before #1239 the step loop built `ProviderRequest{Instructions,
Messages, Tools}` and nothing else, so three of the four generation parameters the seam defines were
unreachable from a turn: `Temperature` and `ToolChoice` were set nowhere in non-test code, and
`MaxTokens` only as a `failover.go` health probe. Both providers had always rendered all three onto
the wire correctly — the gap was purely loop-side, which is why it stayed invisible. The roadmap's §7
scorecard credited `ToolChoice` as shipped, which is how #1056 came to be sized against primitives
that could not actually be driven.

**Unsupported parameters are forwarded, not screened.** A provider sends what it is given, and a
model that rejects a parameter fails the request rather than ignoring it — current Anthropic models
400 on sampling params. That is deliberate: `ProviderError{StatusCode, Body}` already carries the
vendor's message verbatim (up to 2048 bytes) and names the offending field, so a capability layer
would duplicate diagnostics that already exist, and screening by model would need exactly the
per-model table this project has declined once already (the reason agentchat takes
`--context-window` rather than inferring one). Revisit only if a *response*-side signal such as
logprobs (#1053) needs capability negotiation, which degrades to best-effort rather than failing.

### Tool-name sanitization (PR 1129, `agent/toolname.go`)

OpenAI and Anthropic both constrain a function/tool name to `^[a-zA-Z0-9_-]{1,64}$`. A connected
MCP server can expose a name that violates it — a dot like `chat.message`, a slash, spaces, or
over 64 chars — and both providers used to send `td.Name` raw, so the request 400'd and
**aborted the whole turn**.

`sanitizeToolName` maps invalid runs to `_`, empty to `tool`, and caps at 64.
`toolNameMaps(req.Tools)` builds a deterministic, collision-resolved real↔safe bijection. The
request sends the safe name (tools list, assistant-history `tool_calls`, forced `tool_choice`)
and the response reverses it via `realToolName` so the Runner still dispatches correctly.

**The map is a pure function of `req.Tools`, re-derived independently in the request builder and
the response parser — no state is threaded.** The one exception is the streaming structs, which
carry a `nameMap` field because they outlive the request. **A valid name maps to itself**, so
the wire shape is unchanged for the common case and existing wire-shape tests were untouched.

### Shared SSE Recv loop (PR 1131, `agent/sse_stream.go`)

The two providers' `Recv()` bodies were byte-identical except for the payload decode, including
the subtle partial-event-riding-EOF handling — a bug-drift risk. A shared `sseStream` now owns
the loop, parameterized by `decode func(payload string) (deltas []Delta, done bool, err error)`.
Each provider embeds `sseStream` and supplies its own decode: OpenAI handles the `[DONE]`
sentinel and keeps the tool-call `started` index map; Anthropic returns done on `message_stop`
and err on an `error` event.

`decode` returns `[]Delta` rather than appending to a shared queue, so the loop is the sole queue
owner. Per-stream state (the `started` map, `nameMap`, `inputTokens`) lives on the concrete
stream and is captured by the decode method. `buildBody`, the chunk decode, and the `Generate`
response parse stay per-provider, which is where they genuinely diverge.

### Reasoning / "thinking" display — three distinct paths

1. **Local reasoning models** (deepseek-r1, qwq via LM Studio/Ollama) emit reasoning inline as
   `<think>…</think>` in the text stream. `ConnectionConfig.ThinkingHint{OpenTag, CloseTag}` plus
   `agent.NewThinkingProvider` (PR 1064) wrap the provider to re-tag that text as
   `DeltaReasoning`. Boundary-safe across split deltas; an empty openTag means
   reasoning-from-head; an empty closeTag is inert.
2. **Cloud reasoning APIs** (DeepSeek `deepseek-reasoner`, OpenRouter) return reasoning in a
   `reasoning_content` / `reasoning` delta field, which `OpenAIProvider` **already parses** into
   `DeltaReasoning`. So `{baseUrl:"https://api.deepseek.com", model:"deepseek-reasoner"}` shows
   thinking with no hint configured.
3. **Native Anthropic** parses `thinking_delta` into `DeltaReasoning` but does not enable it on
   the request side.

OpenAI chat models return no reasoning at all — chat-completions does not surface o-series
reasoning. The Runner's `consumeStream` turns `DeltaReasoning` into thinking events, and
`render.go` streams the reasoning text dimmed under `· thinking:`.

### Anthropic caching and extended thinking: dropped (#953, constraint A9)

Both are **loop-invisible** provider optimizations. The Runner gets the same `Delta` stream —
caching is just cheaper, thinking is `DeltaReasoning` we already parse and render — so they
reshape nothing above the Provider seam.

The issue's own premise went stale, which is the cautionary datapoint:
`thinking:{type:enabled,budget_tokens:N}` now **400s** on current models. The current shape is
`{type:adaptive}` plus `output_config.effort`, and **temperature must be omitted, not "set to
1"** — sampling params 400 on those models. Full interleaved-thinking-with-tools would also
thread Anthropic *signed thinking blocks* through the neutral `agent.Message` and the Runner,
which is an A2 violation.

The durable line (A9): deep provider features come from **wrapping the vendor's official SDK
behind the seam**, not from growing the no-SDK provider, which would chase API drift forever.
Loop-*visible* capabilities (logprob→routing, grammar→structured output) are kept and exposed
capability-optionally on the seam.

---

## Host wiring

Reusable host application core in `agent/host/`: config loading (providers, servers, policies,
skills), meta-tools, App/REPL wiring. Surface-agnostic — a CLI and a web chat both build on it.

### The Extension contract had no lifecycle hook (#1250 / #1267, PR 1271)

`Extension` shipped five seams and all five are *contributions*: tools, middleware, prompt
sections, commands, context stages. None is a lifecycle hook. The first real extension
(`agent/ext/checkpoint`) needed to know a turn had begun, and the only workaround available was a
`ContextStage` that returns its input unchanged and exists for its side effect — a producer that
produces nothing, which is a hook in disguise.

So `Extension` gained `TurnStart`. It is called from **both** turn entry points, `RunTurn` in
`app.go` and `runProactiveTurn` in `events.go`: a trigger firing runs the model over history
exactly as a user turn does, so an extension that saw only one would scope its state to the wrong
thing. It runs before history is touched, so a failure aborts cleanly. `BaseExtension` no-ops it.

This also forced `App` to retain its extensions — they were previously only on `appOptions` and
dropped after construction.

The general lesson, and #1252's non-goals already state it: a feature that needs a change to how
turns run has found a bug in the seam, not a reason to work around it. Building the first real
extension found one on first use, which is an argument for building a real consumer before the 1.0
freeze rather than after.

### ContextStage is per-turn; the loop it serves is per-step (#1301)

The prediction above held. Building the first `ContextStage` consumer (`agent/ext/lsp`) found four
more gaps, and the first one changed the design before any code was written.

**Stages run once per turn, at `RunTurn`. A coding agent's edit-check-fix cycle runs across the
tool-call steps inside one turn.** A model that edits at step 3 and keeps working to step 12 gets
nothing from a stage for nine steps, because there is no injection point inside a turn except a
tool result. So diagnostics ship on two paths rather than one, and which path carries which claim
is decided by what history does to it:

- A tool result is permanent. That suits "this edit introduced these errors", which stays true as
  a record of the past, and it is the only thing that reaches the model mid-turn.
- A transient stage is per-turn. That suits "the file currently has these errors", which stops
  being true the moment one is fixed and would otherwise accumulate one stale block per edit.

The generalisation worth keeping: **a context producer whose value decays within a turn cannot be
a `ContextStage` alone.** Memory recall does not have this problem because what it retrieves stays
retrieved. Anything reflecting mutable external state does.

The other three, all recorded on #1240:

1. **No shutdown seam.** `TurnStart` had no counterpart, and `App.Close` never touched extensions.
   Fixed here by adding `Extension.Close`, called in reverse registration order, exactly once even
   if `App.Close` is called twice. Reverse because registration order is dependency order, which
   `surfaces.WorkspaceExtensions` relies on. Note this was a **breaking interface change** for any
   implementor not embedding `BaseExtension`, which is the argument for doing it before the freeze.
2. **An extension cannot observe.** `ToolMiddleware`'s doc says a middleware that merely observes
   should be an event subscriber, but `App.Subscribe` is a method on an `App` that an `Extension`
   is constructed before and never receives. So an extension that wants to watch has no sanctioned
   route. lsp sidesteps it because its middleware genuinely changes the result. The gap is still
   there for the next one.
3. **`weaveBeforeUser` is host-private.** Every out-of-tree stage that wants the "closest to the
   user message is most salient" placement re-implements it. lsp does. It is four lines, so the
   duplication is cheap and exporting it would widen the surface being frozen, but a second
   copy means the rule now lives in two places.

### Sub-agent personas

`Config.SubAgents []SubAgentConfig{Name, Description, Instructions, Allow, MaxDepth}` builds
specialist personas, each an `agent.AgentSource` over a child Runner sharing the main provider
and a `FilterSource`-narrowed view of the same server tools, with its own instructions.

`app.serverTools` is a **server-only snapshot** built in `NewApp`, so a persona never sees the
meta-tools or its siblings and cannot recurse. `personaRunnerConfig` (`agent/host/subagents.go`)
is the shared builder used by sub-agent, fan-out, and team-member construction, which keeps all
three serverTools-only and therefore A7 memory-free.

**Latent bug that was fixed with it**: `app.serverTools` used to be built only when `SubAgents`
was set, so a fan-out-only or team-only config nil-dereferenced. It is now built when any of
SubAgents / FanOut / Team is present.

**Team mode is a distinct top-level mode**, validated mutually exclusive with the main agent's
memory, sub-agents, and fan-out (`validateTeamExclusive` errors) because those attach to one
agent. Per-member integration is deferred. `a.activeTeamAgent` persists the active member across
user turns — sticky control-transfer, not re-routing.

### SystemPromptBuilder (#1091, PR 1132, `agent/host/prompt.go`)

The hard-coded `buildInstructions` became a `SystemPromptBuilder`: an ordered `[]PromptSection`
joined per turn by `Build`, wired as `RunnerConfig.InstructionsFunc` so late-connecting servers'
skills still appear live. `PromptSection` is a one-method interface and `PromptSectionFunc`
adapts a plain func, http.HandlerFunc-style.

`NewApp` assembles the default via `app.defaultPromptBuilder()` — base `cfg.Instructions`
section plus `skillsSection` — which is **shared with the test so the two cannot drift** — then
applies any `WithSystemPromptBuilder` mutator (reorder, `Prepend` a domain guide, replace
`Sections`). Behavior-preserving: the common non-empty-base output is byte-identical, and an
empty base no longer emits leading blank lines.

`InstructionsFunc` remains the low-level Runner hook; the builder is host-layer composition on
top. Richer strategies (profile/domain guides, memory-summary-as-a-section) are additive
`PromptSection`s on this seam, which is why #1091 closed rather than staying open as a gap.

### Skills consumption hardening (#1182/#1183, PR 1185, `agent/host/skills.go`)

Two host MUSTs from SEP-2640's Security Implications, host-internal with no `ext/skills` change.
The origin label is the **host-assigned `serverID`, never `serverInfo.name`**.

- **Origin tagging**: every MCP-served skill body carries its origin before it enters context.
  `load_skill` results get an untrusted-origin banner (`wrapSkillOrigin`); eager and catalog
  injected blocks get a per-server origin header (`originHeader` / `withOriginHeader`) framing
  them as untrusted server data rather than higher-authority instruction.
- **Per-origin name resolution**: `load_skill(name, server?)` no longer first-matches across all
  catalogs, which was a silent cross-origin shadow. `resolveCatalogSkill` resolves within a
  per-origin namespace; a bare name served by more than one origin returns a disambiguation
  prompt rather than silently picking one; collisions surface via `HostSessionWarn`
  (`detectSkillCollisionsLocked`).

### Two-tier skills loading (#910, PR 1073)

Eager full-body injection bloats the system prompt at scale (50 skills × ~100 tok, resent every
call), so loading is progressive. **The catalog decision is per-server, not per-skill**:
`ServerConfig.SkillsMode` (`"eager"` / `"catalog"` / `""`) marks a whole skills server, and `""`
auto-selects by skill-md count (`defaultCatalogThreshold = 10`).

Eager injects `skills.InstructionsBlock` (full bodies). Catalog injects `skills.CatalogBlock`
(one `- name: desc` line per entry, roughly a tenth of the tokens) and collects entries.

**One** `load_skill(name)` FuncSource tool spans all catalog-mode servers: find the entry,
`ReadAndVerify(url, digest)`, and return the body as a **tool result** — into tool history, not
re-injected into the prompt, because swinging the prompt is prompt-cache-hostile. Laziness never
bypasses digest verification. An unknown name is app state, not an error.

Per-server mode was chosen over a connect-time `SkillsPolicy` hook (embedder-first; the two can
coexist). Byte-threshold, body caching, and the policy hook are deferred.

### Connections

`ConnectionsConfig` is data, `ConnectionRegistry` is runtime (`agent/host/connections.go`).
Config is named provider connections mirroring the `llm.json` shape; the registry lazily builds
and caches providers and holds `SetActive`.

`Type` maps to a base-URL preset. lmstudio / openai / ollama / gemini / openrouter / litellm are
all OpenAI-wire; **`anthropic` is the exception** — `DefaultProviderBuilder` branches on it to
build the native `AnthropicProvider` (x-api-key, `/v1/messages`), so a config-selected Anthropic
uses the purpose-built provider rather than a compat shim. openrouter and litellm are
router-gateway presets; litellm defaults to the proxy's local `:4000/v1`.

**The embedder is a config role, like `Active` is for chat.** `ConnectionsConfig.Embedder` names
an embedding connection and `ConnectionConfig.Dim` is its vector width.
`host.BuildEmbedder(conn, tp)` builds an OpenAI-wire `agent.Embedder`. **`anthropic` is rejected
— there is no embeddings API** (both in `BuildEmbedder` and in registry validation). This is what
makes semantic memory usable against a cloud embedder with just an API key and no local model.

### Commands, events, and observers

`CommandRegistry` plus `App.Dispatch` (`agent/host/commands.go`): slash commands are a registry
of `Command{Name, Aliases, Help, Run, Complete}` returning a `CmdResult` tagged union.
`App.Dispatch(ctx, line)` routes a `/cmd` line and returns `ErrUnknownCommand` for misses.
`Complete` powers Tab autocompletion. This is host-layer and deliberately not in the CLI.

**`HostEvent` plus `Observer` fan-out, not "UIEvent".** The host emits domain and lifecycle
events (turn done/failed, session changed, skills loaded, task status) that are meaningful to
OTel or a logger even with no renderer, which is why they are Host events rather than UI events.
`WithObserver(o)` fans one-to-many and suppresses the default renderer;
`NewTerminalRenderer(w)` is one Observer.

**`UIPrompt` was dropped**: a prompt is request/response (an `AskFunc` or elicitation `Confirm`),
not fire-and-forget, so it never belonged in the event stream.

### Session listing and paging

Redis SCAN is unordered; gorm returns newest-first via a correlated subquery. **Pagination lives
at the store layer.** `App.SessionsPage(ctx, cursor)` threads the cursor, remembering the last
`NextCursor` on the App so `/sessions more` advances host-side and the opaque cursor never
reaches the surface. `App.SearchSessions(query)` is a bounded id-substring walk
(`maxSessionSearchScan`) with no store-side filter, because content search needs an index.

### oauth login (#1116/#907, PR 1120)

agentchat's interactive authorization-code `"oauth"` auth type (`agent/host/auth.go`):
`authOption` builds an `ext/auth.OAuthTokenSource` — DCR by default when no `clientIdEnv`, else
a pinned client — and returns it as a `loginSource`, tracked in `App.oauthSources[id]`.

**`App.LoginServer(id)` is `src.Invalidate()` plus `ReconnectServer(id)`**: drop the cached token
to force a *fresh* auth, then let the next connect re-run the PKCE browser flow. This reuses
`Group.Reconnect` and adds no new connection primitive.

The servers view carries a **host-layer `ServerStatus{client.MemberStatus; CanLogin}`**, so
`CmdResult.Servers` is `[]ServerStatus` rather than `[]client.MemberStatus`. `CanLogin` is a
config fact the client layer does not carry, so the host wraps rather than pushing auth
awareness down. Scopes stay empty by default so acquisition follows the server's 401
`WWW-Authenticate` challenge.

Deferred: retrofitting a token source onto a server not configured oauth at boot would need
rebuilding its client, and is unsupported.

**`WithBrowserOpener` seam** (#1123, PR 1136) injects how the oauth type opens the authorization
URL (nil means the platform browser), which is what makes the whole login path testable in CI
with no Docker and no real browser (`agent/host/oauth_login_e2e_test.go`). **Gotcha**: the
oneauth test AS accepts an arbitrary public client for PKCE but does **not** serve DCR, so pin
`clientIdEnv` rather than relying on the DCR default. The real-Keycloak variant is #1137.

### OTel metrics (#1023, PR 1141)

The metrics sibling of the SEP-414 spans, through the existing `core.MeterProvider` seam — no new
seam. `RunnerConfig.MeterProvider` opts in (nil is `NoopMeterProvider`, branch-free zero
overhead); instruments are built once in `NewRunner` (`agent/metrics.go`) and recorded at the
same points as the spans.

- **Turn end**: `agent.turns` by `agent.finish_reason`, `agent.turn.duration`, `agent.steps`,
  `agent.tokens` by `direction`.
- **Each tool call**: `agent.tool.calls` by `tool` and `status`, `agent.tool.duration` by `tool`.

`status` ∈ {ok, error, tool_error, denied, cancelled, unavailable}, set on each outcome path and
emitted by a **single `defer` in `callTool`** so no path double-counts or misses.

Host threads it via `host.WithMeterProvider` (main Runner plus every sub-agent persona). The
`mcpkit — agent` Grafana dashboard (uid `mcpkit-agent`) charts turn rate, latency, token
throughput, and tool failure ratio off Mimir. Note the OTel Prometheus rename: dots become
underscores, counters gain `_total`, and histograms gain a unit suffix, so
`agent.turn.duration` reads as `agent_turn_duration_seconds_bucket`.

Deferred: a memory-tracks metrics seam (embed/recall/compaction counters) and exemplars.

### Critique gate (#1061)

`NewCritiqueGate` asks a model whether a proposed tool call is acceptable under stated principles,
and denies it if not. Three decisions are load-bearing:

**It is middleware, not a new Runner hook.** The issue proposed a dedicated pre-dispatch gate in
the Runner. There already is one, and `toolmiddleware.go` says so: ToolMiddleware is *the single
interception seam*, and a second mechanism at the same point is exactly what that doc exists to
prevent. A critique pass changes whether a call happens, which is what middleware is for.

**It sits after extensions and before the approval gate.** Like the gate it must judge the
arguments that will actually execute, so it cannot sit outside middleware that rewrites them.
Unlike the gate it is automated policy, so it runs first and a call it refuses never reaches the
human. Refusal is `DenyTool`, so the Runner emits `EventToolDenied`, tells the model why, and the
turn continues.

**It fails closed.** `AllowOnError` defaults false: a critique that could not be completed denies.
A safety gate that disappears when its provider is down is not a safety gate, and a provider
outage is not evidence a call is safe. The zero value being the safe one is deliberate.

The proposed call reaches the critic inside the **same untrusted fence Spotlight uses**
(`delimitMark` + a per-call `newMarker`), because the arguments are the part an attacker controls:
an injected instruction that reached the agent through a tool result can be echoed straight back
out as an argument, and handing that to the critic as prose hands the attacker a second prompt.
That narrows the surface without closing it, which is the honest claim.

**The critic's output is fenced too, and that is the less obvious half.** Fencing the input and
leaving the return path open protects nothing. The critic is a model that has just read
attacker-controlled arguments, so it can be steered into writing an attacker's text as its reason,
and that reason reaches the agent as `tool call not permitted: ...`. A denial arrives in the
*policy layer's voice*, which the agent has every reason to trust more than the tool result the
text came from, so an unfenced refusal is an injection channel with a promotion attached. The
attacker pays one blocked call for a payload aimed at the next one.

`fenceCritiqueReason` mints a **fresh** marker rather than reusing the one `critiquePrompt` used,
because the critic saw that one: a critic that echoes it would close the fence from inside. It
also does not reuse `delimitMark`, whose sentence names a tool and says the content was fetched
from outside, neither true of a verdict written in-process. A fence whose explanation is false is
the failure mode #1273 is about.

This is what forced `ToolDeniedError.ModelReason`. A fence is several lines, and `agent/host`
renders a denial as one truncated line against the call, so pushing the fence through the surface
showed the user boilerplate and cut the actual reason off. `Reason` is now the legible attributed
line surfaces show and `ModelReason` is what the model is told; empty means they are the same,
which is right for every middleware whose reason is its own prose. The two audiences had been
sharing one string since the type was written, and nothing noticed until a reason needed to be
untrusted. Caught by `TestCritiqueConfigRefusesARealCall`, which asserts on rendered output.

Not built: critiquing the **final answer**. The issue mentions it, and it does not fit
ToolMiddleware because there is no tool call. It needs a seam that does not exist, and inventing
one speculatively is what #1288 is a cautionary tale about. Left for whoever has a caller.

Not audited: whether other middleware echo attacker-controlled argument text into their reasons.
The three `DenyTool` sites in `approval.go` are host-authored constants, so critique is the first
denial whose reason is not the host's own words, but a third-party middleware could be.

---

## Eval

`agent/eval/`: multi-turn `Scenario` plus `RunScenario`, which threads history and a shared
`MemorySource` across turns. **One Runner is reused** — it is stateless over history, and the
store is what persists.

`agent/eval/longmemeval` `SmokeScenarios()` are **hand-authored, not the LongMemEval dataset**:
short turns, not ~100k-token histories. The real dataset loader is #1014.

`MemCase.NewCompactor` and `Scenario.NewMemoryStore` are factories, so the harness can grade
different implementations through the same scenarios.

A tool denial emits `EventToolDenied` with `Event.Reason`, is fed back as model-visible text, and
**the turn continues**. It is deliberately not an error, so `eval.NoError` ignores it and
`eval.NotDenied` is the separate check.

### AgentDojo is a framework, not a dataset (#1060)

#1060 asked for "an adapter that loads AgentDojo-style cases", which assumes a corpus. There is not
one. A user task is a Python class whose `utility(output, pre_env, post_env)` inspects environment
state before and after the run, an injection task pairs a GOAL with a `security(...)` check, and the
environments are stateful Python objects whose tools mutate them. The scoring **is** code run against
live state, so nothing exports as data.

So `eval/agentdojo` rebuilds the *shape* in Go, the way `longmemeval`'s `SmokeScenarios` borrows
LongMemEval's categories without its corpus. Its numbers are ours, not comparable to published ones,
and the package doc says so. The bridge that would buy comparability is #1298.

Three things that suite has to get right, all of which a single pass/fail destroys:

- **The injection lives in tool output, never in the prompt.** The attacker does not control the
  user's request. A payload delivered through instructions measures instruction-following.
- **Two axes, scored independently.** The dangerous cell is utility-pass with security-fail: the
  agent did the job *and* paid the attacker, which reads as clean success to any single verdict. The
  opposite corner matters too, since an agent that refuses everything has perfect security and no use.
- **Scorers read post-run state, not the transcript.** An agent that says it will not pay the
  attacker and then calls `pay` has resisted nothing, and only the state distinguishes the two.

`SuiteReport.Dimensions()` is the per-dimension rollup #1015 deferred to here. It is a **method over
data `CaseReport` already carried**, not new fields, which is what the deferral was betting on.
Inversion (an attack-success rate is `1-Rate` of a resistance dimension) stays in the caller, because
making `Rate` mean opposite things depending on the scorer name would be worse than the arithmetic.

**What the CI tests do not prove:** a stub provider cannot show that spotlighting changes a real
model's behaviour. `TestGuardrailChangesTheMeasurement` verifies the *apparatus* distinguishes
defended from undefended runs, and would pass even if the guardrail were a no-op on real models. The
effect itself is a live-run measurement.

### The adapter seam (#1015)

External suites are **data sources**, not harnesses. `Adapter.Load` yields `[]SuiteCase`, and
adding LoCoMo / BFCL / tau-bench must not touch the Runner, the scorers, or `Suite`.

Three things had to change for that, and each was forced by a real shape rather than anticipated:

- **Scorers are per-case.** `Suite.Scorers` was one list applied to every case, which can only
  express properties true of all of them ("did not error"), never the ground truth that makes a
  benchmark a benchmark. The evidence was `longmemeval/live_test.go`, which hand-rolled its own
  copy of `Suite.Run` because `Suite` could not take it. That file is now the harness call it
  should always have been.
- **`Scenario.History` seeds without running.** A memory benchmark supplies a long prior
  conversation and asks one question about it. Replaying that as `Turns` would call the model once
  per historical message and grade its own invented replies. History is data the agent is told it
  had; Turns are turns the agent takes.
- **`SuiteCase.Configure` returns a config rather than mutating one**, so a compaction case's
  `Compactor` cannot leak into the next case. `TestSuiteCaseConfigureIsPerCase` observes the
  handed-in config through `Configure` rather than through `Result`, because `Result.Case` carries
  only the turn's name and input — asserting there compares two zero values and passes either way.

**Ungradeable is a failure, not a pass.** `Suite.Run` already failed a case with no scorers. The
subtler version bit immediately: the abstention smoke case is graded by a `Rubric`, and its only
deterministic scorer is a `MustNot` that *any* answer satisfies. Reachable from the default build
for the first time (the adapter is untagged; `live_test.go` never was), it passed against a stub
answering "stub answer". So `appendRubric` now yields `eval.Ungradeable` without the `eval_llm`
tag. A case graded only by checks that cannot fail is not graded.

**Aggregation is single-axis on purpose.** `SuiteReport.Passed/Failed` counts whole cases, which
is right for one-question-per-case and wrong for a suite reporting several independent rates — a
security suite wants utility and attack-success counted separately, and one of them inverted.
Deliberately not designed for now: per-scorer verdicts are kept with their names, so a
per-dimension rollup is an addition over existing data rather than a re-plumb. #1060 is the one
that will actually stress it, and reshaping is free while the agent track is unreleased.

---

## Testing

- **Run agent tests with `-race`.** The concurrency-shaped tests (`agent/signal_test.go`,
  `agent_pool_test.go`, `interruptible_test.go`) are the point.
- **Host behavioral tests race on a shared sequential StubProvider** when the main turn and a
  background child both pull from it. So host tests assert *wiring* and the *behavior* is
  agent-layer-tested with isolated per-child providers. `blockingProvider{}` in
  `agent/runner_test.go` is the "child that blocks until cancelled".
- `make test-agent` covers `agent/`, `agent/host/`, the surfaces, and the agent examples.
- **The CI `test-agent` job runs example tests as explicit hardcoded steps in
  `.github/workflows/test.yml`**, not via `make test-agent`. Moving or adding an example needs
  the workflow updated too, not just the Makefile.

### Reading a mutation run (learned on #1275, #1284)

Red-before-green here means mutating the implementation and naming which tests fail.

**A surviving mutation is a signal to diagnose, not a verdict.** Three quite different things
produce an identical-looking "no test failed", and across #1275 and #1284 all three happened:

- **A real test gap.** The one you want to find.
- **An unreachable mutation.** Reverting `edit_file`'s rename to an unconfined `os.Rename` changed
  nothing, because the earlier `Stat` through the root already refused. That is defence in depth
  working. Tell it apart by mutating the whole path at once and seeing whether the behaviour moves.
- **A broken or lying harness.** Two "survivors" were really `declared and not used` and
  `missing return` in the mutation itself, which produces no `--- FAIL` lines and reads exactly like
  a gap.

So the harness needs two guards of its own, and neither is optional:

- **Assert the mutation applied.** `shasum` the file before and after; if it is unchanged the
  pattern did not match and the result is meaningless.
- **Detect build failures separately**, before parsing for `--- FAIL`.

Even with both, do not trust a surprising survivor. On #1284 a run reported the binary-detection
mutation as surviving; running the same mutation by hand three times, with and without the `gofmt`
step, showed the test failing on it exactly as intended. The discrepancy was never explained. The
tests were right and the tooling around them was not, which is the correct order of trust: hand-verify
a survivor before you write a test to close a gap that may not exist.

### A green suite is not evidence of the property you think it is

The failure mutation testing exists to catch, and it turned up four times in one session
(2026-08-10) in four different disguises. Each looked like coverage of a class and exercised only
the member that already worked:

- **`agent/ext/files` symlink containment.** The test used a symlinked *parent directory*, which is
  exactly what parent-resolution catches. A symlink as the *final* component escaped the root, and
  the suite was green while the property did not hold.
- **Malformed `edit_file` arguments.** Every case was an *entirely* malformed list, so dropping a
  bad element left zero hunks and the call still failed, via `ErrNoHunks`, for the wrong reason. A
  list with one good edit and one bad one would have applied the good one and reported success.
- **`checkpoint` capture through a symlink.** `TestSymlinkAtCaptureIsNotFollowed` leaves the link in
  place at restore time, so the restore-side guard refuses either way. Swapping `Lstat` for `Stat`
  in `capture` survived the entire suite.
- **`write_file`'s type and boundary checks.** Both sit in front of `os.Root`, which refuses either
  way, so deleting them broke nothing any test asserted. See below, because this one resolves
  differently from the other three.

The shape is the same each time: **a second guard downstream makes the test pass regardless of the
behaviour it names.** So the fix is usually the same too, and it is what to reach for rather than
adding another case alongside the existing ones. Construct the scenario so the downstream guard is
absent, which usually means letting the interfering condition resolve before the assertion (replace
the symlink with a real file; mix a valid element in with the invalid one). If you cannot remove
the second guard, the test cannot distinguish the two implementations and is not testing what its
name says.

### Sometimes the guard is load-bearing for the message, not the behaviour (#1284)

`write_file` refuses a non-regular target, and distinguishes a path that escapes the workspace from
one that merely does not exist. Remove either and `os.Root` still refuses the write, so no safety
property changes and no outcome-based test can see the difference. The obvious conclusion is that
the guards are redundant. They are not.

What they change is **what the model is told**. Without the type check, writing to a directory is
reported as *"already exists, pass expect_hash to replace it"*, which is advice that can never
work: the model follows it, is refused again, and has burned a turn discovering that the tool
misdescribed the problem. Without the boundary branch, a symlink pointing out of the workspace
reads as an ordinary write failure rather than a containment refusal, which points debugging in the
wrong direction.

For a model-facing tool the message *is* part of the contract, because the model's next action is
computed from it. So those two tests assert the wording rather than the outcome, and say so in a
doc comment — otherwise the next reader sees an assertion on a string, concludes it is brittle, and
deletes it.

---

## Examples

Agent examples live under `examples/agents/`: `agent-async`, `multi-agent`, `kitchen-sink`,
`deep-agent-supervisor`. Skills and tasks-v2 examples stay with their SEP.

Shared recipes in `examples/agents/common.{just,mk}` (`agent` / `demo` / `test`) that each
example imports. `demo.sh` resolves model and endpoint from `llm.json` when `MODEL` / `BASE_URL`
are unset.

**`examples/agents/llm.json`** holds named connections in the `ConnectionsConfig` shape with
**no keys, only `apiKeyEnv` names**, read via `os.Getenv` at runtime. The active connection is a
local model so `demo` works offline. A model router (OpenRouter/LiteLLM) is just a connection
with `baseURL` at the gateway. It ships a per-model menu across providers plus dedicated
embedder connections and an `"embedder"` role, so a copy inherits the full switchable set.
`llm.local.json` is gitignored and overrides per machine.

### kitchen-sink

The "every feature wired at once" harness, reusing the chat surface rather than adding a binary.
Variable-driven (`SESSION_STORE`, `EMBED_*`, `OFFLOAD_THRESHOLD`, `COMPACT_TOKENS`, `EXPORTER`,
`ACTIVE`), with `allup`/`alldown` across `docker/backends` and `docker/observability`, a
`preflight.sh` that probes postgres/OTLP/embedder and prints bring-up commands, and a small demo
MCP server (own go.mod: `greet`, a large-output `report` that trips offloading, and `analyze` for
a sub-agent).

`kitchen-sink.json` wires signals, interruptible turns, and the runner-control pool together
(`analyst` has `canSignal:true`; top-level `signalPolicy:"inject"`, `interruptible:true`,
`runnerControl:true`), plus a `review_team` fan-out and an async `deep_researcher`.
`kitchen-sink-team.json` is the triage → billing/technical team config, run via a `CONFIG`
parameter since team mode has no memory.

**Two operational gotchas that bit hard here**, both also covered in
`agent/surfaces/chat/NOTES.md`:

1. **Server readiness must be a TCP probe, not `curl GET /mcp`.** A GET to an MCP endpoint opens
   the server's SSE stream and never returns, so a curl-based check hangs the launch forever. Use
   `(exec 3<>/dev/tcp/host/port)`. `scripts/playground.sh` still has the hanging-curl form.
2. **Backgrounded demo-server logs must be redirected off the terminal** (`>$LOG 2>&1`). They,
   and mcpkit's per-connection SSE logging, clobber the inline TUI's input region.

**Never commit compiled binaries** — see the root `CLAUDE.md`; this is now gated in CI.
