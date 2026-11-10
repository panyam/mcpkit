# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #1683 (ai_inside_ui, active): 5/5 tickets closed, issue kept open. Exercise `make exercise-aipage` passed 2026-11-09 @ b67480db, and only the v0.8.19 and v0.8.20 releases merged since. To close it, play Lanternfall by hand from Claude Code through `rooms-channel`.
- Mission #1553 (release_installable, active): 1/8; next ready #1319 (`experimental/ext/protogen` is still missing from `SUB_MODS_TO_TAG`). `make check-install V=v0.8.20` passed 11/0 on an empty module cache, logged on the issue.
- Mission #1580 (host_completion): 7/8; only #1576 (A2A client) left, `waiting`.
- Mission #1554 (apps_host): queued behind #1683; next ready #1454.
- Events conformance PRs 521 and 541 (upstream): open, out of draft.
- `origin/deps/sweep-2026-11` holds one minor-sweep commit on top of v0.8.15, with no PR. Open a PR for it, or redo the sweep on main.
- This run: dropped roomsevents/subscribe-before-attach-1676 (PR #1688 merged; chakra#312 closed) and the CodeQL-due line (#1689 cleared the batch).

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
