# NOTES — repo-wide lore

Internal, never published. The rules in `CLAUDE.md` point here for their history. Package lore
lives in each package's `NOTES.md`; conformance lore in `conformance/NOTES.md`; releases in
`RELEASING.md`.

## GitHub credentials

There are two, and the one you want depends on the repo.

`GH_TOKEN="$GH_PERSONAL_TOKEN"` is **fine-grained** and scoped to the owner's account. Use it for
`panyam/*`, where the EMU account cannot reach. It can read `modelcontextprotocol/*` but never
write it. `POST .../pulls`, `gh pr edit` and `PATCH .../pulls/N` all return 403, even on a PR we
authored. No setting fixes it, because fine-grained PATs only scope to repos in the owner's account.

**For upstream writes, use `gh`'s own stored login.** `env -u GH_TOKEN gh …` falls back to the
`gho_` OAuth token in `~/.config/gh/hosts.yml`. It carries full `repo` scope, and it creates and
edits PRs and posts review comments on `modelcontextprotocol/*`. Verified on conformance#504. An
earlier version of this note said upstream edits need the web UI or a classic PAT; that holds only
for the fine-grained token.

Pushing to our fork branches is unaffected. `conformance/NOTES.md` covers the
`panyam/mcpconformance` pre-push hook. It runs the full suite (~6 min), so never wrap that push in
`timeout`. Push it over HTTPS with the `gho_` login, because an SSH connection idles out during the
hook and the push fails after the hook passes.

**The fine-grained PAT cannot create a branch carrying workflow files**, even when no commit on it
touches `.github/`. Creating a ref counts every workflow on that ref as "created". The push is
rejected with "refusing to allow a Personal Access Token to create or update workflow
`.github/workflows/test.yml` without `workflow` scope". Pushing to an existing branch works on the
PAT, and so does pushing tags. A release push therefore succeeds, and the *first* push of a new
branch fails. The `gho_` login carries `workflow`, so hand that push to it:

```
env -u GH_TOKEN git -c credential.helper='!gh auth git-credential' push -u https://github.com/panyam/mcpkit.git <branch>
```

Cost a round-trip on #1405. Pushing to a URL leaves no remote-tracking ref, so a bare
`--force-with-lease` after a rebase is refused. Name the sha you expect:
`--force-with-lease=<branch>:<old-sha>`.

**SSH pushes use the agent, not a key file.** `~/.ssh/id_github` does not exist in the container.
Only `~/.ssh/agent.sock` exists, and it holds the right key. Use
`SSH_AUTH_SOCK=~/.ssh/agent.sock git push …`. The `-i ~/.ssh/id_github` form fails with "Identity
file not accessible", then "Permission denied (publickey)".

Releases behave the same way. `env -u GH_TOKEN gh release create` publishes, and the fine-grained
PAT returns 403 on Contents: write. Reading Dependabot alerts also returns 403 on the PAT. See
`RELEASING.md`.

## Stacked PRs

A PR whose base is not `main` gets no CI. Verify locally. Then retarget to main after the base
merges, or push an empty commit to fire checks. GitHub's `Closes #N` only fires on a merge to the
**default** branch, so carry it on the PR that reaches main.

Two branches that both append tests to the end of one file re-conflict at every cross merge. Land
the shared base before branching the second consumer.

**Upstream, a fork PR cannot stack on another fork PR.** The base must be a branch in the upstream
repo, and we have no push there. So conformance PR 541 bases on `main`, carries all of PR 521's
commits, and says "review #521 first" with a compare link in its body.

## A commit message can close an issue across a line break

A title ending in "closed" and a body starting with "#1683" read to GitHub as "closed #1683".
Merging the PR closed the mission. Keep a closing word (close, fix, resolve and their forms) away
from the end of a line when the next line starts with an issue number. Check the mission is still
open after a merge.

## Compiled binaries

A bare `go build` in a module directory drops an executable named after the directory, with no
extension. `git add -A` then sweeps it into an unrelated commit. Nine accumulated this way. A
`git filter-repo` purge took `.git` from 466 MB to 23 MB. Two layers now gate it, and both detect by
**magic bytes, not filename**:

- `scripts/pre-commit-hook.sh`, local, opt-in via `make setup-hooks`
- `scripts/check-no-binaries.sh`, whole-tree, wired into `test.yml`. This one is the actual gate.

`HANDOFF.md` and `HANDOFF_*.md` are gitignored for the same `git add -A` reason.

