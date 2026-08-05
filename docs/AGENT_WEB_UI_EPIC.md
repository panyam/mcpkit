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
with that surface in **E3**. Retention is unbounded for now (Option A); because `PersistingEmit`
already writes each turn's events to `RunStore`, a bounded in-memory window with deep replay from the
store is the filed follow-up.

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
from offset 0 + live) and the barrier seam E2 builds on. Retention unbounded (Option A); a bounded
gocurrent variant with deep replay from `RunStore` is a filed follow-up. The async subscriber adapter
moved to E3 — see "Do we still need Observer?" for why async local delivery races the terminal contract.
- Accept: a subscriber attached after events were emitted replays them from offset 0 and is notified of
  live ones; `emit` still delivers to local observers synchronously and in order; `just test-agent`
  green with `-race`; no TUI behavior change.

### E2 (1195) — Pending-ask barrier (`agent/host`) — SHIPPED
`barrierElicit` (`agent/host/ask.go`) wraps the local `ElicitationUI` the coordinator drives: it mints
an `AskID`, emits a `HostElicitRequest{AskID, Elicit}` to every surface, runs the local UI as one
responder, and races it against `RespondToAsk(AskID, result, by)` from any other surface. The first
responder wins (a one-shot `sync.Once` per ask); the loser is cancelled; a `HostElicitResolved{AskID,
By}` then tells every surface to retract. With no other surface attached the local UI always wins, so
single-surface behavior is unchanged.

**Design note**: this uses a host-owned pending-ask registry keyed by a minted `AskID`, **not**
`gocurrent`'s offset-keyed `AppendBarrier`/`Resolve`. The reason is the id has to travel *in* the
broadcast `HostElicitRequest` event so a surface knows what to answer, but a log offset is only known
after `Append` returns (chicken-and-egg). The registry mints the id up front; the log still carries the
request/resolved events for rendering and replay.
- Accept: two responders on one ask, first wins, loser cancelled, resolved event names the winner;
  unknown / already-answered `RespondToAsk` returns an app-state error; `just test-agent` green with
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
