# MCP Events — whole-enchilada stage 2 walkthrough

Production-shape multi-tier reference. nginx fronts the event-server tier; Keycloak provides three pre-configured OAuth realms (asgard, babylon, camelot). The stack comes up silent — operator-runnable synthetic drivers (`make drive-chat`, `make drive-presence`) start producing events from sibling terminals. This walkthrough guides you through a multi-terminal demo where each tenant gets its own poller and webhook receiver — per-tenant isolation is the headline.

## What you'll learn

- **Chat-Driver — fire the synthetic producer.** — - **Demonstrates:** operator-controlled producers (stack does NOT auto-emit; events flow only when something pushes them).
- **A1-Poll — Asgard poller (alice).** — - **Demonstrates:** per-tenant delivery scoping (realm-in-bearer is what gates delivery).
- **B1-Poll — Babylon poller (bob).** — - **Demonstrates:** the scoping claim holds across a second tenant.
- **C1-Poll — Camelot poller (carol).** — - **Demonstrates:** clean three-way isolation on the wire.
- **A2-Webhook — Asgard webhook receiver (anand).** — - **Demonstrates:** push-based webhook delivery with the same tenant scoping that poll mode has.
- **B2-Webhook — Babylon webhook receiver (bhavna).** — - **Demonstrates:** webhook scoping for a second tenant.
- **C2-Webhook — Camelot webhook receiver (chandan).** — - **Demonstrates:** webhook scoping completes the 3×2 matrix (3 tenants × {poll, webhook}).
- **Admin — inject a single event per tenant.** — - **Demonstrates:** the per-event tenant tag is the authoritative scope; same producer can target any tenant.
- **Monitor + Admin — kill a replica mid-stream.** — - **Demonstrates:** Redis pub/sub fan-out keeps deliveries flowing through surviving replicas; nginx round-robin routes new connections to the survivors.
- **A1-Poll — restart with the last cursor; resume gap-free.** — - **Demonstrates:** cross-replica cursor durability (any replica reads the same Postgres buffer).
- **Admin — observe buffer TTL truncation.** — - **Demonstrates:** bounded replay (`POSTGRES_BUFFER_TTL=10m` in the compose); stale cursor → server returns `truncated:true` and client resyncs from `latest`.
- **Aarti × 4 — trip the subscription cap.** — - **Demonstrates:** cap is enforced GLOBALLY (Redis Lua-atomic INCR-with-check) — replica-locality of subscribes doesn't help bypass it.
- **Topology — subscribe to the topology stream (alex).** — - **Demonstrates:** `events.topology` is a normal source — any client can subscribe to it.
- **Admin — add a real Discord source on replicas 1 and 3 only.** — - **Demonstrates:** operator-controlled source topology (replicas 1+3 own the Discord WebSocket; replica 2 deliberately skipped to expose per-replica divergence).
- **Discord-Poll — subscribe to discord.message as Asgard (alice).** — - **Demonstrates:** dynamic source events flow through the same SSE + tenant scoping; subscribers on replica 2 (no Discord adapter) still receive events via Redis pubsub.
- **Admin — compare per-replica source views.** — - **Demonstrates:** adapter configs are per-replica state (no cross-replica gossip); the topology stream is what unifies the view.
- **Admin — remove the Discord source.** — - **Demonstrates:** `evctl sources rm` tears down both registry membership AND the upstream Discord WebSocket session.
- **Browser + Admin — sign alice out in Keycloak admin and watch the asgard windows die.** — - **Demonstrates:** synchronously revocable bearer tokens — the demo's headline win over plain JWT.

## Flow

