# Epic — Web UI surface for the agent host (issue 1193)

Status: proposed. Owner: agents workstream. Relationship to `AGENT_SDK_ROADMAP.md`: this is a
surface/observability epic, not an SDK-parity phase. It sits beside Phases 4–7, not inside them.

## Motivation

Two reasons, and the second is the stronger one.

1. **A second live surface.** The host layer (`experimental/agent/host`) was deliberately built surface-agnostic
   so a web chat could reuse the whole thing and swap only stdin/stdout for a socket. `experimental/agent/surfaces/chat`
   is a thin terminal surface over it. A browser surface should be another thin surface over the same
   `App`, viewable at the same time as the TUI.

2. **Observability the TUI cannot linearize.** A lot of what the agent does is not a linear scrollback:
   sub-agent trees and fan-out concurrency, memory injection vs recall vs compaction, tool-result
   offloading stubs and the blobs behind them, token and tree-budget consumption, upward signals and
   preempt grants, team handoffs. A dockable-panel web UI where each of these is its own movable panel
   is a materially better window into a running agent than scrollback can be. Every one of these
   already has a distinct `HostEvent` kind, so the panels map onto an existing taxonomy.

## Non-goals

- **Not a Grafana/OTel dashboard.** The agent already emits SEP-414 spans + metrics and ships a
  Grafana dashboard off Mimir/Tempo. That is ops-grade, aggregate, multi-session. This surface is live
  single-session introspection for development and use. Draw the boundary explicitly.
- **Not a rewrite of the host.** No web framework or proto types leak into `experimental/agent/host`, the same
  discipline that keeps charm/lipgloss out of it. The web surface owns all of that.

## The backbone: `gocurrent.Queue` as the per-session event log

`gocurrent.Queue[T]` (already an indirect dep) is an append-only log with offset-based reads,
per-subscriber wake-up channels, N-consumer fan-out, late-subscriber replay from offset 0, and
`AppendBarrier`/`Resolve`/`AwaitResolution` barrier semantics. It fits this epic almost exactly:

| Design need | Queue mechanism |
|---|---|
| Stream host events to N surfaces (TUI + every browser tab) | `Append(frame)` + per-subscriber `Notify`/`ReadFrom`, each surface tracks its own offset |
| A tab joining a live session sees full history | late-subscriber replay from offset 0, no separate replay path |
| Multi-surface ask: all get it, first responder wins, others auto-dismiss | `AppendBarrier(ask)` blocks the coordinator; any surface `Resolve(offset, resp)`; first writer wins (`ErrAlreadyResolved`); the resolution is a committed log entry every other surface reads and retracts on |

Today the host uses `Observer` (fan-out to renderers) for host→surface and `gocurrent.FanIn` for the
inbound MCP `events/stream` merge. `Queue` is unused. This epic makes a per-session `Queue` the spine:
`emit` records every event on it (the retained source of truth), local observers read it synchronously,
and the web streamer subscribes onto it asynchronously.

### Do we still need `Observer`?

The Queue is the source of truth; `Observer` is the synchronous local view of it. The decisive point
stands: a pending ask **cannot** be an `Observer`, because `On(HostEvent)` returns nothing and an ask
needs a resolution channel (`AppendBarrier`/`Resolve`). So asks ride the Queue regardless, and once
events and asks both ride it, the Queue is the one substrate: retention, replay, the barrier, and the
seam a remote surface subscribes onto.

Implementation (E1) sharpened *how* local observers read it. The first cut made every observer an
async drain goroutine, but that **races the terminal contract**: the built-in renderer writes to a
caller-visible `io.Writer`, and several existing `-race` tests (plus the plain REPL's own prompt
ordering) read that buffer synchronously right after an emit. Nothing needs the async isolation yet —
the only remote consumer, the web streamer, does not exist until E3. So E1 keeps `emit` a **synchronous
local fan-out** (unchanged behavior, `emitMu` retained) while **also appending to the retained log**.
The async subscriber adapter, which a remote surface genuinely wants for backpressure isolation, lands
with that surface in **E3**. Retention was unbounded in the first cut (Option A); the bounded window
with deep replay from the `RunStore` **shipped** (issue 1200) — see the E1 retention note below.

### Three wire categories

Naming these explicitly clarifies the whole protocol. They map cleanly onto Queue ops:

- **Host events** — push, fire-and-forget (the observability stream) → `Append`.
- **Pending asks** — push delivery but correlated request/response resolution (elicitation, approval)
  → `AppendBarrier` / `Resolve`. This does not violate the earlier "a prompt is request/response, not a
  fire-and-forget event" decision. It stays request/response; it just has N delivery endpoints and a
  first-wins collector.
