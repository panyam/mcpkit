# Epic — Web UI surface for the agent host (issue 1193)

Status: proposed. Owner: agents workstream. Relationship to `AGENT_SDK_ROADMAP.md`: this is a
surface/observability epic, not an SDK-parity phase. It sits beside Phases 4–7, not inside them.

## Motivation

Two reasons, and the second is the stronger one.

1. **A second live surface.** The host layer (`agent/host`) was deliberately built surface-agnostic
   so a web chat could reuse the whole thing and swap only stdin/stdout for a socket. `cmd/agentchat`
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
- **Not a rewrite of the host.** No web framework or proto types leak into `agent/host`, the same
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

`agent/web` is a new submodule (own go.mod) importing `agent/host`: the goapplib shell, the Connect
bridge (Queue subscription → `Watch` stream; `Dispatch`/queries → unary; barrier → `RespondToAsk`),
and the Solid/DockView frontend assets. A thin `cmd/agentweb serve` over it, mirroring how
`cmd/agentchat` is thin over the host.

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

### E1 (1194) — Session event log on `gocurrent.Queue` (`agent/host`) — SHIPPED
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

### E2 (1195) — Pending-ask barrier (`agent/host`) — SHIPPED
`barrierElicit` (`agent/host/ask.go`) wraps the local `ElicitationUI` the coordinator drives: it emits a
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
- Accept: two responders on one ask, first wins, loser cancelled, resolved event names the winner;
  out-of-range / already-answered `RespondToAsk` returns the log's error; `just test-agent` green with
  `-race`.

### E3 (1196) — `agent/web` submodule + Connect bridge
New submodule + `cmd/agentweb serve`. **Transport: Connect + buf + `@panyam/massrelay` for the live
stream** — the stack the user's own web apps use (`~/projects/diffpp/main/web`, `~/work/hw/Agni`), and
mcpkit already pulls in `servicekit` + `templar` (goapplib serving) so only `connectrpc`/`buf` are new.
Proto `HostService`: a streaming `Watch` (drains the E1 log via massrelay), unary `Submit` (turn),
`Dispatch` (command → `CmdResult`), `RespondToAsk` (the E2 registry), and query methods mirroring the
host data methods. goapplib/`servicekit` serve of shell + `/static` + Connect handlers.
- **Frame envelope, reconciled with A2**: the wire frame is a thin proto `{kind, payload bytes}` where
  `payload` is the event's own A2 JSON — a 1:1 projection, no per-kind proto schema to drift (A2 forbids
  a translation layer). `HostEvent`'s remaining live-pointer fields (`TaskStatus`, `Task`, failover)
  need serializable snapshots first — that is the already-filed issue 994, a dependency of `Watch`.
- Accept: a Connect client can `Watch` a live session and `Submit` a turn; a TUI and a web client on the
  same `App` see the same stream simultaneously.

### E4 (1197) — Frontend shell (DockView + Solid islands)
Model on **`~/projects/diffpp/main/web`** (preferred over Agni): `dockview-core` v4, `tsappkit-solid`
islands, `@panyam/massrelay` + Connect-Web clients from the E3 proto, and **`MobileOverlays` for the
mobile mode** (the dockview-vs-mobile mode switch the user wants). Saved-layout reconcile carries over.
First slice: one Conversation panel streaming the live turn + a prompt box that `Submit`s, in both the
dockview desktop mode and the mobile-overlay mode.
- Accept: the browser renders live turns from the same `App` the TUI is on; the layout persists across
  reload; the mobile mode presents the same panel as an overlay.

### E5 (1198) — Observability panels
The payoff. Each panel is a Solid island fed by a Frame projection, shipped incrementally:
sub-agent tree (`SubAgentEvent` scope/depth), activity/event timeline, memory inspector (recall /
injection / compaction events + `ListMemories` query), tool-call & offload-blob inspector
(`read_tool_result`), budget/token gauges (tree budget + provider usage).
- Accept: during a `kitchen-sink` run each panel reflects live agent state; adding a panel does not
  re-crowd existing users' saved layouts (reconcile).

### E6 (1199) — Multi-surface elicitation UX + symmetric submission
Wire E2's barrier end to end across web (`RespondToAsk`) and TUI so a real elicitation/approval
broadcasts to all surfaces, first answer wins, others dismiss with "answered by <surface>". Any surface
can submit a turn (turnMu already serializes); everyone watches it stream.
- Accept: a `kitchen-sink` approval-ladder prompt answered in the browser dismisses the TUI prompt and
  vice versa; two near-simultaneous turn submissions serialize cleanly and both surfaces see both turns.

## Suggested slice order

E1 → E2 → E3 → E4 gets one end-to-end proof (TUI-and-browser-on-one-session, with real multi-surface
asks). E5 is the incremental panel build-out on top. E6 is the multi-surface elicitation polish, which
can land alongside E4/E5 once E2's barrier exists.
