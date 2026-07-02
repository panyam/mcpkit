# step-up

**Experimental.** Provider-neutral SEP-2350 server-side scope-challenge SUT.

The SEP-2350 scope-challenge wire shape is provider-blind: HTTP 403 +
`WWW-Authenticate: Bearer error="insufficient_scope", scope="...", resource_metadata="..."`
(RFC 6750 §3.1) plus RFC 9728 PRM discovery. No part of the server knows or
cares which authorization server minted the token, so a single SUT validates
the flow against any RFC-compliant AS. Point `-issuer` at Keycloak, Okta,
Entra, WorkOS, Descope, etc.

Supersedes the provider-named `../step-up-keycloak` (deprecated).

## What it shows

mcpkit's `ext/auth.NewToolScopeMiddleware` emits the SEP-2350 + RFC 6750 §3.1
wire shape any spec-conformant server must:

- HTTP 403 with the `WWW-Authenticate` challenge when the bearer token lacks
  `admin-write`. The handler never runs; the middleware short-circuits.
- A 2xx when the same call is retried with a sufficiently-scoped token.

`ext/auth.JWTValidator` reads scopes from whichever claim shape the IdP emits —
`scope` (space-delimited string, Keycloak/RFC 6749), `scopes` (array, oneauth),
or `scp` (array, Okta/Azure/Entra).

## Run

Against Keycloak (audience unset):

```bash
go run ./examples/auth/step-up -issuer http://localhost:8180/realms/mcpkit-test
```

Against Okta (custom AS sets `aud`, so pass `-audience`):

```bash
# provision + mint tokens from the Okta fixture first:
#   panyam/mcpconformance examples/auth-fixtures/okta (make provision)
source <mcpconformance>/examples/auth-fixtures/okta/okta.env
go run ./examples/auth/step-up -issuer "$OKTA_ISSUER" -audience api://default
```

Flags: `-addr` (default `:3021`), `-issuer` (defaults to the local Keycloak realm
`http://localhost:8180/realms/mcpkit-test`; override via `-issuer` or `$MCP_ISSUER`),
`-audience` (default empty = not validated; Okta needs `api://default`).

## Driving the conformance scenario

The per-provider fixtures (token provisioning + the `MCP_CONFORMANCE_CONTEXT`
blob) live in `panyam/mcpconformance` under `examples/auth-fixtures/<provider>`.
Start this SUT against a provider's issuer, mint that provider's context, and
run the scope-challenge scenario against `http://localhost:3021/mcp`.