- **Queries / commands** — pull, unary request/response (`App.Dispatch` → `CmdResult`, plus the host
  data methods `SessionsPage`, `ServerTools`, `ListMemories`, `ServerStatus`). Not on the Queue; these
  are point-to-point reads of host state.

## Frontend stack (reuse Agni)

Agni's `web/` + `cmd/agni serve` pattern ports almost whole:

- goapplib/templar server-rendered shell at `/`, esbuild bundle under `/static/`, Connect handlers
  under their proto service paths, all on one `servicekit/http` listener.
- `dockview-core` for the dockable layout; Solid islands mounted into server-rendered "holes" via the
  park-container adopt/dispose pattern; `@panyam/tsappkit-solid` island shell.
- Connect-Web typed clients generated from proto (buf), same-origin transport.
- Agni's **saved-layout reconcile** (SavedLayout stores the panel-id registry beside the dockview
  layout; a newly added panel appears in the menu without auto-opening, a user-closed one stays closed)
  is a direct gift: an agent UI accretes panels over time and this handles that for free.

The one net-new piece over Agni is the **live stream**. Agni is request/response only. Connect
server-streaming RPC (`Watch`) is the idiomatic slot, with the Queue subscription as producer.

## Layering

`experimental/agent/surfaces/web` is a new submodule (own go.mod) importing `experimental/agent/host`: the goapplib shell, the Connect
bridge (Queue subscription → `Watch` stream; `Dispatch`/queries → unary; barrier → `RespondToAsk`),
and the Solid/DockView frontend assets. A thin `cmd/agentweb serve` over it, mirroring how
`experimental/agent/surfaces/chat` is thin over the host.

## Open design decisions (resolve during E1/E3, not now)

- **Frame envelope shape.** `HostEvent` and `CmdResult` are churning Go tagged unions. A full proto
  `oneof` over a taxonomy that keeps gaining kinds is drift-prone. Lean toward an envelope with a
  stable `kind` enum plus a structured payload where settled and a JSON/`Any` escape hatch for the long
  tail. Decide against the actual `HostEvent` surface.
- **Observer role (resolved in E1, see above).** The Queue is the one retained substrate; `emit`
  appends to it and delivers synchronously to local observers (the terminal renderer writes a
  caller-visible `io.Writer`, so async local delivery races callers). The async subscriber adapter for
  a remote surface lands in E3, where the isolation is actually needed.
- **Auth.** Local dev tool first. If the surface is ever exposed beyond localhost it needs auth; out of
  scope for the first cut, note it.

## Sub-tickets (dependency-ordered)

### E1 (1194) — Session event log on `gocurrent.Queue` (`experimental/agent/host`) — SHIPPED
Make a per-session `gocurrent.Queue[HostEvent]` the retained event substrate: `emit` appends every
event to it *and* keeps delivering synchronously to the local observers (`emitMu` retained), so the
terminal rendering contract is unchanged. The log is what a web surface (E3) subscribes onto (replay
from offset 0 + live) and the barrier seam E2 builds on. The async subscriber adapter
moved to E3 — see "Do we still need Observer?" for why async local delivery races the terminal contract.
- Accept: a subscriber attached after events were emitted replays them from offset 0 and is notified of
  live ones; `emit` still delivers to local observers synchronously and in order; `just test-agent`
  green with `-race`; no TUI behavior change.

#### E1 retention follow-up (1200) — bounded event log + `RunStore` deep replay — SHIPPED
The unbounded first cut (Option A) is now bounded on gocurrent v0.1.2's `NewBoundedQueue`, and
`App.Subscribe` deep-replays evicted-but-persisted turns from the `RunStore` so a subscriber attaching
after eviction still sees full history.
- `Config.MaxEventLogRetention` (0 = unbounded, today's behavior for embedders; positive caps the
  retained window). `NewApp` builds `NewBoundedQueue(n)` when set. agentchat `--max-event-log`
  defaults to a generous 100000, so a single turn never exceeds the window; a config value wins, and
  `--max-event-log 0` opts back into unbounded.
- `App.Subscribe(ctx) <-chan HostEvent` stitches replay from two sources that meet exactly at
  `persistedOffset` (the event-log offset through which the run's events are in the store, advanced at
  the turn-end persist site once `AppendEvents` succeeds): (a) the run's persisted `agent.Event`s from
  the `RunStore`, each wrapped as `HostEvent{Kind: HostRunnerEvent}` — the deep history that may have
  evicted from the Queue; (b) the Queue tail from `persistedOffset` forward — the unpersisted
  in-progress/recent events, still in the retained window; then (c) live via `Notify`. No dup (the
  store covers `[0, persistedOffset)`, the drain starts AT `persistedOffset`) and no gap. Non-runner
  `HostEvent`s (server-state, skills) before `persistedOffset` are ephemeral and intentionally not
  replayed — the store only holds the runner stream.
