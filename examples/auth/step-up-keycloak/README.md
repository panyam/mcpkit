# step-up-keycloak

> **Deprecated.** Superseded by the provider-neutral [`../step-up`](../step-up), which takes `-issuer`/`-audience` and validates the same wire shape against any authorization server (Keycloak, Okta, Entra, ...). For Keycloak: `go run ./examples/auth/step-up -issuer http://localhost:8180/realms/mcpkit-test`. This directory stays through v0.3.x for the in-flight review and is slated for removal in a later release.

**Experimental.** SEP-2350 server-side scope-challenge demo running against a Keycloak realm.

An experimental Go SDK exercising the same conformance scenario the TypeScript reference implementation in [`modelcontextprotocol/typescript-sdk` PR 1624](https://github.com/modelcontextprotocol/typescript-sdk/pull/1624) is being validated against. The canonical end-to-end runbook lives in [`panyam/mcpconformance`](https://github.com/panyam/mcpconformance/pull/19) under `examples/auth-fixtures/keycloak/README.md` on the `feat/sep-2350-server-scope-challenge` branch. This README is the mcpkit-side runbook for the same flow.

## What it shows

mcpkit's `ext/auth.NewToolScopeMiddleware` emits the SEP-2350 + RFC 6750 §3.1 wire shape any spec-conformant server must:

- HTTP 403 with `WWW-Authenticate: Bearer error="insufficient_scope", scope="admin-write", resource_metadata="..."` when the bearer token lacks the required scope.
- The handler does not run; the middleware short-circuits before dispatch.
- A 2xx response when the same call is retried with a sufficiently-scoped token.

## Quick run

Requires Docker and the Keycloak fixture from `panyam/mcpconformance`'s `feat/sep-2350-server-scope-challenge` branch:

```bash
# Terminal 1: bring up Keycloak (in the mcpconformance clone)
git clone https://github.com/panyam/mcpconformance.git
cd mcpconformance && git checkout feat/sep-2350-server-scope-challenge
make -C examples/auth-fixtures/keycloak up
make -C examples/auth-fixtures/keycloak wait

# Terminal 2: start the SUT (in the mcpkit clone)
cd ~/path/to/mcpkit/examples/auth
go run ./step-up-keycloak -addr :3020

# Terminal 3: smoke test the wire shape directly
INSUFFICIENT=$(curl -sS -X POST http://localhost:8180/realms/mcpkit-test/protocol/openid-connect/token \
    -d "grant_type=client_credentials" \
    -d "client_id=mcp-confidential" \
    -d "client_secret=mcp-test-secret-for-confidential" \
    -d "scope=openid tools-read" | python3 -c "import json,sys; print(json.load(sys.stdin)['access_token'])")

curl -i -X POST http://localhost:3020/mcp \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $INSUFFICIENT" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"admin_call","arguments":{}}}'
# Expect: HTTP/1.1 403 Forbidden
# WWW-Authenticate: Bearer error="insufficient_scope", scope="admin-write", ...
```

## Driving the conformance scenario against this SUT

In the same mcpconformance clone:

```bash
npm install && npm run build
CONTEXT=$(make -s -C examples/auth-fixtures/keycloak tokens-context)
MCP_CONFORMANCE_CONTEXT="$CONTEXT" node dist/index.js server \
    --url http://localhost:3020/mcp \
    --scenario scope-challenge
```

Expected pass shape:

```
Passed: 8/8, 0 failed, 0 warnings
```

with two `scope-challenge-accepted-*` rows SKIPPED (those activate when the scope-gated tool declares `AcceptedScopes`; this example doesn't, to keep the demo focused on the basic shape).

## Where this fits

| | Server side | Client side |
|---|---|---|
| Spec | SEP-2350 (merged) | SEP-2350 (merged) |
| TypeScript SDK reference | PR 1624 (open) | PR 1657 (open) |
| mcpkit | `ext/auth.NewToolScopeMiddleware` (this example) | `ext/auth.OAuthTokenSource.TokenForScopes` (already accumulates via `core.UnionScopes`) |
| Conformance scenario | `scope-challenge` (mcpconformance PR 19) | `scope-handling` (already in mcpconformance main, covers `sep-2350-scope-union-on-reauth`) |

The mcpkit client side is exercised separately by pointing the existing client-side `scope-handling` scenario at `cmd/testclient`; see the parent PR for details.

## Why experimental

- PR 1624 has not merged upstream. API surface in `ext/auth` is stable, but the wire shape we're matching could move if review surfaces changes.
- The mcpkit-side conformance scenario lives in our fork (`panyam/mcpconformance` PR 19) until it lifts upstream after PR 1624 lands.
- The Keycloak fixture is currently the only authorization server validated end-to-end. Entra / Okta / WorkOS coverage is a separate follow-up.

When PR 1624 lands and the scenario lifts upstream, this example loses the experimental marker.
