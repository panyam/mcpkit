<!-- begin:generated -->
<!-- generated against upstream-conformance@abc1234567890abcdef1234567890abcdef123456 · protocol 2025-11-25 · regenerate via scripts/refresh-conformance.sh -->

## Conformance Summary

| Surface | Scenarios pass/total | Checks pass/fail |
|---|---:|---:|
| Server | 3/4 | 14/2 |
| Client | 1/1 | 14/0 |

## mcpkit-local Conformance Suites

These suites exercise SEP-specific behavior beyond what upstream's tier-check covers. Each is wired into `make testall` as a separate stage and may show as PASS, FAIL, INFO (informational, not gating), or SKIP. INFO typically means "work in flight" — see the Tracking column. The Source column links to the branch the scenarios live on; per-suite env vars and default checkout paths are listed below the table.

| Suite | Covers | Stage | Status | Source | Tracking |
|---|---|:---:|:---:|---|---|
| `testconf-example-pass` | Example PASS SEP | 8a | **PASS** | [`example/conformance-fork@feat/pass-suite`](https://github.com/example/conformance-fork/tree/feat/pass-suite) | — |
| `testconf-example-info` | Example INFO SEP | 8b | _INFO_<sup>1</sup> | [`example/conformance-fork@pending`](https://github.com/example/conformance-fork/tree/pending) | mcpkit 999 |
| `testconf-example-skip` | Example SKIP SEP | - | _SKIP_<sup>2</sup> | — | — |

<sup>1</sup> Fixture-side note for the INFO row.
<sup>2</sup> Intentionally not run in this revision.

### Setup — clone the right worktree per suite

Each suite's Makefile target reads `MCPCONFORMANCE_*_PATH` to find its scenario worktree. Defaults assume sibling clones of the source repo at the relative path shown. Override per-invocation when the worktree lives elsewhere.

| Suite | Env var | Default path | Clone command |
|---|---|---|---|
| `testconf-example-pass` | `MCPCONFORMANCE_PASS_PATH` | `../conf-pass` | `git clone -b feat/pass-suite https://github.com/example/conformance-fork.git ../conf-pass` |
| `testconf-example-info` | `MCPCONFORMANCE_INFO_PATH` | `../conf-pending-example` | `git clone -b pending https://github.com/example/conformance-fork.git ../conf-pending-example` |

## SEP Coverage

| SEP | Tested reqs | Excluded | Untested | Status |
|---|---:|---:|---:|---|
| [SEP-2322](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) | [1](#sep-2322-tested "Tested: sep-2322-result-type-included.") | [2](#sep-2322-excluded "1x Server-internal opaque blob — only protocol-observable invar; 1x Not protocol-observable post-parsing.") | [1](#sep-2322-untested "Untested: sep-2322-input-required-on-allowed-method.") | partial |
| [SEP-2549](https://modelcontextprotocol.io/seps/2549-list-ttl) | [1](#sep-2549-tested "Tested: sep-2549-ttl-ms-respected.") | 0 | 0 | **pass** |

_Numeric cells link to per-SEP detail below; hover/long-press surfaces a one-line summary. Status reflects upstream-declared requirements only — Scenario→SEP attribution is not exposed in tier-check JSON today; this column tracks "does upstream have a check ID for this SEP requirement", not "does mcpkit pass it". Per-SEP scenario pass/fail lives in `conformance/UPSTREAM_AUDIT.md`._

## SEP Detail

Per-SEP breakdown of upstream traceability — what is exercised, what is intentionally excluded, and what is declared but not yet exercised. Useful for auditing whether each exclusion still makes sense as upstream evolves. Check IDs link to their definition in the upstream SEP YAML.

### SEP-2322

<a id="sep-2322-tested"></a>

**Tested (1)**

- [`sep-2322-result-type-included`](https://github.com/modelcontextprotocol/conformance/blob/abc1234567890abcdef1234567890abcdef123456/src/seps/sep-2322.yaml)

<a id="sep-2322-excluded"></a>

**Excluded (2)**

| Requirement | Upstream reason |
|---|---|
| Internal requestState format; not observable at protocol level | Server-internal opaque blob — only protocol-observable invariant is round-trip equality, exercised elsewhere. |
| inputRequests is a JSON object; duplicate keys are collapsed by JSON parsing before the harness sees the request. | Not protocol-observable post-parsing. (modelcontextprotocol/conformance#999) |

<a id="sep-2322-untested"></a>

**Untested (1)**

- [`sep-2322-input-required-on-allowed-method`](https://github.com/modelcontextprotocol/conformance/blob/abc1234567890abcdef1234567890abcdef123456/src/seps/sep-2322.yaml) — Servers MUST only send InputRequiredResult for tools/call, prompts/get, or completion/complete.

### SEP-2549

<a id="sep-2549-tested"></a>

**Tested (1)**

- [`sep-2549-ttl-ms-respected`](https://github.com/modelcontextprotocol/conformance/blob/abc1234567890abcdef1234567890abcdef123456/src/seps/sep-2549.yaml)

<a id="sep-2549-excluded"></a>

**Excluded (0)**

_None._

<a id="sep-2549-untested"></a>

**Untested (0)**

_None._


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
