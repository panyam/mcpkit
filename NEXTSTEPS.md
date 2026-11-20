# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #1737 (server_card, active): 2/3. #1437 merged via PR #1741 inside PR #1740 @ 6becf9ea. `make exercise-server-card` passes there on both wires, and its webhook step stays skipped until #1738. Events sends `v1a`, as SEP-3415 says. Standard Webhooks deprecated `v1a` for `v2a`, the user raised that in tasks-events-wg, and servicekit v0.2.4 ships both. Next is #1738, which publishes `SigningJWKS()` on the card.
- Mission #1701 (progressive_disclosure, active): 5/7. #1708 closed. Next is #1726, the groups client scenarios and the upstream PR. The WG's TS prototype (PR 20) still nests `groups/get` under `contents`.
- Mission #1554 (apps_host, active): 0/11, and its exercise (`make test-apps-playwright`) has never been logged. Run it for a baseline before #1454, the AppHost View lifecycle.
- Mission #1683 (ai_inside_ui): 5/5 tickets closed, issue open. It closes after a by-hand Lanternfall run from Claude Code through `rooms-channel`. `make exercise-aipage` last passed 2026-11-09 @ b67480db.
- Mission #1580 (host_completion): 7/8. Only #1576 (A2A client) is left, and it's `waiting`.
- Events conformance PRs 521 and 541 (upstream) are open and out of draft.
- `origin/deps/sweep-2026-11` holds one minor-sweep commit on top of v0.8.15, with no PR. Open a PR for it, or redo the sweep on main.
- This run: #1737 moved to 2/3. servicekit v0.2.4 and oneauth v0.3.13 are pinned in every module (PR #1740).

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
