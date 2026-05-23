# Upstream Conformance Audit

Snapshot of mcpkit graded against `modelcontextprotocol/conformance@bcfd400` — *chore: bump version to 0.2.0-alpha.0 (#306)*.

**Audit run:** 2026-05-23T16:37:02.005Z  
**mcpkit HEAD:** `bcfd400`  
**Driver:** `cmd/testserver` (server scenarios) + `cmd/testclient` (client scenarios)

Informational report — not a CI gate. Regenerate via `make testconf-upstream-audit`.

Legend: **pass** = no FAILURE checks · **partial** = at least one SUCCESS and one FAILURE · **fail** = all checks FAILURE · **harness-gap** = no `checks.json` produced (driver missing) · **fork-covered** = same surface graded by an existing `testconf-*` SEP fork target. Count suffix: `P`=pass · `F`=fail · `W`=warn · `I`=info · `S`=skipped.

## Summary

| Surface | Scenarios | Checks | Pass | Fail | Warn | Info | Skipped | Harness-gap |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Server | 51 | 120 | 51 | 42 | 12 | 6 | 9 | 0 |
| Client | 40 | 1117 | 390 | 66 | 4 | 649 | 8 | 1 |
| **Total** | **91** | **1237** | **441** | **108** | **16** | **655** | **17** | **1** |

## Harness gaps

- `request-metadata` (client)

## By SEP

### Core / Unattributed (54 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/2025-03-26-oauth-endpoint-fallback` | client | partial | 3F / 8I |  |
| `auth/2025-03-26-oauth-metadata-backcompat` | client | partial | 4F / 8I |  |
| `auth/authorization-server-migration` | client | partial | 14P / 2F / 28I |  |
| `auth/basic-cimd` | client | partial | 13P / 1F / 1W / 22I |  |
| `auth/iss-normalized` | client | partial | 13P / 2F / 22I |  |
| `auth/iss-not-advertised` | client | partial | 14P / 1F / 22I |  |
| `auth/iss-supported` | client | partial | 14P / 1F / 22I |  |
| `auth/iss-supported-missing` | client | partial | 13P / 2F / 22I |  |
| `auth/iss-unexpected` | client | partial | 13P / 2F / 22I |  |
| `auth/iss-wrong-issuer` | client | partial | 13P / 2F / 22I |  |
| `auth/metadata-default` | client | partial | 13P / 1F / 22I |  |
| `auth/metadata-issuer-mismatch` | client | partial | 13P / 2F / 22I |  |
| `auth/metadata-var1` | client | partial | 13P / 1F / 24I |  |
| `auth/metadata-var2` | client | partial | 13P / 1F / 24I |  |
| `auth/metadata-var3` | client | partial | 13P / 1F / 24I |  |
| `auth/resource-mismatch` | client | partial | 13P / 2F / 22I |  |
| `auth/scope-from-scopes-supported` | client | partial | 14P / 1F / 22I |  |
| `auth/scope-from-www-authenticate` | client | partial | 14P / 1F / 22I |  |
| `auth/scope-omitted-when-undefined` | client | partial | 14P / 1F / 22I |  |
| `auth/scope-retry-limit` | client | partial | 16P / 1F / 32I |  |
| `auth/scope-step-up` | client | partial | 18P / 1F / 2W / 30I |  |
| `auth/token-endpoint-auth-basic` | client | partial | 18P / 1F / 22I |  |
| `auth/token-endpoint-auth-none` | client | partial | 18P / 1F / 22I |  |
| `auth/token-endpoint-auth-post` | client | partial | 18P / 1F / 22I |  |
| `initialize` | client | partial | 1F / 1I |  |
| `tools_call` | client | partial | 1F / 6I |  |
| `request-metadata` | client | harness-gap | — | No `checks.json` written — driver does not handle this scenario |
| `auth/pre-registration` | client | pass | 13P / 20I |  |
| `completion-complete` | server | pass | 1P |  |
| `dns-rebinding-protection` | server | pass | 2P |  |
| `logging-set-level` | server | pass | 1P |  |
| `ping` | server | pass | 1P |  |
| `prompts-get-embedded-resource` | server | pass | 1P |  |
| `prompts-get-simple` | server | pass | 1P |  |
| `prompts-get-with-args` | server | pass | 1P |  |
| `prompts-get-with-image` | server | pass | 1P |  |
| `prompts-list` | server | pass | 1P |  |
| `resources-list` | server | pass | 1P |  |
| `resources-read-binary` | server | pass | 1P |  |
| `resources-read-text` | server | pass | 1P |  |
| `resources-subscribe` | server | pass | 1P |  |
| `resources-templates-read` | server | pass | 1P |  |
| `resources-unsubscribe` | server | pass | 1P |  |
| `server-initialize` | server | pass | 2P |  |
| `tools-call-audio` | server | pass | 1P |  |
| `tools-call-elicitation` | server | pass | 1P |  |
| `tools-call-embedded-resource` | server | pass | 1P |  |
| `tools-call-error` | server | pass | 1P |  |
| `tools-call-image` | server | pass | 1P |  |
| `tools-call-mixed-content` | server | pass | 1P |  |
| `tools-call-sampling` | server | pass | 1P |  |
| `tools-call-simple-text` | server | pass | 1P |  |
| `tools-call-with-logging` | server | pass | 1P |  |
| `tools-call-with-progress` | server | pass | 1P |  |

### [SEP-986](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/SEP/SEP-986.md) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `tools-list` | server | pass | 2P |  |

### [SEP-990-ENTERPRISE-MANAGED-OAUTH](https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/enterprise-oauth.mdx) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/enterprise-managed-authorization` | client | partial | 8P / 2F / 12I |  |

### [SEP-1034](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1034) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `elicitation-sep1034-client-defaults` | client | partial | 5F / 6I |  |
| `elicitation-sep1034-defaults` | server | pass | 5P |  |

### [SEP-1046-CLIENT-CREDENTIALS](https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/oauth-client-credentials.mdx) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/client-credentials-basic` | client | partial | 8P / 2F / 12I |  |
| `auth/client-credentials-jwt` | client | partial | 8P / 2F / 12I |  |

### [SEP-1330](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1330) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `elicitation-sep1330-enums` | server | pass | 5P |  |

### [SEP-1613](https://github.com/modelcontextprotocol/specification/pull/655) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `json-schema-2020-12` | server | fail | 1F | `Tool 'json_schema_2020_12_tool' not found. Available tools: echo, add, fail, test_simple_text, test_…` |

### [SEP-1699](https://github.com/modelcontextprotocol/modelcontextprotocol/issues/1699) (3 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `sse-retry` | client | partial | 1F / 5I |  |
| `server-sse-multiple-streams` | server | pass | 2P |  |
| `server-sse-polling` | server | pass | 3W / 6I |  |

### [SEP-2106](https://modelcontextprotocol.io/seps/2106-json-schema-2020-12#security-implications) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `json-schema-ref-no-deref` | client | pass | 1P |  |

### [SEP-2164](https://modelcontextprotocol.io/specification/draft/server/resources#error-handling) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `sep-2164-resource-not-found` | server | pass | 2P / 1W |  |

### [SEP-2207-REFRESH-TOKEN-GUIDANCE](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2207) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `auth/offline-access-not-supported` | client | partial | 14P / 1F / 22I |  |
| `auth/offline-access-scope` | client | partial | 13P / 1F / 1W / 23I |  |

### [SEP-2243-CUSTOM-HEADERS](https://modelcontextprotocol.io/specification/draft/basic/transports#server-behavior-for-custom-headers) (2 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-custom-headers` | client | fail | 5F | `Client did not send a tools/call request for test_custom_headers.` |
| `http-custom-header-server-validation` | server | pass | 5S |  |

### [SEP-2243-SERVER-VALIDATION](https://modelcontextprotocol.io/specification/draft/basic/transports#server-validation) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-header-validation` | server | partial | 5P / 3F / 5W |  |

### [SEP-2243-X-MCP-HEADER](https://modelcontextprotocol.io/specification/draft/server/tools#x-mcp-header) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-invalid-tool-headers` | client | partial | 10P / 1F |  |

### [SEP-2243-STANDARD-HEADERS](https://modelcontextprotocol.io/specification/draft/basic/transports#standard-mcp-request-headers) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `http-standard-headers` | client | partial | 3F / 8S |  |

### [SEP-2322](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) (14 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `input-required-result-basic-elicitation` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-basic-list-roots` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-basic-sampling` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-capability-check` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-multi-round` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-multiple-input-requests` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-non-tool-request` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-request-state` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-result-type` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-tampered-state` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-unsupported-methods` | server | fail | 1F | `Failed: Unexpected token 'm', "missing Mc"... is not valid JSON` |
| `input-required-result-ignore-extra-params` | server | pass | 1W |  |
| `input-required-result-missing-input-response` | server | pass | 1W |  |
| `input-required-result-validate-input` | server | pass | 1W |  |

### [SEP-2322-MRTR](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `sep-2322-client-request-state` | client | fail | 5F |  |

### [SEP-2549](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2549) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `caching` | server | fork-covered | 7F | Also graded by `testconf-list-ttl` |

### [SEP-2575](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2575) (1 scenarios)

| Scenario | Surface | Status | Checks | Note |
|---|---|---|---|---|
| `server-stateless` | server | partial | 2P / 20F / 4S |  |


## Methodology

- `make testconf-upstream-audit` spawns `cmd/testserver` (Streamable HTTP on port 18099), builds `cmd/testclient`, then drives upstream's CLI: `node dist/index.js server --url ... --suite all` and `... client --command ... --suite all`.
- Upstream's CLI writes one `<scenario>/checks.json` per scenario; this report aggregates by `specReferences[]` (first matching `SEP-NNNN` wins as primary group).
- Scenarios with no `checks.json` are tagged `harness-gap` — they require driver work in `cmd/testclient` (or a dedicated client harness) before the upstream runner can invoke them.
- `also-covered-by-fork` is hand-maintained in `scripts/conformance-audit-report.ts` (`FORK_OVERLAP` map). Update there as SEP-fork targets land coverage.
- Raw per-check JSON lives in `${AUDIT_OUT:-/tmp/conf-audit}/` — inspect there for failure details beyond the first 100 chars shown above.
