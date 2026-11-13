# CLAUDE.md — MCPKit

Go library for building production-grade MCP servers and clients. The agent SDK moved to
[chakra](https://github.com/panyam/chakra), and nothing here may require it.

This file is a router. Keep it under 10,000 characters. Detail goes beside the code it describes,
per **Where knowledge lives**.

## Commands

`make` is authoritative, and the only runner CI uses. The justfiles are partial.

```bash
make test              # core/server/client/testutil; sub-modules have their own targets
make testall           # everything + Keycloak + HTML report; needs `make conf-pending-setup` and a free KC_PORT
make audit             # govulncheck + gosec + gitleaks + race
make tidy-all          # after touching core/ imports, or sub-module go.sum drifts
make exercise-wire     # after any wire change: TestWireParity + testconf testconf-stateless testconf-agents
make tag-push V=vX.Y.Z / make release-publish V=vX.Y.Z   # RELEASING.md
```

`conformance/Makefile` runs the `testconf-*` suites, and `conformance/NOTES.md` explains them.

## Package layout

| Package | Docs |
|---------|------|
| `core/`, `server/`, `client/` | each has `README.md` and `CONSTRAINTS.md`; `server/NOTES.md` |
| `ext/auth/`, `ext/tasks/`, `ext/skills/`, `ext/ui/`, `ext/otel/` (separate go.mods) | `ext/auth/docs/DESIGN.md`, `ext/tasks/README.md`, `ext/skills/NOTES.md`, `docs/APPS_*.md` + `ext/ui/NOTES.md`, `ext/otel/README.md` |
| `experimental/ext/events/` (+ `clients/go/bridge`) | `README.md`, `NOTES.md`, `DEPLOYMENT.md` |
| `experimental/ext/agents/` | `README.md`, `NOTES.md` |
| `experimental/ext/rooms/` (+ `massrelay/`, `ts/`), `experimental/integrations/roomsevents/` | each has `README.md` and `NOTES.md`; page wire in `rooms/WIRE.md` |
| `experimental/ext/protogen/` | `docs/DESIGN.md` |
| `conformance/` | `NOTES.md` |
| `examples/` | `README.md`, `CONVENTIONS.md`, `NOTES.md` |

## Where knowledge lives

- **`README.md`**: how to use a package. `core`, `server`, `client`, `ext/tasks` and every
  `examples/*` README is published to the docs site verbatim. No internal lore there.
- **`NOTES.md`**: why the code has its shape, and what bit us. Internal. The root `NOTES.md` holds
  repo-wide lore: credentials, hygiene, CI gaps.
- **`CONSTRAINTS.md`**: enforceable rules, at the root and in `core/`, `server/`, `client/`.
- **`CLAUDE.md`**: routing, plus rules that cause wrong edits when missed.
- Status goes in issues and `NEXTSTEPS.md`. Design docs go in `docs/`. Don't recreate
  `CAPABILITIES.md`, retired 2026-07-18.

## Issues and missions

Work is queued by mission (`~/.claude/skills/retriage/CONVENTIONS.md`). A new issue gets `P0`–`P3`,
plus a mission link (blocked-by on the mission issue) or `waiting` with its trigger. Run a
mission's exercise after each merged PR on it and log the result on the mission issue. Active
missions, one worktree each:

- **#1683** `mission_ai_inside_ui`: `make exercise-aipage`, plus a by-hand Claude Code run through
  `rooms-channel`.
- **#1553** `mission_release_installable`: `make check-install V=<tag>`.
- **#1701** `mission_progressive_disclosure`: `make exercise-disclosure`, which #1700 adds.

#1554 (`mission_apps_host`, label `mcpapps`) is queued behind #1683.

## Rules that cause wrong edits

Wire and server:
- Both wires run `Server.runRequest`. Put a wire difference in a `wire` branch, never a second
  handler (C7, #1625). A new wire bug gets a `TestWireParity` row first.
- The stateless wire needs `Mcp-Method` and `Mcp-Name` headers (#1567). Capability helpers read
  the request `_meta` too (#1470). Base list methods page at 250 (#1356). Detail:
  `server/NOTES.md` § Request rules every handler meets.
- Background goroutines use `core.DetachForBackground(ctx)`, never `context.WithoutCancel`.
- Tests can't call `srv.Dispatch()` directly. Use httptest plus a client.
- A list response has a stable order (C10). A notification is `req.IsNotification()`, never
  `req.ID == nil` (#1520). A per-caller notification goes through `NotifyListeners`, not
  `Broadcast` (#1523).
- With no `WithAllowedOrigins`, a server under a public name refuses unauthenticated clients
  (#1547). `httptest` hides it; set `req.Host`.
- Webhook subscribes verify the endpoint by default (#1444). Test stacks opt out with
  `WithUnsafeSkipEndpointVerification()`.

Build and CI:
- Never commit compiled binaries. A bare `go build` leaves one (`scripts/check-no-binaries.sh`).
- Never `gofmt -w` a glob. The tree isn't gofmt-clean. Format files by name.
- Makefile and justfile recipes dispatch to `scripts/`; no inline logic (C8).
- Generated files: `conformance/path-defaults.*` (`gen_conf_paths.py`), `CONFORMANCE.md` and the
  badge (`make refresh-conformance`), `examples/*/WALKTHROUGH.md` (from Go; `examples/host` inverts
  it). Don't hand-edit them. A new `testconf-*` target trips two gates (`conformance/NOTES.md`).
- A new example with tests needs an entry in `examples/Makefile`'s `test` umbrella (#1431).
- `SUB_MODS_TO_TAG` in the root `Makefile` is the module list, and a script parses its shape.
- Experimental modules are separate modules. Run `go test` inside them.
- CI runs `-race` only in the rooms jobs. Run it by hand after concurrent changes.
- `INFO` suites don't go red (#1446). After touching a surface one grades, run it with `make`.
- A green suite may not be grading mcpkit (#1358). Break the fixture and watch it go red.
- Keycloak runs only on auth-path PRs, `main` and nightly (#1597). Locally `make testkcl-auto`.
- `check-dep-consistency` wants `--prune-baseline`, never `--update-baseline`.
- CodeQL rejects `make(T, len(a)+len(b))`. Size from one operand.
- `govulncheck` green doesn't mean dependencies are current (`DEPENDENCY_POLICY.md`).

Git, GitHub and releases:
- `GH_TOKEN="$GH_PERSONAL_TOKEN"` for `panyam/*`. `env -u GH_TOKEN gh …` for upstream writes,
  releases, and the first push of a branch carrying workflows. SSH uses
  `SSH_AUTH_SOCK=~/.ssh/agent.sock`. Detail: `NOTES.md` § GitHub credentials.
- Stacked PRs get no CI, and `Closes #N` fires only on merge to `main`.
- Keep closing words (close, fix, resolve) off a line end when the next line starts with `#N`.
- One session, one worktree of each conformance fork checkout (`conformance/NOTES.md`).
- Release tags carry no `replace` directives, and `main` keeps them (#1488, `RELEASING.md`).
- Repo security settings are GitHub settings, not files.

## Tasks v1 vs v2

`server.RegisterTasksV1` is frozen. `tasks.Register` in `ext/tasks/` is v2 and canonical
(`docs/TASKS_V2_MIGRATION.md`).
