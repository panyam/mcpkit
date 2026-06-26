# MCP Events — whole-enchilada stage 2 walkthrough

Production-shape multi-tier reference. nginx fronts the event-server tier; Keycloak provides three pre-configured OAuth realms (asgard, babylon, camelot). The stack comes up silent — operator-runnable synthetic drivers (make drive-chat, make drive-presence) start producing events from sibling terminals. This walkthrough guides you through a multi-terminal demo where each tenant gets its own streaming push subscriber and webhook receiver — per-tenant isolation is the headline.

## What you'll learn

- **Open six terminals and run these.** — - Demonstrates: per-tenant delivery scoping (realm-in-bearer gates delivery) on both push surfaces.
- **Admin — inject one event per tenant.** — - Demonstrates: per-event tenant tag is the authoritative delivery scope; same inject endpoint can target any tenant.
- **Chat-Driver + Monitor + Admin — kill a replica mid-stream.** — - Demonstrates: Redis pub/sub fan-out keeps deliveries flowing through surviving replicas; nginx round-robin routes new connections to the survivors. Every event-server replica stamps X-Replica on its HTTP responses AND on every outbound webhook POST. Every subscriber window prints `replica=event-server-N` on each event line — the webhook receiver reads it off the delivery POST, the streamer/poller off their response header — so the round-robin spray is visible per delivery, not just on connect.
- **Walkthrough — fire events/list × N, log X-Replica rotation.** — - Demonstrates: replica-agnostic read path. nginx round-robin spreads calls across the event-server replicas; every replica answers from the same in-process source registry, so the response is byte-identical content-wise.
- **Ad-hoc Poller — restart with the last cursor; resume gap-free.** — - Demonstrates: cross-replica cursor durability (any replica reads the same Postgres buffer).
- **Admin — observe buffer TTL truncation.** — - Demonstrates: bounded replay (POSTGRES_BUFFER_TTL=10m in the compose); stale cursor → server returns truncated:true and client resyncs from latest.
- **Aarti × 4 — trip a tightened subscription cap.** — - Demonstrates: cap is enforced GLOBALLY (Redis Lua-atomic INCR-with-check) — replica-locality of subscribes doesn't help bypass it.
- **TTL matrix — three tenants suggest different ttlMs values, observe the granted refreshBefore.** — - Demonstrates: server clamps client suggestions to the [MinWebhookTTL, MaxWebhookTTL] envelope; the 'one sanctioned exception' (clamp UP to the floor) is observable in window A; window B's in-envelope suggestion lands verbatim; window C's `null` request yields `refreshBefore: null` because the demo's event-server is started with EVENTS_ALLOW_INFINITE_TTL=true.
- **Receiver-behavior matrix — same `make webhook` target, different reply status.** — - Demonstrates: server's per-delivery response branching: 410 abandons THIS delivery without affecting the subscription; 500 triggers the retry loop + the suspend transition past threshold.
- **No-expiry restart-survival — restart the event-server tier, confirm the no-expiry sub stays.** — - Demonstrates: GORM-backed WebhookStore is the durability backbone — both finite-TTL and no-expiry subs survive a rolling restart of the event-server tier because their rows persist in Postgres (a no-expiry sub stores expires_at as NULL; the row survives regardless).
- **Failure-based GC capstone — no-expiry subscriber dies after 3 deliveries; server drops the sub.** — - Demonstrates: the full failure-based GC end-to-end. EXIT_AFTER=3 kills the receiver after 3 deliveries → next inject fails with connection_refused → server's `FailingContinuouslySince` anchors → past `EVENTS_NO_EXPIRY_GC_WINDOW=2m` (set on the event-server) the registry drops the no-expiry sub + POSTs a `terminated` envelope.
- **Topology — subscribe to the topology stream (alex).** — - Demonstrates: events.topology is a normal source — any client can subscribe to it.
- **Admin — add a real Discord source on replicas 1 and 3 only.** — - Demonstrates: operator-controlled source topology (replicas 1+3 own the Discord WebSocket; replica 2 deliberately skipped to expose per-replica divergence).
- **Discord-Poll — subscribe to discord.message as Asgard (alice).** — - Demonstrates: dynamic source events flow through the same SSE + tenant scoping; subscribers on replica 2 (no Discord adapter) still receive events via Redis pubsub.
- **Admin — compare per-replica source views.** — - Demonstrates: adapter configs are per-replica state (no cross-replica gossip); the topology stream is what unifies the view.
- **Admin — remove the Discord source.** — - Demonstrates: evctl sources rm tears down both registry membership AND the upstream Discord WebSocket session.
- **Browser + Admin — sign alice out in Keycloak admin and watch the asgard windows die.** — - Demonstrates: synchronously revocable bearer tokens — the demo's headline win over plain JWT. Revocation fires across both push surfaces uniformly.

