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

1. `capabilities.extensions["io.mcpkit/agents"]`, advertised via the
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

One case still disagrees, and the bug is in `tools/list`, not here. The stateless `tools/list`
never strips `x-mcp-file` (`server/stateless/handlers.go:19`), so a stateless client without
`fileInputs` sees it there and not in `agents/get`. Tracked as #1515, which also adds that case to
the parity test. If `tools/list` ever gains another per-caller rewrite or authorization filter,
`resolveTools` has to apply it too, or the parity tests go red.

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

**Switch to the `io.modelcontextprotocol/` identifier when this implementation matches the SEP's
shape**, not merely when the name becomes known. Renaming on the name alone would reintroduce
exactly the problem this avoids.

The WG has since picked the extension shape (#22), and SEP-3392 (Extension Lifecycle, open)
proposes that an identifier is fixed at creation and kept through the Experimental and Beta
stages, with implementations required to validate what they receive. If 3392 lands as written,
the switch is no longer tied to #22 merging, only to this implementation matching it. The
remaining gaps are `listChanged` and step-up (#1393).

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