```mermaid
sequenceDiagram
    participant Operator as The person running the demo — you
    participant Nginx as Frontdoor reverse proxy (localhost:9090)
    participant Server as Event-server (introspection-mode auth wired)
    participant Drivers as Operator-runnable synthetic producers (`make drive-chat`, `make drive-presence`)
    participant Keycloak as OAuth AS — three realms pre-imported on first start (localhost:8180)

    Note over Operator,Keycloak: Step 1: Chat-Driver — fire the synthetic producer.

    Note over Operator,Keycloak: Step 2: A1-Poll — Asgard poller (alice).

    Note over Operator,Keycloak: Step 3: B1-Poll — Babylon poller (bob).

    Note over Operator,Keycloak: Step 4: C1-Poll — Camelot poller (carol).

    Note over Operator,Keycloak: Step 5: A2-Webhook — Asgard webhook receiver (anand).

    Note over Operator,Keycloak: Step 6: B2-Webhook — Babylon webhook receiver (bhavna).

    Note over Operator,Keycloak: Step 7: C2-Webhook — Camelot webhook receiver (chandan).

    Note over Operator,Keycloak: Step 8: Admin — inject a single event per tenant.

    Note over Operator,Keycloak: Step 9: Monitor + Admin — kill a replica mid-stream.

    Note over Operator,Keycloak: Step 10: A1-Poll — restart with the last cursor; resume gap-free.

    Note over Operator,Keycloak: Step 11: Admin — observe buffer TTL truncation.

    Note over Operator,Keycloak: Step 12: Aarti × 4 — trip the subscription cap.

    Note over Operator,Keycloak: Step 13: Topology — subscribe to the topology stream (alex).

    Note over Operator,Keycloak: Step 14: Admin — add a real Discord source on replicas 1 and 3 only.

    Note over Operator,Keycloak: Step 15: Discord-Poll — subscribe to discord.message as Asgard (alice).

    Note over Operator,Keycloak: Step 16: Admin — compare per-replica source views.

    Note over Operator,Keycloak: Step 17: Admin — remove the Discord source.

    Note over Operator,Keycloak: Step 18: Browser + Admin — sign alice out in Keycloak admin and watch the asgard windows die.
```

## Steps

### Before you start

Run `make predemo` once first — it gives you a clean Keycloak slate, brings up the backends + observability + events stacks fresh, and opens the Keycloak admin (`localhost:8180`) and Grafana (`localhost:3000`) in your browser. Optionally run `make alllogs` for a single iTerm window with 3 panes tailing each stack's logs.

The walkthrough binary you're reading does **not** make MCP calls. Each Step tells you which window to open and exactly what command to run; the actual protocol traffic happens in those operator-run binaries.

**Window plan** — at peak you'll have these open:

| Label | Role | First step |
|---|---|---|
| Chat-Driver | synth producer | 1 |
| A1-Poll, B1-Poll, C1-Poll | tenant poll subscribers (alice / bob / carol) | 2 / 3 / 4 |
| A2-Webhook, B2-Webhook, C2-Webhook | tenant webhook subscribers (anand / bhavna / chandan) | 5 / 6 / 7 |
| Admin | one-shot commands (inject, evctl, docker exec, psql) | 8 |
| Monitor | Redis MONITOR | 9 |
| Topology | events.topology meta-source subscriber | 13 |
| Discord-Poll | discord.message poller | 15 |
| Browser | Keycloak admin UI for revocation | 18 |

### Phase 1 — Stack is alive but silent

Predemo finished and everything is healthy, but no events flow until you fire a producer.

### Step 1: Chat-Driver — fire the synthetic producer.

- **Demonstrates:** operator-controlled producers (stack does NOT auto-emit; events flow only when something pushes them).
- **Expected:** terminal logs `[chat-driver] tenant=…` lines every 2s, rotating asgard / babylon / camelot round-robin. Leave it running for the rest of the demo.

#### Run this

```bash
make drive-chat
```

### Phase 2 — Per-tenant isolation on pollers

Three poll-mode subscribers, one per realm. Each sees only its tenant's events; this is the headline isolation claim of the demo.

### Step 2: A1-Poll — Asgard poller (alice).

- **Demonstrates:** per-tenant delivery scoping (realm-in-bearer is what gates delivery).
- **Expected:** prints chat.message events tagged for asgard; babylon / camelot events never reach this window.

#### Run this

```bash
make poller TENANT=A USERNAME=alice
```

### Step 3: B1-Poll — Babylon poller (bob).

- **Demonstrates:** the scoping claim holds across a second tenant.
- **Expected:** prints only babylon events; A1 and (next) C1 remain silent for B's events.

#### Run this

```bash
make poller TENANT=B USERNAME=bob
```

### Step 4: C1-Poll — Camelot poller (carol).

- **Demonstrates:** clean three-way isolation on the wire.
- **Expected:** each event the chat driver fires lights up exactly one of A1 / B1 / C1, never two at once.

#### Run this

```bash
make poller TENANT=C USERNAME=carol
```

### Phase 3 — Same isolation on webhook mode

Webhook is the second delivery surface. Distinct users per role (anand / bhavna / chandan) keep Keycloak sessions clean and avoid bumping into the subscription cap demo later.

### Step 5: A2-Webhook — Asgard webhook receiver (anand).

- **Demonstrates:** push-based webhook delivery with the same tenant scoping that poll mode has.
- **Expected:** webhook receiver logs an HMAC-verified delivery for every asgard chat.message; A1 prints the same events via poll mode.

