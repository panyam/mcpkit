# MCP Conformance

mcpkit's posture against the [MCP conformance suite](https://github.com/modelcontextprotocol/conformance) and the per-SEP traceability manifest, regenerated on every PR and gated for staleness by CI.

This file has two parts:

- **Overview** (hand-edited, preserved across regenerations) — what the report covers, what it does *not* cover, and how to read it.
- **Generated block** below the begin-marker — rebuilt from `npx @modelcontextprotocol/conformance tier-check --output json` + `src/seps/traceability.json`. Do not hand-edit; changes are overwritten by `scripts/refresh-conformance.sh`.

## How to read this

- **Conformance Summary** counts wire-level scenarios from the upstream conformance suite running against `cmd/testserver`. Aggregate pass/total at the scenario level + check-level pass/fail totals.
- **SEP Coverage** is sourced from upstream's traceability manifest, which maps each SEP to its declared requirements. A row's `Status` says whether upstream has emitted a check ID for every declared requirement — *not* whether mcpkit passes them. Scenario-level pass/fail per SEP lives in [`conformance/UPSTREAM_AUDIT.md`](conformance/UPSTREAM_AUDIT.md), which grades mcpkit against every scenario upstream currently ships.
- **Open Gaps** lists failing scenarios + traceability rows with no emitted check. Tracking links and one-line context come from [`conformance/known-gaps.yaml`](conformance/known-gaps.yaml).

## What this report is *not*

The renderer drops the tier-check checks that depend on live GitHub state (labels, triage SLA, P0 resolution, stable release, policy signals, spec tracking). Those are useful tier-1 signals but they change daily independent of code, which would break the CI staleness gate. To see the full `tier-check` scorecard for a point-in-time tier judgement, run `npx @modelcontextprotocol/conformance tier-check --repo panyam/mcpkit --output markdown` directly.

## Regenerate locally

```
bash scripts/refresh-conformance.sh
```

Needs Node.js 22+ and a clone of `modelcontextprotocol/conformance` at `../conf-upstream-main` (override with `MCPCONFORMANCE_BASE_PATH=...`). Output is deterministic — re-running on unchanged input produces a byte-identical file.

---

<!-- begin:generated -->
<!-- generated against upstream-conformance@bcfd400 · protocol 2025-11-25 · regenerate via scripts/refresh-conformance.sh -->

## Conformance Summary

| Surface | Scenarios pass/total | Checks pass/fail |
|---|---:|---:|
| Server | 46/47 | 92/1 |
| Client | 0/0 | 0/0 |

## SEP Coverage

| SEP | Tested reqs | Excluded | Untested | Status |
|---|---:|---:|---:|---|
| [SEP-837](https://modelcontextprotocol.io/specification/draft/basic/authorization#application-type-and-redirect-uri-constraints) | 1 | 4 | 0 | **pass** |
| [SEP-2106](https://modelcontextprotocol.io/seps/2106-json-schema-2020-12#security-implications) | 1 | 4 | 0 | **pass** |
| [SEP-2164](https://modelcontextprotocol.io/specification/draft/server/resources#error-handling) | 2 | 1 | 0 | **pass** |
| [SEP-2207](https://modelcontextprotocol.io/specification/draft/basic/authorization#refresh-tokens) | 1 | 3 | 0 | **pass** |
| [SEP-2243](https://modelcontextprotocol.io/specification/draft/basic/transports#standard-mcp-request-headers) | 18 | 4 | 2 | partial |
| [SEP-2260](https://modelcontextprotocol.io/specification/draft/basic/transports#streamable-http) | 0 | 12 | 0 | _untested_ |
| [SEP-2322](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) | 17 | 16 | 0 | **pass** |
| [SEP-2350](https://modelcontextprotocol.io/specification/draft/basic/authorization#runtime-insufficient-scope-errors) | 1 | 2 | 0 | **pass** |
| [SEP-2352](https://modelcontextprotocol.io/specification/draft/basic/authorization#authorization-server-binding) | 3 | 3 | 0 | **pass** |
| [SEP-2468](https://modelcontextprotocol.io/specification/draft/basic/authorization#authorization-response-validation) | 6 | 3 | 0 | **pass** |
| [SEP-2549](https://modelcontextprotocol.io/specification/draft/server/utilities/caching) | 7 | 13 | 0 | **pass** |
| [SEP-2575](https://modelcontextprotocol.io/specification/draft/basic/lifecycle) | 22 | 13 | 0 | **pass** |

_Status reflects upstream-declared requirements only. Scenario→SEP attribution is not exposed in tier-check JSON today; this column tracks "does upstream have a check ID for this SEP requirement", not "does mcpkit pass it". Per-SEP scenario pass/fail lives in `conformance/UPSTREAM_AUDIT.md`._

## Open Gaps

### Failing scenarios

| Scenario | Surface | Checks fail/pass | Tracking |
|---|---|---:|---|
| `server-stateless` | server | 1/19 | — |

### Declared requirements with no emitted check

| SEP | Check ID | Tracking |
|---|---|---|
| SEP-2243 | `sep-2243-server-not-expect-null` | MCP-Name fail-closed semantics on tasks/* — covered locally by server/middleware test |
| SEP-2243 | `sep-2243-server-reject-missing-required` | MCP-Name fail-closed semantics on tasks/* — covered locally by server/middleware test |
<!-- end:generated -->
