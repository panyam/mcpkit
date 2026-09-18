# CLAUDE.md — MCPKit

Go library for building production-grade MCP servers and clients.

The agent SDK that used to sit above the protocol here now lives in its own repository,
[chakra](https://github.com/panyam/chakra). Nothing in this tree depends on it.

This file is a router. Detail lives beside the code it describes. See **Where knowledge lives**
below before adding anything here.

## Quick Commands

`make` is the supported runner and the only one CI uses. Justfiles mirroring these names exist but
are an experiment; `make` is authoritative when they disagree.

```bash
make test              # Core tests (core/server/client/testutil)
make test-auth         # ext/auth sub-module
make test-ui           # ext/ui sub-module
make test-e2e          # E2E tests (auth + apps)
make test-examples     # examples/ orchestrator
make testall           # Everything (9 stages, 21 sub-stages) + Keycloak + HTML report
make audit             # govulncheck + gosec + gitleaks + race
make tidy-all          # Required after touching core/ imports
make tag-push V=vX.Y.Z # Tag root + all sub-modules and push (RELEASING.md; pre-release is vX.Y.Z-bN)
```

Conformance targets (`testconf`, `testconf-client`, `testconf-tasks-v2`, `testconf-mrtr`,
`testconf-skills`, `testconf-events`, `testconf-stateless`, `testconf-upstream-audit`,
`refresh-conformance`, `check-conformance-stale`, …) are orchestrated in `conformance/Makefile`, which also documents each
suite's `MCPCONFORMANCE_*_PATH`. See `conformance/NOTES.md` for how they are wired and which
upstream changes to watch.

## Package Layout

| Package | Docs |
|---------|------|
| `core/` — Protocol types, typed contexts, session APIs | `core/README.md`, `core/CONSTRAINTS.md` |
| `server/` — Server, transports, middleware, v1 tasks (frozen) | `server/README.md`, `server/CONSTRAINTS.md`, `server/NOTES.md` |
| `client/` — Client, transports, reconnection, auth retry | `client/README.md`, `client/CONSTRAINTS.md` |
| `ext/auth/` — JWT, PRM, OAuth (separate go.mod) | `ext/auth/docs/DESIGN.md` |
| `ext/tasks/` — SEP-2663 v2 tasks extension (separate go.mod) | `ext/tasks/README.md` |
| `ext/skills/` — SEP-2640 skills (data-only, enforced) | `ext/skills/NOTES.md` |
| `ext/ui/` — MCP Apps, Bridge JS, AppHost, ServerRegistry | `docs/APPS_DESIGN.md`, `docs/APPS_HOST.md`, `ext/ui/NOTES.md` |
| `ext/otel/` — SEP-414 OpenTelemetry adapter | `ext/otel/README.md`, `docs/SEP_414_OTEL.md` |
| `experimental/ext/events/` — MCP Events protocol | `experimental/ext/events/README.md`, `experimental/ext/events/NOTES.md` |
| `experimental/ext/agents/` — Server-declared agent discovery (pre-SEP) | `experimental/ext/agents/README.md`, `experimental/ext/agents/NOTES.md` |
| `experimental/ext/protogen/` — Proto → MCP codegen | `experimental/ext/protogen/docs/DESIGN.md` |
| `conformance/` — Suite orchestration + audits | `conformance/NOTES.md` |
| `examples/` — Working examples | `examples/README.md`, `examples/CONVENTIONS.md`, `examples/NOTES.md` |
| `testutil/`, `tests/e2e/`, `tests/keycloak/` — Helpers and integration tests | `tests/e2e/apps/README.md` |

## Where knowledge lives

Three kinds of file, three audiences. Put new material in the right one rather than here.

- **`README.md`** — how to use the package. Some of these are **published to the docs site**
  verbatim (`core`, `server`, `client`, `ext/tasks`, and every `examples/*`), wired via
  `docs/site/content/`. Do not put internal lore in those.
- **`NOTES.md`** — why the code is shaped this way and what bit us. Internal, never published.
  This is where implementation lore belongs.
- **`CLAUDE.md`** (this file, plus nested ones) — routing and the rules that cause wrong edits when
  missed. A nested `CLAUDE.md` loads automatically when working in that subtree, so keep it short.
  There are none today; this is the only one in the tree. Conformance lore lives in
  `conformance/NOTES.md`.
- **`CONSTRAINTS.md`** — enforceable architectural rules. Project-wide at the root; per-package in
  `core/`, `server/`, `client/`.

Design docs live in `docs/`: `ARCHITECTURE.md`, `APPS_DESIGN.md`, `SEP_414_OTEL.md`, and the
per-SEP migration guides. The `AGENT_*.md` set left with the agent SDK and now lives in
[chakra](https://github.com/panyam/chakra) under its `docs/`.

**Do not recreate `CAPABILITIES.md`** (retired 2026-07-18, `bc402a03`). Learnings go in the
per-package `NOTES.md`, the design docs, and the roadmap.

## Sub-Modules

**`SUB_MODS_TO_TAG` in the root `Makefile` is the authoritative list.** Do not maintain a copy
here, it rots. `make test` does not cover sub-modules; each has its own target.

Run **`make tidy-all` after touching `core/` imports** or sub-module `go.sum` files drift and CI
fails in a module you did not edit.

`docs/site/` is the GitHub Pages renderer. It is a tool, not a library, and is excluded from
`SUB_MODS_TO_TAG`.

**The agent SDK is not in this repo.** It was extracted to
[chakra](https://github.com/panyam/chakra) and consumes mcpkit as an ordinary dependency. The
dependency runs one way: nothing here may require chakra. `scripts/verify-submodule-deps.sh` now
enforces two policies rather than three, protocol and non-library, the agent policy having left with
the tree.

## Cross-cutting rules

These span packages and will bite on a task that never opens a routed doc.

- **Background goroutines use `core.DetachForBackground(ctx)`, never `context.WithoutCancel`.** It
  replaces the dead POST-scoped requestFunc/notifyFunc with the session-level persistent push.
- **The server requires initialization.** A direct `srv.Dispatch()` in a test fails; use httptest
  plus a client.
- **Never commit compiled binaries.** A bare `go build` in a module directory drops an executable
  named after the directory, with no extension, which `git add -A` then sweeps into an unrelated
  commit. Nine accumulated this way before a `git filter-repo` purge took `.git` from 466 MB to
  23 MB. Two layers now gate it, both detecting by **magic bytes, not filename**:
  `scripts/pre-commit-hook.sh` (local, opt-in via `make setup-hooks`) and
  `scripts/check-no-binaries.sh` (whole-tree, wired into `test.yml`, the actual gate).
  `HANDOFF.md` / `HANDOFF_*.md` are gitignored for the same `git add -A` reason.
- **A stateless handler that skips `InvokeWithMiddleware` is a silent middleware bypass.** The
  session wire wraps `d.Dispatch` with the chain and filters by nothing, so it sees every method.
  The SEP-2575 wire dispatches per method in `server/stateless/handlers.go` and reaches the chain
  only if the handler calls `Backend.InvokeWithMiddleware` itself. Omit it and the method still
  works, the response still looks right, and the middleware never runs. `resources/read` was in
  that state until #1352: a scope gate held on `tools/call` and not on `resources/read`, so a caller
  refused a tool could ask for the resource. Constraint C7, gated by
  `make check-stateless-middleware`. Five handlers are still knowingly unrouted, listed in the
  script's `ALLOWED`.
- **Makefiles and justfiles dispatch; they do not hold shell scripts.** Control flow, or four or
  more statements, goes in `scripts/` and the recipe calls it. Nearly every directory carries both
  a `Makefile` and a `justfile` that must stay name-and-behavior identical, so inline logic is
  written twice in two escaping dialects and maintained in neither. `just clean-backends` became a
  byte-identical copy of `just clean`, wiping the events volumes while promising to wipe
  `docker/backends` (#1396). Constraint C8, gated by `make check-recipe-complexity`. The
  sweep is done and `scripts/recipe-complexity-allowed.txt` is empty, so any entry appearing there
  is a new decision. The gate's own precision is self-tested: `$(if ...)` in a Makefile is a
  function, not a script.
- **`conformance/path-defaults.{mk,sh,just}` are generated, not hand-edited.** They come from
  `conformance/local-suites.yaml` via `uv run scripts/gen_conf_paths.py --write`. Editing two of
  the three by hand fails CI as "case E drift" in `check_local_suites.py`, and the `just` runner is
  the one people forget. Adding a `testconf-*` target means adding a manifest entry too; the drift
  check enforces both directions.
- **Adding a `testconf-*` target trips two independent CI gates, and passing the first says
  nothing about the second.** `check-local-suites-stale` covers the manifest and the generated
  path-defaults; `check-conformance-stale` covers `CONFORMANCE.md` and the badge JSON, which are
  rendered *from* `local-suites.yaml` and go stale the moment an entry is added. Run
  `make refresh-conformance` and commit the result, or CI fails after everything else has gone
  green. Cost a round-trip on #1378. The full checklist for a new target is in
  `conformance/NOTES.md` § Adding a testconf-* target.
- **The docs-site conformance page needs no separate update.**
  `docs/site/content/conformance/index.html` is a shim that renders `CONFORMANCE.md` at build
  time, so regenerating that file *is* the site update. Same for the audit pages.
- **`govulncheck` green does not mean dependencies are current.** Default govulncheck is
  *reachability*-based, so it exits 0 while advisories sit unfixed in required modules. Version
  matching is a separate pass. Command, blockers, and rationale: `DEPENDENCY_POLICY.md`
  § Security updates.
- **Two GitHub credentials, and the one you want depends on the repo.**
  `GH_TOKEN="$GH_PERSONAL_TOKEN"` is **fine-grained** and scoped to the owner's account: use it for
  `panyam/*`, where the EMU account cannot reach. It can read but never write
  `modelcontextprotocol/*` — `POST .../pulls`, `gh pr edit` and `PATCH .../pulls/N` all 403 even on
  a PR we authored, and no setting fixes it, because fine-grained PATs only scope to repos in the
  owner's account.
  **For upstream writes, use `gh`'s own stored login instead**: `env -u GH_TOKEN gh …` falls back to
  the `gho_` OAuth token in `~/.config/gh/hosts.yml`, which carries full `repo` scope and *does*
  create and edit PRs and post review comments on `modelcontextprotocol/*`. Verified on
  conformance#504. An earlier version of this note said upstream edits need the web UI or a classic
  PAT; that is only true of the fine-grained token.
  Pushing to our fork branches is unaffected. See `conformance/NOTES.md`.
  **The fine-grained PAT cannot create a branch carrying workflow files**, even when no commit on
  it touches `.github/`. Creating a ref counts every workflow present on that ref as "created", so
  the push is rejected with "refusing to allow a Personal Access Token to create or update workflow
  `.github/workflows/test.yml` without `workflow` scope". Pushing to a branch that already exists,
  and pushing tags, both work on the PAT, which is why a release push succeeds and the *first* push
  of a new branch does not. The `gho_` login carries `workflow`, so hand that push to it:
  `env -u GH_TOKEN git -c credential.helper='!gh auth git-credential' push -u https://github.com/panyam/mcpkit.git <branch>`.
  Cost a round-trip on #1405.
  **SSH pushes use the agent, not a key file.** `~/.ssh/id_github` does not exist in the container —
  only `~/.ssh/agent.sock`, which holds the right key. Use
  `SSH_AUTH_SOCK=~/.ssh/agent.sock git push …`; the `-i ~/.ssh/id_github` form fails with
  "Identity file not accessible" then "Permission denied (publickey)".
  Releases are the same story: `env -u GH_TOKEN gh release create` publishes fine, while the
  fine-grained PAT 403s on Contents: write. So does reading Dependabot alerts. See `RELEASING.md`.
- **Stacked PRs get no CI** when the base is not `main`. Verify locally, then either retarget to
  main after the base merges or push an empty commit to fire checks. GitHub's `Closes #N` only
  fires on a merge to the **default** branch, so carry it on whichever PR actually reaches main.
  Two branches that both append tests to the end of the same file will re-conflict at every cross
  merge; land the shared base before branching the second consumer.
- **`check-dep-consistency` failures want `--prune-baseline`, not `--update-baseline`.** The CI
  error text suggests the latter, which also accepts any *new* divergence silently, defeating the
  point of the baseline. Prune only drops entries that stopped diverging. A cross-module
  `go mod tidy` sweep converges pins as a side effect, so this fires on dependency bumps that look
  unrelated to it.
- **CodeQL rejects `make(T, len(a)+len(b))`** as an allocation size that may overflow. Capacity is
  a hint and the map or slice grows anyway, so size from one operand.
- **Repo security settings are settings, not files.** Dependabot alerts, security updates, and
  private vulnerability reporting need no commit; `dependabot.yml` governs *version* updates only.
  A 403 is not a 404, and a status check that treats any non-success as "disabled" reports a
  configured repo as unprotected.

- **mcpkit does not paginate by default, anywhere.** `server/pagination.go` sets
  `defaultPageSize = 0` for tools, resources, templates and prompts, which `paginate` reads as
  "return everything, emit no cursor". `ext/skills` gained `WithSkillsListPageSize` and
  `WithDirectoryReadPageSize` in 2026-09, and the four base methods still have no override. Conformant
  (the spec makes paging optional) but it means a large catalog ships in one response, and it meant
  the paging helpers were unreachable while unit tests certified them. Tracked as #1356 against the
  1.0 freeze, since changing the default afterwards is a breaking wire change.

## Conformance

All tier-scored surfaces are at 100% on upstream tier-check: **Server 31/31** (checks 73/0) and
**Client 21/21** (573/8), per the generated block in `CONFORMANCE.md`. Full client suite **41/43**,
the two failures being `auth/dpop` and `auth/dpop-nonce`, both gated on SEP-1932 leaving draft
(#803). Take the numbers from `CONFORMANCE.md` rather than this paragraph — the prose here went
stale by a whole server scenario once already.

`CONFORMANCE.md` is generated and CI-gated for staleness; `conformance/UPSTREAM_AUDIT.md` grades
mcpkit against every upstream scenario. Do not hand-edit either, or the README badge.

**A green suite is not proof it graded mcpkit.** `testconf-tasks-v2` and `testconf-mrtr` reported
PASS for months while scoring upstream's own reference server, because they set env vars nothing
upstream reads (#1358). Before trusting a new or edited suite, break its fixture and confirm the
suite goes red. `conformance/NOTES.md` § A green suite is not proof it graded mcpkit also covers why
the MRTR fixture is `cmd/testserver`, how to regenerate the reports when a red suite has left them
stale, and the two inputs regeneration never touches (`known-gaps.yaml`,
`client-check-counts.json`).

**SEP-2640 is Accepted** (CM vote 2026-09-01). Conformance tests are one of three deliverables
gating Final and are ours: `modelcontextprotocol/conformance` PR 330, 96 requirement rows with 89
checks, three server scenarios and five client scenarios. Cross-checked against three independent
implementations (mcpkit, go-sdk, csharp-sdk), all green. Running it against someone else's
implementation is how two bugs in the suite were found and fixed, neither reachable from mcpkit
alone. Detail in `ext/skills/NOTES.md`, per-SDK setup in `RUNNING_SEP2640.md` on the conformance
branch.

**MCP Events has a conformance suite, and it is red on purpose.** `testconf-events` (stage 8i,
`INFO`) drives `examples/events/kitchen-sink` against scenarios proposed upstream as a draft in
`modelcontextprotocol/conformance` PR 504. It scores against the design sketch that merged
2026-09-08 in `modelcontextprotocol/experimental-ext-triggers-events`, which is a design document
with **no SEP number**, so every check id carries a placeholder `sep-9999-` prefix that must be
renamed before that PR can merge. Phase 1 ships 2 of 5 scenarios and emits 45 of 131 declared rows;
push and webhook follow. Building it surfaced six divergences in our own implementation, three of
which #1379 and #1381 have since closed; the rest are #1380. Detail in `conformance/NOTES.md`
§ MCP Events suite.

`testconf-scope-challenge` runs mcpkit against the upstream SEP-2350 server scope-challenge
scenario (`modelcontextprotocol/conformance` PR 481), currently 17/17. It is `INFO` rather than
gating because it tracks an unmerged PR head, so a red run there means the fixture contract moved.
Flip it to a gate against upstream `main` once 481 lands.

**The SEP Coverage table counts requirements, not tests.** A SEP showing "1 tested" may be covered
by dozens of assertions or by one; the two numbers are unrelated and reflect different upstream
commits. Per-suite pass counts in the local-suites table are hand-recorded from a run, not ingested
from artifacts, so treat them as claims with a date.

**A conformance ratio hides warnings and skips.** Upstream's runner counts only `SUCCESS + FAILURE`
in the denominator, so nine checks with one warning print as `8/8`, reading exactly like a scenario
where a check never ran. Read the `N failed, M warnings` tail too, and prefer our `testconf-*`
wrappers, which print `pass / fail / warn / skip`. This produced a wrong claim in a draft review
comment on 2026-09-07; see `conformance/NOTES.md` § Read the denominator, not just the ratio.

## Tasks v1 vs v2

Two surfaces, two entry points: `server.RegisterTasksV1` (frozen) and `tasks.Register`
(v2 / SEP-2663, canonical, in `ext/tasks/`). See `docs/TASKS_V2_MIGRATION.md`.
