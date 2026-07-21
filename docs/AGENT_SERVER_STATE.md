# Agent server state: async, graceful-degrade connection

Status: design agreed, phased implementation. Establishes the async graceful-degrade
half of root `CONSTRAINTS.md` **C6** (MCP server lifecycle decoupled from the agent).

## Problem

`NewApp` today connects to every configured server synchronously and **fail-fast**:
the loop in `agent/host/app.go` does `client.NewClient(sc.URL)` + `c.Connect()`, and any
connect error tears the App down and returns. One down / unreachable / needs-auth server
aborts the whole agent boot. And because the system prompt is baked once (see below), a
server that is absent at boot has no path to integrate when it later comes up.

Every real MCP client (Claude Code, Cursor) boots regardless of individual server health and
shows each server as `connected` / `failed` / `needs-login`. That is the target.

### Why eager skills are the hard part

`RunnerConfig.Instructions` is documented as "the system prompt sent on every step"
(`agent/runner.go`), and it is assembled **once** in `NewApp` — `cfg.Instructions` plus each
eager server's skill block appended at construction. The Runner replays that same system
prompt on every step; it is never recomputed. So a server whose eager skills belong in the
system prompt cannot integrate after boot without changing how the system prompt is produced.

## Goals

- Boot even when some servers are down; usable immediately with whatever connected.
- Per-server state observable and surfaced (`/servers`, a status-line counter, live events).
- Tools / catalog skills / events register as connections reach `Ready`, de-register when they drop.
- Background reconnect with backoff; a returning server wires itself in with **no restart**.
- Per-server **`required`**: a user can make boot wait for an essential server.

## Non-goals (deferred)

- Host-managed server *spawning* (`ServerConfig.command` + stdio) — separate follow-up.
- Full interactive OAuth re-login flow — phase 3.

## Decisions (locked)

1. **Eager skills → dynamic system prompt.** `RunnerConfig` gains an `InstructionsFunc`
   recomputed at the top of each turn; the host assembles `cfg.Instructions` + eager blocks
   from the currently-`Ready` servers. Keeps eager in the top-level system prompt (full
   authority) while tracking the Ready-set. Chosen over demoting eager to an injected
   `RoleSystem` message because a dynamic system prompt is a generally useful primitive
   (dynamic date/context, not just skills).
2. **`client.Group` from the start.** The connection lifecycle (async connect, per-server
   state, backoff reconnect) is client-layer — a script / dashboard / `cmd/testclient` would
   want it, per the agent-vs-client layering rule — so it lives in `client/`, not the host.
   The host consumes it and owns only the registration *reaction*.
3. **Fail-fast escape is per-server `required`.** `required: true` blocks boot until that
   server is `Ready` (bounded by a timeout → fail listing the laggards). Non-required servers
   connect in the background and never block boot.

## Core model — per-server state machine

A `serverConn` per configured server: `{id, cfg, client, state, lastErr, srcHandle, retry}`.

```
Disabled                          (config off)
Connecting → Ready                (connected + capabilities negotiated → register)
Connecting → Failed               (connect/handshake error; backoff → Connecting)
Connecting → NeedsLogin           (401/403; wait for user action, don't hammer)
Ready      → Failed               (live connection dropped → de-register, retry)
```

Each transition emits a **`HostServerStateChanged`** event so surfaces update live. On
`→ Ready`: `multi.Add(id, src)`, register that server's catalog skills + `load_skill`,
`startEventStreams` for it, and (phase 2) recompute the eager system-prompt block.

**On `→ Failed` (server drops mid-conversation): keep the tool defs, fail calls
gracefully.** The tool set does not shrink mid-turn — the model keeps seeing the tools it
knew about, so it is not surprised. Calls that land while the server is down are handled
below.

## Tool calls to a non-Ready server

Because tool defs stay registered, the model can call a tool whose server is not `Ready`.
Rather than a hard failure or an indefinite block:

