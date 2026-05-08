# Events demo split — planning notes

Seed for splitting the discord and telegram events demos from one big linear walkthrough into multiple smaller scenario-focused demos. Floated 2026-05-08; deferred until η settles or whenever the linear walkthrough becomes too long to present comfortably in one sitting (currently 14 steps, ~5-7 minutes live).

## Why

The walkthrough has grown past the comfortable size for a single live presentation. A developer who only cares about webhook delivery shouldn't have to watch the cursorless-typing step. A reader who wants the auth-and-subscription-validation story shouldn't have to read past the spec-validation proofs to find them. Splitting by thread keeps each piece presentable in 3-5 minutes and matches the natural mental model.

## Proposed scenario groupings

Five sub-demos based on the existing story arc. Each is a coherent narrative thread that the audience can follow without surrounding context.

| Sub-demo | Absorbs current steps | Story |
|---|---|---|
| **demo-basics** | 1, 2, 3, 4, 5 | Connect, events/list, push (events/stream), poll, cursorless. The "what shapes do events come in" tour. |
| **demo-health** | 6 | Source-side health signals — `YieldError` transient and (mention of) `YieldTerminated` terminal. Small standalone. |
| **demo-webhook** | 7, 8, 9 | Subscribe + auto-refresh + multi-sub routing + delivery health + suspend. The biggest single-topic story. |
| **demo-validation** | 10, 11, 12, 13 | The four spec-validation proofs as one cluster. Tightest narrative. |
| **demo-live** | 14 | Live Discord (or Telegram) interaction. Bot-token-required. |

## Design constraint: ordering must be evident

The five sub-demos have a natural reading order (basics → health → webhook → validation → live). Anyone reading without prior context should be able to figure out which one to run first.

Two mechanisms together:

1. **Top-level `make demo` runs all sub-demos in sequence.** Default goal of the Makefile invokes them in the canonical order with a brief pause between each (or just runs them sequentially with a "next demo" banner). A presenter who wants the full tour types one command. Anyone curious "what does the demo do" gets the full thing.
2. **Each sub-demo's last step points at the next.** The closing note is something like "Next: `make demo-webhook` to see how the same source plus a callback URL gives you delivery beyond a connection." This carries the narrative even when a presenter only ran one piece.

The two together mean: linear path is preserved (run them all in order), and the order is discoverable from any single demo (look at its closing note).

## File structure proposal

```
examples/events/discord/
  walkthrough.go              # entry point — dispatches by --scenario flag
  scenario_basics.go          # demo-basics steps
  scenario_health.go          # demo-health steps
  scenario_webhook.go         # demo-webhook steps
  scenario_validation.go      # demo-validation steps
  scenario_live.go            # demo-live steps
  scenarios_shared.go         # connect helper, common actors, etc.
  WALKTHROUGH-INDEX.md        # top-level index pointing at the five
  WALKTHROUGH-basics.md       # auto-generated per scenario
  WALKTHROUGH-health.md
  WALKTHROUGH-webhook.md
  WALKTHROUGH-validation.md
  WALKTHROUGH-live.md
```

The Makefile gains:

```make
demo: demo-basics demo-health demo-webhook demo-validation demo-live  ## Run all sub-demos in canonical order

demo-basics:
	go run . --tui --scenario=basics
demo-health:
	go run . --tui --scenario=health
demo-webhook:
	go run . --tui --scenario=webhook
demo-validation:
	go run . --tui --scenario=validation
demo-live:
	go run . --tui --scenario=live

readme: ## Regenerate all per-scenario WALKTHROUGH files plus the index
	go run . --doc=md --scenario=basics > WALKTHROUGH-basics.md
	go run . --doc=md --scenario=health > WALKTHROUGH-health.md
	go run . --doc=md --scenario=webhook > WALKTHROUGH-webhook.md
	go run . --doc=md --scenario=validation > WALKTHROUGH-validation.md
	go run . --doc=md --scenario=live > WALKTHROUGH-live.md
	go run . --doc=md --index > WALKTHROUGH-INDEX.md
```

The current `WALKTHROUGH.md` either gets retired (replaced by WALKTHROUGH-INDEX.md) or kept as the all-in-one render for readers who want it linear.

## Telegram parity

Telegram demo gets the same split for consistency. Same scenario names, same Make targets. Telegram's content is currently a tighter subset of discord's (it skips a few discord-specific steps like multi-sub routing); some scenarios may be empty or one-liner-only on the telegram side. That is fine.

## Open questions when this picks up

- **Does demokit support per-scenario filtering today?** If yes, the implementation is just adding a flag and tagging steps. If no, the seed work is upstream in demokit (add a `.Scenario("name")` chain method or similar) before the events demo can use it.
- **Where does the "next demo" pointer live in the demokit DSL?** A new `.NextDemo("demo-webhook", "see how ...")` chain method on the last step of each scenario? Or just a closing Note? The latter is cheaper but the former renders consistently across TUI and Markdown.
- **Should `make demo` (with no flag) run all five sequentially, or print the index and prompt the user to pick?** The "run all" default matches the current behavior for someone typing `make demo` for the first time. The "print index" default matches the more interactive use case. The Makefile supports both — could pick one as default and document the other.
- **Mermaid diagram per scenario.** Each scenario's diagram declares only the actors it uses (Discord actor only appears in demo-live, etc.) so each diagram stays small and renders quickly.

## Related context

- The current monolithic walkthrough source lives in `examples/events/discord/walkthrough.go` and `examples/events/telegram/walkthrough.go`. Both regenerate `WALKTHROUGH.md` via `make readme`.
- The earlier deferred branching idea (`BRANCHING_NOTES.md` was floated but not committed because the user wanted scenario-splitting instead, which subsumes it) — branching at the demokit level is overkill if we can just have separate scenarios. Park branchability as a "future demokit feature" if the scenario approach stops scaling.
- This split should land **after** the η walkthrough updates per `docs/EVENTS_ETA_PLAN.md` "Tutorial follow-up" so the η-related changes don't have to be split-aware mid-flight.
