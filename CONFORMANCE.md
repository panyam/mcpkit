# MCP Conformance

mcpkit's posture against the [MCP conformance suite](https://github.com/modelcontextprotocol/conformance) and the per-SEP traceability manifest. Refreshed before each tagged release; CI keeps the rendering in sync on PRs.

This file has two parts:

- **Overview** (hand-edited, preserved across regenerations) — what the report covers, what it does *not* cover, and how to read it.
- **Generated block** below the begin-marker — rebuilt from `npx @modelcontextprotocol/conformance tier-check --output json` + `src/seps/traceability.json`. Do not hand-edit; changes are overwritten by `scripts/refresh-conformance.sh`.

## How to read this

- **Conformance Summary** counts wire-level scenarios from the upstream conformance suite running against `cmd/testserver`. Aggregate pass/total at the scenario level + check-level pass/fail totals.
- **SEP Coverage** is sourced from upstream's traceability manifest, which maps each SEP to its declared requirements. A row's `Status` says whether upstream has emitted a check ID for every declared requirement — *not* whether mcpkit passes them. Scenario-level pass/fail per SEP lives in [`conformance/UPSTREAM_AUDIT.md`](conformance/UPSTREAM_AUDIT.md), which grades mcpkit against every scenario upstream currently ships.
- **Open Gaps** lists failing scenarios + traceability rows with no emitted check. Tracking links and one-line context come from [`conformance/known-gaps.yaml`](conformance/known-gaps.yaml).

Every report on this site grades an mcpkit **server**. For the inverse — the mcpkit **client** graded by an independent third-party gauntlet on the integrated stateless draft wire (`2026-07-28`) — see [`conformance/EXTERNAL_CHECKER.md`](conformance/EXTERNAL_CHECKER.md), regenerated via `just testconf-external-checker`.

## What this report is *not*

The renderer drops the tier-check checks that depend on live GitHub state (labels, triage SLA, P0 resolution, stable release, policy signals, spec tracking). Those are useful tier-1 signals but they change daily independent of code, which would break the CI staleness gate. To see the full `tier-check` scorecard for a point-in-time tier judgement, run `npx @modelcontextprotocol/conformance tier-check --repo panyam/mcpkit --output markdown` directly.

## Regenerate locally

```
bash scripts/refresh-conformance.sh
```

Needs Node.js 22+ and a clone of `modelcontextprotocol/conformance` at `../conf-upstream-main` (override with `MCPCONFORMANCE_BASE_PATH=...`). Output is deterministic — re-running on unchanged input produces a byte-identical file.

---

<!-- begin:generated -->
<!-- generated against upstream-conformance@da56f6631c83bec2b3b7e40a82f392ae360b790c · protocol 2025-11-25 · regenerate via scripts/refresh-conformance.sh -->

## Conformance Summary

| Surface | Scenarios pass/total | Checks pass/fail |
|---|---:|---:|
| Server | 30/30 | 42/0 |
| Client | 41/43 | 535/8 |

## mcpkit-local Conformance Suites

These suites exercise SEP-specific behavior beyond what upstream's tier-check covers. Each is wired into `just testall` as a separate stage and may show as PASS, FAIL, INFO (informational, not gating), or SKIP. INFO typically means "work in flight" — see the Tracking column. The Source column links to the branch the scenarios live on; per-suite env vars and default checkout paths are listed below the tables.

### Upstream-scenario suites

_Scenarios owned and maintained in `modelcontextprotocol/conformance`; mcpkit supplies only the fixture under test._

