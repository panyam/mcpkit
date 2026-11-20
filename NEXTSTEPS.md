# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #1737 (server_card, active): 1/3. #531 merged as PR #1739 @ 463dec05, and `make exercise-server-card` passed there on both wires. Next is #1437 (v1a signing). The plan is to push `SignV1a`/`VerifyV1a` down into servicekit `http/webhooks`, release v0.2.4, then bump. It's on hold: Standard Webhooks deprecated `v1a` for `v2a` on 2026-11-18 (standard-webhooks#372), and SEP-3415 still says `v1a,`. The leaning is servicekit gets both and events sends `v1a`, plus a tasks-events-wg post. That decision is the user's.
- Mission #1701 (progressive_disclosure, active): 5/7. #1708 closed. Next is #1726, the groups client scenarios and the upstream PR. The WG's TS prototype (PR 20) still nests `groups/get` under `contents`.
- Mission #1554 (apps_host, active): 0/11, and its exercise (`make test-apps-playwright`) has never been logged. Run it for a baseline before #1454, the AppHost View lifecycle.
- Mission #1683 (ai_inside_ui): 5/5 tickets closed, issue open. It closes after a by-hand Lanternfall run from Claude Code through `rooms-channel`. `make exercise-aipage` last passed 2026-11-09 @ b67480db.
- Mission #1580 (host_completion): 7/8. Only #1576 (A2A client) is left, and it's `waiting`.
- Events conformance PRs 521 and 541 (upstream) are open and out of draft.
- `origin/deps/sweep-2026-11` holds one minor-sweep commit on top of v0.8.15, with no PR. Open a PR for it, or redo the sweep on main.
- This run: added mission #1737. Updated #1701 (#1708 closed).

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
