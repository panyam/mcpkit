# experimental/ext/agents — implementation notes

Server-declared agent discovery (agents-wg issue 20, pre-SEP). For the API see `README.md`.

Research surface under `experimental/ext/`, with its own go.mod, mirroring `experimental/ext/events`.
**Promote to `ext/agents` only when a SEP merges.** Like `experimental/ext/events`, it is **not in
the per-PR `test.yml` matrix**. It runs via the experimental umbrella and `make testall` only.

---

## What it is

A server hosting a fleet of specialist agents advertises them as a small roster of tuples for
routing, so a supervisor host never eager-loads a flat `tools/list` of every specialist's schemas.

**Three-level progressive disclosure**, the same shape as two-tier skills:

1. `capabilities.extensions["io.mcpkit/agents"]`, advertised via the
   `core.ExtensionProvider` mechanism
2. `agents/list` → a roster of `AgentSummary` (agentId, description, capabilities, exampleTasks,
   delegateTool, tasksEnabled, skillUri), with **no tool schemas**
3. `agents/get {agentId}` → `AgentDetail` (the summary **embedded**, plus instructions and scoped
   `tools[]`)

**Only discovery is new wire surface.** Invocation rides the existing `tools/call` via each agent's
advertised `delegateTool`.

---

## Why the identifier is `io.mcpkit/agents`, not `io.modelcontextprotocol/agents`

It used to be the latter. The value tracked the Agents WG's working name, which read as harmless
while the primitive was clearly experimental.

It is not harmless, because the WG has not chosen among the three wire shapes its research doc
lists (RPC, resources under `agent://`, or an extension) and mcpkit ships the RPC shape. Matching
the *name* while the *shape* is unsettled is the worse of the two failure modes: a client that
recognises the standard identifier negotiates it and then receives a payload that does not match
what the eventual spec defines. A client that does not recognise `io.mcpkit/agents` simply skips
the primitive, which is correct behaviour for an unratified surface. Not matching fails safely;
matching wrongly does not.

This is the same class of bug as the `config` envelope in #1334: a declaration that misrepresents
what is behind it, where the mismatch surfaces as silence rather than an error.

`io.mcpkit/auth` already set the vendor-namespace precedent for surfaces mcpkit originates.

**Switch to the `io.modelcontextprotocol/` identifier when a SEP lands and this implementation
matches the ratified shape** — not merely when the name becomes known. Renaming on the name alone
would reintroduce exactly the problem this avoids.

---

## Advertised via the extension mechanism, not a new capability field

There is deliberately **no** `core.ServerCapabilities.Agents` field. It is advertised as an entry
under `capabilities.extensions`, matching skills, tasks, and UI, which reuses
`ServerSupportsExtension` and keeps a churning pre-SEP surface out of `core/`.

The research doc's "capabilities.agents" is basically the conceptual capability, realized as the advertised
extension entry. Which the WG actually intends is still an open question, and a fairly consequential one, since it changes the
negotiation envelope.

---

## Stateless wire parity came for free

The primitive needed **zero** stateless-specific code, because both halves ride shared seams: the
stateless caps builder (`server/stateless_backend.go`) copies `dispatcher.extensions`, and any
`HandleMethod` method dispatches through `dispatcher.customHandlers` in the stateless backend's
`InvokeWithMiddleware` default case. A stateless client reads the extension from `server/discover`
instead of `initialize` (both call `captureServerExtensions`), so `SupportsAgents()` is identical.

Guarded by `TestStatelessWireParity`.

The general datapoint worth remembering is that **keeping only discovery as new surface kept the
primitive small enough to inherit both wires unchanged.** That is the same legacy-vs-stateless
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
