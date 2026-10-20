# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — no open thread.
- Mission #1552 (wire_2026_07_28, active): 4/16; next ready #1555, which makes the exercise able to fail. Exercise logged 2026-10-20 @ 67bca447: green, and still grades none of the mission's bugs (it was green before #1570 too).
- Mission #1553 (release_installable, active, own worktree): 0/8; next ready #1488. Exercise logged 2026-10-16: broken at `go install …/cmd/mcpskills@v0.8.0`.
- Mission #1554 (apps_host): queued behind #1552.
- Events conformance PRs 521/541: re-pinned to SEP-3415 @ `2ec39b9c` and green against mcpkit since #1570. Still owed from the 2026-10-20 reply to the rmcp review: the receiver docs and a self-signed https receiver option on PR 521. Preston offered PRs for the wire-schema scoping and the #541 listen-row skips.
- This run (events clone, 2026-10-20): merged #1570 (#1567); filed #1569, narrowed after finding #1111 already covered Mcp-Param-*. No other branch carries a thread.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
