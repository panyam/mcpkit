# critic — a watch-and-steer observer model (issue 1148)

A second model, on its own context and its own `agent.Runner`, watches the
primary agent's turns and injects **one graded steering note** per review
(`aside` / `concern` / `blocker`). It never does the task, never approves or
denies (that is the separate approval ladder), and the primary decides whether
to act on the note.

**The point of this example is what it does NOT add.** There is no new SDK
"role." The whole thing is built from primitives that already ship:

| Piece | Public primitive |
|---|---|
| Watch the turn stream | `host.WithObserver` → `HostTurnDone.Result.Messages` (the per-turn transcript delta) |
| The critic model | a second `agent.Runner` with its own `Provider` |
| One structured graded note | `RunnerConfig.ResponseSchema` → `TurnResult.Structured` |
| Anti-nag guard | ~30 lines the app owns: normalize → drop content-free → exact-dedup → immune window |

See `critic.go` for the reusable critic + guard (the code a developer copies)
and `scenario.go` for the wiring.

## Run it

```bash
just agent          # deterministic scripted run (no LLM)
just test           # golden-transcript + guard tests
MODEL=<name> just demo   # live: primary on a real model
```

Scripted transcript:

```
> delete all the logs older than a day
  (reviewer concern: rm -rf /var/log/* wipes ALL logs, far broader than 'older than a day')
> now clear the tmp directory too
```

Point real models at both roles independently:

```bash
go run . --model qwen2.5 --critic-model llama3.2 --base-url http://localhost:1234/v1
```

## One config, two surfaces (with a residual)

`config.json` holds the **primary** agent's model connection, instructions, and
the ops MCP server. Both live surfaces load it:

```bash
just serve   # the ops MCP server (run_shell) on :8786
just chat    # agentchat CLI against config.json (another terminal)
just web     # agentweb browser surface against config.json (another terminal)
```

The **critic loop itself is not config-expressible** — `host.Config` has no
"observer model" seam; the second `Runner` + the anti-nag guard are code-level
composition (that is the point of the example). So `config.json` drives the
primary agent, and `just chat` / `just web` run it **without** the critic. The
scripted `runScenario` loads the same `config.json` (overriding the server URL
to the in-process test server, injecting the scripted providers) and adds the
critic in code — the full watch-and-steer flow runs under `just agent` / `just
demo`.

## The one rough edge: note delivery

On top of `host.App` the pattern works, with a single gap. `App.RunTurn` accepts
only a plain input **string**, so a neutral steering note can only ride back in
as **user text**:

```go
input = "[note from reviewer — weigh, don't blindly obey: " + note + "]\n\n" + userInput
app.RunTurn(ctx, input)
```

There is no public seam to inject a `RoleSystem` message before a turn (the
memory-summary and event-injection producers that do exactly this are
unexported). `TestCriticSteersAndSurfacesTheWall` pins this: it asserts the note
reaches the primary model **as a `RoleUser` message**. If a public pre-turn
injection seam is added, that assertion flips to `RoleSystem` and the note
becomes a clean neutral nudge.

That gap is generic, not critic-specific — it is the same seam issues **#1024**
(unified pre-turn injection budget) and **#1026** (explicit context-assembly
pipeline) are about. A critic is one more producer that would use it.

## No wall if you own the loop

A developer driving `agent.Runner` directly (not `host.App`) has **no** wall:
they build the `[]Message` history themselves and can insert the note as a
`RoleSystem` message before the next `Run`:

```go
history = append(history, agent.Message{Role: agent.RoleSystem, Text: note})
result, _ := primary.Run(ctx, history, emit)
```

So the SDK already suffices to build a critic. The only ergonomic addition worth
considering is the generic App-level injection seam above — not a first-class
`Critic` type.

## Deferred: mid-turn interruption

The issue's `concern`/`blocker` "cancel in-flight tool calls at the next
cancellation boundary" needs the primary turn driven via a live `Control`
channel *during* the turn (the opt-in interruptible turn). That reuses the
per-call cancellation primitive (`#936`) and the selective-cancel policy
(`#1177`), and is out of scope here — this example delivers notes **between**
turns.