## Flow

```mermaid
sequenceDiagram
    participant Operator as The person running the demo — you
    participant Nginx as Frontdoor reverse proxy (localhost:9090)
    participant Server as Event-server (introspection-mode auth wired)
    participant Drivers as Operator-runnable synthetic producers (make drive-chat, make drive-presence)
    participant Keycloak as OAuth AS — three realms pre-imported on first start (localhost:8180)

    Note over Operator,Keycloak: Step 1: Open six terminals and run these.

    Note over Operator,Keycloak: Step 2: Admin — inject one event per tenant.

    Note over Operator,Keycloak: Step 3: Chat-Driver + Monitor + Admin — kill a replica mid-stream.

    Note over Operator,Keycloak: Step 4: Walkthrough — fire events/list × N, log X-Replica rotation.

    Note over Operator,Keycloak: Step 5: Ad-hoc Poller — restart with the last cursor; resume gap-free.

    Note over Operator,Keycloak: Step 6: Admin — observe buffer TTL truncation.

    Note over Operator,Keycloak: Step 7: Aarti × 4 — trip a tightened subscription cap.

    Note over Operator,Keycloak: Step 8: TTL matrix — three tenants suggest different ttlMs values, observe the granted refreshBefore.

    Note over Operator,Keycloak: Step 9: Receiver-behavior matrix — same `make webhook` target, different reply status.

    Note over Operator,Keycloak: Step 10: No-expiry restart-survival — restart the event-server tier, confirm the no-expiry sub stays.

    Note over Operator,Keycloak: Step 11: Failure-based GC capstone — no-expiry subscriber dies after 3 deliveries; server drops the sub.

    Note over Operator,Keycloak: Step 12: Topology — subscribe to the topology stream (alex).

    Note over Operator,Keycloak: Step 13: Admin — add a real Discord source on replicas 1 and 3 only.

    Note over Operator,Keycloak: Step 14: Discord-Poll — subscribe to discord.message as Asgard (alice).

    Note over Operator,Keycloak: Step 15: Admin — compare per-replica source views.

    Note over Operator,Keycloak: Step 16: Admin — remove the Discord source.

    Note over Operator,Keycloak: Step 17: Browser + Admin — sign alice out in Keycloak admin and watch the asgard windows die.
```

## Steps

### Before you start

Run 'make predemo' once first — it gives you a clean Keycloak slate, brings up the backends + observability + events stacks fresh, and opens the Keycloak admin (localhost:8180) and Grafana (localhost:3000) in your browser. Optionally run 'make alllogs' for a single iTerm window with 3 panes tailing each stack's logs.

This walkthrough mostly orchestrates what you read between actions — only Phase 4's replica-rotation beat makes MCP calls from inside this binary. Each Step that requires terminal work tells you which window to open and what command to run; the actual protocol traffic happens in those operator-run binaries.

Window plan — at peak you'll have these open:

  - A1-Stream, B1-Stream, C1-Stream     — tenant streaming push subscribers (alice / bob / carol; SEP-2575 response-as-SSE). First used in step 1.
  - A2-Webhook, B2-Webhook, C2-Webhook   — tenant webhook receivers (anand / bhavna / chandan). First used in step 1.
  - Admin                                — one-shot commands (inject, evctl, docker exec, psql). First used in step 2.
  - Chat-Driver                          — synth producer (continuous flow for the kill-replica beat). First used in step 3.
  - Monitor                              — Redis MONITOR. First used in step 3.
  - Topology                             — events.topology meta-source subscriber. First used in step 8.
  - Discord-Poll                         — discord.message poller. First used in step 10.
  - Browser                              — Keycloak admin UI for revocation. First used in step 13.