#### Run this

```bash
make webhook TENANT=A USERNAME=anand
```

### Step 6: B2-Webhook — Babylon webhook receiver (bhavna).

- **Demonstrates:** webhook scoping for a second tenant.
- **Expected:** receives only babylon events; never sees asgard or camelot deliveries.

#### Run this

```bash
make webhook TENANT=B USERNAME=bhavna
```

### Step 7: C2-Webhook — Camelot webhook receiver (chandan).

- **Demonstrates:** webhook scoping completes the 3×2 matrix (3 tenants × {poll, webhook}).
- **Expected:** receives only camelot events. Every chat.message lights up exactly TWO windows (one poll + one webhook), both for the same tenant.

#### Run this

```bash
make webhook TENANT=C USERNAME=chandan
```

### Phase 4 — Manual injects confirm tenant tag is the scope

Up to now the chat driver rotates tenants. Direct injects prove the tenant tag on the event is what scopes delivery (not the producer or the connection).

### Step 8: Admin — inject a single event per tenant.

- **Demonstrates:** the per-event tenant tag is the authoritative scope; same producer can target any tenant.
- **Expected:** A's inject lights up A1+A2 only; B's lights up B1+B2 only; C's (presence.changed) lights up C1+C2 only.

#### Run these in turn

```bash
make inject TENANT=A EVENT=chat.message TEXT='hi from Asgard'
make inject TENANT=B EVENT=chat.message TEXT='hi from Babylon'
make inject TENANT=C EVENT=presence.changed USER=carol STATE=online
```

### Phase 5 — Multi-replica resilience

Stack is N=3 by default. Redis Publisher/Subscriber fans every yielded event to every replica's local delivery loop, so killing a replica mid-stream doesn't drop subscriber state on the survivors.

### Step 9: Monitor + Admin — kill a replica mid-stream.

- **Demonstrates:** Redis pub/sub fan-out keeps deliveries flowing through surviving replicas; nginx round-robin routes new connections to the survivors.
- **Expected:** Redis MONITOR shows `publish mcpkit.events.chat.message ...` on every event. After killing replica 1, A/B/C subscriber windows keep printing without gaps. Start replica 1 again when done.

#### Open Monitor first, then run the kill / start in Admin

```bash
# Monitor window — leave running:
docker exec -it mcpkit-redis redis-cli MONITOR | grep mcpkit.events

# Admin window — kill, then restore:
docker compose kill event-server-1
docker compose start event-server-1
```

### Phase 6 — Cursor durability

Postgres-backed event buffer is the single source of truth across replicas. Poll-mode subscribers can stop, restart on a different replica, and resume gap-free.

### Step 10: A1-Poll — restart with the last cursor; resume gap-free.

- **Demonstrates:** cross-replica cursor durability (any replica reads the same Postgres buffer).
- **Expected:** after Ctrl+C, restart with `--start-cursor=<N>` and the poller resumes exactly where it left off, even if nginx routes the new connection to a different replica.

#### Stop, note the cursor, restart

```bash
make poller TENANT=A USERNAME=alice
# Ctrl+C — note the last cursor printed (call it N)
make poller TENANT=A USERNAME=alice -- --start-cursor=<N>
```

### Step 11: Admin — observe buffer TTL truncation.

- **Demonstrates:** bounded replay (`POSTGRES_BUFFER_TTL=10m` in the compose); stale cursor → server returns `truncated:true` and client resyncs from `latest`.
- **Expected:** after waiting past the TTL, restarting the poller with the old cursor produces a `truncated:true` response visible in the poller logs; it then continues from `latest`.

#### Run this in Admin

```bash
docker exec mcpkit-postgres psql -U postgres -d events \
  -c "SELECT source_name, min(cursor), count(*) FROM event_buffer GROUP BY source_name;"
```

### Phase 7 — Subscription quota enforcement

`EVENTS_QUOTA_CAPS=chat.message=3` is wired in compose. The Redis-backed QuotaStore enforces this per-principal globally — the 4th subscribe rejects even when it lands on a different replica.

### Step 12: Aarti × 4 — trip the subscription cap.

- **Demonstrates:** cap is enforced GLOBALLY (Redis Lua-atomic INCR-with-check) — replica-locality of subscribes doesn't help bypass it.
- **Expected:** first three windows print steady delivery; the fourth exits immediately with `-32013 ResourceExhausted limit=subscriptions max=3`. We use `aarti` (not alice/anand) so the existing subscriptions from Phases 2-3 don't already count toward her cap.

