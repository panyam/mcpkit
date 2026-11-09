# Next steps

Where each line of work left off. Work items live in GitHub issues; this file points at them.
Maintained by /checkpoint: one thread per branch, pruned when the branch merges.

## At a glance

- roomsevents/subscribe-before-attach-1676 — #1676 (PR #1688) — next: merge after CodeQL, then run `make exercise-aipage` and log it on #1683.
- Mission #1683 (ai_inside_ui, active): 4/5, #1676 last. Exercise `make exercise-aipage` last run 2026-11-09 @ cd868851, passed with the LangGraph game master. Closing it needs the by-hand Claude Code run through `rooms-channel`.
- Mission #1553 (release_installable, active): 1/8; next ready #1319. v0.8.18 passed `make check-install` once the proxy dropped its cached miss.
- Mission #1580 (host_completion): 7/8; only #1576 (A2A client) left, `waiting`.
- Mission #1554 (apps_host): queued behind #1683; next ready #1454.
- Events conformance PRs 521 and 541 (upstream): open, out of draft.
- Released v0.8.18 on 2026-11-09 for chakra: join endpoint, declared page events, WIRE.md, Lanternfall.
- This run: dropped the stale #1552 lines (mission closed) and the v0.8.13 to v0.8.15 release notes.

## Across threads

- Conformance fork checkouts (`../conf-events*`) are shared between sessions: one worktree per session (CLAUDE.md, conformance/NOTES.md).
- CodeQL runs every few PRs rather than on each; it is due on #1688, the third PR since the last run.

## roomsevents/subscribe-before-attach-1676

- **Last touched**: 2026-11-09 (Srirams-MBP:~/newstack/mcpkit/mcpkit-1676)
- **Ticket**: #1676 (PR #1688)
- **Why**: hosts that open event streams before `attach` (chakra#312) heard nothing; the mission's last ticket.
- **Where it stopped**: PR open, CI running; `onSubscribe` accepts any session, `match` gates on `Bridge.AttachedSince`. All rooms suites and the exercise pass.
- **Next action**: after merge, run `make exercise-aipage`, post the mission log on #1683, and tell chakra#312 it can drop its setup-call ordering for room events.
- **Open questions**: whether to close #1683 before the by-hand Claude Code run, or keep it open until someone plays Lanternfall through `rooms-channel`. Leaning: keep it open.
