# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- main — #1535 — next: post the events WG draft, then undraft conformance #521/#541 or name the blocker
- Mission #1508 (events, SEP-3415 suites green on ours and one other implementation): 13/14 closed; #1535 is the last ticket. Exercise last ran 2026-10-15 @ 9903ac20, green (server 122/1/12, client green on both lifecycles).
- This run: created this file; no other branches carry one.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).

## main

- **Last touched**: 2026-10-15 (Srirams-MBP:~/newstack/mcpkit/main)
- **Ticket**: #1535 (implementation in #1538 and #1549, both merged)
- **Why**: the mission's done-when needs results posted in `#triggers-events-wg` and the upstream suites out of draft or blocked by name.
- **Where it stopped**: metronome re-run 2026-10-16: its three findings are fixed and nothing fails outright, TS reference client green. Five untraceable rows excluded on the fork (`4a73fc6`). The post is drafted locally in `drafts/wg-events-1535.md` (untracked, excluded via `.git/info/exclude`), unposted.
- **Next action**: the author posts the draft; then undraft #521/#541 and close #1535.
