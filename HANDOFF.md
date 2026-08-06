# HANDOFF — Tasks + Events into MCP Apps (SEP scoping)

Pick-up doc for the effort to bring "modern" MCP features (Tasks, Events) into MCP Apps,
with the larger goal of making MCP + Apps able to deliver everything CIP delivers. Written
mid-conversation with Luca (Tasks SEP author) at the events WG. Self-contained; read top to
bottom.

## 1. Goal and context

- Proposed at the events WG: bring modern features (tasks, events) into MCP Apps. Good reception.
  Casey Chow (respected voice) showed interest and is a target to loop in.
- **Larger goal (the user's):** obsolete CIP, i.e. make MCP + Apps able to do everything CIP
  proposes. The user's read is that ~90% of CIP is already expressible in MCP. This doc's job is
  to nail the remaining ~10% and sequence it as small, landable SEPs.
- **Live thread:** Luca (core maintainer, author of Tasks/SEP-2663) is engaged on the *tasks*
  half. He is skeptical-but-collaborative. Getting a small SEP co-authored with him is the
  priority, over getting everything in.

## 2. The three codebases (what was learned)

Three parallel research passes were run. Key findings:

### MCP Apps (mcpkit `ext/ui/`, upstream ext-apps spec `2026-01-26`)
- An MCP App = a tool whose `_meta.ui.resourceUri` points at a resource serving self-contained
  HTML with MIME `text/html;profile=mcp-app`, rendered by the host in a **sandboxed iframe**.
- Server and app never talk directly. Two channels bridged by the host: MCP/JSON-RPC (host↔server)
  and postMessage (app↔host). The **bridge** (`ext/ui/assets/mcp-app-bridge.ts`, exposes global
  `MCPApp`) is a JSON-RPC-over-postMessage client, and its message catalog is a **fixed enum**.
- **The core gap: there is no server→app push path.** Server notifications (`resources/updated`,
  `progress`) die at the host; they never reach the iframe. `notifications/progress` isn't even in
  the bridge switch. The sanctioned workaround is client-side polling on a timer (system-monitor
  polls; pdf-server is parked waiting on a "long-poll backend", issue 554).

### mcpkit Events (`experimental/ext/events/`) and Tasks v2 (`ext/tasks/`, SEP-2663)
- Both have a **pull half** (`tasks/get`, `events/poll`) that is plain request/response and rides
  the existing bridge with **zero new transport**. `pollIntervalMs`/`nextPollSeconds` are in the
  wire shapes already.
- Both have a **push half** (`events/stream`, `notifications/tasks`, `notifications/events/event`)
  that bottoms out in `core.CanNotify()` needing an SSE channel that terminates at the **host**,
  not the iframe. Webhooks are a non-starter for a browser UI (SSRF guard rejects loopback).
- Precedent: SEP-2575 stateless-wire already forbids server push and routes input-gathering
  through MRTR (`tasks/update` with an on-the-wire continuation token). That poll-driven
  `tasks/get → inputRequests → tasks/update` loop is the transport-agnostic shape an iframe wants.

### CIP (`~/work/cip-server-workspace/`)
- CIP = "Context-Injected Prompts", a GM Envolve reference server for bidirectional AI↔UI. The AI
  both observes user actions in a live web app and drives that app's UI.
- CIP gets its richness by **abandoning the sandbox**: the AI drives the host app's own
  first-party SPA components over a typed event bus (`<cip-ai-box>`). It explicitly cannot reach
  into an iframe and assumes SPA-at-app-root. So its architecture is the inverse of MCP Apps and
  cannot be copied wholesale.
- CIP capabilities of interest: typed open-ended server→UI event stream; async turn model; live
  tool/thinking lifecycle; `ui-event` (AI→UI fire-and-forget commands); `ui-request` (AI→UI
  awaited RPC = elicitation as native app UI); **UI→AI automatic context injection (the namesake
  mechanism)**; runtime capability discovery + live system-prompt regeneration; durable/resumable
  streaming (Redis Streams + cursors); distributed cancellation; the Playbook proactive engine
  (event-triggered, stateful, server-initiated behavior with no user turn).

## 3. The central architectural insight (topology)

- **CIP = "AI in a UI".** The app/SPA is the host; the AI is a guest it summons. May have no chat
  box; the "conversation" is implicit and the AI reacts to user actions.
- **MCP Apps = "UI in an AI chat".** The chat/agent is the host; server UI is a guest dropped into
  the conversation.
- The iframe bridge (MCP Apps) and the `cip-ai-box` event bus (CIP) are **the same mediation
  layer** with the host role flipped. The iframe/sandbox is not fundamental to "UI + AI"; it is
  fundamental to "*untrusted guest* UI in my page". Flip the host so the app is first-party and
  the sandbox requirement evaporates. Topology is about **who is host / who trusts whom**, not
  capability.
- **What "AI inside a UI" requires, and where MCP stands:**
  1. Embeddable agent runtime — **MCP has it** (`agent/host` is surface-agnostic).
  2. UI→agent typed-event channel that can also **trigger a turn** — **MCP lacks it as a standard**
     (has the consuming machinery: `EventInjectionPolicy`/`TriggerPolicy`/`IncomingEvent`/Ingest,
     and the untyped stub `updateModelContext`, but no typed wire contract and no turn-triggering).
     This is the load-bearing gap.
  3. Agent→UI control — **mostly present** (model calls app-registered tools = `ui-request`); the
     delta is `ui-event` fire-and-forget commands.
  4. Non-transcript rendering surface — a consumer choice, not a wire gap.
  5. UI-as-loop-driver (turn triggered by a UI event, not a typed message) — a control inversion;
     footgun (loops/cost); must be policy-gated + rate-limited from day one.
- **Payoff:** the events-from-apps direction is what makes the bridge *symmetric*. Once a UI can
  emit typed events the agent ingests (and optionally trigger turns), the same protocol surface
  supports both topologies and **which one is host becomes a deployment choice, not a protocol
  fork.** So events-from-apps is the strategically load-bearing SEP for the CIP-obsolescence goal.

## 4. Verified facts (checked against source, safe to assert)

- Bridge app→host surface is only: `callTool`→`tools/call`, `readResource`→`resources/read`,
  `sendMessage`→`ui/message`, `updateModelContext`→`ui/update-model-context` (wraps arg as
  `{context}`, typed `unknown`), `openLink`, `downloadFile`, `requestDisplayMode`, plus
  notifications `size-changed`/`initialized`/`teardown`/`log`/`tools/list_changed`. Source:
  `ext/ui/assets/mcp-app-bridge.ts`.
- `ui/message` and `ui/update-model-context` are **untyped, imperative, and have no host-side
  semantics defined anywhere.** mcpkit's own `AppHost.handleAppRequest` (`ext/ui/app_host.go:227`)
  **blindly forwards every app→host request to the MCP server** via `h.client.Call`, so it does
  not route them to any model. "Forwards to the LLM" is unspecified per-host folklore, not a
  contract. The "rich" fixtures (budget-allocator, map) already call `updateModelContext` after
  every UI interaction, so latent demand for typed context injection exists, met today by an
  ad-hoc untyped call.
- **The bridge has ZERO task affordance** (no `tasks/get`/`update`/`cancel`, no task-id concept).
  Verified by grep.
- App tools carry `Execution` as a passthrough; the convention is `taskSupport: "forbidden"`
  (`ext/ui/extension.go:102-106`). Nothing wires a task-capable app tool to task-aware rendering.

## 5. The SEP program (three small SEPs)

The CIP-obsolescence goal resolves into three small, landable SEPs (not one grand one):

1. **Tasks in Apps = "Task-backed, resumable App views rendered from task creation."** The
   near-term win, being co-designed with Luca. Details in section 6.
2. **Events from Apps** = type the untyped `updateModelContext` push into a declared, discoverable
   UI-event-source contract, add optional trigger semantics. The strategically load-bearing one
   (makes the bridge symmetric; enables AI-in-UI). Ambient-context injection = mandatory core;
   trigger-a-turn = optional, policy-gated. The user wants to work the nuances of this next after
   tasks.
3. **AI→UI push commands** (CIP `ui-event`) = a later channel; the one direction with no primitive
   at all yet.

Conceding scenarios 1 and 3 to Luca (below) is **not** a loss for the CIP goal — those are cases
the iframe already handles, i.e. proof of the "already 90% MCP" thesis. Architecture stays
deliberately different from CIP (sandboxed, untrusted-server-UI); that difference is the pitch:
"CIP's capability set plus the security model CIP gave up."

## 6. Tasks SEP — the state of the Luca discussion

### Luca's pushbacks (his words, decoded)
He responded point-by-point to three task scenarios that had been floated:
- **(1) app-invoked long work:** No Task needed. The view returns instantly, does its own work off
  the connection (no SSE/sessions, works on plain sHTTP), and hands results to the model via
  `ui/message`.
- **(2) a task with a UI:** The one he **likes**. "A nice use case for stacking an App on top of a
  Task." Names the blocker himself: the App resource only renders on the **final** `CallToolResult`,
  so a task (returns `CreateTaskResult` immediately) can't have a UI. Suggests: **scaffold a view
  on a `CreateTaskResult`.**
- **(3) in-app input:** No Task needed. The view renders/collects input itself, "possibly without
  an actual `input_required` state transition at all."
- **Resumability (he raised it himself):** reconstructing a torn-down App isn't safe in the general
  case for idempotency reasons. Unsure it justifies integration alone, "but combined with (2) it
  could be."

Net: he conceded (2), rejected (1)/(3) as redundant with iframe + `ui/message`, and handed over
the justification (resumability + idempotency) that makes (2) load-bearing. Collaborative
narrowing.

### Our position
- **Concede 1 and 3 outright** (the iframe genuinely does them; fighting costs credibility).
- **Defend the 2 + resumability core.** The user is fine landing *just* 2 + resumability.
- **Sharpened argument (the key one):** *resumption is the host's job, not the app's.* The host is
  what tears the iframe down (display-mode change, tab switch, backgrounding, reconnect) and the
  Apps design already says the view can vanish. So the host must bring it back, and it can only do
  so safely with a handle that is (a) host-visible, (b) idempotent to read, (c) re-renderable
  without re-executing. App-private state fails (a); replaying the tool fails (b, Luca's own
  point). A **Task** satisfies all three: addressable, host-visible, `tasks/get` is a pure read.
  That is why the durable state must be a Task.
- **Honest boundary (state before Luca finds it):** task-backing resumes only view state that is a
  *function of the task* (progress/result), not arbitrary local DOM. Fine, because the class this
  is for is exactly "a view onto a long operation."
- **Concrete minimal surface (none of it exists today, verified):** (i) let a task-capable tool
  carry a ui resource; (ii) host renders the view on `CreateTaskResult`; (iii) the view receives
  its `taskId` (the one genuinely new bridge datum); (iv) the view drives `tasks/get`/`update`/
  `cancel` over the bridge (poll baseline, no new transport); (v) on teardown+reopen the host
  re-scaffolds from `(resourceUri, taskId)`, view reconstructs via `tasks/get`; (vi) optional
  later: push status instead of poll (needs a host→iframe notification-forward seam).

### Messages: sent vs pending
- **First message drafted and approved to send** (concedes 1 and 3, parks 2). Content:
  > Yeah, fair. You've talked me out of 1 and 3. For 1, the view returns instantly and does its own
  > work off the connection, then hands anything back to the model via `ui/message`, so wrapping
  > that in a Task buys nothing. Same for 3: the view can render and collect the input itself
  > without ever going through an `input_required` transition. Tasks add ceremony there, not
  > capability. Which leaves 2, and I think that's the one that's actually interesting. Let me chew
  > on it a bit before I make the case. I want to be sure I've got the nuances right rather than
  > hand-wave it.
- **Pending / NOT yet drafted-to-send:** the message making the case for 2 + resumability. The user
  wants to **work through the nuances of 2 first** before drafting it. A full draft reply exists in
  conversation (the "resumption is the host's job" one) but is on hold until the user is ready.

### Likely Luca follow-ups to have answers ready for
- "The host doesn't actually tear iframes down in practice." → It already can and does
  (display-mode, tab switch, mobile background, memory pressure, reconnect), and the Apps design
  explicitly tells authors the view is ephemeral.
- "Why not just cache the `CallToolResult` and re-deliver it?" → For a long op there is no result
  to cache until done; the operation is in-flight across teardown. The Task is the addressable
  in-flight handle; a cached result does not exist yet, and re-invoking to get one isn't idempotent.

## 7. Communication constraints (do not drop)

- **Do NOT mention CIP to Luca.** CIP framing is for the user's own strategy only.
- Send to Luca in **small messages** (concede first, then hold, then make the 2 case later).
- Prefer **plain text** for any GitHub references (no auto-backlinks). No em-dashes in prose.
- The user values being challenged. Push back on architectural smell with reasons.

## 8. Casey Chow (pending)
- Wants to ping Casey to loop him in. A ping draft exists (leads with the missing-server→app-push
  hook + the keystone/tasks/events shape). **Open questions before sending:** where to reach him
  (Discord / WG channel / email), and send-now (plant flag) vs wait-for-concrete-shape. Do NOT
  mention CIP in anything shared externally.

## 9. Immediate next actions
1. Work through the **nuances of scenario 2** with the user (what "scaffold a view on
   `CreateTaskResult`" changes in the render flow; exactly what state survives teardown; where the
   idempotency line sits). Then draft the second Luca message.
2. Optionally prep the two Luca-follow-up counters (section 6) as ready replies.
3. Return to the **events-from-apps** SEP framing (type `updateModelContext`; discovery; optional
   triggers) once tasks is settled.
4. Resolve the **Casey ping** logistics and send.
5. Optional deliverables offered but not yet made: a one-page topology diagram (two topologies +
   shared mediation layer + the one channel that makes them symmetric); a sequencing section for
   the brief (three SEPs, dependency order, who to co-author with).

## 10. Artifacts
- A fuller scoping brief was drafted this session at
  `<session-scratchpad>/apps-async-sep-brief.md` (session-scratchpad is ephemeral; the essential
  content is folded into this HANDOFF). Re-generate into the repo if a durable shareable copy is
  wanted.
- Key source refs: `ext/ui/assets/mcp-app-bridge.ts` (bridge/message catalog),
  `ext/ui/app_host.go` (host forwarding, `:227`), `ext/ui/extension.go` (`:102-106` taskSupport
  convention), `ext/tasks/` + `core/task_v2.go` (Tasks v2), `experimental/ext/events/` (Events),
  `docs/APPS_DESIGN.md` / `docs/APPS_HOST.md` / `examples/apps/FLOW.md` (Apps flow + edge cases).