### Phase 1 — Set up the subscriber matrix (six silent windows)

Six push subscribers: three streaming (alice/bob/carol) and three webhook receivers (anand/bhavna/chandan). All six sit silent until Phase 2 fires the first events — proving the path with a single deliberate inject rather than ambient noise. Poll mode is still available via `make poller` for ad-hoc operator use, but it isn't part of the headline narrative — the push surfaces are.

### Step 1: Open six terminals and run these.

- Demonstrates: per-tenant delivery scoping (realm-in-bearer gates delivery) on both push surfaces.
- A1/B1/C1 are streaming push on the SEP-2575 stateless wire — open POST + response-as-SSE, nginx round-robins every replica freely. Lowest delivery latency.
- A2/B2/C2 are HTTP webhook receivers — HMAC-signed POSTs, retry-on-failure, durable delivery records.
- Expected: all six windows sit silent. Once Phase 2 fires, each asgard event lights up A1+A2; babylon events light up B1+B2; camelot events light up C1+C2. No cross-tenant leakage on any surface.

#### Open six terminal windows and run one of these in each

```bash
# A1-Stream — Asgard streaming subscriber (alice)
make streamer TENANT=A USERNAME=alice

# B1-Stream — Babylon streaming subscriber (bob)
make streamer TENANT=B USERNAME=bob

# C1-Stream — Camelot streaming subscriber (carol)
make streamer TENANT=C USERNAME=carol

# A2-Webhook — Asgard webhook receiver (anand)
make webhook TENANT=A USERNAME=anand

# B2-Webhook — Babylon webhook receiver (bhavna)
make webhook TENANT=B USERNAME=bhavna

# C2-Webhook — Camelot webhook receiver (chandan)
make webhook TENANT=C USERNAME=chandan
```

### Phase 2 — First events: manual inject validates the path

Subscribers are up and silent. Now fire one event per tenant and watch which windows light up — this proves the per-event tenant tag is the authoritative scope (not the producer or the connection).

### Step 2: Admin — inject one event per tenant.

- Demonstrates: per-event tenant tag is the authoritative delivery scope; same inject endpoint can target any tenant.
- Expected: A's inject lights up A1+A2 (asgard stream + webhook); B's lights up B1+B2; C's lights up C1+C2. No cross-tenant leakage on any surface.
- All six Phase 1 windows subscribed to chat.message specifically (the events SDK takes one source name per subscription; no wildcard), so we use the same event type for every inject and watch the per-tenant scoping decide who sees it.

#### Run these in turn

```bash
make inject TENANT=A EVENT=chat.message TEXT='hi from Asgard'
make inject TENANT=B EVENT=chat.message TEXT='hi from Babylon'
make inject TENANT=C EVENT=chat.message TEXT='hi from Camelot'
```

### Phase 3 — Multi-replica resilience

Stack is N=3 by default. Redis Publisher/Subscriber fans every yielded event to every replica's local delivery loop, so killing a replica mid-stream doesn't drop subscriber state on the survivors.

### Step 3: Chat-Driver + Monitor + Admin — kill a replica mid-stream.

