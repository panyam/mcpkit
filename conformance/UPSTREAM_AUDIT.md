# Upstream Conformance Audit

Snapshot of mcpkit graded against `modelcontextprotocol/conformance@49103de` — *feat(sdk-runner): add rust-sdk + per-spec-version config overlays (#419)*.

**mcpkit HEAD:** `49103de`  
**Driver:** `cmd/testserver` (server scenarios) + `cmd/testclient` (client scenarios). SEP-2663 `tasks-*` server scenarios are graded against `examples/tasks-v2` instead, which wires `ext/tasks` in its own module (keeping the root module free of that dependency) — mirroring how `testconf-stateless` uses `examples/stateless`.

Informational report — not a CI gate. Regenerate via `just testconf-upstream-audit`.

Status legend: **pass** = no FAILURE checks · **partial** = at least one SUCCESS and one FAILURE · **fail** = all checks FAILURE · **harness-gap** = no `checks.json` produced (driver missing) · **fork-covered** = same surface graded by an existing `testconf-*` SEP fork target.

## Summary

| Surface | Scenarios | Checks | Pass | Fail | Warn | Info | Skipped | Harness-gap |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Server | 61 | 231 | 197 | 19 | 8 | 6 | 1 | 0 |
| Client | 43 | 1536 | 536 | 8 | 0 | 990 | 2 | 0 |
| **Total** | **104** | **1767** | **733** | **27** | **8** | **996** | **3** | **0** |

## Harness gaps

_None — every scenario produced results._

## By SEP

### Core / Unattributed (53 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/2025-03-26-oauth-endpoint-fallback` | client | pass | 11 pass / 36 info |  |
| `auth/2025-03-26-oauth-metadata-backcompat` | client | pass | 16 pass / 36 info |  |
| `auth/authorization-server-migration` | client | pass | 31 pass / 48 info |  |
| `auth/basic-cimd` | client | pass | 17 pass / 32 info |  |
| `auth/iss-normalized` | client | pass | 8 pass / 14 info |  |
| `auth/iss-not-advertised` | client | pass | 12 pass / 18 info |  |
| `auth/iss-supported` | client | pass | 12 pass / 18 info |  |
| `auth/iss-supported-missing` | client | pass | 8 pass / 14 info |  |
| `auth/iss-unexpected` | client | pass | 8 pass / 14 info |  |
| `auth/iss-wrong-issuer` | client | pass | 8 pass / 14 info |  |
| `auth/metadata-default` | client | pass | 17 pass / 34 info |  |
| `auth/metadata-issuer-mismatch` | client | pass | 3 pass / 10 info |  |
| `auth/metadata-var1` | client | pass | 17 pass / 36 info |  |
| `auth/metadata-var2` | client | pass | 17 pass / 36 info |  |
| `auth/metadata-var3` | client | pass | 17 pass / 36 info |  |
| `auth/pre-registration` | client | pass | 19 pass / 38 info |  |
| `auth/resource-mismatch` | client | pass | 2 pass / 8 info |  |
| `auth/scope-from-scopes-supported` | client | pass | 18 pass / 34 info |  |
| `auth/scope-from-www-authenticate` | client | pass | 18 pass / 34 info |  |
| `auth/scope-omitted-when-undefined` | client | pass | 18 pass / 34 info |  |
| `auth/scope-retry-limit` | client | pass | 16 pass / 38 info |  |
| `auth/scope-step-up` | client | pass | 24 pass / 46 info |  |
| `auth/token-endpoint-auth-basic` | client | pass | 22 pass / 34 info |  |
| `auth/token-endpoint-auth-none` | client | pass | 22 pass / 34 info |  |
| `auth/token-endpoint-auth-post` | client | pass | 22 pass / 34 info |  |
| `completion-complete` | server | pass | 2 pass |  |
| `dns-rebinding-protection` | server | pass | 2 pass |  |
| `initialize` | client | pass | 1 pass / 1 info |  |
| `logging-set-level` | server | pass | 2 pass |  |
| `ping` | server | pass | 2 pass |  |
| `prompts-get-embedded-resource` | server | pass | 2 pass |  |
| `prompts-get-simple` | server | pass | 2 pass |  |
| `prompts-get-with-args` | server | pass | 2 pass |  |
| `prompts-get-with-image` | server | pass | 2 pass |  |
| `prompts-list` | server | pass | 2 pass |  |
| `resources-list` | server | pass | 2 pass |  |
| `resources-read-binary` | server | pass | 2 pass |  |
| `resources-read-text` | server | pass | 2 pass |  |
| `resources-subscribe` | server | pass | 2 pass |  |
| `resources-templates-read` | server | pass | 2 pass |  |
| `resources-unsubscribe` | server | pass | 2 pass |  |
| `server-initialize` | server | pass | 3 pass |  |
| `tools_call` | client | pass | 2 pass |  |
| `tools-call-audio` | server | pass | 2 pass |  |
| `tools-call-elicitation` | server | pass | 2 pass |  |
| `tools-call-embedded-resource` | server | pass | 2 pass |  |
| `tools-call-error` | server | pass | 2 pass |  |
| `tools-call-image` | server | pass | 2 pass |  |
| `tools-call-mixed-content` | server | pass | 2 pass |  |
| `tools-call-sampling` | server | pass | 2 pass |  |
| `tools-call-simple-text` | server | pass | 2 pass |  |
| `tools-call-with-logging` | server | pass | 2 pass |  |
| `tools-call-with-progress` | server | pass | 2 pass |  |

### [SEP-986](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/986) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `tools-list` | server | pass | 3 pass |  |

### [SEP-990-ENTERPRISE-MANAGED-OAUTH](https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/enterprise-managed-authorization.mdx) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/enterprise-managed-authorization` | client | pass | 13 pass / 28 info |  |

### [SEP-1034](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1034) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `elicitation-sep1034-client-defaults` | client | pass | 5 pass / 20 info |  |
| `elicitation-sep1034-defaults` | server | pass | 6 pass |  |

### [SEP-1046-CLIENT-CREDENTIALS](https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/oauth-client-credentials.mdx) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/client-credentials-basic` | client | pass | 14 pass / 34 info |  |
| `auth/client-credentials-jwt` | client | pass | 12 pass / 28 info |  |

### [SEP-1330](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1330) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `elicitation-sep1330-enums` | server | pass | 6 pass |  |

### [SEP-1613](https://github.com/modelcontextprotocol/specification/pull/655) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `json-schema-2020-12` | server | pass | 8 pass |  |

### [SEP-1699](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1699) (3 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `server-sse-multiple-streams` | server | pass | 2 pass |  |
| `server-sse-polling` | server | pass | 3 warn / 6 info |  |
| `sse-retry` | client | pass | 3 pass / 17 info |  |

### [SEP-1932-DPOP](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/1932) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/dpop` | client | partial | 9 pass / 3 fail / 34 info |  |
| `auth/dpop-nonce` | client | partial | 9 pass / 5 fail / 34 info |  |

### [SEP-1933-WORKLOAD-IDENTITY-FEDERATION](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/1933) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/wif-jwt-bearer` | client | pass | 12 pass / 28 info |  |

### [SEP-2106](https://modelcontextprotocol.io/seps/2106-json-schema-2020-12#security-implications) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `json-schema-ref-no-deref` | client | pass | 1 pass |  |

### [SEP-2164](https://modelcontextprotocol.io/specification/draft/server/resources#error-handling) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `sep-2164-resource-not-found` | server | pass | 3 pass / 1 warn |  |

### [SEP-2207-REFRESH-TOKEN-GUIDANCE](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2207) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/offline-access-not-supported` | client | pass | 12 pass / 18 info |  |
| `auth/offline-access-scope` | client | pass | 9 pass / 18 info |  |

### [SEP-2243-CUSTOM-HEADERS](https://modelcontextprotocol.io/specification/draft/basic/transports#server-behavior-for-custom-headers) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-custom-header-server-validation` | server | partial | 1 pass / 5 fail |  |
| `http-custom-headers` | client | pass | 18 pass |  |

### [SEP-2243-SERVER-VALIDATION](https://modelcontextprotocol.io/specification/draft/basic/transports#server-validation) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-header-validation` | server | partial | 12 pass / 1 fail / 1 warn |  |

### [SEP-2243](https://modelcontextprotocol.io/seps/2243-http-standardization) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `tasks-request-headers` | server | partial | 4 pass / 1 fail |  |

### [SEP-2243-X-MCP-HEADER](https://modelcontextprotocol.io/specification/draft/server/tools#x-mcp-header) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-invalid-tool-headers` | client | pass | 11 pass |  |

### [SEP-2243-STANDARD-HEADERS](https://modelcontextprotocol.io/specification/draft/basic/transports#standard-mcp-request-headers) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-standard-headers` | client | pass | 11 pass |  |

### [SEP-2322](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) (16 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `tasks-mrtr-composition` | server | partial | 1 pass / 1 fail |  |
| `tasks-mrtr-input` | server | partial | 3 pass / 1 fail |  |
| `input-required-result-basic-elicitation` | server | pass | 3 pass |  |
| `input-required-result-basic-list-roots` | server | pass | 3 pass |  |
| `input-required-result-basic-sampling` | server | pass | 3 pass |  |
| `input-required-result-capability-check` | server | pass | 2 pass |  |
| `input-required-result-ignore-extra-params` | server | pass | 2 pass |  |
| `input-required-result-missing-input-response` | server | pass | 2 pass |  |
| `input-required-result-multi-round` | server | pass | 4 pass |  |
| `input-required-result-multiple-input-requests` | server | pass | 3 pass |  |
| `input-required-result-non-tool-request` | server | pass | 3 pass |  |
| `input-required-result-request-state` | server | pass | 3 pass |  |
| `input-required-result-result-type` | server | pass | 2 pass |  |
| `input-required-result-tampered-state` | server | pass | 2 pass |  |
| `input-required-result-unsupported-methods` | server | pass | 2 pass |  |
| `input-required-result-validate-input` | server | pass | 2 pass / 1 warn |  |

### [SEP-2322-MRTR](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `sep-2322-client-request-state` | client | pass | 5 pass |  |

### [SEP-2549](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `caching` | server | pass | 8 pass |  |

### [SEP-2575](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2575) (3 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `server-stateless` | server | fork-covered | 23 pass / 5 fail / 2 warn | Also graded by `testconf-stateless` |
| `request-metadata` | client | pass | 6 pass / 2 skip |  |
| `tasks-required-task-error` | server | pass | 3 pass |  |

### [SEP-2663](https://modelcontextprotocol.io/seps/2663-tasks-extension) (6 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `tasks-capability-negotiation` | server | partial | 4 pass / 1 fail |  |
| `tasks-dispatch-and-envelope` | server | partial | 8 pass / 1 fail |  |
| `tasks-lifecycle` | server | partial | 8 pass / 1 fail |  |
| `tasks-request-state-removal` | server | partial | 2 pass / 1 fail |  |
| `tasks-wire-fields` | server | partial | 3 pass / 1 fail |  |
| `tasks-status-notifications` | server | pass | 1 skip |  |


## Methodology

- `just testconf-upstream-audit` spawns `cmd/testserver` (Streamable HTTP on port 18099), builds `cmd/testclient`, then drives upstream's CLI: `node dist/index.js server --url ... --suite all` once, and `... client --command ... --scenario <name>` per scenario in a loop (sequentially — upstream's parallel `--suite all` mode is flaky on the client side). The `tasks-*` server scenarios are then re-graded against `examples/tasks-v2` on a second port (port 18101), replacing the bulk-sweep results.
- Upstream's CLI writes one `<scenario>/checks.json` per scenario; this report aggregates by `specReferences[]` (first matching `SEP-NNNN` wins as primary group).
- Scenarios with no `checks.json` are tagged `harness-gap` — they require driver work in `cmd/testclient` (or a dedicated client harness) before the upstream runner can invoke them.
- `also-covered-by-fork` is hand-maintained in `scripts/conformance-audit-report.ts` (`FORK_OVERLAP` map). Update there as SEP-fork targets land coverage.
- Raw per-check JSON lives in `${AUDIT_OUT:-/tmp/conf-audit}/` — inspect there for failure details beyond the first 100 chars shown above.
