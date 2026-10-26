# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — no open thread.
- Mission #1552 (wire_2026_07_28, active): 16/20; next ready #346 (`notifications/tasks` goes to session SSE rather than a `tasks/get` stream; re-read SEP-2663 before designing). Left besides it: #1569 with #1111 (one PR), and #1356 (needs the user's call on a default page size). Exercise `make exercise-wire` last logged 2026-10-26 @ 629192c3, 19/19 rows, suites unchanged.
- Mission #1553 (release_installable, active): 1/8; next ready #1486. Last logged 2026-10-25 on v0.8.12, 11 ok / 0 FAIL.
- Mission #1580 (host_completion): 7/8; only #1576 (A2A client) left, `waiting`. chakra `make host-exercise` 10/10 PASS on 2026-10-26 @ e2014ade. chakra can drop `listEventDefs` for `eventsclient.List` after it bumps (chakra#135). #1579 (events bridge CI loop) is still open.
- Events conformance PRs 521/541: green against mcpkit since #1570. Still owed from the 2026-10-20 rmcp reply: the receiver docs and a self-signed https receiver option on PR 521.
- Released v0.8.13 and v0.8.14 on 2026-10-26 (agni and chakra pick-ups). Unreleased since v0.8.14: #1649, #1650, #1652, #1653, cut as v0.8.15 right after this checkpoint.
- This run (main, 2026-10-26): #1520, #825, #1547, #1523 and #1651 closed via #1647, #1649, #1650, #1652, #1653; every worktree from them removed.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