#### Run in four sibling windows; the 4th rejects

```bash
make webhook TENANT=A USERNAME=aarti   # window 1 — succeeds
make webhook TENANT=A USERNAME=aarti   # window 2 — succeeds
make webhook TENANT=A USERNAME=aarti   # window 3 — succeeds (at cap)
make webhook TENANT=A USERNAME=aarti   # window 4 — rejects with -32013
```

### Phase 8 — Dynamic source topology

The events SDK lets you AddSource / RemoveSource at runtime. mcpkit ships `events.topology` as a meta-source that yields one event for every lifecycle mutation — observe topology through the same subscription primitives clients already know.

### Step 13: Topology — subscribe to the topology stream (alex).

- **Demonstrates:** `events.topology` is a normal source — any client can subscribe to it.
- **Expected:** window sits silent right now (no sources have been added since boot). Will print `source.added` / `source.removed` events the moment Phase 8 fires them.

#### Run this

```bash
make poller EVENT=events.topology TENANT=A USERNAME=alex
```

### Step 14: Admin — add a real Discord source on replicas 1 and 3 only.

- **Demonstrates:** operator-controlled source topology (replicas 1+3 own the Discord WebSocket; replica 2 deliberately skipped to expose per-replica divergence).
- **Expected:** evctl prints per-replica responses showing the source was registered on 1 and 3 only. Topology window immediately prints `{"type":"source.added","name":"discord.message",...}`.

#### Requires DISCORD_BOT_TOKEN + DISCORD_CHANNEL_IDS exported

```bash
make add-discord TOKEN=$DISCORD_BOT_TOKEN CHANNELS=$DISCORD_CHANNEL_IDS REPLICAS=1,3 TENANTS=asgard,camelot
```

### Step 15: Discord-Poll — subscribe to discord.message as Asgard (alice).

- **Demonstrates:** dynamic source events flow through the same SSE + tenant scoping; subscribers on replica 2 (no Discord adapter) still receive events via Redis pubsub.
- **Expected:** real Discord messages from the configured channels arrive, tagged for asgard. Send a test message in Discord; it shows here within ~1s.

#### Run this

```bash
make poller EVENT=discord.message TENANT=A USERNAME=alice
```

### Step 16: Admin — compare per-replica source views.

- **Demonstrates:** adapter configs are per-replica state (no cross-replica gossip); the topology stream is what unifies the view.
- **Expected:** replica 1 and replica 3 list `discord.message` with config metadata; replica 2 does NOT.

#### Run these in turn

```bash
make list-sources REPLICAS=1   # includes discord.message
make list-sources REPLICAS=2   # does NOT include discord.message
make list-sources REPLICAS=3   # includes discord.message
```

### Step 17: Admin — remove the Discord source.

- **Demonstrates:** `evctl sources rm` tears down both registry membership AND the upstream Discord WebSocket session.
- **Expected:** topology window prints `{"type":"source.removed","name":"discord.message",...}`. The Discord-Poll window terminates with NotFound on its next poll cycle.

#### Run this

```bash
make rm-source SOURCE=discord.message REPLICAS=1,3
```

### Phase 9 — Token revocation kills only affected subscribers

One Keycloak admin click fires TWO distinct revocation paths: introspection-cache eviction for poll-mode subscribers (~5s) and OIDC Back-Channel Logout for webhook subscribers (immediate).

### Step 18: Browser + Admin — sign alice out in Keycloak admin and watch the asgard windows die.

- **Demonstrates:** synchronously revocable bearer tokens — the demo's headline win over plain JWT.
- **Expected:** within ~5s, A1-Poll exits with `token invalidated by AS (401)`. A2-Webhook receives a `{type:terminated}` envelope on its webhook stream and disconnects. B and C windows are entirely untouched — revocation is per-realm.

#### Open the browser, then tail logs in Admin

```bash
# Browser:
#   http://localhost:8180/admin/master/console/#/asgard/users
#   admin / admin → click 'alice' → Sessions → Sign out

# Admin window — see the back-channel logout fire:
docker compose logs -f event-server-1 | grep BCL
```

### That's the demo

You've now seen: producer/consumer split, per-tenant scoping on both delivery modes, cross-replica fan-out and resilience, durable cursors with bounded replay, globally-enforced subscription quotas, runtime source topology with the SDK's self-aware meta-stream, and synchronous token revocation. Everything is operator-runnable from sibling terminals — `make predemo` re-runs the prep at any time.

## Run it

```bash
go run ./examples/events/whole-enchilada/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/events/whole-enchilada/ --non-interactive
```
