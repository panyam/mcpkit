# whole-enchilada — production-shape MCP Events demo

Multi-tier reference deployment for the [MCP Events extension](https://github.com/modelcontextprotocol/experimental-ext-triggers-events) built on mcpkit. Stages plug onto the same compose graph; this leaf currently ships **stage 1**.

```
Host  ──[MCP / SSE]──>  Nginx  ──>  Event-server  <──[HTTP /events/<name>/inject]──  Push-server
                                          │
                                          └──[webhook POST]──>  Receiver  (example consumer)
```

## What stage 1 ships

- **Compose graph** (`docker-compose.yaml`) with nginx + N event-server replicas + M push-server replicas + one example receiver, plus commented-out blocks for stages 2/3/4 (Keycloak, Postgres, Redis, admin frontend, OTel + Grafana / Loki / Mimir).
- **Templated** — `make gen-compose N=<n> M=<m>` regenerates the compose YAML and nginx config for arbitrary replica counts.
- **DNS naming convention** — every service answers a `*.whole_enchilada` hostname both inside the compose network and (with `make hosts-install`) from the host shell / browser. See "Hostname routing" below.
- **All three delivery modes** work end-to-end: poll, push (SSE), webhook.
- **In-memory stores** — restart wipes state. Stage 3 plugs in Postgres + Redis.

## What stage 2 adds

- **Keycloak as the OAuth AS**, pinned to `quay.io/keycloak/keycloak:26.0`. Three realms pre-imported on first start: `tenant-a`, `tenant-b`, `tenant-c`. Admin UI at <http://localhost:8180/admin/> (`admin` / `admin`).
- **Multi-realm introspection on the event-server.** The new `MultiRealmIntrospectionValidator` fans every bearer token out to all three realms' `/introspect` endpoints and accepts the token if any realm says active. Tenant comes from whichever realm validated, encoded as `<realm>/<sub>` into `core.Claims.Subject` (PR 692).
- **Per-event tenant tagging.** `ChatMessageData` / `PresenceChangedData` now carry a `Tenant` field; the push-server rotates events across tenants by default. The event-server's `tenantMatchFunc` only delivers events to subscribers whose `Claims.Tenant` matches — cross-tenant isolation is enforced at delivery time.
- **Demo-only client secrets**, pre-baked in the committed realm JSONs at `keycloak/realms/`. See `keycloak/README.md` for the bring-your-own-client recipe.
- **Three tenants**, not two, so isolation is visually obvious: with one terminal per tenant the demo can show events for one tenant *not* showing up on the other two.

Subsequent stages still in flight: stage-3 wires the GORM stores from PR 685 (multi-replica state survives restart); stage-4 polishes push survival, the WG-announcement artifact, and revocation walkthrough docs.

## Quickstart

```bash
make demo-up           # docker compose up -d with N=1, M=1 (+ Keycloak in stage 2)
make demo              # interactive walkthrough (TUI)
make demo-test         # non-interactive walkthrough (CI / scripting)
make demo-down         # tear down
```

Scale replicas:

```bash
make demo-up N=3 M=2   # 3 event-servers, 2 push-servers
```

Local in-process tests (no Docker):

```bash
make test              # event-server e2e tests — includes 8 tenant-isolation cases
```

## Stage-2 4-terminal interactive demo

Once `make demo-up` is running, open multiple terminals to see per-tenant isolation in action. Each terminal authenticates as a single tenant via Keycloak and prints what it sees:

```bash
# T1 — keep this running
make demo-up

# T2 — Tenant A poller. Browser opens for login as alice@tenant-a (alice/alice).
TA=$(make newtoken TENANT=A)
make poller TENANT=A TOKEN=$TA

# T3 — Tenant B poller (different terminal). Login as bob@tenant-b.
TB=$(make newtoken TENANT=B)
make poller TENANT=B TOKEN=$TB

# T4 — Tenant A webhook receiver.
make webhook TENANT=A TOKEN=$TA

# T5 — Tenant B webhook receiver.
make webhook TENANT=B TOKEN=$TB

# T6 — Inject events from the host. Only the matching tenant's terminals print.
make inject TENANT=A EVENT=chat.message TEXT="hi from A"
make inject TENANT=B EVENT=chat.message TEXT="hi from B"
make inject TENANT=C EVENT=presence.changed USER=carol STATE=online
```

Default `make demo-up` also runs the push-server, which auto-rotates synthetic events across all three tenants — leave the pollers running and watch the rotation.

### Revocation walkthrough (the load-bearing demo step)

The introspection-based auth has *synchronously revocable* tokens — the demo's key claim that JWT can't make. From your browser:

1. Open <http://localhost:8180/admin/master/console/#/tenant-a/users>, login as `admin` / `admin`.
2. Click user `alice` → **Sessions** tab → **Sign out**.
3. Within `OAUTH_CACHE_TTL` seconds (default 5s), Tenant A's poller + webhook terminals die with `-32012 Forbidden`.
4. Tenant B + Tenant C terminals stay alive — revocation is per-realm, isolation holds.

This is the operator-facing flow a real production admin would use; nothing in the demo "fakes" the revocation. Re-acquire a token (`make newtoken TENANT=A`) and the subscribers reconnect.

## Observability (traces in Grafana)

Both the event-server and push-server emit OTel traces via SEP-414. Bring up the shared LGTM observability stack alongside the demo and the spans land in Grafana automatically:

```bash
# T1 — observability stack (Tempo + Loki + Mimir + Grafana + OTel Collector)
make -C ../../../docker up           # ports: Grafana :3000, OTLP :4317

# T2 — whole-enchilada demo (auto-attaches to the shared `mcpkit` docker
# network when the collector is reachable)
make demo-up
```

The compose template sets `EXPORTER=auto`, which means **best-effort OTLP with silent Noop fallback**. Translation: `make demo-up` works whether the observability stack is up or not. When it IS up, traces land at `http://localhost:3000` → Explore → Tempo → search by service name `whole-enchilada-event-server` or `whole-enchilada-push-server`.

To force OTLP and fail loudly when the collector is missing, override:

```bash
EXPORTER=otlp make demo-up
```

The shared docker network is named `mcpkit` and is created by whichever stack starts first; both composes declare it with the same literal name.

## Bring your own client

Two paths, depending on whether you want to use the demo's Keycloak or your own IdP:

### Use the demo's Keycloak (introspection mode)

1. <http://localhost:8180/admin/> (admin / admin) → realm `tenant-a` → **Clients** → **Create**.
2. Type **OpenID Connect**, give it a client ID, enable Service Accounts / Standard Flow / Direct Access Grants as you need.
3. **Save**, then **Credentials** tab → copy the generated secret.
4. From your client, acquire a token against `http://localhost:8180/realms/tenant-a/protocol/openid-connect/token` using whichever OAuth flow fits your client (client_credentials, auth code, etc.).
5. Send `Authorization: Bearer <token>` when calling `http://localhost:8080/mcp`. The event-server's `MultiRealmIntrospectionValidator` already accepts any token issued by any of the three realms — no further server-side configuration.

### Bring your own IdP (JWT mode)

For "I have my own Auth0 / Okta / Keycloak", flip the event-server from introspection to JWT-mode validation:

```bash
# in your shell before make demo-up
export OAUTH_INTROSPECTION_URLS=    # explicitly clear
export OAUTH_ISSUER=https://your-idp.example.com/realms/your-realm
make demo-up
```

The event-server's `tryEnableAuth()` picks up `OAUTH_ISSUER` and fetches JWKS from `<issuer>/protocol/openid-connect/certs`. Tokens signed by your IdP are validated locally; no callback to your AS per request. **Trade-off:** revocation is no longer synchronously visible — tokens stay valid until they expire (the JWT-vs-introspection trade-off; see `ext/auth/introspection_validator.go` doc for context).

## What stage 2 adds

## Architecture

### Tiers

| Tier | What it owns | Why a separate process |
|---|---|---|
| **nginx** | Frontdoor reverse proxy. Routes by `Host` header to per-service backends. | Single entry point; client-facing TLS termination point in production. |
| **event-server** (N replicas) | MCP Events extension (events/list, events/poll, events/subscribe, events/stream), webhook delivery, push fanout. | Scales with MCP client count + delivery throughput. |
| **push-server** (M replicas) | Source-side concerns — upstream integration (real-world: Discord WebSocket, Telegram bot, OAuth refresh; this demo: synthetic chat + presence feeders). Pushes events into the event-server via `events.HTTPSource` over HTTP. | Scales with upstream-integration count, not with MCP client count. Credentials for upstreams live here, never in the event-server. |
| **receiver** | **Example** webhook consumer. Verifies Standard Webhooks signatures, exposes `/__received` for the walkthrough + e2e drainage. | In production, your receivers are your own apps in your own infra; this one is here to show the wire shape. |

### How events flow

1. `push-server` calls `eventsclient.Pusher.PushNamed("chat.message", data)` against `http://event_server.whole_enchilada/events/chat.message/inject`.
2. `event-server`'s `events.HTTPSource[ChatMessageData]` handler decodes and yields into the library's `YieldingSource`.
3. The library fans out: push subscribers receive the event via SSE on `events/stream`, webhook subscribers get an HTTP POST with a Standard Webhooks signature, poll subscribers see it on their next `events/poll`.
4. The `receiver` verifies the signature and logs the payload.

### Why HTTPSource (the third source pattern)

`experimental/ext/events/` ships three source patterns:

| Pattern | Source-side code | Used in |
|---|---|---|
| `YieldingSource` | `yield(data)` in-process | `discord/`, `telegram/` |
| `TypedSource` | `Poll(cursor, limit)` in-process | DB-backed demos |
| **`HTTPSource`** | Remote process POSTs to `{base}/events/{name}/inject` | this demo |

`HTTPSource` is what makes the push-server / event-server split tractable: the SDK provides both sides (`HTTPSource[Data]` on the event-server, `eventsclient.Pusher` on the push-server). See [`experimental/ext/events/HTTP_SOURCE.md`](../../../experimental/ext/events/HTTP_SOURCE.md).

## Hostname routing

Every service answers a `<role>.whole_enchilada` hostname via Docker network aliases (inside the compose network) and nginx server-name routing (from the host).

| Hostname | Resolves to |
|---|---|
| `nginx.whole_enchilada` | nginx frontdoor (port 80) |
| `event_server.whole_enchilada` | Round-robins across all N event-server replicas |
| `event_server_1.whole_enchilada`, `event_server_2.whole_enchilada`, … | Specific replica (regex-routed by nginx) |
| `pusher.whole_enchilada` | Round-robins across all M push-server admin ports |
| `pusher_1.whole_enchilada`, `pusher_2.whole_enchilada`, … | Specific push-server (admin port) |
| `receiver.whole_enchilada` | Example webhook consumer |

**From the host shell**, install the `/etc/hosts` entries once:

```bash
make hosts-install        # appends 127.0.0.1 nginx.whole_enchilada ... (needs sudo)
make hosts-uninstall      # removes them
```

After that:

```bash
curl http://event_server.whole_enchilada/mcp                    # any replica
curl http://event_server_2.whole_enchilada/healthz              # specifically replica 2
curl http://pusher.whole_enchilada/status                       # any push-server's admin
```

**From inside a container**, the same names just work — Docker's embedded DNS resolves the network aliases.

## Future stages (planned, not in this PR)

| Stage | What it adds | Issue |
|---|---|---|
| 2 | Keycloak realm + tenant-scoped subscribe + multi-tenant routing on nginx. | #637 |
| 3 | Postgres-backed Cursor/Webhook/Quota stores. Redis EventBus. Cross-replica fanout (verified by killing a replica mid-stream). | #639 |
| 4 | Admin frontend. M push-servers driven by admin-configured source bindings. OTel collector + Jaeger + Grafana + Loki + Mimir. Push survival walkthrough. | #638 |

The directory layout and the `*.whole_enchilada` naming convention are forward-compatible — later stages add services without restructuring.

## Layout

```
whole-enchilada/
├── docker-compose.yaml     # GENERATED (committed default: N=1, M=1)
├── nginx/nginx.conf        # GENERATED
├── tools/gen-compose/      # Template + Go renderer
├── event-server/           # MCP Events server, HTTPSource consumer
├── push-server/            # Synthetic feeders + Pusher client
├── receiver/               # Example webhook consumer
├── walkthrough/            # demokit walkthrough binary
├── Makefile                # demo-up / demo-test / demo-down / gen-compose
├── README.md               # this file
└── WALKTHROUGH.md          # GENERATED by `make readme`
```

## Where each thing is documented

- [`examples/events/CONVENTIONS.md`](../CONVENTIONS.md) — the events-demo family conventions.
- [`experimental/ext/events/HTTP_SOURCE.md`](../../../experimental/ext/events/HTTP_SOURCE.md) — the `HTTPSource` pattern + `Pusher` client.
- [`experimental/ext/events/README.md`](../../../experimental/ext/events/README.md) — the events library overall.
- [`experimental/ext/events/DEPLOYMENT.md`](../../../experimental/ext/events/DEPLOYMENT.md) — production deployment guidance (WAF, SSRF guards, retry semantics).
