# CLAUDE.md — MCPKit

Go library for building production-grade MCP servers and clients, plus an agent SDK layered above
the protocol.

This file is a router. Detail lives beside the code it describes — see **Where knowledge lives**
below before adding anything here.

## Quick Commands

`make` is the supported runner and the only one CI uses. Justfiles mirroring these names exist but
are an experiment; `make` is authoritative when they disagree.

```bash
make test              # Core tests (core/server/client/testutil)
make test-agent        # experimental/agent/ and its sub-modules
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
`testconf-skills`, `testconf-stateless`, `testconf-upstream-audit`, `refresh-conformance`,
`check-conformance-stale`, …) are orchestrated in `conformance/Makefile`, which also documents each
suite's `MCPCONFORMANCE_*_PATH`. See `conformance/NOTES.md` for how they are wired and which
upstream changes to watch.

## Package Layout

| Package | Docs |
|---------|------|
| `core/` — Protocol types, typed contexts, session APIs | `core/README.md`, `core/CONSTRAINTS.md` |
| `server/` — Server, transports, middleware, v1 tasks (frozen) | `server/README.md`, `server/CONSTRAINTS.md`, `server/NOTES.md` |
| `client/` — Client, transports, reconnection, auth retry | `client/README.md`, `client/CONSTRAINTS.md` |
| `experimental/agent/` — Provider, Runner loop, ToolSource, memory, composition | `experimental/agent/CLAUDE.md`, `experimental/agent/CONSTRAINTS.md`, `experimental/agent/NOTES.md` |
| `experimental/agent/host/` — Reusable, surface-agnostic host application core | `experimental/agent/host/README.md` |
| `experimental/agent/ext/checkpoint/` — Reversal seam (restore vs compensate) + file checkpoints | `experimental/agent/ext/checkpoint/README.md` |
| `experimental/agent/ext/files/` — Workspace file tools: read, edit, write, list, search (stale and ambiguous edits refused) | `experimental/agent/ext/files/README.md` |
| `experimental/agent/ext/exec/` — Allowlisted project commands, sandboxed (darwin backend; elsewhere refuses) | `experimental/agent/ext/exec/README.md` |
| `experimental/agent/ext/lsp/` — Language servers in the loop: diagnostics on two paths, symbol-addressed navigation | `experimental/agent/ext/lsp/README.md` |
| `experimental/agent/surfaces/chat/` — Terminal CLI (binary: `agentchat`) | `README.md`, `NOTES.md`, `CLAUDE.md` in that dir |
| `experimental/agent/surfaces/web/` — Connect bridge + DockView frontend (binary: `agentweb`) | `experimental/agent/surfaces/web/README.md`, `docs/AGENT_WEB_UI_EPIC.md` |
| `ext/auth/` — JWT, PRM, OAuth | `ext/auth/docs/DESIGN.md`, `ext/auth/NOTES.md` |
| `ext/tasks/` — SEP-2663 v2 tasks extension | `ext/tasks/README.md` |
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
  `experimental/agent/` and `experimental/agent/surfaces/chat/` have one.
- **`CONSTRAINTS.md`** — enforceable architectural rules. Project-wide at the root; per-package in
  `core/`, `server/`, `client/`, `experimental/agent/`.

Design docs live in `docs/`: `ARCHITECTURE.md`, `AGENT_DESIGN.md`, `AGENT_COMPOSITION.md`,
`AGENT_MEMORY_FLOW.md`, `AGENT_SDK_ROADMAP.md`, `AGENT_WEB_UI_EPIC.md`, `APPS_DESIGN.md`,
`SEP_414_OTEL.md`, and the per-SEP migration guides.

**`CAPABILITIES.md` was retired** (commit `ebc41058`). Checkpoint and start_pr must not sync or
recreate it. Fold learnings into the per-package `NOTES.md`, the design docs, and the roadmap.

## Sub-Modules

**`SUB_MODS_TO_TAG` in the root `Makefile` is the authoritative list** — do not maintain a copy
here, it rots. `make test` does not cover sub-modules; each has its own target.

Run **`make tidy-all` after touching `core/` imports** or sub-module `go.sum` files drift and CI
fails in a module you did not edit.

`docs/site/` is the GitHub Pages renderer. It is a tool, not a library, and is excluded from
`SUB_MODS_TO_TAG`.

**The agent SDK is on a separate, unreleased version line.** `experimental/agent/` and its nine
sub-modules are absent from `SUB_MODS_TO_TAG` by design and are listed in `AGENT_MODS_TO_TAG`
instead, tagged by the staged `make tag-agent` (which refuses to run without `AGENT_RELEASE_OK=1`).
Two rules follow, both CI-enforced by `scripts/verify-submodule-deps.sh`:

- **Do not add an agent module to `SUB_MODS_TO_TAG`**, and do not run `tag-agent` as part of a
  protocol release. The point of the split is that the protocol surface can commit to API stability
  while the agent surface keeps breaking things.
- **Do not "fix" the `v0.0.0` pins in agent `go.mod` files.** Agent-to-agent requires sit at
  `v0.0.0` behind `replace` directives, and since Go ignores replace outside the main module, that
  is what keeps the tree unresolvable from outside. Bumping one to a real version is the failure the
  inverted check exists to catch. Requiring a *published* module (`ext/otel`, `ext/auth`) at a real
  tag is fine and already happens.

Rationale and the extraction path are in `VERSIONING.md` § Agent SDK.

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
  `scripts/check-no-binaries.sh` (whole-tree, wired into `test.yml` — the actual gate).
  `HANDOFF.md` / `HANDOFF_*.md` are gitignored for the same `git add -A` reason.
- **`govulncheck` green does not mean dependencies are current.** Default govulncheck is
  *reachability*-based, so it exits 0 while advisories sit unfixed in required modules. Version
  matching is a separate pass. Command, blockers, and rationale: `DEPENDENCY_POLICY.md`
  § Security updates.
- **GitHub access needs the personal token and key.** `GH_TOKEN="$GH_PERSONAL_TOKEN"` — the EMU
  account cannot reach personal repos. `git push` to `panyam-github` likewise needs the key pinned,
  because the ssh-agent offers the EMU key first and GitHub rejects it before reaching
  `~/.ssh/id_github`:
  `GIT_SSH_COMMAND="ssh -i ~/.ssh/id_github -o IdentitiesOnly=yes" git push …`.
  Release creation and editing have their own PAT gap — see `RELEASING.md`.
- **Stacked PRs get no CI** when the base is not `main`. Verify locally, then either retarget to
  main after the base merges or push an empty commit to fire checks. GitHub's `Closes #N` only
  fires on a merge to the **default** branch, so carry it on whichever PR actually reaches main.
  Two branches that both append tests to the end of the same file will re-conflict at every cross
  merge; land the shared base before branching the second consumer.
- **Repo security settings are settings, not files.** Dependabot alerts, security updates, and
  private vulnerability reporting need no commit; `dependabot.yml` governs *version* updates only.
  A 403 is not a 404 — a status check that treats any non-success as "disabled" reports a
  configured repo as unprotected.

## Conformance

All tier-scored surfaces are at 100% on upstream tier-check: **Server 30/30, Client Core 4/4,
Client Auth 16/16.** Full client suite **41/43**, the two failures being `auth/dpop` and
`auth/dpop-nonce`, both gated on SEP-1932 leaving draft (#803).

`CONFORMANCE.md` is generated and CI-gated for staleness; `conformance/UPSTREAM_AUDIT.md` grades
mcpkit against every upstream scenario. Do not hand-edit either, or the README badge.

## Tasks v1 vs v2

Two surfaces, two entry points: `server.RegisterTasksV1` (frozen) and `tasks.Register`
(v2 / SEP-2663, canonical, in `ext/tasks/`). See `docs/TASKS_V2_MIGRATION.md`.