## Makefiles and justfiles dispatch

Constraint C8 has the rule. Nearly every directory carries both a `Makefile` and a `justfile`. In
`examples/` they must stay name-and-behavior identical (`examples/CONVENTIONS.md` §6). Inline logic
therefore gets written twice, in two escaping dialects, and maintained in neither. `just
clean-backends` became a byte-identical copy of `just clean`: it wiped the events volumes while
promising to wipe `docker/backends` (#1396).

The root justfile is partial. 72 Makefile targets had no twin as of 2026-10-22, and nothing gates
its parity. Keep the Makefile complete. Bringing the justfile up to it is open to whoever wants it.

The sweep is done, and `scripts/recipe-complexity-allowed.txt` is empty. Any entry that appears
there is a new decision. The gate tests its own precision: `$(if ...)` in a Makefile is a function,
not a script.

## `SUB_MODS_TO_TAG` is parsed, not just expanded

`scripts/check_dep_consistency.py` reads the list out of the `Makefile` with a regex. The parser
expects the backslash-continued form, so the list's *shape* matters. The justfile used to carry a
second copy that no recipe referenced. Deleting it as dead broke that gate with
`SUB_MODS_TO_TAG not found in justfile`. Before removing a variable from a build file, grep the
whole tree, not only the recipes. A Python script reading a Makefile does not look like a reference.

`scripts/verify-submodule-deps.sh` enforces two policies, protocol and non-library. The agent policy
left with the agent SDK, which now lives in [chakra](https://github.com/panyam/chakra).

## `gofmt -w` on a glob

The tree is not gofmt-clean. `gofmt -w pkg/*.go` rewrites pre-existing drift alongside your file,
and `git add -A` sweeps it into the commit. It happened twice in one session, 36 files then 9. Both
were caught only by reading `git status` before pushing. Format the files you edited, by name.

## `-race` in CI

CI runs `-race` only in the rooms jobs. `test-rooms-massrelay.sh` runs that module fully under
`-race` since servicekit v0.1.10 fixed its `OnClose` race. roomsevents runs under it too. A client
race in `Close` (#1614) first showed there; the root `test` job never runs `-race`. The other
experimental scripts don't either. The events bridge shipped a test-helper race that failed 5 runs
in 6 under `-race` and never showed in CI. After touching anything concurrent, run `go test -race`
on the package a few times by hand.

## Experimental modules report `[setup failed]` from the root

`go test ./experimental/ext/rooms/` from the repo root reports `[setup failed]`, which reads like a
flaky failure. The experimental modules are separate modules. Run `go test` inside the module
directory, or through its `make test-experimental-*` target.

## A mutation that breaks the build reads as a pass

A red-before-green runner that greps for `--- FAIL` reports a broken build as "no test went red".
Check for `build failed` too. It hid two mutations in #1613 until a direct run showed the build
error.

## `check-dep-consistency` wants `--prune-baseline`

The CI error text suggests `--update-baseline`. That flag also accepts any *new* divergence
silently, which defeats the baseline. `--prune-baseline` only drops entries that stopped diverging.
A cross-module `go mod tidy` sweep converges pins as a side effect, so this gate fires on dependency
bumps that look unrelated to it.

## CodeQL and allocation sizes

CodeQL rejects `make(T, len(a)+len(b))` as an allocation size that may overflow. Capacity is a hint,
and the map or slice grows anyway. Size from one operand.

## Grading chakra's host exercise against your tree

Mission #1580's exercise is chakra's `make host-exercise`. It runs against `../mcpkit/main` through
chakra's replaces. That shared checkout is often stale, or carries another session's edits. To grade
your own tree, `git clone --local` chakra into your scratch directory. Then rewrite every
`=> (../)+mcpkit/main` replace in its `go.mod` files to your checkout's absolute path.

## The wire exercise outlived its mission

#1552 (`mission_wire_2026_07_28`) closed 2026-11-09. `make exercise-wire` still guards the wire. It
runs the session-vs-stateless parity rows (`TestWireParity` in `server/` and
`experimental/ext/agents`), then `testconf testconf-stateless testconf-agents`. Those rows are the
only cross-wire check anywhere. Each upstream suite grades one wire on its own fixture: `testconf` on
`cmd/testserver` sessions, `testconf-stateless` on the cart example. #1352, #1355 and the #1637 gaps
all shipped with every suite green.