| Suite | Covers | Stage | Status | Source | Tracking |
|---|---|:---:|:---:|---|---|
| `testconf-tasks-v2` | SEP-2663 Tasks v2 | 8d | **PASS** | [`modelcontextprotocol/conformance@main`](https://github.com/modelcontextprotocol/conformance/tree/main) | — |
| `testconf-mrtr` | SEP-2322 MRTR | 8e | **PASS** | [`modelcontextprotocol/conformance@main`](https://github.com/modelcontextprotocol/conformance/tree/main) | — |
| `testconf-client` | Client core + auth (full suite) | - | **PASS**<sup>1</sup> | [`modelcontextprotocol/conformance@main`](https://github.com/modelcontextprotocol/conformance/tree/main) | — |
| `testconf-stateless` | SEP-2575 Stateless wire | - | **PASS**<sup>2</sup> | [`modelcontextprotocol/conformance@main`](https://github.com/modelcontextprotocol/conformance/tree/main) | — |

### mcpkit-authored suites

_Scenarios authored by this project, typically in the `panyam/mcpconformance` fork while the SEP they cover is still in flight upstream. They assert what the spec says, not what mcpkit does, but they have not been through upstream review._

| Suite | Covers | Stage | Status | Source | Tracking |
|---|---|:---:|:---:|---|---|
| `testconf-file-inputs` | SEP-2356 File inputs | 8f | **PASS** | [`panyam/mcpconformance@pending`](https://github.com/panyam/mcpconformance/tree/pending) | — |
| `testconf-auth-server` | MCP authz 2025-11-25 | 8g | **PASS** | [`panyam/mcpconformance@pending`](https://github.com/panyam/mcpconformance/tree/pending) | — |
| `testconf-skills` | SEP-2640 Skills | 8h | _INFO_<sup>3</sup> | [`panyam/mcpconformance@chore/sep-2640-yaml`](https://github.com/panyam/mcpconformance/tree/chore/sep-2640-yaml) | mcpkit 567 |

<sup>1</sup> Same scenario set tier-check's --client-cmd runs. Expected failures (extension + draft + backcompat categories, none tier-scored) live in conformance/baseline.yml.
<sup>2</sup> 30/30 as of upstream 0.2.0-alpha.9 (#376 fixed the former array-vs-object requiredCapabilities test).
<sup>3</sup> Fixture spawns and runs cleanly. Fork-side Scenario classes blocked on WG iteration of sep-2640.yaml in panyam/mcpconformance PR 330.

### Setup — clone the right worktree per suite

Each suite's Makefile target reads `MCPCONFORMANCE_*_PATH` to find its scenario worktree. Defaults assume sibling clones of the source repo at the relative path shown. Override per-invocation when the worktree lives elsewhere.

| Suite | Env var | Default path | Clone command |
|---|---|---|---|
| `testconf-tasks-v2` | `MCPCONFORMANCE_TASKS_V2_PATH` | `../conf-upstream-main` | `git clone -b main https://github.com/modelcontextprotocol/conformance.git ../conf-upstream-main` |
| `testconf-mrtr` | `MCPCONFORMANCE_MRTR_PATH` | `../conf-upstream-main` | `git clone -b main https://github.com/modelcontextprotocol/conformance.git ../conf-upstream-main` |
| `testconf-file-inputs` | `MCPCONFORMANCE_FILE_INPUTS_PATH` | `../conf-pending` | `git clone -b pending https://github.com/panyam/mcpconformance.git ../conf-pending` |
| `testconf-auth-server` | `MCPCONFORMANCE_AUTH_PATH` | `../conf-pending` | `git clone -b pending https://github.com/panyam/mcpconformance.git ../conf-pending` |
| `testconf-client` | `MCPCONFORMANCE_CLIENT_PATH` | `../conf-upstream-main` | `git clone -b main https://github.com/modelcontextprotocol/conformance.git ../conf-upstream-main` |
| `testconf-stateless` | `MCPCONFORMANCE_STATELESS_PATH` | `../conf-upstream-main` | `git clone -b main https://github.com/modelcontextprotocol/conformance.git ../conf-upstream-main` |
| `testconf-skills` | `MCPCONFORMANCE_SKILLS_PATH` | `../conf-skills` | `git clone -b chore/sep-2640-yaml https://github.com/panyam/mcpconformance.git ../conf-skills` |

## SEP Coverage

| SEP | Tested reqs | Excluded | Untested | Status |
|---|---:|---:|---:|---|
| [SEP-837](https://modelcontextprotocol.io/specification/draft/basic/authorization#application-type-and-redirect-uri-constraints) | [1](#sep-837-tested "Tested: sep-837-application-type-present.") | [4](#sep-837-excluded "2x harness cannot determine the client-under-test application c; 1x robustness requirement with no defined wire-level success cr; 1x UI/DX behavior, not protocol-observable") | 0 | **pass** |
| [SEP-2106](https://modelcontextprotocol.io/seps/2106-json-schema-2020-12#security-implications) | [1](#sep-2106-tested "Tested: sep-2106-no-network-ref-deref.") | [4](#sep-2106-excluded "1x Migration/deprecation guidance for SDK maintainers; about SD; 1x Restates pre-existing schema-validation behavior and is too ; 1x Applies only when the non-default opt-in network-$ref fetch ; 1x Internal validator resource limits (max schema depth, subsch") | 0 | **pass** |
| [SEP-2164](https://modelcontextprotocol.io/specification/draft/server/resources#error-handling) | [2](#sep-2164-tested "Tested: sep-2164-no-empty-contents, sep-2164-error-code.") | [1](#sep-2164-excluded "1x Client-side error handling is implementation-defined; not pr") | 0 | **pass** |
| [SEP-2207](https://modelcontextprotocol.io/specification/draft/basic/authorization#refresh-tokens) | [1](#sep-2207-tested "Tested: sep-2207-client-metadata-grant-types.") | [3](#sep-2207-excluded "1x The server suite does not yet exercise the SDK server as an ; 1x Confidentiality of refresh tokens in storage is client-inter; 1x A client assuming refresh tokens will be issued is mental-") | 0 | **pass** |
| [SEP-2243](https://modelcontextprotocol.io/specification/draft/basic/transports#standard-mcp-request-headers) | [18](#sep-2243-tested "Tested: sep-2243-client-includes-standard-headers, sep-2243-header-name-case-insensitive, sep-2243-server-reject-invalid-headers, +15 more.") | [4](#sep-2243-excluded "2x Intermediary requirement; conformance harness tests clients ; 1x Log output is not wire-observable.; 1x Design guidance to humans; not protocol-observable.") | [2](#sep-2243-untested "Untested: sep-2243-server-not-expect-null, sep-2243-server-reject-missing-required.") | partial |
| [SEP-2260](https://modelcontextprotocol.io/specification/draft/basic/transports#streamable-http) | 0 | [12](#sep-2260-excluded "10x No longer needed this behavior is enabled by default SEP-232; 1x Semantic association (relate to) is not protocol-observabl; 1x Implementation preference for keepalive mechanism choice; no") | 0 | _untested_ |
| [SEP-2322](https://modelcontextprotocol.io/specification/draft/basic/utilities/mrtr) | [17](#sep-2322-tested "Tested: sep-2322-result-type-included, sep-2322-default-result-type-complete, sep-2322-not-on-unsupported-requests, +14 more.") | [16](#sep-2322-excluded "8x Tasks moved to an extension as of SEP-2663; no longer part o; 1x inputRequests is a JSON object; duplicate keys are collapsed; 1x Architectural migration statement; tested indirectly through; 1x Internal security posture; not observable at protocol level; 1x Internal implementation choice about encryption/signing; not; 1x Internal requestState format; not observable at protocol lev; 1x Internal enforcement policy; conformance harness cannot dete; 1x Server-internal robustness assumption; not observable at pro; 1x Duplicates integrity-protection requirements above; internal") | 0 | **pass** |
| [SEP-2350](https://modelcontextprotocol.io/specification/draft/basic/authorization#runtime-insufficient-scope-errors) | [1](#sep-2350-tested "Tested: sep-2350-scope-union-on-reauth.") | [2](#sep-2350-excluded "1x All scopes required for the current operation has no harne; 1x reword of pre-existing requirement (request->operation, +RFC") | 0 | **pass** |
| [SEP-2352](https://modelcontextprotocol.io/specification/draft/basic/authorization#authorization-server-binding) | [3](#sep-2352-tested "Tested: sep-2352-no-cross-as-credential-reuse, sep-2352-no-reuse-on-as-change, sep-2352-reregister-on-as-change.") | [3](#sep-2352-excluded "1x internal storage requirement; not directly observable on the; 1x internal state-keying requirement; not protocol-observable; 1x UI behavior; the negative half (do not send mismatched crede") | 0 | **pass** |
| [SEP-2468](https://modelcontextprotocol.io/specification/draft/basic/authorization#authorization-response-validation) | [6](#sep-2468-tested "Tested: sep-2468-client-validate-metadata-issuer, sep-2468-client-compare-iss-supported, sep-2468-client-reject-missing-iss, +3 more.") | [3](#sep-2468-excluded "1x Targets the authorization server under test; observing iss i; 1x Conditional on the AS actually including iss in an authoriza; 1x display is UI-facing; act-on has no protocol-observable sign") | 0 | **pass** |
| [SEP-2549](https://modelcontextprotocol.io/specification/draft/server/utilities/caching) | [7](#sep-2549-tested "Tested: sep-2549-tools-list-caching-hints, sep-2549-prompts-list-caching-hints, sep-2549-resources-list-caching-hints, +4 more.") | [13](#sep-2549-excluded "9x Client-side caching behavior; not observable at the protocol; 1x Client-side polling behavior; not observable at the protocol; 1x Client/cache-side behavior; not observable at the protocol l; 1x Server-side awareness requirement; implementation guidance n; 1x Server-side access control; implementation guidance not test") | 0 | **pass** |
| [SEP-2575](https://modelcontextprotocol.io/specification/draft/basic/lifecycle) | [22](#sep-2575-tested "Tested: sep-2575-client-populates-meta, sep-2575-server-rejects-undeclared-capability, sep-2575-missing-capability-http-400, +19 more.") | [13](#sep-2575-excluded "6x stdio client harness not implemented — see https://github.co; 1x internal server state, not directly wire-observable; the obs; 1x not observable from a black-box harness; every harness reque; 1x treated as cancellation is internal server state; once the; 1x stop work as soon as practical is unobservable from a blac; 1x architectural guidance, observable only via subscriptionId/t; 1x client-internal demux; not observable on the wire from the h; 1x internal comparison; gracefully has no wire-observable def") | 0 | **pass** |

_Numeric cells link to per-SEP detail below; hover/long-press surfaces a one-line summary. Status reflects upstream-declared requirements only — Scenario→SEP attribution is not exposed in tier-check JSON today; this column tracks "does upstream have a check ID for this SEP requirement", not "does mcpkit pass it". Per-SEP scenario pass/fail lives in `conformance/UPSTREAM_AUDIT.md`._

## SEP Detail

Per-SEP breakdown of upstream traceability — what is exercised, what is intentionally excluded, and what is declared but not yet exercised. Useful for auditing whether each exclusion still makes sense as upstream evolves. Check IDs link to their definition in the upstream SEP YAML.

### SEP-837

<a id="sep-837-tested"></a>

**Tested (1)**

- [`sep-837-application-type-present`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-837.yaml)

<a id="sep-837-excluded"></a>

**Excluded (4)**

| Requirement | Upstream reason |
|---|---|
| Native applications (desktop applications, mobile apps, CLI tools, and locally-hosted web applications accessed via localhost) SHOULD use application_type: "native". | harness cannot determine the client-under-test application class (native vs web) out-of-band; only presence and value validity are wire-observable |
| Web applications (remote browser-based applications served from a non-local host) SHOULD use application_type: "web". | harness cannot determine the client-under-test application class (native vs web) out-of-band; only presence and value validity are wire-observable |
| MCP clients MUST be prepared to handle registration failures due to redirect URI constraints when authorization servers implement OIDC. | robustness requirement with no defined wire-level success criterion |
| When a registration request is rejected, clients SHOULD surface a meaningful error to the user or developer. | UI/DX behavior, not protocol-observable |

<a id="sep-837-untested"></a>

**Untested (0)**

_None._

### SEP-2106

<a id="sep-2106-tested"></a>

**Tested (1)**

- [`sep-2106-no-network-ref-deref`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2106.yaml)

<a id="sep-2106-excluded"></a>

**Excluded (4)**

| Requirement | Upstream reason |
|---|---|
| SDK maintainers SHOULD: Document the migration in SDK release notes; Where ergonomic, provide typed helpers (e.g. generics over a tool's `outputSchema`) so consumers do not need to write narrowing guards by hand | Migration/deprecation guidance for SDK maintainers; about SDK source code, not protocol-observable wire behavior |
| JSON Schema validation already handles type checking, value constraints, and required field validation, and implementations MUST continue to validate all inputs and outputs against declared schemas | Restates pre-existing schema-validation behavior and is too broad to attribute to a specific observable SEP-2106 check; input/output validation overlaps existing tool scenarios |
| An opt-in mode that fetches non-local `$ref`s SHOULD enforce an allowlist of hosts (or at minimum reject loopback, link-local, and private network addresses), apply timeouts and size limits, and log dereferenced URIs | Applies only when the non-default opt-in network-$ref fetch mode is enabled; not observable in default conformance runs |
| Implementations SHOULD apply reasonable bounds — for example, a maximum schema depth, a cap on the total number of subschemas, or a per-validation time budget — to prevent a malicious tool definition from acting as a CPU DoS vector against the validator | Internal validator resource limits (max schema depth, subschema cap, per-validation time budget); a defensive measure not observable on the wire |

<a id="sep-2106-untested"></a>

**Untested (0)**

_None._

### SEP-2164

<a id="sep-2164-tested"></a>

**Tested (2)**

- [`sep-2164-no-empty-contents`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2164.yaml)
- [`sep-2164-error-code`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2164.yaml)

<a id="sep-2164-excluded"></a>

**Excluded (1)**

| Requirement | Upstream reason |
|---|---|
| clients SHOULD also accept -32002 as a resource not found error | Client-side error handling is implementation-defined; not protocol-observable |

<a id="sep-2164-untested"></a>

**Untested (0)**

_None._

### SEP-2207

<a id="sep-2207-tested"></a>

**Tested (1)**

- [`sep-2207-client-metadata-grant-types`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2207.yaml)

<a id="sep-2207-excluded"></a>

**Excluded (3)**

| Requirement | Upstream reason |
|---|---|
| MCP Servers (Protected Resources) SHOULD NOT include `offline_access` in `WWW-Authenticate` scope or Protected Resource Metadata `scopes_supported`, as refresh tokens are not a resource requirement | The server suite does not yet exercise the SDK server as an OAuth protected resource (no Protected Resource Metadata or WWW-Authenticate probing); revisit once server-side authorization scenarios exist (https://github.com/modelcontextprotocol/conformance/issues/116) |
| MCP Clients that desire refresh tokens MUST keep refresh tokens confidential in transit and storage as specified in OAuth 2.1 Section 4.3 | Confidentiality of refresh tokens in storage is client-internal state, and in-transit (TLS) confidentiality is not exercised by the harness over localhost HTTP; not protocol-observable |
| MCP Clients that desire refresh tokens MUST NOT assume refresh tokens will be issued; the AS retains discretion | A client "assuming" refresh tokens will be issued is mental-state; only manifests as general authorization-flow completion, which other checks already cover; not directly protocol-observable |

<a id="sep-2207-untested"></a>

**Untested (0)**

_None._

### SEP-2243

<a id="sep-2243-tested"></a>

**Tested (18)**

- [`sep-2243-client-includes-standard-headers`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-header-name-case-insensitive`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-server-reject-invalid-headers`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-server-reject-error-code`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-client-supports-custom-headers`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-client-mirrors-designated-params`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-x-mcp-header-not-empty`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-x-mcp-header-charset`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-x-mcp-header-unique`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-x-mcp-header-primitive-only`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-client-reject-invalid-tool`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-client-encode-values`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-client-base64-unsafe`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-server-decode-base64`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-client-omit-null`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-server-reject-invalid-param-chars`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-server-validate-param-match`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)
- [`sep-2243-server-reject-param-mismatch`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml)

<a id="sep-2243-excluded"></a>

**Excluded (4)**

| Requirement | Upstream reason |
|---|---|
| Clients SHOULD log a warning when rejecting a tool definition due to invalid x-mcp-header, including the tool name and the reason. | Log output is not wire-observable. |
| Server developers SHOULD NOT mark sensitive parameters (such as passwords, API keys, tokens, or PII) with x-mcp-header. | Design guidance to humans; not protocol-observable. |
| Intermediaries MUST return an appropriate HTTP error status for validation failures. | Intermediary requirement; conformance harness tests clients and servers, not intermediaries. |
| Intermediate servers that do not recognize an Mcp-Param-{Name} header MUST forward it and otherwise ignore it. | Intermediary requirement; conformance harness tests clients and servers, not intermediaries. |

<a id="sep-2243-untested"></a>

**Untested (2)**

- [`sep-2243-server-not-expect-null`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml) — Parameter value is null or omitted: Server MUST NOT expect the header.
- [`sep-2243-server-reject-missing-required`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2243.yaml) — Required parameter is omitted: Server MUST reject with JSON-RPC error.

### SEP-2260

<a id="sep-2260-tested"></a>

**Tested (0)**

_None._

<a id="sep-2260-excluded"></a>

**Excluded (12)**

| Requirement | Upstream reason |
|---|---|
| roots/list, sampling/createMessage, and elicitation/create requests MUST NOT be sent on standalone streams. | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Clients MUST return standard JSON-RPC errors for common failure cases: Server sends an elicitation/create request with no associated client-to-server request: -32602 (Invalid params) | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Clients SHOULD return standard JSON-RPC errors for common failure cases: Server sends a roots/list request with no associated client-to-server request: -32602 (Invalid params) | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Clients SHOULD return errors for common failure cases: Sampling request not associated with a client-to-server request: -32602 (Invalid params) | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| These messages MUST relate to the originating client request. | Semantic association ("relate to") is not protocol-observable. The harness cannot determine whether a server request conceptually relates to the client request without understanding application logic. |
| Implementations SHOULD prefer transport-level SSE keepalive mechanisms for idle-connection maintenance. | Implementation preference for keepalive mechanism choice; not observable on the wire. |
| Servers MUST send server-to-client requests (such as roots/list, sampling/createMessage, or elicitation/create) only in association with an originating client request (e.g., during tools/call, resources/read, or prompts/get processing). | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Standalone server-initiated requests of these types on independent communication streams (unrelated to any client request) are not supported and MUST NOT be implemented. | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Servers MUST send sampling/createMessage requests only in association with an originating client request (e.g., during tools/call, resources/read, or prompts/get processing). | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Standalone server-initiated sampling on independent communication streams (unrelated to any client request) is not supported and MUST NOT be implemented. | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Servers MUST send server-to-client requests (such as roots/list, sampling/createMessage, or elicitation/create) only in association with an originating client request (e.g., during tools/call, resources/read, or prompts/get processing). | No longer needed this behavior is enabled by default SEP-2322 MRTR. |
| Standalone server-initiated requests of these types on independent communication streams (unrelated to any client request) are not supported and MUST NOT be implemented. | No longer needed this behavior is enabled by default SEP-2322 MRTR. |

<a id="sep-2260-untested"></a>

**Untested (0)**

_None._

### SEP-2322

<a id="sep-2322-tested"></a>

**Tested (17)**

- [`sep-2322-result-type-included`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-default-result-type-complete`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-not-on-unsupported-requests`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-elicitation-incomplete`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-sampling-incomplete`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-list-roots-incomplete`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-reject-tampered-state`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-request-state-incomplete`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-respect-client-capabilities`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-client-request-state-echoed`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-client-no-state-omitted`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-client-jsonrpc-id-different`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-client-parallel-isolation`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-validate-input-responses`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-error-on-protocol-error`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-ignore-unexpected-params`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)
- [`sep-2322-missing-response-rerequests`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2322.yaml)

<a id="sep-2322-excluded"></a>

**Excluded (16)**

| Requirement | Upstream reason |
|---|---|
| inputRequests keys are server assigned identifiers and MUST be unique within the scope of the request. | inputRequests is a JSON object; duplicate keys are collapsed by JSON parsing before the harness can observe them, so key uniqueness is not testable at the protocol level |
| Servers MUST send server-to-client requests (such as roots/list, sampling/createMessage, or elicitation/create) using the MRTR pattern. | Architectural migration statement; tested indirectly through all MRTR scenarios |
| servers MUST treat requestState as an attacker-controlled input | Internal security posture; not observable at protocol level |
| servers MUST protect its integrity (e.g. HMAC or AEAD) | Internal implementation choice about encryption/signing; not observable at protocol level |
| servers SHOULD include the authenticated principal, a short expiry (TTL), and an identifier for the originating request inside the integrity-protected requestState payload and verify each on receipt | Internal requestState format; not observable at protocol level |
| Servers for which a given requestState must be consumed at most once MUST enforce that invariant server-side | Internal enforcement policy; conformance harness cannot determine which servers require single-use semantics |
| Servers MUST NOT assume that clients will fulfill the inputRequests or retry the original request | Server-internal robustness assumption; not observable at protocol level |
| Servers MUST validate request state as described in the server requirements above. | Duplicates integrity-protection requirements above; internal security detail |
| Servers MUST include an inputRequests field in the tasks/result response when the task is in status input_required. | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |
| inputRequests keys are server assigned identifiers and MUST be unique within the scope of a Task. | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |
| When tasks/get shows status input_required, clients MUST call tasks/result to get the inputRequests and optional requestState. | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |
| Clients SHOULD construct the results of those requests and call tasks/input_response with the inputResponses & requestState (if present). | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |
| Receivers MUST reject tasks/input_response requests for tasks that are not in input_required status with error code -32602 (Invalid params). | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |
| When a receiver receives a tasks/result request for a task in working status, it MUST block the response until the task reaches a terminal status or input_required status. | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |
| When a receiver receives a tasks/result request for a task in input_required status, it MUST return an InputRequiredResult containing the inputRequests that the requestor must fulfill. | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |
| After sending tasks/input_response, the requestor SHOULD resume polling via tasks/get. | Tasks moved to an extension as of SEP-2663; no longer part of core conformance |

<a id="sep-2322-untested"></a>

**Untested (0)**

_None._

### SEP-2350

<a id="sep-2350-tested"></a>

**Tested (1)**

- [`sep-2350-scope-union-on-reauth`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2350.yaml)

<a id="sep-2350-excluded"></a>

**Excluded (2)**

| Requirement | Upstream reason |
|---|---|
| Regardless of the approach chosen, servers SHOULD include all scopes required for the current operation in a single challenge. | "All scopes required for the current operation" has no harness-observable ground truth; the challenge is the only place the server declares its requirements. Detecting the negative (incremental challenging) would need server-side auth scenarios that do not exist yet and would test the example app's scope config rather than SDK behavior; the spec also permits dynamic per-request scope determination, so a second challenge is not conclusively non-conformant. |
| When responding with insufficient scope errors, servers SHOULD include the scopes needed to satisfy the current operation in the scope parameter, consistent with RFC 6750 Section 3.1. | reword of pre-existing requirement (request->operation, +RFC6750 cite); no normative delta; harness already emits scope= in WWW-Authenticate |

<a id="sep-2350-untested"></a>

**Untested (0)**

_None._

### SEP-2352

<a id="sep-2352-tested"></a>

**Tested (3)**

- [`sep-2352-no-cross-as-credential-reuse`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2352.yaml)
- [`sep-2352-no-reuse-on-as-change`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2352.yaml)
- [`sep-2352-reregister-on-as-change`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2352.yaml)

<a id="sep-2352-excluded"></a>

**Excluded (3)**

| Requirement | Upstream reason |
|---|---|
| Clients MUST maintain separate registration state (client credentials, tokens) per authorization server. | internal storage requirement; not directly observable on the wire |
| Clients that use pre-registered credentials, or persist client credentials obtained via Dynamic Client Registration, MUST associate those credentials with the specific authorization server that issued them, keyed by the authorization server issuer identifier. | internal state-keying requirement; not protocol-observable |
| If the authorization server indicated by protected resource metadata no longer matches the one the credentials were registered with, clients SHOULD surface an error rather than silently attempting to use mismatched credentials. | UI behavior; the negative half (do not send mismatched credentials) is covered by sep-2352-no-reuse-on-as-change |

<a id="sep-2352-untested"></a>

**Untested (0)**

_None._

### SEP-2468

<a id="sep-2468-tested"></a>

**Tested (6)**

- [`sep-2468-client-validate-metadata-issuer`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2468.yaml)
- [`sep-2468-client-compare-iss-supported`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2468.yaml)
- [`sep-2468-client-reject-missing-iss`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2468.yaml)
- [`sep-2468-client-compare-iss-unadvertised`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2468.yaml)
- [`sep-2468-client-proceed-no-iss`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2468.yaml)
- [`sep-2468-client-no-normalization`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2468.yaml)

<a id="sep-2468-excluded"></a>

**Excluded (3)**

| Requirement | Upstream reason |
|---|---|
| MCP authorization servers SHOULD include the iss parameter in authorization responses, including error responses, as defined in RFC9207 Section 2. | Targets the authorization server under test; observing iss in an authorization response requires driving an Authorization Code Grant against the AS, which the authorization-server suite does not implement yet (it only probes the metadata endpoint). (https://github.com/modelcontextprotocol/conformance/issues/208) |
| Authorization servers that include the iss parameter MUST advertise this by setting authorization_response_iss_parameter_supported to true in their metadata (RFC9207 Section 2.3). | Conditional on the AS actually including iss in an authorization response, which requires driving an Authorization Code Grant against the AS under test; the authorization-server suite does not implement that yet. (https://github.com/modelcontextprotocol/conformance/issues/208) |
| This validation applies equally to error responses - on mismatch the client MUST NOT act on or display error, error_description, or error_uri. | display is UI-facing; act-on has no protocol-observable signal beyond the existing reject-on-mismatch checks |

<a id="sep-2468-untested"></a>

**Untested (0)**

_None._

### SEP-2549

<a id="sep-2549-tested"></a>

**Tested (7)**

- [`sep-2549-tools-list-caching-hints`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2549.yaml)
- [`sep-2549-prompts-list-caching-hints`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2549.yaml)
- [`sep-2549-resources-list-caching-hints`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2549.yaml)
- [`sep-2549-resources-templates-list-caching-hints`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2549.yaml)
- [`sep-2549-resources-read-caching-hints`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2549.yaml)
- [`sep-2549-ttl-non-negative`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2549.yaml)
- [`sep-2549-cache-scope-valid`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2549.yaml)

<a id="sep-2549-excluded"></a>

**Excluded (13)**

| Requirement | Upstream reason |
|---|---|
| If ttlMs is 0, the response SHOULD be considered immediately stale. | Client-side caching behavior; not observable at the protocol level |
| If ttlMs is positive, the client SHOULD consider the result fresh for that many milliseconds after receiving the response. | Client-side caching behavior; not observable at the protocol level |
| If ttlMs is absent, clients SHOULD assume a default of 0 (immediately stale) and rely on their own caching heuristics or notifications. | Client-side caching behavior; not observable at the protocol level |
| If ttlMs is negative, clients SHOULD ignore it and treat it as 0. | Client-side caching behavior; not observable at the protocol level |
| Once the TTL expires, the response is stale and the client SHOULD re-fetch on next access. | Client-side caching behavior; not observable at the protocol level |
| Clients SHOULD NOT treat TTL as a polling interval that triggers automatic background refetches. | Client-side caching behavior; not observable at the protocol level |
| Implementations that do choose to poll MUST apply jitter and backoff. | Client-side polling behavior; not observable at the protocol level |
| Cached responses MAY be reused for the same authorization context. Caches MUST NOT be shared across authorization contexts (e.g. a different access token requires a different cache). | Client/cache-side behavior; not observable at the protocol level |
| When a cached page expires, the client SHOULD re-fetch that page using its cursor. | Client-side caching behavior; not observable at the protocol level |
| Clients that require a consistent snapshot of the full list SHOULD re-fetch from the beginning (without a cursor). | Client-side caching behavior; not observable at the protocol level |
| If a cursor becomes invalid (e.g., the server returns an error for a previously valid cursor), the client SHOULD discard all cached pages and re-fetch from the beginning. | Client-side caching behavior; not observable at the protocol level |
| Servers MUST be aware that responses with a "public" cacheScope may be shared between callers even if the Result is coming from an authenticated endpoint. | Server-side awareness requirement; implementation guidance not testable via protocol messages |
| Server implementors MUST apply appropriate per-primitive access controls, and MUST NOT rely on cacheScope alone to prevent unauthorized access to primitives. | Server-side access control; implementation guidance not testable via protocol messages |

<a id="sep-2549-untested"></a>

**Untested (0)**

_None._

### SEP-2575

<a id="sep-2575-tested"></a>

**Tested (22)**

- [`sep-2575-client-populates-meta`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-rejects-undeclared-capability`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-missing-capability-http-400`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-tags-subscription-id`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-unsupported-version-error`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-client-retry-supported-version`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-implements-discover`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-http-server-no-independent-requests-on-stream`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-http-client-sends-version-header`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-http-version-header-matches-meta`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-http-server-header-mismatch-400`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-http-server-unsupported-version-400`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-http-server-method-not-found-404`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-honors-notification-filter`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-sends-subscription-ack`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-client-declares-elicitation-capability`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-client-declares-roots-capability`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-client-declares-sampling-capability`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-declares-prompts-in-discover`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-sends-prompts-list-changed-on-subscription`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-sends-tools-list-changed-on-subscription`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)
- [`sep-2575-server-no-log-without-loglevel`](https://github.com/modelcontextprotocol/conformance/blob/da56f6631c83bec2b3b7e40a82f392ae360b790c/src/seps/sep-2575.yaml)

<a id="sep-2575-excluded"></a>

**Excluded (13)**

| Requirement | Upstream reason |
|---|---|
| A server MUST NOT treat connection or process identity as a proxy for conversation or session continuity. / Servers MUST NOT rely on prior requests over the same connection to establish context (e.g., capabilities, protocol version, client identity). | internal server state, not directly wire-observable; the observable consequence (rejecting requests with incomplete _meta rather than falling back to remembered state) is covered by sep-2575-request-meta-invalid-* — see https://github.com/modelcontextprotocol/conformance/issues/296 |
| Servers MUST NOT require that a client reuse the same connection to perform related operations. | not observable from a black-box harness; every harness request already arrives on an independent connection — see https://github.com/modelcontextprotocol/conformance/issues/296 |
| Closing the SSE response stream MUST be treated by the server as cancellation of that request. | "treated as cancellation" is internal server state; once the stream is closed there is no channel left on which to observe the effect — see https://github.com/modelcontextprotocol/conformance/issues/296 |
| The server SHOULD stop work on the cancelled request as soon as practical and MUST NOT send any further messages for it [HTTP]. | "stop work as soon as practical" is unobservable from a black-box harness, and "no further messages" cannot be verified once the response stream is closed — see https://github.com/modelcontextprotocol/conformance/issues/296 |
| State that needs to span multiple requests (e.g., long-running tasks, application-level handles) MUST be referenced by an explicit identifier the client passes on each request. | architectural guidance, observable only via subscriptionId/task-id rows already listed |
| To distinguish notifications belonging to different concurrent subscriptions, clients MUST correlate notifications using the io.modelcontextprotocol/subscriptionId field carried in _meta. | client-internal demux; not observable on the wire from the harness |
| The client SHOULD check the acknowledged filter against what it requested and handle any unsupported types gracefully. | internal comparison; "gracefully" has no wire-observable definition |
| Because there is no per-request status code to drive fallback, a client that supports both eras SHOULD probe with server/discover first [stdio backward compatibility]. | stdio client harness not implemented — see https://github.com/modelcontextprotocol/conformance/issues/258 |
| To cancel an in-flight request [on stdio], the client MUST send a notifications/cancelled notification referencing the request ID. | stdio client harness not implemented — see https://github.com/modelcontextprotocol/conformance/issues/258 |
| Servers SHOULD stop work on a cancelled request as soon as practical and MUST NOT send any further messages for it [stdio]. | stdio client harness not implemented — see https://github.com/modelcontextprotocol/conformance/issues/258 |
| If the server process exits unexpectedly, the client SHOULD restart it. | stdio client harness not implemented — see https://github.com/modelcontextprotocol/conformance/issues/258 |
| If the server returns UnsupportedProtocolVersionError, [the stdio client] SHOULD retry using one of the advertised supportedVersions rather than falling back to initialize. | stdio client harness not implemented — see https://github.com/modelcontextprotocol/conformance/issues/258 |
| On stdio, if the connection is terminated and then re-established, the client MUST re-send subscriptions/listen to re-establish its subscriptions. | stdio client harness not implemented — see https://github.com/modelcontextprotocol/conformance/issues/258 |

<a id="sep-2575-untested"></a>

**Untested (0)**

_None._


## Open Gaps

### Failing scenarios

| Scenario | Surface | Checks fail/pass | Tracking |
|---|---|---:|---|
| `auth/dpop` | client | 3/9 | https://github.com/panyam/mcpkit/issues/803 — Extension category, not tier-scored. SEP-1932 DPoP deferred until the spec exits draft. |
| `auth/dpop-nonce` | client | 5/9 | https://github.com/panyam/mcpkit/issues/803 — Extension category, not tier-scored. SEP-1932 DPoP server-required-nonce variant. |

### Declared requirements with no emitted check

| SEP | Check ID | Tracking |
|---|---|---|
| SEP-2243 | `sep-2243-server-not-expect-null` | MCP-Name fail-closed semantics on tasks/* — covered locally by server/middleware test |
| SEP-2243 | `sep-2243-server-reject-missing-required` | MCP-Name fail-closed semantics on tasks/* — covered locally by server/middleware test |
<!-- end:generated -->
