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

The agent track's recent work (the approval and safety block of 2026-08-09/10, and the first real
`host.Extension`) moved with the SDK to [chakra](https://github.com/panyam/chakra). Its rationale
lives in that repo's `NOTES.md` and `CONSTRAINTS.md` now.

## Toward 1.0

The tier-advancement request to the SDK Working Group is a self-assessment issue filed against `modelcontextprotocol/modelcontextprotocol` once 1.0 is published. It was previously held until the agent track was ready to freeze alongside the protocol modules. **It no longer is** (issue 1290, then the extraction): the agent SDK left the release train and then the repository, so 1.0 is a protocol-surface commitment and nothing on the agent track gates it.

That split was possible because the coupling was never real. `agent/` was always its own module with its own tag namespace, so Go never required the two to share a version, and the lockstep lived entirely in `SUB_MODS_TO_TAG` and one sentence of `VERSIONING.md`. Holding both to one line would have meant either freezing an agent surface that shipped in two months and still takes breaking reshapes, or holding back a protocol surface already at the conformance and policy bar. See `VERSIONING.md` § Agent SDK.

**Issue 1240 is the 1.0 tracker.** 1.0 is primarily an API stability commitment rather than a feature milestone: it retires the `VERSIONING.md` clause allowing breaking changes in minor releases. Its scope narrowed with 1290. The freeze agenda now covers the protocol modules, and the agent-surface items on it (exported-vs-internal audit 1289, the approval-seam decision 1288) become work that can be paced against real usage instead of a release date.

## Longer-term

- **SEP-2577 removals:** Roots, Sampling, and Logging surfaces are deprecated with a 12-month annotation window; removal is tracked in issue 850 and lands no earlier than 2027.
- **Extensions** (not required for any tier, tracked per-issue): Tasks v2, MCP Apps, OTel/SEP-414, Events, Skills.

## Out of scope for spec tracking

The agent SDK was extracted to its own repository, [chakra](https://github.com/panyam/chakra). It
sits above the protocol layer, is not part of the MCP specification surface, and nothing in it
affects the conformance posture above. Its roadmap travelled with it.

It no longer gates the 1.0 cut. mcpkit does not depend on it, so a breaking change there cannot
reach this tree.
