# Keycloak — stage-2 OAuth AS

This directory holds the Keycloak realm-export JSONs that `docker compose up` imports on first start. The Keycloak service in `docker-compose.yaml` mounts `./keycloak/realms/` at `/opt/keycloak/data/import/` so every JSON in here becomes a live realm when the container boots.

## Realms

| Realm | Test user | Password | Default access-token lifespan |
|---|---|---|---|
| `asgard` | `alice` | `alice` | 30 min |
| `babylon` | `bob` | `bob` | 30 min |
| `camelot` | `carol` | `carol` | 30 min |

Three realms make the isolation story tangible: with one terminal per tenant active, an event injected into Babylon's stream visibly *stays quiet* on Tenants A and C's terminals. The multi-realm validator on the event-server fans out to all three introspection endpoints; adding a fourth realm is a JSON drop-in + one URL added to `OAUTH_INTROSPECTION_URLS`.

Each realm contains three confidential OAuth clients:

| Client ID | Used by | Grant flows enabled |
|---|---|---|
| `mcp-event-server` | The MCP resource server (calls `/token/introspect` when validating bearers) | `client_credentials` |
| `mcp-events-poller` | `make poller TENANT=...` | `authorization_code` + PKCE, `password` (ROPC for CI) |
| `mcp-events-webhook` | `make webhook TENANT=...` | Same as poller |

**All client secrets are pre-baked as `mcpkit-demo-secret-DEMO-ONLY`.** They are clearly labeled, identical across both realms, and ARE NOT suitable for any non-demo deployment — they only exist so anyone can clone the repo and run the demo without first generating credentials in Keycloak.

## Operator surfaces

The Keycloak admin UI is the operator surface for the demo's revocation walkthrough step:

- **Admin URL**: <http://localhost:8180/admin/>
- **Admin credentials**: `admin` / `admin` (set on the Keycloak service env in `docker-compose.yaml` — also DEMO ONLY).
- **Revoke a user**: open `localhost:8180/admin/master/console/#/<realm>/users` → click user → "Sessions" tab → "Sign out" per session, or "Sign out all sessions" globally.

Within `OAUTH_CACHE_TTL` seconds (default 5s), the event-server's introspection cache expires, the next introspection call returns `active: false`, and the affected client receives `-32012 Forbidden`. Other tenants and other users are unaffected.

## Bring your own client

If you want to point your own MCP client at the demo's Keycloak:

1. Open <http://localhost:8180/admin/> → realm-switcher → `asgard` (or `babylon`).
2. **Clients** → **Create client**.
3. Set client type to OpenID Connect, client ID to whatever you want.
4. Enable **Service Accounts**, **Standard Flow**, **Direct Access Grants** as needed.
5. **Save**, then **Credentials** tab → copy the generated secret.
6. Configure your client with `client_id`, `client_secret`, and the AS URL `http://localhost:8180/realms/<realm>/`.

The event-server's introspection validator already accepts any token issued by either of the two realms — your custom client's tokens work end-to-end with no further configuration on the resource-server side.

## Bring your own AS (JWT mode)

For the JWT mode (validates token signatures via JWKS, no introspection callback), see the parent README's "Bring your own IdP" section. The event-server's `tryEnableAuth()` path takes over when `OAUTH_ISSUER` is set instead of `OAUTH_INTROSPECTION_URL_*`.

## Customizing the realms

Edit the JSONs in `realms/`, then:

```bash
make demo-down            # tear the stack down
docker volume prune       # optional: clear Keycloak's volume so the next start re-imports
make demo-up              # restart; new realm config takes effect
```

Realm-export schema is Keycloak's documented format — see [Keycloak admin docs](https://www.keycloak.org/server/importExport). The version pinned in `docker-compose.yaml` is `26.0`.
