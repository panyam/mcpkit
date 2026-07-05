# Enterprise-Managed Authorization (EMA / SEP-990)

A runnable end-to-end demo of the MCP Enterprise-Managed Authorization flow: an
enterprise IdP vouches for the user, an ID-JAG carries that identity to the MCP
authorization server, and the AS issues the access token the MCP server checks.

This is the ID-JAG chain in plain terms:

```
IdP /token   (RFC 8693 token-exchange)   id_token  ->  id-jag
AS  /token   (RFC 7523 jwt-bearer)        id-jag    ->  MCP access token
MCP /mcp     (Bearer)                      access    ->  tool result
```

The client side is mcpkit's `ext/auth.EnterpriseManagedTokenSource`. The IdP and
AS are both in-process [oneauth](https://github.com/panyam/oneauth) servers so the
whole flow runs from a single binary with no external services.

## Run

```bash
go run ./enterprise-managed
```

It prints each stage: the demo `id_token`, the minted ID-JAG, the issued access
token, and the authenticated tool result.

## What the IdP must emit (the ID-JAG contract)

This section is the reference for anyone implementing the IdP / issuer side (for
example, adding ID-JAG issuance to Keycloak or another AS). The MCP client is
fixed on these exact values, so a conforming issuer must match them verbatim.

**Stage 1 request** the client sends to the IdP token endpoint (RFC 8693):

| form field | value |
|---|---|
| `grant_type` | `urn:ietf:params:oauth:grant-type:token-exchange` |
| `subject_token` | the user's IdP `id_token` |
| `subject_token_type` | `urn:ietf:params:oauth:token-type:id_token` |
| `requested_token_type` | `urn:ietf:params:oauth:token-type:id-jag` |
| `audience` | the MCP authorization server's issuer |
| `resource` | the MCP server URL |
| `client_id` | optional; the client's identity at the resource-server AS |

**Stage 1 response** the issuer returns:

```json
{
  "access_token": "<the id-jag, a signed JWT>",
  "issued_token_type": "urn:ietf:params:oauth:token-type:id-jag",
  "token_type": "N_A"
}
```

**The ID-JAG itself** is a signed JWT with:

- header `typ`: `oauth-id-jag+jwt`
- claims:
  - `iss` — the IdP
  - `sub` — the user
  - `aud` — the MCP AS (bound from the request `audience`, not a static default)
  - `client_id` — from the request `client_id`
  - `jti`, `iat`, `exp` — short-lived and single-use

**Stage 2** the client redeems the ID-JAG at the MCP AS via RFC 7523 jwt-bearer
(`grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer`, `assertion=<id-jag>`,
client auth via `client_secret_basic`). The AS validates the ID-JAG against the
trusted IdP, authenticates the client as the confidential client the ID-JAG's
`client_id` names, then issues the MCP access token.

## What the IdP does NOT need

There are no MCP-specific requirements on the IdP beyond conforming to the ID-JAG
draft. In particular:

- No MCP-defined lifecycle or revocation protocol. The ID-JAG is single-use and
  short-lived; the client re-runs the exchange when the downstream access token
  expires. Revocation is handled by short TTLs plus single-use `jti`, not an IdP
  revocation callback.
- No MCP-specific discovery on the IdP. Standard OAuth/OIDC metadata is enough.

## Roles and where they run today

| Role | This demo | Production |
|---|---|---|
| Browser login / `id_token` | in-process oneauth (minted for a demo user) | your enterprise IdP |
| ID-JAG issuance (exchange) | in-process oneauth | your enterprise IdP |
| ID-JAG redemption (AS) | in-process oneauth | the MCP resource-server AS |
| MCP server + tool | mcpkit | your MCP server |

## Status

- ID-JAG issuance is oneauth's native capability: the IdP AS opts in via
  `OneAuthConfig.IDJAGIssuer` (`apiauth.NewJWTIDJAGIssuer`), shipped in oneauth
  v0.1.33. Nothing in this example is a stub. The RS AS redeems the ID-JAG via
  oneauth's jwt-bearer granter, which applies ID-JAG hardening (`typ` check,
  single-use `jti`) automatically.
- Browser-based login (the user actually authenticating in a browser) is a
  planned increment. Today the demo mints the initial `id_token` directly for a
  demo user (`idtoken.go`) to keep the first run self-contained.
- Driving the flow against a real Keycloak-as-issuer is the follow-on interop
  step, tracked as mcpkit issue 844, once upstream Keycloak ID-JAG issuer
  support ships.

## References

- MCP EMA extension: `modelcontextprotocol/ext-auth`,
  `specification/draft/enterprise-managed-authorization.mdx`
- ID-JAG draft: `draft-ietf-oauth-identity-assertion-authz-grant-04`
- Client implementation: `ext/auth/enterprise_managed.go`