- Demonstrates: Redis pub/sub fan-out keeps deliveries flowing through surviving replicas; nginx round-robin routes new connections to the survivors. Every event-server replica stamps X-Replica on its HTTP responses AND on every outbound webhook POST. Every subscriber window prints `replica=event-server-N` on each event line — the webhook receiver reads it off the delivery POST, the streamer/poller off their response header — so the round-robin spray is visible per delivery, not just on connect.
- The stream subscriber is the most telling check — SEP-2575 stateless wire means its open POST connection lands on ONE specific replica, so its `replica=` stays fixed while it's bound. Killing that replica drops the SSE connection; the streamer logs `connection lost ... reconnecting to a surviving replica` and transparently re-opens, and its event lines resume with the new `replica=` value. (It resumes live delivery, not gap-free replay across replicas — that needs the global cursor, issue 833.)
- Expected: Once make drive-chat runs, A1/B1/C1/A2/B2/C2 all start ticking through their tenant's events. Redis MONITOR shows publish mcpkit.events.chat.message ... on every event. Webhook receiver logs show replica= rotating across the event-server replicas. After killing replica 1, webhook windows keep printing without gaps; the stream window whose connection was bound to replica 1 briefly reconnects (operator may see a terminated frame then a fresh subscribe) and resumes delivery on a surviving replica. Start replica 1 again when done; leave drive-chat running for the rest of the demo.

#### Three windows: Chat-Driver, Monitor, Admin

```bash
# Chat-Driver window — leave running for rest of demo:
make drive-chat

# Monitor window — leave running:
docker exec -it mcpkit-redis redis-cli MONITOR | grep mcpkit.events

# Admin window — kill replica 1, watch subscribers keep delivering, then restore:
docker compose kill event-server-1
docker compose start event-server-1
```

### Phase 4 — Replica-rotation poll: same data, any replica

With Phase 3 still running (drive-chat + N=3 replicas), this walkthrough binary itself fires a handful of events/list calls in a row against the nginx frontdoor. Each call lands on whichever replica nginx round-robins to; we log the X-Replica response header per call and compare response shapes — same source list, different replica.

### Step 4: Walkthrough — fire events/list × N, log X-Replica rotation.

- Demonstrates: replica-agnostic read path. nginx round-robin spreads calls across the event-server replicas; every replica answers from the same in-process source registry, so the response is byte-identical content-wise.
- Expected: prints one line per call: 'call k served by event-server-N — events=M'. Over ~6 calls you'll see at least two distinct X-Replica values (nginx round-robin); the event count M is identical across all calls.
- Try it by hand: the Run step does this in-binary, but the shell variant below is the same events/list call via curl so you can watch the X-Replica response header rotate yourself. Any tenant's token works — events/list just enumerates event types.

#### Same events/list call by curl — watch X-Replica rotate

```bash
# A bearer from any tenant (events/list just lists event types):
T=$(make newtoken-ci TENANT=A USER=usera1 PASSWORD=usera1)

# Fire it a handful of times; X-Replica flips across event-server-1/2/3.
# params._meta is the SEP-2575 stateless-wire envelope the server
# requires on every request (protocolVersion + clientInfo +
# clientCapabilities) — the per-request equivalent of an initialize
# handshake. Omit it and the request falls through to the legacy
# session path and 400s on the missing Mcp-Session-Id.
for i in $(seq 6); do
  curl -s -D - -o /dev/null -X POST http://localhost:9090/mcp \
    -H "Authorization: Bearer $T" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"events/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"curl-demo","version":"0"},"io.modelcontextprotocol/clientCapabilities":{}}}}' \
    | grep -i '^x-replica'
done
```

### Phase 5 — Cursor durability

Postgres-backed event buffer is the single source of truth across replicas. A poll-mode subscriber can stop, restart on a different replica, and resume gap-free. We use the make poller binary ad-hoc for this beat — it's the right surface for showing cursor restart.

### Step 5: Ad-hoc Poller — restart with the last cursor; resume gap-free.

- Demonstrates: cross-replica cursor durability (any replica reads the same Postgres buffer).
- Expected: each poll prints a `cursor advanced to N` line. After Ctrl+C, restart with START_CURSOR=<N> and the poller resumes exactly where it left off, even if nginx routes the new connection to a different replica.

#### Stop, note the cursor, restart

```bash
make poller TENANT=A USERNAME=alice
# Ctrl+C — note the last "cursor advanced to N" line printed (call it N)
make poller TENANT=A USERNAME=alice START_CURSOR=<N>
```

### Step 6: Admin — observe buffer TTL truncation.

- Demonstrates: bounded replay (POSTGRES_BUFFER_TTL=10m in the compose); stale cursor → server returns truncated:true and client resyncs from latest.
- Expected: after waiting past the TTL, restarting the poller with the old cursor produces a truncated:true response visible in the poller logs; it then continues from latest.