- **Consistency (the persist race):** the snapshot — Queue `Subscribe`, reading `persistedOffset`, and
  reading the `RunStore` — is taken under `turnMu`, which the persist site also holds while it advances
  `persistedOffset` and writes `AppendEvents` together. So a turn-end persist can never interleave the
  snapshot: `RunStore` contents and `persistedOffset` are always read as one consistent pair, ruling
  out both the dup (store ahead of the offset) and the gap (offset ahead of the store) a two-step read
  would race. Subscribing before releasing `turnMu` means no live `Append` is missed. Delivery then
  happens off-lock in a goroutine, so a slow subscriber never blocks a turn (the same store-under-
  `turnMu` shape `Resume`/`Fork`/`AttachRun` already use).
- Accept: with a small retention the log evicts and caps; a subscriber attaching after eviction (with a
  `RunStore`) replays the full conversation with no dup and no missing turn; without a `RunStore` it
  gets the retained window then live; `persistedOffset` advances at turn end; `just test-agent` green
  with `-race`.

### E2 (1195) — Pending-ask barrier (`experimental/agent/host`) — SHIPPED
`barrierElicit` (`experimental/agent/host/ask.go`) wraps the local `ElicitationUI` the coordinator drives: it emits a
`HostElicitRequest{Elicit}` (appended to the log at offset `off`), runs the local UI as one responder,
and races it against `RespondToAsk(off, result, by)` from any other surface. **Resolution rides the
event log's own barrier**: local and remote responders both call `eventLog.Resolve(off, ...)`, so the
first writer wins and `RespondToAsk` gets `ErrAlreadyResolved` / `ErrOffsetOutOfRange` for free; the
loser's context is cancelled; a `HostElicitResolved{AskID: off, By}` tells every surface to retract.
With no other surface attached the local UI always wins, so single-surface behavior is unchanged.

**Design note**: the ask is identified by its **log offset** (the position a surface reads from its
`Watch` frame), not a minted id, so it needs no id field in the request event. This is simpler than a
separate pending-ask registry and reuses the log's tested first-writer-wins barrier; a late-joining
surface can even call `eventLog.Resolution(off)` to see an ask was already answered. Two small costs:
`AwaitResolution` is not ctx-cancellable, so a cancelled turn resolves the ask itself to unblock the
awaiter; and `emit` now returns the append offset.

