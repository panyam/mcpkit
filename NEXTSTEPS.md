# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — no open thread.
- Mission #1552 (wire_2026_07_28, active): 9/19; next ready #1628 (dual mode never serves a 2026-07-28 session), which blocks #1625. Exercise `make exercise-wire` last logged 2026-10-25 @ 3e8cada2 (v0.8.12), all green; one PR (#1636, rooms only) merged since.
- Mission #1553 (release_installable, active): 1/8; next ready #1486. Exercise works again since v0.8.9; last logged 2026-10-25 on v0.8.12, 11 ok / 0 FAIL.
- #1579 (events bridge) is open for the dogfooded CI loop and event-bound prompts. #1603 holds the #1600 to #1602 follow-ups and is next ready on mission #1580 (5/7), whose exercise couldn't run on 2026-10-24 (chakra#88 open; `make pg` needs a local model).
- Events conformance PRs 521/541: green against mcpkit since #1570. Still owed from the 2026-10-20 rmcp reply: the receiver docs and a self-signed https receiver option on PR 521.
- Unreleased on main since v0.8.12: massrelay v0.3.3 in Go and npm (#1636). Folds into the next release; no tag cut for it.
- This run (main, 2026-10-25): merged #1636; dropped the rooms line (#1621 and massrelay#42 closed, TS `trustBridges` shipped in 146494fb).

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