#### Run this in Admin

```bash
docker exec mcpkit-postgres psql -U postgres -d events \
  -c "SELECT source_name, min(cursor), count(*) FROM event_buffer GROUP BY source_name;"
```

### Phase 6 — Subscription quota enforcement

Compose ships `EVENTS_QUOTA_CAPS=chat.message=10` as the default (room for normal multi-window play without tripping). The Redis-backed QuotaStore enforces this per-principal globally — the cap+1'th subscribe rejects even when it lands on a different replica. For a tight demonstration this step overrides the cap down to 3 for the duration of the beat.

### Step 7: Aarti × 4 — trip a tightened subscription cap.

- Demonstrates: cap is enforced GLOBALLY (Redis Lua-atomic INCR-with-check) — replica-locality of subscribes doesn't help bypass it.
- Expected: first three windows print steady delivery; the fourth exits immediately with -32013 ResourceExhausted limit=subscriptions max=3. We use aarti (not alice/anand) so the subscriptions from Phase 1 don't already count toward her cap.

#### Tighten the cap first, then run in four sibling windows; the 4th rejects

```bash
# Lower the cap to 3 for this beat, recreate the event-server tier
# (no replica enumeration — compose recreates whichever event-servers
# exist for the current N):
EVENTS_QUOTA_CAPS=chat.message=3 docker compose up -d
make clear-all   # clear stale aarti subs from any prior runs

make webhook TENANT=A USERNAME=aarti   # window 1 — succeeds
make webhook TENANT=A USERNAME=aarti   # window 2 — succeeds
make webhook TENANT=A USERNAME=aarti   # window 3 — succeeds (at cap)
make webhook TENANT=A USERNAME=aarti   # window 4 — rejects with -32013

# Restore the looser default afterwards:
docker compose up -d
```

### Phase 6b — TTL negotiation and receiver-behavior matrix

Three orthogonal subscription knobs the operator can mix and match on `make webhook`. TTL_MS shapes the client's TTL suggestion to the server (absent / int / null). REPLY_STATUS shapes what the receiver returns per delivery (200 default / 410 abandon / 5xx retry-then-suspend). EXIT_AFTER shapes whether the receiver lives long enough for the server's failure-based GC to fire on a no-expiry sub. This phase walks the matrix one knob at a time, ending with the capstone combo.

### Step 8: TTL matrix — three tenants suggest different ttlMs values, observe the granted refreshBefore.

- Demonstrates: server clamps client suggestions to the [MinWebhookTTL, MaxWebhookTTL] envelope; the 'one sanctioned exception' (clamp UP to the floor) is observable in window A; window B's in-envelope suggestion lands verbatim; window C's `null` request yields `refreshBefore: null` because the demo's event-server is started with EVENTS_ALLOW_INFINITE_TTL=true.
- Expected: A's window logs `refreshBefore=<now+~5min>` (clamped UP); B's logs `refreshBefore=<now+~15min>` (granted as-is); C's logs `refreshBefore=null (no-expiry granted)`.

#### Open three sibling webhook windows, one per tenant

```bash
# Sub-floor suggestion → clamped UP to MinWebhookTTL (5 minutes).
make webhook TENANT=A USERNAME=anand TTL_MS=30000

# In-envelope suggestion → granted as-is (15 minutes).
make webhook TENANT=B USERNAME=bhavna TTL_MS=900000

# null → no-expiry, refreshBefore:null on the wire (event-server has EVENTS_ALLOW_INFINITE_TTL=true).
make webhook TENANT=C USERNAME=chandan TTL_MS=null
```

### Step 9: Receiver-behavior matrix — same `make webhook` target, different reply status.

- Demonstrates: server's per-delivery response branching: 410 abandons THIS delivery without affecting the subscription; 500 triggers the retry loop + the suspend transition past threshold.
- Expected: 410 window logs the verification + receives the inject, server-side logs (`make logs | grep webhook`) show `abandoned per receiver 410 Gone (subscription unaffected)`; the next inject in the same tenant lands on a separate (default-200) window normally. 500 window: server retries 3× with backoff, then logs the suspend transition + posts a `terminated` envelope; the sub stays in the registry as paused, observable via `make psql-webhooks`.

