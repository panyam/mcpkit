# Auth Spec Coverage Matrix

Hand-curated per-clause traceability for mcpkit's MCP auth implementation. Each row links a spec requirement (MUST / SHOULD / MAY) to the file:line that implements it and the test that proves it.

> **Scaffold.** Filled rows cover the work landed in PRs #492, #502, #505, #506, #507. Future contributors fill rows in their domain as they touch areas — see [How to maintain](#how-to-maintain) below.

## Companion docs

Three artifacts describe mcpkit's conformance posture, at three granularities:

| Doc | Generated? | Granularity | Answers |
|---|---|---|---|
| [`CONFORMANCE.md`](../CONFORMANCE.md) | Auto (#507; `make refresh-conformance`) | Per-SEP rollup (`tested / excluded / untested` counts) | "Does upstream have a check for this SEP?" |
| [`conformance/UPSTREAM_AUDIT.md`](UPSTREAM_AUDIT.md) | Auto (`make testconf-upstream-audit`) | Per-scenario pass/fail | "Does mcpkit pass the scenarios upstream wrote?" |
| **This file** | Hand-curated | Per-clause (spec MUST → impl → test) | "Where does mcpkit implement this, and what test proves it?" |

The three are **complementary**, not redundant. CONFORMANCE.md catches "upstream wrote a check and we failed it." This matrix catches "the spec said MUST but upstream didn't write a check" — and provides the lookup table for "where in the codebase do we do X?"

## What this catches that the others don't

1. **Per-clause impl + test links.** CONFORMANCE.md says "SEP-2468 has 6 tested + 3 excluded = pass." It can't say *where* in mcpkit each MUST is implemented. This matrix does.
2. **Excluded-but-mcpkit-relevant rows.** Upstream excludes some MUSTs for principled reasons ("display is UI-facing", "covered indirectly"). Excluded ≠ doesn't apply. Each excluded row still needs a mcpkit answer: "what do we do here?"
3. **Pure-RFC MUSTs not surfaced as SEPs.** RFC 7591 / 8414 / 8707 / 9207 / 9728 have MUSTs that don't map 1:1 to SEPs. Upstream's traceability has no row for them; this matrix is the only place they're tracked.

## How to maintain

- **On each SEP merge:** walk the upstream `src/seps/sep-NNNN.yaml`, add a row per `requirements` entry (both `check:`-bearing AND `excluded:`-bearing).
- **On RFC reference:** when ext/auth code reaches for an RFC clause, add a row citing the RFC section + the impl/test sites.
- **Status conventions:**
  - ✅ — mcpkit implements + tests this. Impl + Test cells filled.
  - 🟡 — partial: implements but no dedicated test, or implements with a known gap.
  - ❌ — gap. Tracking issue link required.
  - **N/A** — server-side / not in mcpkit's scope / handled by an upstream dep. Reason required.
- **Status conventions for "Excluded by upstream":** Upstream may exclude a check from its test suite, but the MUST still applies to mcpkit. Use ✅ / 🟡 / ❌ as above; add a note explaining how mcpkit covers it (or doesn't).

When in doubt about granularity, mirror the upstream `src/seps/*.yaml` shape — one row per `requirements[]` entry.

---

## MCP Authorization Spec (2025-11-25)

Spec: https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization

| Clause | mcpkit impl | Test | Status | Notes |
|---|---|---|---|---|
| §2.3 — Client MUST try WWW-Authenticate first, fall back to well-known | `ext/auth/discovery.go:103-131` | `ext/auth/discovery_test.go:TestDiscoverMCPAuth_FullChain`, `TestDiscoverMCPAuth_WellKnownPathBased`, `TestDiscoverMCPAuth_WellKnownRootFallback` | ✅ | Two-path discovery: parse `resource_metadata=` from WWW-Authenticate header if present, else try `/.well-known/oauth-protected-resource{path}` then root fallback. |
| §2.3 — Scope selection: WWW-Authenticate > PRM scopes_supported > empty | `ext/auth/discovery.go:178-182` | `ext/auth/discovery_test.go:TestDiscoverMCPAuth_WWWAuthenticateScope`, `TestDiscoverMCPAuth_PRMScopesFallback` | ✅ | Priority order is C18 in the auth checklist. |
| §3 — Client MUST use PKCE S256 | `ext/auth/token_source.go:494` (`ValidatePKCES256`); enforced at `ext/auth/token_source.go:204` | `ext/auth/token_source_test.go:TestValidatePKCES256_*` | ✅ | Refuses to proceed if AS doesn't advertise S256. |
| §3 — Client MUST use HTTPS for AS endpoints | `ext/auth/token_source.go:210` (`client.ValidateHTTPS`, delegated to oneauth) | `ext/auth/token_source_test.go:TestValidateHTTPS_*` | ✅ | Bypassable via `AllowInsecure` for local dev only. |
| §C6 — Client registration priority: pre-registered > CIMD > DCR | `ext/auth/token_source.go:436` (`resolveClientID`) | _gap — no direct test for the priority order_ | 🟡 | Logic is exercised by integration tests that hit each branch independently, but the priority resolution itself isn't unit-tested. |

> **More clauses TBD.** Add rows incrementally as future contributors touch areas.

---

## RFC 7591 — Dynamic Client Registration

Spec: https://www.rfc-editor.org/rfc/rfc7591

| Clause | mcpkit impl | Test | Status | Notes |
|---|---|---|---|---|
| §2 — Required client metadata fields | `ext/auth/dcr.go:DefaultClientRegistration` | `tests/e2e/jwt_validation_test.go:TestE2E_ClientCredentials_FullFlow` | ✅ | Includes `client_name`, `grant_types`, `token_endpoint_auth_method`. Updated to RFC 7591 DCR endpoint in PR #494. |
| §3.2.1 — DCR response carries `client_id` (+ `client_secret` when applicable) | `ext/auth/dcr.go:RegisterClient` (re-exported from oneauth) | `tests/e2e/jwt_validation_test.go:TestE2E_ClientCredentials_FullFlow` | ✅ | Delegated to oneauth `client.RegisterClient`. |

> **More clauses TBD.** Refresh-token rotation, redirect_uri validation, DCR error responses not yet rowed.

---

## RFC 8414 — Authorization Server Metadata

Spec: https://www.rfc-editor.org/rfc/rfc8414

| Clause | mcpkit impl | Test | Status | Notes |
|---|---|---|---|---|
| §3 — Client fetches AS metadata via well-known URL | `ext/auth/discovery.go:190` (`client.DiscoverAS`) | `ext/auth/discovery_test.go:TestDiscoverMCPAuth_FullChain` | ✅ | Delegated to oneauth v0.1.11+. |
| §3.3 — `issuer` field in AS metadata MUST equal the issuer identifier used to construct the well-known URL | _oneauth `client.DiscoverAS`_ | _upstream `auth/metadata-issuer-mismatch` scenario_ | ✅ | Validated inside oneauth (v0.1.11, via oneauth#235). mcpkit consumes the validation transparently; PR #505 confirmed via upstream audit flip. |
| §2 — `authorization_response_iss_parameter_supported` field | _oneauth `client.ASMetadata.AuthorizationResponseIssParameterSupported`_ | `ext/auth/token_source.go:281-283` (read into `asAdvertisedSupport`, then passed to OnCallback at line 295) | ✅ | Surfaced by oneauth v0.1.12 (oneauth#239). |

---

## RFC 8707 — Resource Indicators

Spec: https://www.rfc-editor.org/rfc/rfc8707

| Clause | mcpkit impl | Test | Status | Notes |
|---|---|---|---|---|
| §2 — Client MUST validate that PRM `resource` matches the protected resource URL | `ext/auth/discovery.go:195` (call site), `ext/auth/discovery.go:272` (`validatePRMResource`), `ext/auth/discovery.go:292` (`sameResourceURL`) | `ext/auth/discovery_test.go:TestDiscoverMCPAuth_PRMResourceMismatch_Rejected`, `TestDiscoverMCPAuth_PRMResourceMatch_TrailingSlash`, `TestDiscoverMCPAuth_PRMResourceOriginOnly_Accepts`, `TestDiscoverMCPAuth_PRMResourceEmpty_Accepts` | ✅ | PR #502. Normalized comparison (case-fold + trailing slash) per RFC 3986 §6.2, with empty-path carve-out for origin-only PRM resources. See `project_oauth_url_comparison_rules` memory for why this differs from RFC 9207 iss handling. |
| §2 — Client SHOULD send `resource` parameter on token request | `ext/auth/token_source.go:292` (`Resource: s.ServerURL`) | _integration via the browser flow_ | ✅ | Threaded through oneauth's `BrowserLoginRequest.Resource`. |

---

## RFC 9207 — Authorization Server Issuer Identification

Spec: https://www.rfc-editor.org/rfc/rfc9207

All 6 SEP-2468 check IDs covered, plus the spec-strict normalization rule.

| Clause / Check ID | mcpkit impl | Test | Status | Notes |
|---|---|---|---|---|
| `sep-2468-client-validate-metadata-issuer` | (see RFC 8414 §3.3 row above) | (see above) | ✅ | Inherits oneauth's DiscoverAS validation. |
| `sep-2468-client-compare-iss-supported` — AS advertised + iss present → MUST match | `ext/auth/iss_validator.go:60` (`validateIss`); wired in `ext/auth/token_source.go:290-296` (`OnCallback` closure) | `ext/auth/iss_validator_test.go:TestValidateIss` ("happy: matches when AS advertised" + "iss-wrong-issuer" cases) | ✅ | PR #506. |
| `sep-2468-client-reject-missing-iss` — AS advertised + iss absent → MUST reject | `ext/auth/iss_validator.go:60` (`validateIss`) | `ext/auth/iss_validator_test.go:TestValidateIss` ("iss-supported-missing" case) | ✅ | Requires oneauth v0.1.12 surface (oneauth#239); wired via `asAdvertisedSupport` parameter. |
| `sep-2468-client-compare-iss-unadvertised` — AS didn't advertise + iss present → MUST match | `ext/auth/iss_validator.go:60` (`validateIss`) | `ext/auth/iss_validator_test.go:TestValidateIss` ("iss-unexpected" case) | ✅ | Same byte comparison; presence implies validation regardless of advertisement. |
| `sep-2468-client-proceed-no-iss` — AS didn't advertise + iss absent → MUST proceed | `ext/auth/iss_validator.go:60` (`validateIss`) | `ext/auth/iss_validator_test.go:TestValidateIss` ("legacy: AS did not advertise, iss omitted") | ✅ | Legacy AS pre-RFC 9207. |
| `sep-2468-client-no-normalization` — comparison MUST be byte-strict | `ext/auth/iss_validator.go:60` (`validateIss` — `iss != expectedIssuer`) | `ext/auth/iss_validator_test.go:TestValidateIss` ("iss-normalized: trailing-slash variant rejects", "case difference rejects") | ✅ | RFC 9207 §2.4 inherits RFC 9068 §2.1.1 byte-equal semantics. Critical: this is the opposite of RFC 8707 resource validation (which normalizes). See `project_oauth_url_comparison_rules` memory. |

### Excluded by upstream (still applies to mcpkit)

| Upstream excluded MUST | mcpkit posture | Notes |
|---|---|---|
| "iss validation applies equally to error responses" | 🟡 partial | mcpkit's `OnCallback` validates iss on any callback that has it; error responses with iss would go through the same path. No dedicated test for the error-response branch. |
| "AS SHOULD include iss in authorization responses, including error responses" | **N/A** | Server-side; mcpkit is client-side here. (Server-side SEP-2468 lives in oneauth.) |
| "AS that include iss MUST advertise via `authorization_response_iss_parameter_supported`" | **N/A** | Server-side. |

---

## RFC 9728 — Protected Resource Metadata

Spec: https://www.rfc-editor.org/rfc/rfc9728

| Clause | mcpkit impl | Test | Status | Notes |
|---|---|---|---|---|
| §3.1 — PRM well-known path: `/.well-known/oauth-protected-resource{path}` with root fallback | `ext/auth/discovery.go:127-145` | `ext/auth/discovery_test.go:TestDiscoverMCPAuth_WellKnownPathBased`, `TestDiscoverMCPAuth_WellKnownRootFallback` | ✅ | Path-based first, root fallback on 404. |
| §3.2 — `resource` field | (see RFC 8707 §2 row above) | (see above) | ✅ | mcpkit treats RFC 8707 + 9728 resource semantics as one rule. |
| §3.2 — `authorization_servers` array | `ext/auth/discovery.go:172-176` | `ext/auth/discovery_test.go:TestDiscoverMCPAuth_FullChain` | ✅ | Empty array is rejected. |

---

## SEP-2352 — Authorization-Server Binding

Spec: https://modelcontextprotocol.io/specification/draft/basic/authorization#authorization-server-binding

| Check ID | mcpkit impl | Test | Status | Notes |
|---|---|---|---|---|
| `sep-2352-reregister-on-as-change` — Client MUST re-register at the new AS when PRM `authorization_servers` changes | `ext/auth/token_source.go:114-121` (`dcrAS` field declaration), `ext/auth/token_source.go:140` (`Invalidate`), `ext/auth/token_source.go:460-464` (resolveClientID AS-change check) | `ext/auth/token_source_as_migration_test.go:TestOAuthTokenSource_resolveClientID_ASChange_ClearsDCRCache`, `TestOAuthTokenSource_Invalidate_ClearsCache`, `TestOAuthTokenSource_resolveClientID_ASUnchanged_KeepsCache` | ✅ | PR #502 Cluster D. |
| `sep-2352-no-reuse-on-as-change` — Client MUST NOT present AS₁ credentials to AS₂ | (same impl — DCR cache clears on AS mismatch) | (same tests) | ✅ | The cache-clear inherently prevents reuse. |
| `sep-2352-no-cross-as-credential-reuse` — Client MUST NOT assume credentials are portable | (same impl) | (same tests) | ✅ | Distinct check-ID in upstream; same wire observation. |

---

## SEP-2243 — Standard MCP Request Headers (excluded reqs only)

Spec: https://modelcontextprotocol.io/specification/draft/basic/transports#standard-mcp-request-headers

Most SEP-2243 clauses are covered by upstream conformance and visible in CONFORMANCE.md / UPSTREAM_AUDIT.md. The excluded rows are tracked here:

| Excluded check-ID | mcpkit posture | Notes |
|---|---|---|
| `sep-2243-server-not-expect-null` (MCP-Name fail-closed on tasks/*) | ✅ covered locally | Per `conformance/known-gaps.yaml`: "covered locally by server/middleware test." |
| `sep-2243-server-reject-missing-required` (same) | ✅ covered locally | Same — locally tested. |

---

## How rows map to PRs

| PR | What it added to this matrix |
|---|---|
| #492 — SEP-2322 input-required-result | Adjacent to MRTR; would add rows in a future MRTR section once we sweep that area |
| #502 — Clusters C+D | RFC 8707 §2, SEP-2352 (3 rows) |
| #505 — oneauth v0.1.11 bump | RFC 8414 §3.3 (delegated to oneauth) |
| #506 — Cluster A | RFC 9207 (6 rows) + the byte-strict carve-out for `sep-2468-client-no-normalization` |
| #507 — CONFORMANCE.md auto-gen | The companion-doc that this matrix complements |

## Open follow-ups for this matrix

- **Full MCP Auth spec sweep** — read the 2025-11-25 spec end-to-end and add per-clause rows for §1–§10. Currently ~5 rows; full sweep is probably ~25.
- **RFC 7591 / 8414 / 8707 / 9207 / 9728 MUST sweep** — pull every MUST from each RFC and add rows. Many will be N/A (server-side); the matrix records that explicitly.
- **Auto-generation** — extend `tools/conformance-report` to render this matrix from a structured annotation file (likely YAML, similar to `known-gaps.yaml`). Tracked as a follow-up if the manual scaffold proves valuable.
