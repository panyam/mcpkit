# backends/

Shared backend services for mcpkit examples — identity, relational state, cache + pub/sub. Sibling to [`docker/observability/`](../observability/); same shape (own compose project, joined via the `mcpkit` bridge network), different role.

## What runs

| Container | Image | Purpose | Ports |
|---|---|---|---|
| `mcpkit-keycloak` | `quay.io/keycloak/keycloak:26.0` | OAuth AS (3 tenant realms) | `8180` (admin UI + token endpoints) |
| `mcpkit-keycloak-init` | `quay.io/keycloak/keycloak:26.0` | One-shot sidecar: master realm sslRequired=NONE | — (exits after init) |
| `mcpkit-postgres` | `postgres:18-alpine` | Relational state (events: WebhookStore + EventBufferStore) | internal-only (5432) |
| `mcpkit-redis` | `redis:7-alpine` | Cache + pub/sub (events: QuotaStore + Emitter) | internal-only (6379) |

Postgres and Redis are NOT host-port-mapped — they're reachable from any container on the `mcpkit` network by alias (`postgres:5432`, `redis:6379`). An example needing direct host access can either `docker exec` into the container or add a `ports:` block in a local override.

## Quick start

```
cd docker/backends && just up       # bring the stack up
open http://localhost:8180          # Keycloak admin UI (admin/admin)
cd docker/backends && just down     # tear it down
```

## Reaching these from an example

In a sibling compose file, attach the consumer service to the shared `mcpkit` network and reference these by alias:

```yaml
networks:
  mcpkit:
    name: mcpkit
    driver: bridge

services:
  my-server:
    environment:
      POSTGRES_DSN: postgres://postgres:postgres@postgres:5432/events?sslmode=disable
      REDIS_ADDR: redis:6379
      OAUTH_INTROSPECTION_URL: http://keycloak:8080/realms/tenant-a/protocol/openid-connect/token/introspect
    networks:
      - mcpkit
```

The literal `name: mcpkit` (no project prefix) is what makes the cross-compose lookups resolve. Whichever stack starts first creates the network.

## Realms

The Keycloak instance imports three tenant realms at first boot from [`keycloak/realms/`](keycloak/realms/):

- `tenant-a`, `tenant-b`, `tenant-c` — each with users `alice` / `bob` / `carol` (passwords = usernames) plus `user{a,b,c}{1..5}` for parallel walkthrough beats.
- Client `mcp-events-poller` is registered in each realm with the shared demo secret `mcpkit-demo-secret-DEMO-ONLY` (demo-only — production registers a distinct client per realm).
- Issuer base is `http://localhost:8180` — tokens carry `iss: http://localhost:8180/realms/<realm>`. Examples that validate `iss` must use the same base.

To add a realm for a new example, drop a `realm-<name>.json` into `keycloak/realms/` and `cd docker/backends && just down && just up` to re-import. In-place realm edits go through `kcadm.sh` (see the `keycloak-init` sidecar for the pattern).

## Production note

This stack is sized for a developer laptop — single-binary modes, shared default credentials, no replication. It exists for running mcpkit examples that need auth + persistence, not for serving real workloads. A production deployment would run Keycloak in HA mode against an external DB, replicate Postgres + Redis, rotate the demo secrets, and split Keycloak realms into their own clients.
