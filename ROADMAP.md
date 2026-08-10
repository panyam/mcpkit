# Roadmap

This roadmap tracks mcpkit's implementation of the MCP specification, per the [SDK tiering requirements](https://modelcontextprotocol.io/community/sdk-tiers). Work is tracked in GitHub issues; this file points at the durable entry points rather than duplicating them.

## How work is tracked

- **Per spec release:** a Release Tracker issue labeled `release:<date>` carries the live implementation status for every SEP in that release. The tracker for the 2026-07-28 release (issue 431) is closed; a new one opens when the next spec release is announced.
- **Conformance posture:** `CONFORMANCE.md` (regenerated from upstream's tier-check) and `conformance/UPSTREAM_AUDIT.md` (mcpkit graded against every upstream scenario).
- **Release notes and migrations:** `CHANGELOG.md` plus `docs/releases/vX.Y.Z.md`.

## Current status

All required (non-experimental) features of the current spec, including the optional sampling and elicitation capabilities, are implemented. Upstream conformance: 30/30 server scenarios and 41/43 client scenarios, the two failures being the draft-SEP DPoP pair (issue 803). See `CONFORMANCE.md`.

Stable v0.4.0 shipped 2026-08-03, inside the 30-day window after the 2026-07-28 spec GA. Two releases have followed: v0.5.0 (2026-08-06) and v0.5.1 (2026-08-07). Scope and migrations for each are in `CHANGELOG.md` and `docs/releases/`.

## Near-term

- **SEP-2243 server-side custom param-header validation** (issue 1111), ahead of the upstream conformance suite activating those checks. Upstream conformance PR 325 is still open, so this is preparatory rather than blocking.
- **Version-matching dependency scanning in CI.** `govulncheck` is reachability-based and stayed green through the advisories v0.5.1 fixed, so a second scanning pass is needed. The command, the four known blockers, and the rationale are in `DEPENDENCY_POLICY.md` under Security updates.

Recently done: stable v0.4.0 and the v0.5.x line, client conformance harness (tier-check `--client-cmd`), 2025-03-26 legacy discovery fallback, WIF JWT-bearer grant, data-driven badges, triage auto-labeling, and the SEP-1730 Tier 1 documentation pass.

On the agent track, an approval/safety block landed 2026-08-09/10: reversibility as an approval axis distinct from `readOnlyHint` (1260), provenance labels replacing the spotlight boolean (1262), and the reversal seam with file checkpoints, the host extension, and the model-proposed undo tier (1267). Two of those were breaking API reshapes taken deliberately before the 1.0 freeze. The rationale for each lives in `agent/NOTES.md` § Safety, approval, and reversal, and the invariant they share is constraint **A11**.

## Toward 1.0

The tier-advancement request to the SDK Working Group is deliberately held until the 1.0 release. The protocol surface is already at the conformance and policy bar; the remaining work is on the agent track (see below), and cutting 1.0 once that lands means the tiering system's stable-release and spec-tracking checks are satisfied by a release that represents the whole project rather than a protocol-only slice. The request is a self-assessment issue filed against `modelcontextprotocol/modelcontextprotocol` once 1.0 is published.

**Issue 1240 is the 1.0 tracker.** 1.0 is primarily an API stability commitment rather than a feature milestone: it retires the `VERSIONING.md` clause allowing breaking changes in minor releases, and the agent surface is the young part of the tree. The tracker carries the freeze agenda — generation parameters on the provider seam (1239, done), package layout (constraint A10, done; the `agent/providers/` extraction question still open), an exported-vs-internal audit, seam shapes, naming, and contract documentation.

## Longer-term

- **SEP-2577 removals:** Roots, Sampling, and Logging surfaces are deprecated with a 12-month annotation window; removal is tracked in issue 850 and lands no earlier than 2027.
- **Extensions** (not required for any tier, tracked per-issue): Tasks v2, MCP Apps, OTel/SEP-414, Events, Skills.

## Out of scope for spec tracking

The agent SDK (`agent/`, `agent/host/`, `agent/store/`, and the terminal and web surfaces under `agent/surfaces/`) is a separate track above the protocol layer, with its own roadmap in `docs/AGENT_SDK_ROADMAP.md` and the web surface epic in `docs/AGENT_WEB_UI_EPIC.md`. It is not part of the MCP specification surface, and nothing in it affects the conformance posture above.

It does gate the 1.0 cut, per the section above. Phases 0 through 3 have shipped; Phase 4 was evaluated and dropped as a non-goal (constraint A8 in `agent/CONSTRAINTS.md`); Phases 5 through 7 remain open as epics (issues 1050, 1051, 1052). Because this track sits above the protocol layer, it may be extracted into its own repository in the future if that better serves its cadence; such a move would not affect the protocol modules or their consumers.