**Invariant — at most one pending input at a time.** The barrier per offset can in principle handle N
concurrent pending asks, but there is never more than one: the `ElicitationCoordinator` FIFO-serializes
*every* ask (elicitations and approvals alike) onto one UI via a single baton, and `barrierElicit` runs
*inside* that baton, so a second ask (even from a parallel tool call in the same turn) blocks in the
coordinator and its `HostElicitRequest` is never even emitted until the first resolves. The log is
therefore always `… req_A … resolved_A … req_B …`, never two live asks. So the consumer side never has
a queue of pending inputs to reconcile, and the FIFO stream stays non-blocking for consumers (they
render the ask and keep draining; only the producer's tool call blocks). The one scenario that would
create concurrent inputs is multi-user / multi-surface routing (different asks to different users, the
issue-1157 mediator's territory) — and the per-offset barrier already scales to that with zero change,
so keying resolution on the log offset is both simpler now and the right bet later. The load-bearing
fact is the coordinator invariant; the barrier does not depend on it.
- Accept: two responders on one ask, first wins, loser cancelled, resolved event names the winner;
  out-of-range / already-answered `RespondToAsk` returns the log's error; `just test-agent` green with
  `-race`.

### E3 (1196) — `experimental/agent/surfaces/web` submodule + Connect bridge — SHIPPED
New submodule (`experimental/agent/surfaces/web`, own go.mod, module `github.com/panyam/mcpkit/experimental/agent/surfaces/web`) + a thin
`cmd/agentweb` serve binary (inside the module, mirroring how `experimental/agent/surfaces/chat` is thin over the host).
**Transport: Connect + buf.** Proto `mcpkit.agentweb.v1.HostService` (in `experimental/agent/surfaces/web/protos/`, generated
Go + Connect committed under `experimental/agent/surfaces/web/gen/go/`): a server-streaming `Watch` (drains the E1 log via the
new `App.Subscribe` seam), unary `Submit` (turn), `Dispatch` (command → `CmdResult`), `RespondToAsk`
(the E2 offset barrier), and two trivial queries `ListSessions` + `GetStatus`. `servicekit/http` serves
the placeholder shell + `/static` + the Connect handlers on one listener.

- **`App.Subscribe(ctx) <-chan HostEvent` (`experimental/agent/host/subscribe.go`)** — the async subscriber adapter
  E1 deferred. It replays the retained log from offset 0 then follows `Notify()`, on a drain goroutine
  scoped to ctx. A slow consumer cannot block `emit`: `emit` only `Append`s (non-blocking) and fans out
  to local observers, while the drain reads the retained log at its own pace, so back-pressure stays
  contained to the one subscriber and nothing is lost. Local observers stay synchronous (unchanged).
  It also stamps the **ask id** — E2's event-log offset — onto the delivered `HostElicitRequest` copy
  (the stored entry is emitted without it), so a remote surface reads the id off its Watch frame and
  answers via `RespondToAsk(offset, …)`; the frame's `ask_id` is that offset, cast at the RPC boundary.
- **Frame envelope, reconciled with A2**: `Frame{kind, payload bytes}` where `payload =
  json.Marshal(HostEvent)` — a 1:1 projection, no per-kind proto schema to drift. `Dispatch` reuses the
  same `{kind, json}` shape for `CmdResult`. The pointer-bearing kinds (`TaskStatus`, `Task`, the
  failover inside a command result — issue 994) fall back to a minimal `{kind}` payload on a marshal
  failure so one un-serializable event never stalls the stream.
- **Deviations from the brief**: (1) `Watch` is an idiomatic **Connect server-streaming RPC**, not a
  `@panyam/massrelay` room. massrelay is the E4 *frontend* consumption transport (WebSocket fan-out, as
  in diffpp); its `AppMessage{Kind, Payload}` shape is exactly this `Frame`, so E4 bridges Frame → room
  1:1. Keeping the server side a plain Connect stream is what makes the whole bridge testable in-process
  with a Go Connect client (no WebSocket, no browser). (2) `Watch` sends an empty-`kind` ready sentinel
  as its first frame: the Connect protocol flushes response headers on the first `Send`, so without it a
  client attaching to an idle session blocks on `Watch` until the first event; the sentinel opens the
  stream promptly. Clients skip an empty-kind frame. (3) The shell is a minimal inline HTML page, not a
  templar template — the real templar/DockView shell is E4.
- Accept (met): a Connect client `Watch`es a live session and `Submit`s a turn; a local Observer and a
  Watch client on the same `App` see the same stream, including replay-from-offset-0; a `RespondToAsk`
  over the wire wins an ask the local UI is blocking on (`experimental/agent/surfaces/web/host_bridge_test.go`, run with
  `-race`). The serve binary serves the shell, `/static`, and the Connect endpoints (`GetStatus`
  returns the model label; `ListSessions` returns `failed_precondition` with no RunStore).

### E4 (1197) — Frontend shell (DockView + Solid islands) — SHIPPED
Modeled on **`~/projects/diffpp/main/web`**: `dockview-core` v4, `tsappkit-solid` islands, Connect-Web
clients generated from the E3 proto, and **`MobileOverlays` for the mobile mode** (the dockview-vs-mobile
switch). Saved-layout reconcile carries over. The frontend lives in `experimental/agent/surfaces/web/web/`; the built esbuild
bundle is committed under `experimental/agent/surfaces/web/static/` (Go embeds it, so `just test-agent` needs no Node step).

**Scope change from the plan: massrelay is deferred.** The browser consumes the E3 Connect `Watch`
server-stream **directly** via connect-web (`web/src/watch.ts` — decode each `Frame` payload to a
`HostEvent`, reconnect with backoff; replay-from-0 makes a re-subscription safe). Reintroducing massrelay
for a shared multi-tab room is a later follow-up, not needed for the single-surface live stream.

What shipped: the placeholder shell is replaced by a server-rendered shell (`shell.go`, stdlib
`html/template` — island holes + `data-layout`, no goapplib/templar dep for one static page); one
**Conversation** panel (streams the live turn off `Watch`, a prompt box that `Submit`s, an approval prompt
on `HostElicitRequest` answered via `RespondToAsk(AskID, …)`); a framework-neutral panel registry +
saved-layout reconcile (`web/src/dock.ts`) ready for E5 to add panels; the DockView desktop layout and the
mobile-overlay layout over one shared store; and a `--demo` mode on `cmd/agentweb` (offline streaming
provider) + `experimental/agent/surfaces/web/run.sh` as the runnable proof. Unit tests (vitest) cover Frame-decode/dispatch, the
event fold, and the dock reconcile.
- Accept (met): the browser renders live turns from the same `App`; the layout persists across reload
  (localStorage); the mobile mode presents the same panel as an overlay.

### E5 (1198) — Observability panels — SHIPPED
The payoff. Five Solid islands, each a projection of the one Watch/`HostEvent` stream, added to the
DockView registry (`web/src/dock.ts`) via the saved-layout reconcile so they appear in the Panels menu:
- **Sub-agent tree** (`web/src/subagents.ts` + `SubAgentPanel.tsx`) — `HostSubAgentEvent` scope/depth
  assembled into a nested pre-order tree with per-node status + tool-call count.
- **Activity timeline** (`timeline.ts` + `TimelinePanel.tsx`) — the whole `HostEvent` stream as a
  bounded, kind-filterable ledger.
- **Memory inspector** (`memory.ts` + `MemoryPanel.tsx`) — compaction events off the runner stream plus
  an on-demand `Dispatch("/memory")` read. Recall / injection are transient pre-turn transforms with no
  event, so compaction is the event-driven half.
- **Tool-call & offload inspector** (`tools.ts` + `ToolsPanel.tsx`) — the tool lifecycle matched
  begin to end by call id, with an offload stub's ref surfaced (the blob is fetched internally via
  `read_tool_result`, not exposed on the web bridge).
- **Budget / token gauges** (`budget.ts` + `BudgetPanel.tsx`) — per-turn provider usage + steps
  accumulated across turns, with an input/output split bar.

One `WatchStream` now feeds a `PanelStores` bundle (`stores.ts`) whose `ingest` fans each event to every
projection; the mobile overlay grows one launcher tile per panel. New panels register in `DOCK_PANELS`
(so they show in the menu and in a fresh default layout) but are gated out of `RECONCILE_AUTO_OPEN`, so a
user's saved #1197 arrangement is not re-crowded. Vitest covers the trickier projections (sub-agent tree
assembly, timeline reduce, tool matching/offload detection, budget fold). The `--demo` provider was
extended to delegate to two personas so a single demo turn populates every panel.
- Accept: during a demo (or `kitchen-sink`) run each panel reflects live agent state; adding a panel does
  not re-crowd existing users' saved layouts (reconcile). ✓

