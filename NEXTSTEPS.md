# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — no open thread.
- Mission #1552 (wire_2026_07_28, active): 12/20; next ready #346 (`notifications/tasks/status` goes to session SSE). Small alternatives: #1520, #1569, #1356 (which would make #1645's stateless cursor fix testable). Exercise `make exercise-wire` last logged 2026-10-26 @ 34fe2202, 18/18 rows, suites unchanged.
- Mission #1553 (release_installable, active): 1/8; next ready #1486. Last logged 2026-10-25 on v0.8.12, 11 ok / 0 FAIL.
- #1579 (events bridge) is open for the dogfooded CI loop and event-bound prompts. #1603 holds the #1600 to #1602 follow-ups and is next ready on mission #1580 (5/7), whose exercise couldn't run on 2026-10-24 (chakra#88 open; `make pg` needs a local model).
- Events conformance PRs 521/541: green against mcpkit since #1570. Still owed from the 2026-10-20 rmcp reply: the receiver docs and a self-signed https receiver option on PR 521.
- Unreleased on main since v0.8.12: #1636, #1638, #1639, #1642, #1643, #1644, #1645. #1644 and #1645 carry a Breaking CHANGELOG entry (`server/stateless` lost `Dispatcher`/`New`/`Backend`); the user wants it on a 0.8.x patch tag, not 0.9.
- This run (main, 2026-10-26): #1637, #1628 and #1625 closed via #1638, #1639, #1642, #1644, #1645; their worktrees and branches removed.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
