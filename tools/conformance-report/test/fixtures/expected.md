<!-- begin:generated -->
<!-- generated against upstream-conformance@abc1234567890abcdef1234567890abcdef123456 · protocol 2025-11-25 · regenerate via scripts/refresh-conformance.sh -->

## Conformance Summary

| Surface | Scenarios pass/total | Checks pass/fail |
|---|---:|---:|
| Server | 3/4 | 14/2 |
| Client | 1/1 | 14/0 |

## SEP Coverage

| SEP | Tested reqs | Excluded | Untested | Status |
|---|---:|---:|---:|---|
| [SEP-2322](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) | 1 | 0 | 1 | partial |
| [SEP-2549](https://modelcontextprotocol.io/seps/2549-list-ttl) | 1 | 0 | 0 | **pass** |

_Status reflects upstream-declared requirements only. Scenario→SEP attribution is not exposed in tier-check JSON today; this column tracks "does upstream have a check ID for this SEP requirement", not "does mcpkit pass it". Per-SEP scenario pass/fail lives in `conformance/UPSTREAM_AUDIT.md`._

## Open Gaps

### Failing scenarios

| Scenario | Surface | Checks fail/pass | Tracking |
|---|---|---:|---|
| `mrtr-basic` | server | 2/3 | mcpkit#487 — stateless wire drops inputResponses (gotcha tracked in CLAUDE.md) |

### Declared requirements with no emitted check

| SEP | Check ID | Tracking |
|---|---|---|
| SEP-2322 | `sep-2322-input-required-on-allowed-method` | conformance#262 — scenario coverage pending upstream merge |
<!-- end:generated -->
