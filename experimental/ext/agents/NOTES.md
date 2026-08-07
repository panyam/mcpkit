# experimental/ext/agents — implementation notes

Server-declared agent discovery (agents-wg issue 20, pre-SEP). For the API see `README.md`.

Research surface under `experimental/ext/`, with its own go.mod, mirroring `experimental/ext/events`.
**Promote to `ext/agents` only when a SEP merges.** Like `experimental/ext/events`, it is **not in
the per-PR `test.yml` matrix** — it runs via the experimental umbrella and `make testall` only.

---

## What it is

A server hosting a fleet of specialist agents advertises them as a small roster of tuples for
routing, so a supervisor host never eager-loads a flat `tools/list` of every specialist's schemas.

**Three-level progressive disclosure**, the same shape as two-tier skills:

1. `capabilities.extensions["io.modelcontextprotocol/agents"]`, advertised via the
   `core.ExtensionProvider` mechanism
2. `agents/list` → a roster of `AgentSummary` (agentId, description, capabilities, exampleTasks,
   delegateTool, tasksEnabled, skillUri) — **no tool schemas**
3. `agents/get {agentId}` → `AgentDetail` (the summary **embedded**, plus instructions and scoped
   `tools[]`)

**Only discovery is new wire surface.** Invocation rides the existing `tools/call` via each agent's
advertised `delegateTool`.

---

## Advertised via the extension mechanism, not a new capability field

There is deliberately **no** `core.ServerCapabilities.Agents` field. It is advertised as an entry
under `capabilities.extensions`, matching skills, tasks, and UI, which reuses
`ServerSupportsExtension` and keeps a churning pre-SEP surface out of `core/`.

The research doc's "capabilities.agents" is the conceptual capability, realized as the advertised
extension entry. Which the WG actually intends is an open question, since it changes the
negotiation envelope.

---

## Stateless wire parity came for free

The primitive needed **zero** stateless-specific code, because both halves ride shared seams: the
stateless caps builder (`server/stateless_backend.go`) copies `dispatcher.extensions`, and any
`HandleMethod` method dispatches through `dispatcher.customHandlers` in the stateless backend's
`InvokeWithMiddleware` default case. A stateless client reads the extension from `server/discover`
instead of `initialize` (both call `captureServerExtensions`), so `SupportsAgents()` is identical.

Guarded by `TestStatelessWireParity`.

This is the general datapoint worth remembering: **keeping only discovery as new surface kept the
primitive small enough to inherit both wires unchanged.** It is exactly the legacy-vs-stateless
dispatch-parity trap that bites `server/` repeatedly (see `server/NOTES.md`), dodged here by
construction plus a regression test.

---

## Deliberate non-coupling (A6)

`tasksEnabled` (SEP-2663) and `skillUri` (skills) are an advertised bool and string, **not
imports**. This package traffics only in protocol objects.

Turning an `agents/get` result into a Runner-backed `AgentSource` is agent-layer work and lives
elsewhere (#1144).

---

## Client SDK

`clients/go`: `agentsclient.New(mcp)` → `SupportsAgents` / `ListAgents` / `GetAgent`, with tolerant
decoders. Tested via the experimental umbrella; wired into `SUB_MODS_TO_TAG`.
