# experimental/ext/agents — implementation notes

Server-declared agent discovery, tracking the agents-wg#22 SEP draft (head `9b16840e`). For the API see `README.md`.

Research surface under `experimental/ext/`, with its own go.mod, mirroring `experimental/ext/events`.
**Promote to `ext/agents` only when a SEP merges.** Both modules run in the per-PR `test.yml`
matrix, added with the #22 alignment because the parity tests are the only thing holding the
"fields MUST match" rule.

---

## What it is

A server groups its tools into agents and advertises them as a compact roster, so a supervisor
host never eager-loads a flat `tools/list` of every agent's schemas.

**Three-level progressive disclosure**, the same shape as two-tier skills:

1. `capabilities.extensions["io.modelcontextprotocol/agents"]`, advertised via the
   `core.ExtensionProvider` mechanism
2. `agents/list` → paginated `AgentCard`s (name, description, capabilities), with **no tool
   schemas**
3. `agents/get {name}` → a flat result: the agent's name, instructions, optional
   `modelRoutingHint`, and its tools

**Only discovery is new wire surface.** The host builds a local subagent from the `agents/get`
result and calls its tools through `tools/call`.

---

## Tools are names resolved per request, not stored copies

`AgentDef.Tools` used to be `[]core.ToolDef`. #22 requires every tool `agents/get` returns to
match the caller's `tools/list` entry in every field, and a copy breaks that two ways. It goes
stale when the tool is re-registered, and it skips the per-client rewrite `tools/list` applies
(the SEP-2356 `x-mcp-file` strip for clients without `fileInputs`). Both were live in the old code.

So `Tools` is `[]string`, looked up in `srv.Registry().ToolDef` on every `agents/get`, with the
strip applied from `ctx.ClientCaps()`. `TestGetToolsMatchToolsList` and the stateless parity tests
compare the two responses as JSON for both kinds of client.

One case used to disagree, and the bug was in `tools/list`, not here. The stateless `tools/list`
never stripped `x-mcp-file`, so a stateless client without `fileInputs` saw it there and not in
`agents/get`. #1515 fixed it and added the case to the parity test, and since #1625 both wires
answer `tools/list` from the same handler. If `tools/list` ever gains another per-caller rewrite or authorization filter,
`resolveTools` has to apply it too, or the parity tests go red.

---

## Why the identifier went to `io.mcpkit/agents` and came back

It is `io.modelcontextprotocol/agents` now, the SEP's own identifier. It has been that before,
then `io.mcpkit/agents` for a while, and the reasoning for the detour is worth keeping.

It used to be the SEP's name. The value tracked the Agents WG's working name, which read as harmless
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

**Switch to the `io.modelcontextprotocol/` identifier when this implementation matches the SEP's
shape**, not merely when the name becomes known. Renaming on the name alone would reintroduce
exactly the problem this avoids.