#### Open two sibling windows, then fire injects from Admin

```bash
# Window 1: receiver abandons each delivery with 410.
make webhook TENANT=A USERNAME=aarti REPLY_STATUS=410

# Window 2: receiver fails with 500 — server retries then suspends.
make webhook TENANT=A USERNAME=alex REPLY_STATUS=500

# Admin window — inject one event per receiver type:
make inject TENANT=A EVENT=chat.message TEXT='aarti gets 410, sub unaffected'
make inject TENANT=A EVENT=chat.message TEXT='alex gets 500, retried then suspended'

# Then inspect the registry:
make psql-webhooks
```

### Step 10: No-expiry restart-survival — restart the event-server tier, confirm the no-expiry sub stays.

- Demonstrates: GORM-backed WebhookStore is the durability backbone — both finite-TTL and no-expiry subs survive a rolling restart of the event-server tier because their rows persist in Postgres (a no-expiry sub stores expires_at as NULL; the row survives regardless).
- Expected: the chandan window's no-expiry sub appears in `make psql-webhooks` before AND after the restart, with `expires_at IS NULL`. A post-restart inject still lands on chandan's window. Finite-TTL subs survive the restart too — the meaningful contrast surfaces only when the CLIENT stops refreshing (which the no-expiry sub never needs to do).

#### With Phase 6b TTL window C still running, do this in Admin

```bash
# Before the restart — confirm chandan's no-expiry row exists.
make psql-webhooks

# Rolling restart of all three event-server replicas.
make restart-event-servers

# After the restart — same row, same expires_at IS NULL.
make psql-webhooks

# Inject one event — chandan's no-expiry window still receives it.
make inject TENANT=C EVENT=chat.message TEXT='hi after restart'
```

### Step 11: Failure-based GC capstone — no-expiry subscriber dies after 3 deliveries; server drops the sub.

- Demonstrates: the full failure-based GC end-to-end. EXIT_AFTER=3 kills the receiver after 3 deliveries → next inject fails with connection_refused → server's `FailingContinuouslySince` anchors → past `EVENTS_NO_EXPIRY_GC_WINDOW=2m` (set on the event-server) the registry drops the no-expiry sub + POSTs a `terminated` envelope.
- Expected: the receiver window receives 3 events then exits with the log line `exit-after target reached`. Next inject lands in the server logs as a delivery retry; after ~2 minutes of continuous failure, `make psql-webhooks` no longer shows chandan's no-expiry row. The failing-continuously-since column would be visible mid-flight if you queried during the failure run.

#### Run in a fresh window — the receiver self-terminates

```bash
# No-expiry sub that intentionally dies after 3 events.
make webhook TENANT=C USERNAME=chandan TTL_MS=null EXIT_AFTER=3

# In another window, fire 3 events to trip EXIT_AFTER, then a 4th to start the failure run:
make inject TENANT=C EVENT=chat.message TEXT='1 — delivers'
make inject TENANT=C EVENT=chat.message TEXT='2 — delivers'
make inject TENANT=C EVENT=chat.message TEXT='3 — delivers, then receiver exits'
make inject TENANT=C EVENT=chat.message TEXT='4 — first failure of the run'

# Watch the registry. Within ~2m the no-expiry sub vanishes:
watch -n 30 make psql-webhooks

# Server logs show the eventual drop:
make logs | grep -E "no-expiry subscription dropped|FailingContinuouslySince"
```

### Phase 7 — Dynamic source topology (PULL pattern)

The events SDK lets you AddSource / RemoveSource at runtime. mcpkit ships `events.topology` as a meta-source that yields one event for every lifecycle mutation — observe topology through the same subscription primitives clients already know.

### Step 12: Topology — subscribe to the topology stream (alex).

