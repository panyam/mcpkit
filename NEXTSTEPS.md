# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — no open thread.
- The room bridge is done for #1581 and #1610: it runs on both wires, and pages hold agreement as grants to principals. `@panyam/mcpkit-rooms` publishes with each release, and the first automatic publish was 0.8.6. Next on rooms, all `waiting` on a consumer:
  - #1611: a runtime catalog and scope-filtered `tools/list` on the stateless wire
  - #1615: fan-out to many tabs, which needs panyam/massrelay#43
  - #1616: presence for pages
  - the TS `TrustBridges`, which waits on panyam/massrelay#42
- #1579 (events bridge) is open for the dogfooded CI loop and event-bound prompts. #1603 holds the follow-ups from #1600 to #1602, including events lost while awaiting approval.
- Mission #1552 (wire_2026_07_28, active): 5/17; next ready #1555. Exercise last logged 2026-10-20 @ 67bca447, and it hasn't run since. Many PRs have merged since, none of them on the mission.
- Mission #1553 (release_installable, active): 0/8; next ready #1488. The exercise hasn't run since 2026-10-16, though five releases have shipped (v0.8.3 to v0.8.7). `rooms-channel` has #1488's problem too (noted there).
- Events conformance PRs 521/541: re-pinned to SEP-3415 @ `2ec39b9c` and green against mcpkit since #1570. Still owed from the 2026-10-20 reply to the rmcp review: the receiver docs and a self-signed https receiver option on PR 521.
- This run (main, 2026-10-23):
  - merged #1586 to #1608 and #1612 to #1614
  - released v0.8.4, v0.8.5, v0.8.6 and v0.8.7
  - filed #1603, #1611, #1615 and #1616, plus massrelay #31, #33, #42 and #43, and servicekit #41
  - closed #1581, #1585, #1589 and #1610

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
