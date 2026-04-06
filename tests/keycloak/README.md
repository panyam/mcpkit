# Keycloak Interop Tests

End-to-end tests that validate mcpkit's auth system against a real Keycloak instance. These tests issue real JWTs from Keycloak and validate them through mcpkit's `JWTValidator` via JWKS.

## Quick Start

```bash
# From project root:
make upkcl              # Start Keycloak (first time takes ~30s to import realm)
make test-auth-keycloak # Run interop tests
make downkcl            # Stop Keycloak when done
```

## Realm Configuration

The `realm.json` file configures Keycloak with:

| Item | Value | Purpose |
|------|-------|---------|
| Realm | `mcpkit-test` | Isolated test realm |
| Signing | RS256 | Standard JWT signing |
| `mcp-confidential` | Client with secret | client_credentials + password grant |
| `mcp-public` | Public client | PKCE S256 (future OAuth flow tests) |
| `mcp-testuser` | Test user / `testpassword` | Password grant tests |
| `tools-read` | Custom scope | MCP read scope |
| `tools-call` | Custom scope | MCP tool execution scope |
| `admin-write` | Custom scope | MCP admin scope |

## Test Coverage

| Test | What it verifies |
|------|-----------------|
| `TestKeycloak_MCPServer_ValidToken` | Keycloak client_credentials token accepted by mcpkit |
| `TestKeycloak_MCPServer_TamperedToken` | Modified token rejected (RS256 signature check) |
| `TestKeycloak_MCPServer_ScopeAllowed` | Token with correct scope passes RequireScope |
| `TestKeycloak_MCPServer_ScopeDenied` | Token without scope denied by RequireScope |
| `TestKeycloak_MCPServer_PRM` | PRM endpoint includes Keycloak as authorization_server |
| `TestKeycloak_MCPServer_WWWAuthenticate` | 401 response has parseable WWW-Authenticate header |
| `TestKeycloak_MCPServer_PasswordGrant` | User token with subject claim works end-to-end |

## Requirements

- Docker (for Keycloak container)
- Keycloak 26.0 (auto-pulled by `make upkcl`)
- Local checkout of oneauth (for `replace` directive in `go.mod`)

## Skip Behavior

Tests call `skipIfKeycloakNotRunning(t)` at the start. If Keycloak is not reachable, tests skip with a message like:

```
SKIP: Keycloak not reachable at http://localhost:8180 (run 'make upkcl' to start)
```

This means `go test ./...` is always safe to run without Docker.