- **Default — model-visible miss.** The dispatch short-circuits with an **`ErrNotAvailableNow`**
  sentinel (recognized before the call reaches the disconnected client) and feeds back a
  **non-fatal** `ToolResult` naming the server and its state — e.g. "tool `create_issue` is
  unavailable: server `atlassian` is not connected (needs-login); retry, route around it, or
  tell the user." A distinct **`EventToolUnavailable`** carries `Reason` + state (NOT `Error`,
  so error-keyed eval scorers don't miscount — the same care taken for `EventToolDenied` /
  `EventToolCancelled`). The turn continues; the model adapts. This is the same
  "non-fatal tool outcome → model-visible → continue" family as approval-denial and
  cancellation.
- **Narrow exception — bounded wait for `Connecting`.** If the server is mid-connect (the
  common first-turn boot race, where the model calls a tool before an async server finished
  connecting), the dispatch does a short **capped** wait for `Ready` before falling through
  to the miss. Never wait on `Failed` / `NeedsLogin` / `Disabled` — they won't resolve
  without action, so blocking is pointless.

**Rejected:** blocking the turn indefinitely (queue-and-wait) — it couples turn latency to
reconnect timing and hangs on servers that may never come up.

## `client.Group` (client-layer primitive)

Holds N clients, connects them concurrently, exposes per-connection state and a change
stream, and runs backoff reconnect. Sketch:

```go
type ConnState int // Connecting, Ready, Failed, NeedsLogin, Disabled

type Group struct { ... }

func NewGroup(opts ...GroupOption) *Group
func (g *Group) Add(id string, c *Client, required bool)
func (g *Group) Start(ctx context.Context)             // kick off async connects
func (g *Group) WaitRequired(ctx context.Context) error // block until all required are Ready (or timeout)
func (g *Group) State(id string) (ConnState, error)     // per-server snapshot
func (g *Group) Events() <-chan StateChange             // Ready/Failed/... transitions
func (g *Group) Close() error
```

`WaitRequired` is what `NewApp` blocks on for required servers; optional servers keep
connecting in the background while the agent is already usable.

## Dynamic system prompt (`RunnerConfig.InstructionsFunc`)

```go
// InstructionsFunc, when set, is called at the top of each turn to compute the
// system prompt for that turn (overriding the static Instructions). It lets the
// system prompt track dynamic state — currently-Ready eager-skill servers, the
// date, injected context — instead of being frozen at construction.
InstructionsFunc func(context.Context) string
```

Recomputed **per turn** (not per step) so the prompt is stable within a turn and the
provider cache only breaks when the value actually changes (i.e. when the Ready-set
changes — rare after startup). The host sets it to assemble `cfg.Instructions` + the eager
blocks of currently-`Ready` servers.

## Status surface

- **`/servers`** command → a `CmdServers` result (id, url, state, tool count, skills,
  lastErr), rendered like Claude Code's server list.
- Status line gains a `servers N/M` counter (reuses the status-line machinery from the
  #1074 work).
- `HostServerStateChanged` drives live updates on both surfaces.

## Reconnect & auth

- Backoff retry per `serverConn` for never-connected / failed servers (the client already
  reconnects a *live* dropped connection; initial-failure retry is the new part).
- `NeedsLogin` is terminal-until-user-acts — surface it, don't retry-hammer. Phase 3 adds a
  `/login <server>` that drives the ext/auth OAuth flow.

## Phasing

1. **Async connect + state + `/servers` + graceful boot + per-server `required`.**
   `client.Group` lands here. Eager skills at boot: a `required` eager server blocks boot
   (baked as today); a non-required eager server that is down is simply absent until phase 2.
   Flips C6's noted target toward done.
2. **Seamless late integration.** `RunnerConfig.InstructionsFunc` + host recompute of eager
   blocks from the Ready-set; reconnect/de-register of tools/catalog-skills/events. A
   returning server wires in with no restart.
3. **Interactive re-auth** (`NeedsLogin` → `/login`).

## Resolved

- **`WaitRequired` timeout: default 30s.** `0` = wait indefinitely, for the "hang until this
  server is up, however long it takes" case; a non-zero value fails with the required servers
  that didn't reach `Ready` in time.
- **A `Ready` server that drops keeps its tool defs; calls fail gracefully** (see the tool-call
  section above) rather than shrinking the tool set mid-turn.
