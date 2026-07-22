# Roadmap

This roadmap tracks mcpkit's implementation of the MCP specification, per the [SDK tiering requirements](https://modelcontextprotocol.io/community/sdk-tiers). Work is tracked in GitHub issues; this file points at the durable entry points rather than duplicating them.

## How work is tracked

- **Per spec release:** a Release Tracker issue labeled `release:<date>` (currently issue 431 for the 2026-07-28 spec release) carries the live implementation status for every SEP in that release.
- **Conformance posture:** `CONFORMANCE.md` (regenerated from upstream's tier-check) and `conformance/UPSTREAM_AUDIT.md` (mcpkit graded against every upstream scenario).
- **Release notes and migrations:** `CHANGELOG.md` plus `docs/releases/vX.Y.Z.md`.

## Current status

All required (non-experimental) features of the current spec, including the optional sampling and elicitation capabilities, are implemented. Upstream conformance: 100% of applicable server scenarios (see `CONFORMANCE.md`).

## Near-term

- **Stable v0.4.0 release** shortly after the 2026-07-28 spec GA. The scope of the 0.4.0 bundle is in `CHANGELOG.md` and `docs/releases/v0.4.0.md`.
- **Client conformance harness:** wire mcpkit's client into upstream tier-check's `--client-cmd` runner so the client scenario suites score alongside the server ones.
- **Tier 1 process items:** issue-triage SLA hygiene and the tier-advancement request to the SDK Working Group.

## Longer-term

- **SEP-2577 removals:** Roots, Sampling, and Logging surfaces are deprecated with a 12-month annotation window; removal is tracked in issue 850 and lands no earlier than 2027.
- **Extensions** (not required for any tier, tracked per-issue): Tasks v2, MCP Apps, OTel/SEP-414, Events, Skills.

## Out of scope for spec tracking

The agent SDK (`agent/`, `agent/host/`, `cmd/agentchat`) is a separate track above the protocol layer, with its own roadmap in `docs/AGENT_SDK_ROADMAP.md`. It is not part of the MCP specification surface and is versioned and released independently. Because it sits above the protocol layer, it may be extracted into its own repository in the future if that better serves its independent cadence; such a move would not affect the protocol modules or their consumers.
