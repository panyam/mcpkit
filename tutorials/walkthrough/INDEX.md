# Walkthrough Index

Single-page projection of the entire walkthrough graph. Per-page headers in the individual files are the source of truth; this file is an aggregated view.

Use this to:

- Draw the full graph without parsing every page header
- Spot orphans (pages no other page leads to / no other page references)
- Check the precondition closure when adding a new root
- See all mid-journey branch points in one place

> [!NOTE]
> When you add or change a page, also update its row here. If this file falls out of sync with the per-page headers, the per-page headers win.

## Nodes

| Page | Kind | Preconditions | End-state (summary) | Leads to |
|------|------|---------------|---------------------|----------|
| [README](./README.md) | meta | — | reader knows where to start and where to find conventions / graph | — |
| [STRUCTURE](./STRUCTURE.md) | meta | — | author/reader knows the DAG model, root contract, note-block roles, branch-point convention, target-shape tracking | — |
| [bring-up](./bringup.md) | root | none (foundational) | session live; transport chosen; auth resolved; protocol version + capabilities locked; `initialized` sent | transport-mechanics; (forthcoming) per-request anatomy; (forthcoming) re-init / resumption |
| [transport-mechanics](./transport-mechanics.md) | root | none (foundational) | host/session/HTTP-request/SSE-event/JSON-RPC-message arity distinct; wire format known per transport; layering (MCP/JSON-RPC/framing/bytes); POST vs GET roles (POST = client→server one-shot; GET = standing server→client back-channel, may idle); `Mcp-Session-Id` server-issued, mandatory on subsequent requests, **routing key on server (not client filter)**; sessions isolated; JSON-RPC correlation + per-direction ID spaces; reverse-call origination gated by handler context, recorded for cancellation propagation | (forthcoming) per-request anatomy; (forthcoming) reverse-call; (forthcoming) tasks; SSE resumption (leaf); experimental events ext (branch) |

## Mid-journey branch points

Inline `> [!NOTE] **Branch →**` callouts within journeys, aggregated:

| In page | At step | Branches to |
|---------|---------|-------------|
| transport-mechanics | "GET: long-lived server→client back-channel" / `Last-Event-ID` | (forthcoming) SSE resumption |
| transport-mechanics | "GET: long-lived server→client back-channel" / events as first-class | [`experimental/ext/events/`](../../experimental/ext/events/README.md) |
| transport-mechanics | "Reverse-call origination" | (forthcoming) reverse-call mechanics |

## Forthcoming nodes (referenced but not yet written)

These are mentioned as "Leads to" or "Branch →" targets on existing pages. Written as the conversation reaches them.

| Planned page | Kind | Will assume | Will establish |
|--------------|------|-------------|----------------|
| per-request anatomy | root | bring-up, transport-mechanics | dispatch model, middleware chains, handler context, typed binding, response correlation |
| reverse-call mechanics | root | bring-up, transport-mechanics, per-request anatomy | parent-handler-context constraint operating live; mrtr-on-both-sides symmetry |
| notifications | root | per-request anatomy | progress / list-changed / resource-updated; fire-and-forget dispatch model |
| tasks (v1 / v2 / hybrid) | root | per-request anatomy, notifications | long-running operations, detach/resume, task store; the v1→v2 migration shape |
| SSE resumption | leaf | transport-mechanics | replay semantics; `event_ids.go` mechanics |
| middleware composition | branch | per-request anatomy | request-side vs. sending-side; ext/auth and ext/ui interception points |
| initialize deep-dive | leaf | bring-up | full capability flag enumeration; version negotiation edge cases |

## Full graph

```mermaid
graph LR
    L0[L0 · a request happens]

    bringup["bring-up<br/>(root, foundational)"]
    wire["transport mechanics<br/>(root, foundational)"]

    anat["per-request anatomy<br/>(root, planned)"]
    rev["reverse-call mechanics<br/>(root, planned)"]
    notif["notifications<br/>(root, planned)"]
    tasks["tasks v1/v2/hybrid<br/>(root, planned)"]

    resume["SSE resumption<br/>(leaf, planned)"]
    mw["middleware composition<br/>(branch, planned)"]
    init["initialize deep-dive<br/>(leaf, planned)"]

    L0 --> bringup
    L0 --> wire

    bringup -.->|drills into| wire
    bringup -.->|unlocks| anat
    wire -.->|unlocks| anat
    wire -.->|unlocks| rev
    wire -.->|unlocks| tasks

    anat --> rev
    anat --> notif
    anat --> mw
    notif --> tasks

    wire --> resume
    bringup --> init

    classDef written fill:#e8f5e9,stroke:#2e7d32,color:#000;
    classDef planned fill:#fafafa,stroke:#9e9e9e,stroke-dasharray:4 3,color:#555;
    class bringup,wire written;
    class anat,rev,notif,tasks,resume,mw,init planned;

    click bringup "./bringup.md"
    click wire "./transport-mechanics.md"
```

Solid green = written. Dashed grey = planned but not yet written.

## Orphan / coverage check

- **Pages with no inbound edges** (other than the README/L0): none currently — both written roots are foundational.
- **Pages with no outbound edges**: none currently.
- **Roots whose end-state nothing depends on yet**: bring-up and transport-mechanics each have planned dependents, but until those are written, the dependency is implicit.