- Demonstrates: events.topology is a normal source — any client can subscribe to it.
- Expected: window sits silent right now (no sources have been added since boot). Will print source.added / source.removed events the moment the next step fires them.

#### Run this

```bash
make poller EVENT=events.topology TENANT=A USERNAME=alex
```

### Step 13: Admin — add a real Discord source on replicas 1 and 3 only.

- Demonstrates: operator-controlled source topology (replicas 1+3 own the Discord WebSocket; replica 2 deliberately skipped to expose per-replica divergence).
- Expected: evctl prints per-replica responses showing the source was registered on 1 and 3 only. Topology window immediately prints {"type":"source.added","name":"discord.message",...}.

#### Requires DISCORD_BOT_TOKEN + DISCORD_CHANNEL_IDS exported

```bash
make add-discord TOKEN=$DISCORD_BOT_TOKEN CHANNELS=$DISCORD_CHANNEL_IDS REPLICAS=1,3 TENANTS=asgard,camelot
```

### Step 14: Discord-Poll — subscribe to discord.message as Asgard (alice).

- Demonstrates: dynamic source events flow through the same SSE + tenant scoping; subscribers on replica 2 (no Discord adapter) still receive events via Redis pubsub.
- Expected: real Discord messages from the configured channels arrive, tagged for asgard. Send a test message in Discord; it shows here within ~1s.

#### Run this

```bash
make poller EVENT=discord.message TENANT=A USERNAME=alice
```

### Step 15: Admin — compare per-replica source views.

- Demonstrates: adapter configs are per-replica state (no cross-replica gossip); the topology stream is what unifies the view.
- Expected: replica 1 and replica 3 list discord.message with config metadata; replica 2 does NOT.

#### Run these in turn

```bash
make list-sources REPLICAS=1   # includes discord.message
make list-sources REPLICAS=2   # does NOT include discord.message
make list-sources REPLICAS=3   # includes discord.message
```

### Step 16: Admin — remove the Discord source.

- Demonstrates: evctl sources rm tears down both registry membership AND the upstream Discord WebSocket session.
- Expected: topology window prints {"type":"source.removed","name":"discord.message",...}. The Discord-Poll window terminates with NotFound on its next poll cycle.

#### Run this

```bash
make rm-source SOURCE=discord.message REPLICAS=1,3
```

### Phase 8 — Token revocation kills only affected subscribers

One Keycloak admin click fires TWO distinct revocation paths: introspection-cache eviction for stream-mode subscribers (~5s) and OIDC Back-Channel Logout for webhook subscribers (immediate).

### Step 17: Browser + Admin — sign alice out in Keycloak admin and watch the asgard windows die.

- Demonstrates: synchronously revocable bearer tokens — the demo's headline win over plain JWT. Revocation fires across both push surfaces uniformly.
- Expected: within ~5s, A1-Stream receives a terminal frame on its open POST response and exits (the request-scoped principal claims drop out from under the dispatcher). A2-Webhook receives a {type:terminated} envelope on its webhook stream and disconnects. B and C windows are entirely untouched — revocation is per-realm. The handling replica logs `[event-server] BCL fire: realm=asgard sub=... killed=N`.

#### Open the browser, then tail logs in Admin

```bash
# Browser:
#   http://localhost:8180/admin/master/console/#/asgard/users
#   admin / admin → click 'alice' → Sessions → Sign out

# Admin window — see the back-channel logout fire. Keycloak POSTs to the
# event-server.whole-enchilada round-robin alias, so the BCL lands on ANY
# replica. Tail every service and let grep filter — only event-servers
# emit BCL lines, so this stays correct for any N:
docker compose logs -f | grep BCL
```

### That's the demo

You've now seen: producer/consumer split, per-tenant scoping on both push surfaces, cross-replica fan-out and resilience, replica-agnostic read path, durable cursors with bounded replay, globally-enforced subscription quotas, runtime source topology with the SDK's self-aware meta-stream, and synchronous token revocation. Everything is operator-runnable from sibling terminals — `make predemo` re-runs the prep at any time.

## Run it

```bash
go run ./examples/events/whole-enchilada/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/events/whole-enchilada/ --non-interactive
```