The WG has since picked the extension shape (#22), and SEP-3392 (Extension Lifecycle, open)
proposes that an identifier is fixed at creation and kept through the Experimental and Beta
stages, with implementations required to validate what they receive. If 3392 lands as written,
the switch is no longer tied to #22 merging, only to this implementation matching it. The two
remaining gaps, `listChanged` and step-up, have both landed (#1536, then step-up with the
`examples/agents/discovery` PR, #1540), so the switch went in on its own right after (#1393).
`TestAdvertisedExtensionMatchesSEP` decodes the SEP's capability example and checks that a served
server advertises the same key and settings, so a drift in either direction fails.

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
stateless caps builder (`Dispatcher.statelessCapabilities`) copies `dispatcher.extensions`, and any
`HandleMethod` method dispatches through `dispatcher.customHandlers` in the method table both wires
share (`Dispatcher.route`). A stateless client reads the extension from `server/discover`
instead of `initialize` (both call `captureServerExtensions`), so `SupportsAgents()` is identical.

Guarded by `TestStatelessWireParity`.

The general datapoint worth remembering is that **keeping only discovery as new surface kept the
primitive small enough to inherit both wires unchanged.** That is the same legacy-vs-stateless
dispatch-parity trap that bites `server/` repeatedly (see `server/NOTES.md`), dodged here by
construction plus a regression test.

---

## Fields dropped for #22

The #20-era roster carried `exampleTasks`, `delegateTool`, `tasksEnabled` and `skillUri`. #22's
`AgentCard` has none of them, and they went in the #1393 alignment.

- `delegateTool` let the server run the whole agent behind one tool. #22 lists "agent exposed as a
  single tool" as a rejected alternative, because offering both paths left hosts guessing which
  the server intended.
- `exampleTasks` helped few-shot routing. If it comes back, it should come back through the SEP,
  not as an mcpkit-only field a host cannot rely on.
- `tasksEnabled` and `skillUri` were advisory pointers at Tasks and Skills. Neither has a place in
  #22, and the package still imports neither.

Turning an `agents/get` result into a runnable subagent is host work and lives in chakra, not
here.

---

## Client SDK

`clients/go`: `agentsclient.New(mcp)` → `SupportsAgents` / `ListAgentsPage` / `ListAgents` (walks
every page) / `GetAgent`, plus `IsAgentNotFound` and `IsInvalidAgentDefinition` for the two
`data.reason` errors. Tested via the experimental umbrella; wired into `SUB_MODS_TO_TAG`.

The cache and the routing helper live here too. Two choices in them that are not obvious from the
code:

- **The cache keeps a generation counter.** A fetch records the generation when it starts and
  stores its result only if no invalidation happened meanwhile. Without it, an `agents/get` that
  was in flight when `notifications/agents/list_changed` arrived would store pre-change details
  for a whole TTL. `TestCacheInFlightFetchAcrossInvalidateIsNotStored` pins it.
- **`RoleTable.Resolve` escalates and never downgrades.** The SEP leaves resolution to the host
  and says only that an unsatisfiable hint may fall back to the default model. Moving up a role
  when the hinted one is missing (or lacks vision) keeps the agent at least as capable as the
  server asked. Moving down would quietly run it on less. When nothing larger fits, `Default`
  wins, which is usually the supervisor's own model. The reference Python branch has no
  equivalent to compare against, at least as of its `feat/mcp-agents-exp` head on 2026-10-14.

## `list_changed`

`Register` declares `listChanged: true` and registers the `agentsListChanged` filter key, the
same way `events.Register` handles `eventsListChanged` (#1531). Declaring it unconditionally is
deliberate, since a server that starts with no agents and adds them later still owes its clients
the notification. Changes the registry cannot see, such as a `Visible` policy that starts
answering differently, need an explicit `NotifyListChanged`.

## Step-up is middleware, and checks visibility first

`AgentDef.ScopeChallenge` is enforced by a server middleware that `Register` installs, not by the
`agents/get` handler. Only a refusal raised before dispatch becomes a real HTTP 403 with a
`WWW-Authenticate` header, which is what a host needs to step up. A handler can only answer with a
JSON-RPC error inside a 200, and by then the headers are gone. `ext/auth`'s `NewScopeMiddleware`
is shaped the same way for the same reason. The stateless wire routes extension methods through
the same chain (`Server.runRequest`), so there's nothing wire-specific to do.

The middleware asks `Visible` before it asks the challenge. Done the other way round, a caller who
may not know an agent exists would get a 403 naming its scopes, while an unknown name gets
`agent_not_found`, and the difference would confirm the agent is there. #22 requires hidden and
unknown agents to look identical, so a hidden agent passes straight through to the handler and
gets the ordinary `agent_not_found`. `TestStepUpNeverRevealsHiddenAgent` pins it.

The 403 builder moved from `ext/auth` into `core` (`core.InsufficientScopeChallenge`) for this,
the same move the parser made earlier, so this module didn't have to start depending on
`ext/auth` for one string function.

One thing that bit while writing the tests: a stepped-up token has to belong to the same user.
The session wire binds a session to its principal, so swapping in a token for a different subject
fails with `session principal mismatch` rather than stepping up. That's correct behaviour, and it's
what a real authorization server issues anyway, but a test whose two tokens map to two subjects
will look like step-up is broken on the session wire and fine on the stateless one.
