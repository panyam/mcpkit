# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — no open thread.
- #1585 (follow-ups from the events and room bridges, serves #1580 host_completion, off the active missions) is split into PRs: PR 1 `feat/client-server-gaps-1585` (client cancel and progress, typed 403 scope error, session-close hook, core scope challenge, C10 reword) and PR 3a `feat/events-followons-1585` (Receiver subscription id, `StopAndUnsubscribe`, per-watch tool lists) in progress; PR 2 #1586 (stale-suite warning) in review. Still to do: PR 3b (rooms adopts PR 1's hook and scope helper), and moving the interactive `Approver` item to its own issue when #1585 closes.
- #1579 and #1581 stay open: #1579 lacks the dogfooded CI loop and event-bound prompts; #1581 has slice 1 merged (#1584), and the massrelay adapter, TS `joinToolRoom`, and events server plus stdio channel front end are next.
- Mission #1552 (wire_2026_07_28, active): 4/16; next ready #1555, which makes the exercise able to fail. Exercise logged 2026-10-20 @ 67bca447: green, and still grades none of the mission's bugs (it was green before #1570 too).
- Mission #1553 (release_installable, active, own worktree): 0/8; next ready #1488. Exercise logged 2026-10-16: broken at `go install …/cmd/mcpskills@v0.8.0`. v0.8.3 is being tagged 2026-10-22 so agni can depend on `experimental/ext/rooms`; it doesn't fix #1488.
- Events conformance PRs 521/541: re-pinned to SEP-3415 @ `2ec39b9c` and green against mcpkit since #1570. Still owed from the 2026-10-20 reply to the rmcp review: the receiver docs and a self-signed https receiver option on PR 521. Preston offered PRs for the wire-schema scoping and the #541 listen-row skips.
- This run (main, 2026-10-22): merged #1582 (#1558), #1583 (#1579 bridge, part), #1584 (#1581 slice 1); filed #1585; dropped the 2026-10-20 run line.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
