# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — no open thread.
- Mission #1552 (wire_2026_07_28, active): 9/18; next ready #1625 (one middleware chain above the stateless method switch; `make exercise-wire` output unchanged is its acceptance). The queue lists #346 first by age. Exercise is now `make exercise-wire`, last logged 2026-10-24 @ 9ff89cef, all rows and suites green. #1628 (dual mode never serves a 2026-07-28 session) is waiting, to decide before #1625 starts.
- Mission #1553 (release_installable, active): 1/8; next ready #1486. #1624 closed #1488, and the exercise stays broken until a tag is cut after it.
- Rooms: #1611, #1615 and #1616 all merged (#1623, #1622, #1620). Still waiting: #1621 (adopt massrelay PR 44 once massrelay tags it) and the TS `TrustBridges` on panyam/massrelay#42.
- #1579 (events bridge) is open for the dogfooded CI loop and event-bound prompts. #1603 holds the #1600 to #1602 follow-ups and is next ready on mission #1580 (4/7), whose exercise couldn't run on 2026-10-24 (chakra#88 open; `make pg` needs a local model).
- Events conformance PRs 521/541: green against mcpkit since #1570. Still owed from the 2026-10-20 rmcp reply: the receiver docs and a self-signed https receiver option on PR 521.
- This run (main, 2026-10-24): merged #1627 (closed #1555, #1515, #1546) and #1626; filed #1628.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
