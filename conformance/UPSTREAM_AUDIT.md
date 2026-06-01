# Upstream Conformance Audit

Snapshot of mcpkit graded against `modelcontextprotocol/conformance@bcfd400` — *chore: bump version to 0.2.0-alpha.0 (#306)*.

**mcpkit HEAD:** `bcfd400`  
**Driver:** `cmd/testserver` (server scenarios) + `cmd/testclient` (client scenarios)

Informational report — not a CI gate. Regenerate via `make testconf-upstream-audit`.

Status legend: **pass** = no FAILURE checks · **partial** = at least one SUCCESS and one FAILURE · **fail** = all checks FAILURE · **harness-gap** = no `checks.json` produced (driver missing) · **fork-covered** = same surface graded by an existing `testconf-*` SEP fork target.

## Summary

| Surface | Scenarios | Checks | Pass | Fail | Warn | Info | Skipped | Harness-gap |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Server | 51 | 135 | 112 | 1 | 7 | 6 | 9 | 0 |
| Client | 40 | 1128 | 422 | 7 | 4 | 689 | 6 | 1 |
| **Total** | **91** | **1263** | **534** | **8** | **11** | **695** | **15** | **1** |

## Harness gaps

- `request-metadata` (client)

## By SEP

### Core / Unattributed (54 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/2025-03-26-oauth-endpoint-fallback` | client | partial | 3 fail / 8 info |  |
| `auth/2025-03-26-oauth-metadata-backcompat` | client | partial | 4 fail / 8 info |  |
| `request-metadata` | client | harness-gap | — | No `checks.json` written — driver does not handle this scenario |
| `auth/authorization-server-migration` | client | pass | 26 pass / 44 info |  |
| `auth/basic-cimd` | client | pass | 14 pass / 1 warn / 24 info |  |
| `auth/iss-normalized` | client | pass | 8 pass / 12 info |  |
| `auth/iss-not-advertised` | client | pass | 15 pass / 24 info |  |
| `auth/iss-supported` | client | pass | 15 pass / 24 info |  |
| `auth/iss-supported-missing` | client | pass | 8 pass / 12 info |  |
| `auth/iss-unexpected` | client | pass | 8 pass / 12 info |  |
| `auth/iss-wrong-issuer` | client | pass | 8 pass / 12 info |  |
| `auth/metadata-default` | client | pass | 14 pass / 24 info |  |
| `auth/metadata-issuer-mismatch` | client | pass | 3 pass / 8 info |  |
| `auth/metadata-var1` | client | pass | 14 pass / 26 info |  |
| `auth/metadata-var2` | client | pass | 14 pass / 26 info |  |
| `auth/metadata-var3` | client | pass | 14 pass / 26 info |  |
| `auth/pre-registration` | client | pass | 15 pass / 28 info |  |
| `auth/resource-mismatch` | client | pass | 2 pass / 6 info |  |
| `auth/scope-from-scopes-supported` | client | pass | 15 pass / 24 info |  |
| `auth/scope-from-www-authenticate` | client | pass | 15 pass / 24 info |  |
| `auth/scope-omitted-when-undefined` | client | pass | 15 pass / 24 info |  |
| `auth/scope-retry-limit` | client | pass | 17 pass / 36 info |  |
| `auth/scope-step-up` | client | pass | 19 pass / 2 warn / 34 info |  |
| `auth/token-endpoint-auth-basic` | client | pass | 19 pass / 24 info |  |
| `auth/token-endpoint-auth-none` | client | pass | 19 pass / 24 info |  |
| `auth/token-endpoint-auth-post` | client | pass | 19 pass / 24 info |  |
| `completion-complete` | server | pass | 1 pass |  |
| `dns-rebinding-protection` | server | pass | 2 pass |  |
| `initialize` | client | pass | 1 pass / 1 info |  |
| `logging-set-level` | server | pass | 1 pass |  |
| `ping` | server | pass | 1 pass |  |
| `prompts-get-embedded-resource` | server | pass | 1 pass |  |
| `prompts-get-simple` | server | pass | 1 pass |  |
| `prompts-get-with-args` | server | pass | 1 pass |  |
| `prompts-get-with-image` | server | pass | 1 pass |  |
| `prompts-list` | server | pass | 1 pass |  |
| `resources-list` | server | pass | 1 pass |  |
| `resources-read-binary` | server | pass | 1 pass |  |
| `resources-read-text` | server | pass | 1 pass |  |
| `resources-subscribe` | server | pass | 1 pass |  |
| `resources-templates-read` | server | pass | 1 pass |  |
| `resources-unsubscribe` | server | pass | 1 pass |  |
| `server-initialize` | server | pass | 2 pass |  |
| `tools_call` | client | pass | 1 pass / 10 info |  |
| `tools-call-audio` | server | pass | 1 pass |  |
| `tools-call-elicitation` | server | pass | 1 pass |  |
| `tools-call-embedded-resource` | server | pass | 1 pass |  |
| `tools-call-error` | server | pass | 1 pass |  |
| `tools-call-image` | server | pass | 1 pass |  |
| `tools-call-mixed-content` | server | pass | 1 pass |  |
| `tools-call-sampling` | server | pass | 1 pass |  |
| `tools-call-simple-text` | server | pass | 1 pass |  |
| `tools-call-with-logging` | server | pass | 1 pass |  |
| `tools-call-with-progress` | server | pass | 1 pass |  |

### [SEP-986](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/986) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `tools-list` | server | pass | 2 pass |  |

### [SEP-990-ENTERPRISE-MANAGED-OAUTH](https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/enterprise-managed-authorization.mdx) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/enterprise-managed-authorization` | client | pass | 9 pass / 20 info |  |

### [SEP-1034](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1034) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `elicitation-sep1034-client-defaults` | client | pass | 5 pass / 12 info |  |
| `elicitation-sep1034-defaults` | server | pass | 5 pass |  |

### [SEP-1046-CLIENT-CREDENTIALS](https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/oauth-client-credentials.mdx) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/client-credentials-basic` | client | pass | 10 pass / 26 info |  |
| `auth/client-credentials-jwt` | client | pass | 8 pass / 20 info |  |

### [SEP-1330](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1330) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `elicitation-sep1330-enums` | server | pass | 5 pass |  |

### [SEP-1613](https://github.com/modelcontextprotocol/specification/pull/655) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `json-schema-2020-12` | server | pass | 7 pass |  |

### [SEP-1699](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1699) (3 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `server-sse-multiple-streams` | server | pass | 2 pass |  |
| `server-sse-polling` | server | pass | 3 warn / 6 info |  |
| `sse-retry` | client | pass | 3 pass / 13 info |  |

### [SEP-2106](https://modelcontextprotocol.io/seps/2106-json-schema-2020-12#security-implications) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `json-schema-ref-no-deref` | client | pass | 1 pass |  |

### [SEP-2164](https://modelcontextprotocol.io/specification/draft/server/resources#error-handling) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `sep-2164-resource-not-found` | server | pass | 2 pass / 1 warn |  |

### [SEP-2207-REFRESH-TOKEN-GUIDANCE](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2207) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/offline-access-not-supported` | client | pass | 15 pass / 24 info |  |
| `auth/offline-access-scope` | client | pass | 14 pass / 1 warn / 25 info |  |

### [SEP-2243-CUSTOM-HEADERS](https://modelcontextprotocol.io/specification/draft/basic/transports#server-behavior-for-custom-headers) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-custom-header-server-validation` | server | pass | 5 skip |  |
| `http-custom-headers` | client | pass | 18 pass |  |

### [SEP-2243-SERVER-VALIDATION](https://modelcontextprotocol.io/specification/draft/basic/transports#server-validation) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-header-validation` | server | pass | 13 pass |  |

### [SEP-2243-X-MCP-HEADER](https://modelcontextprotocol.io/specification/draft/server/tools#x-mcp-header) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-invalid-tool-headers` | client | pass | 11 pass |  |

### [SEP-2243-STANDARD-HEADERS](https://modelcontextprotocol.io/specification/draft/basic/transports#standard-mcp-request-headers) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-standard-headers` | client | pass | 5 pass / 6 skip |  |

### [SEP-2322](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) (14 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `input-required-result-basic-elicitation` | server | pass | 2 pass |  |
| `input-required-result-basic-list-roots` | server | pass | 2 pass |  |
| `input-required-result-basic-sampling` | server | pass | 2 pass |  |
| `input-required-result-capability-check` | server | pass | 1 pass |  |
| `input-required-result-ignore-extra-params` | server | pass | 1 pass |  |
| `input-required-result-missing-input-response` | server | pass | 1 pass |  |
| `input-required-result-multi-round` | server | pass | 3 pass |  |
| `input-required-result-multiple-input-requests` | server | pass | 2 pass |  |
| `input-required-result-non-tool-request` | server | pass | 2 pass |  |
| `input-required-result-request-state` | server | pass | 2 pass |  |
| `input-required-result-result-type` | server | pass | 1 pass |  |
| `input-required-result-tampered-state` | server | pass | 1 pass |  |
| `input-required-result-unsupported-methods` | server | pass | 1 pass |  |
| `input-required-result-validate-input` | server | pass | 1 pass / 1 warn |  |

### [SEP-2322-MRTR](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `sep-2322-client-request-state` | client | pass | 5 pass |  |

### [SEP-2549](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `caching` | server | pass | 7 pass |  |

### [SEP-2575](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2575) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `server-stateless` | server | fork-covered | 19 pass / 1 fail / 2 warn / 4 skip | Also graded by `testconf-stateless` |


## Methodology

- `make testconf-upstream-audit` spawns `cmd/testserver` (Streamable HTTP on port 18099), builds `cmd/testclient`, then drives upstream's CLI: `node dist/index.js server --url ... --suite all` once, and `... client --command ... --scenario <name>` per scenario in a loop (sequentially — upstream's parallel `--suite all` mode is flaky on the client side).
- Upstream's CLI writes one `<scenario>/checks.json` per scenario; this report aggregates by `specReferences[]` (first matching `SEP-NNNN` wins as primary group).
- Scenarios with no `checks.json` are tagged `harness-gap` — they require driver work in `cmd/testclient` (or a dedicated client harness) before the upstream runner can invoke them.
- `also-covered-by-fork` is hand-maintained in `scripts/conformance-audit-report.ts` (`FORK_OVERLAP` map). Update there as SEP-fork targets land coverage.
- Raw per-check JSON lives in `${AUDIT_OUT:-/tmp/conf-audit}/` — inspect there for failure details beyond the first 100 chars shown above.
