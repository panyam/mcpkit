# agent/ — read before editing here

The agent SDK sits **above** the protocol layer. It is versioned and released independently and
is not part of the MCP specification surface.

## Where things are

| Need | File |
|---|---|
| Enforceable invariants (A1–A9) | `agent/CONSTRAINTS.md` |
| Why the code is shaped this way, what bit us | `agent/NOTES.md` |
| Terminal surface lore (TUI, notebook, overlays) | `agent/surfaces/chat/NOTES.md` |
| Design frames | `docs/AGENT_DESIGN.md`, `docs/AGENT_COMPOSITION.md`, `docs/AGENT_MEMORY_FLOW.md` |
| Roadmap and phase status | `docs/AGENT_SDK_ROADMAP.md` |
| How to use the host / the CLI | `agent/host/README.md`, `agent/surfaces/chat/README.md` |

## The invariants that cause wrong edits when missed

Full text in `agent/CONSTRAINTS.md`. These four are the ones that get violated by accident:

- **A6 — mechanism in the client, policy in the agent.** A primitive belongs in `client/` if any
  non-agent consumer would want it (a script, a service, a dashboard poller). It belongs here
  only if it needs a model and a turn to make sense. The tell is the return type: a protocol
  object is client-layer, a model-facing object is agent-layer.
- **A2 — Runner events are wire-serializable.** Scope, depth, and other envelope metadata go on
  the envelope, never inside `Event`. This is what forbids threading provider-specific opaque
  blobs (Anthropic signed thinking blocks, for instance) through `agent.Message`.
- **A7 — sub-agents get no ambient parent state.** Memory is not shared downward. A child that
  needs memory owns its own store and namespace entirely. Guarded by
  `TestSubAgentCannotReachParentMemory`.
- **A9 — the provider seam exposes loop-visible capabilities only.** Loop-invisible provider
  optimizations (prompt caching, extended thinking) stay out; wrap the vendor SDK behind the seam
  if one is ever genuinely needed.

Also: **A8 rules out building a workflow engine here.** Orchestration is model-driven or
integrated with a dedicated engine.

## Traps

- **Background goroutines use `core.DetachForBackground(ctx)`, never `context.WithoutCancel`.**
  Anything that outlives a turn *and* calls MCP server tools needs the session-level persistent
  push. Applies to async sub-agents, the agent pool, and task dispatch.
- **Run tests with `-race`.** The signal, pool, and interruptible tests exist to catch races.
- **Host behavioral tests race on a shared sequential StubProvider** when a main turn and a
  background child both pull from it. Assert *wiring* in host tests and test the *behavior* at
  the agent layer with isolated per-child providers.
- **Memory injection never writes into `a.history`.** Summary and recall are transient per-turn
  producers; appending them to history stacks them up in both history and the RunStore log.
- **The CI `test-agent` job hardcodes example steps** in `.github/workflows/test.yml` rather than
  calling `make test-agent`. Adding or moving an example needs the workflow updated too.
