# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- Mission #1701 (progressive_disclosure, active): 2/5. `make exercise-disclosure` passed 2026-11-16 @ e5a74c28 on both wires, with the groups path at 14% of the flat list's bytes. Next #1708, groups conformance scenarios from WG PR 19. The WG's TS prototype (PR 20) still nests `groups/get` under `contents`.
- Mission #1554 (apps_host, active): 0/11, and its exercise (`make test-apps-playwright`) has never been logged. Run it for a baseline before #1454, the AppHost View lifecycle.
- Mission #1683 (ai_inside_ui): 5/5 tickets closed, issue open. It closes after a by-hand Lanternfall run from Claude Code through `rooms-channel`. `make exercise-aipage` last passed 2026-11-09 @ b67480db.
- Mission #1580 (host_completion): 7/8; only #1576 (A2A client) left, `waiting`.
- Events conformance PRs 521 and 541 (upstream): open, out of draft.
- `origin/deps/sweep-2026-11` holds one minor-sweep commit on top of v0.8.15, with no PR. Open a PR for it, or redo the sweep on main.
- This run: mission #1710 (web_examples) closed, after nine `/try/` playgrounds were published (#1713 through #1725). Dropped the #1553 line (mission closed). CLAUDE.md's missions list now follows the `mission:active` label.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