### E6 (1199) — Multi-surface elicitation UX + symmetric submission — SHIPPED
Wire E2's barrier end to end across web (`RespondToAsk`) and TUI so a real elicitation/approval
broadcasts to all surfaces, first answer wins, others dismiss with "answered by <surface>". Any surface
can submit a turn (turnMu already serializes); everyone watches it stream.

The web side is a projection off the one Watch stream, in `web/src/conversation.ts`:
- **Retraction with a receipt.** A `HostElicitResolved{AskID, By}` retracts the shown prompt and, when
  another surface answered, shows a receipt: `answered on terminal` (the terminal responder resolves as
  `local`) or `answered in another browser tab` (a peer web surface resolves as `web`). A self-answer
  just retracts, no receipt. The reducer keeps `activeAskId` after an optimistic retract and tracks the
  offsets this tab answered, so the race where this tab clicked but another surface won first still
  reads the correct receipt (`By` names the winner, not the clicker).
- **Symmetric submission.** A `turn-begin` with no local submit pending is a turn from elsewhere; the
  transcript tags it (`· from another surface`) on the streaming bubble and the committed turn. Local
  turns are untagged. The origin badge is best-effort (two surfaces submitting in the same instant can
  mis-attribute one cosmetic badge); control serialization is the ask barrier's job, not the badge's.
- Vitest covers both reducers (`conversation.test.ts`): request → resolved-by-other → retracted +
  receipt; the local-answer path; the this-tab-clicked-but-terminal-won race; stale-receipt clearing;
  and the remote-vs-local origin tagging.
- `--demo` gates the `researcher` delegate behind an approval `ask` and stands in a scripted terminal
  responder (`demoElicitUI`, an 8s auto-accept), so a single browser demonstrates the prompt and the
  cross-surface "answered on terminal" retraction with no second surface to script.
- Accept: an approval-ladder prompt answered in the browser dismisses the TUI prompt and vice versa;
  two near-simultaneous turn submissions serialize cleanly and both surfaces see both turns.

## Suggested slice order

E1 → E2 → E3 → E4 gets one end-to-end proof (TUI-and-browser-on-one-session, with real multi-surface
asks). E5 is the incremental panel build-out on top. E6 is the multi-surface elicitation polish, which
can land alongside E4/E5 once E2's barrier exists.
