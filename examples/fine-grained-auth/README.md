# Fine-Grained Authorization — Scope Step-Up (UC2) + Ephemeral Credentials (UC3)

> ⚠ **EXPERIMENTAL** — Tracks [SEP-2643](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2643) (Structured Authorization Denials), currently a draft. UC2 + UC3 demonstrated end-to-end. Wire format may change as the SEP evolves.

A scripted MCP host walking through two of the SEP-2643 use cases:

- **UC2 — Scope step-up** — server returns `HTTP 403 + WWW-Authenticate: Bearer scope="..."` when a tool requires a scope the bearer token lacks; the host parses the required scopes per RFC 6750 and re-authorizes with the union.
- **UC3 — Ephemeral credentials (RFC 9396 RAR)** — server returns a JSON-RPC error carrying a `remediationHint` of type `oauth_authorization_details`; the host uses those details *verbatim* in a token request to mint a transaction-bound credential.

The MCP server runs an in-process [oneauth](https://github.com/panyam/oneauth) Authorization Server in a goroutine because Keycloak does not yet support RFC 9396 RAR. Tokens are RS256-signed; the MCP server validates them via the AS's JWKS endpoint.

## Quick Start

```bash
# Terminal 1 — start the MCP server + in-process AS
make serve

# Terminal 2 — run the scripted walkthrough
make demo
```

See [WALKTHROUGH.md](WALKTHROUGH.md) for the full sequence diagram and step-by-step description (regenerate via `make readme`).

## What it demonstrates

- **HTTP 403 + WWW-Authenticate per RFC 6750** for scope-step-up denials. `auth.NewToolScopeMiddleware` rejects pre-handler when `core.ToolDef.RequiredScopes` aren't satisfied; the mcpkit client surfaces it as `*client.ClientAuthError` with parsed `RequiredScopes`.
- **OAuth 2.0 client_credentials** grant against the in-process AS to mint scoped access tokens (RS256, validated by the MCP server via JWKS).
- **Auto-step-up flow**: the host parses the WWW-Authenticate scopes verbatim and requests a broader token containing the union (read + write), then retries — the smart-host pattern the spec encodes.
- **JSON-RPC `remediationHint` envelope** for UC3 — `error.data.authorization.remediationHints[].type = "oauth_authorization_details"` with RFC 9396 detail object describing the required authorization (`{type: "payment_initiation", amount, currency, payee, ...}`).
- **RFC 9396 Rich Authorization Requests** — host echoes the detail object back to the AS at `POST /token`; the AS embeds the resulting `authorization_details` claim in the issued JWT.
- **Server-side enforcement** of `authorization_details` via `paymentAuthorizationMiddleware` — reads the claim from the JWT and validates that a `payment_initiation` entry matches the request before letting `initiate_payment` execute.
- **CORS configuration** for browser-based MCP hosts (MCPJam) — `Mcp-Session-Id` in both Allow-Headers and Expose-Headers; `DELETE` in allowed methods.

## Where to look in the code

- Walkthrough steps + auto-step-up + RAR token request: [`main.go`](main.go) (the `runDemo` block + `requestToken` helper)
- In-process oneauth AS bootstrap (token endpoint, JWKS, dynamic client registration): [`main.go`](main.go) (`startInProcessAS`)
- Tool registration + `paymentAuthorizationMiddleware`: [`main.go`](main.go) (`registerTools`, `paymentAuthorizationMiddleware`)
- Scope-step-up middleware: [`ext/auth/scope_middleware.go`](../../ext/auth/scope_middleware.go) (`auth.NewToolScopeMiddleware`)
- JWT validator: [`ext/auth/jwt_validator.go`](../../ext/auth/jwt_validator.go)
- Companion: [`examples/elicitation/`](../elicitation/) — UC1 (URL elicitation / consent approval flow)

## Notes

- CORS for browser-based MCP hosts is applied via `server.WithHandlerWrap(cors)` so it covers `/mcp` plus the `auth.MountAuth` routes plus `/demo/bootstrap` uniformly. Same pattern as `examples/elicitation/`.
- The walkthrough's "no user interaction" assumption is **demo only**. A real banking host would prompt the user to confirm the payment amount/payee before requesting the RAR-bound token.
- The custom `isError=true` and `tool=` color rules on the demo server logger are passed as variadic extras to `common.NewMCPLogger` — they layer on top of the canonical 5-rule set without re-declaring it.
