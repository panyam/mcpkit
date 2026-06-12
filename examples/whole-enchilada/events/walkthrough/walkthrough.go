// walkthrough is the demokit-driven narrative guide for the
// whole-enchilada stage-2 demo. It is **pure prose** — no MCP wire
// activity happens inside this binary. The protocol mechanics
// (initialize, events/poll, events/stream, events/subscribe webhook)
// are demonstrated by the per-tenant `make poller` / `make webhook`
// binaries the operator runs in sibling terminals; this binary just
// orchestrates what they read between actions.
//
// Stage-1 ran a single MCP client inside this binary to walk the
// protocol step-by-step. Stage-2 wires authentication into the
// event-server (every method requires a real tenant token), and the
// 4-terminal flow is the natural fit for showing per-tenant isolation
// — having this binary auto-acquire a token would duplicate work the
// operator's tenant terminals already do, so the demo is split:
// poller / webhook drive the wire; this binary teaches.
package main

import (
	"github.com/panyam/demokit"
	common "github.com/panyam/mcpkit/examples/common"
)

// expectedTokenEnvs is the set of bearer-token env vars the 4-terminal
// Token pre-acquisition env vars were removed when the walkthrough
// flipped to on-demand acquisition — each `make poller` / `make webhook`
// step now passes USERNAME / PASSWORD directly and the binary does its
// own ROPC login. `make predemo` clears stale Keycloak sessions so the
// first per-step login starts clean.

// runDemo drives the demokit walkthrough against the running compose
// stack. serverURL / receiverURL are accepted for symmetry with the
// stage-1 signature; they are no longer used inside this binary
// because no MCP traffic originates here.
func runDemo(_ /*serverURL*/, _ /*receiverURL*/ string) {
	demo := demokit.New("MCP Events — whole-enchilada stage 2 walkthrough").
		Dir("events/whole-enchilada").
		Description("Production-shape multi-tier reference. nginx fronts the event-server tier; Keycloak provides three pre-configured OAuth realms (asgard, babylon, camelot). The stack comes up silent — operator-runnable synthetic drivers (`make drive-chat`, `make drive-presence`) start producing events from sibling terminals. This walkthrough guides you through a multi-terminal demo where each tenant gets its own poller and webhook receiver — per-tenant isolation is the headline.").
		Actors(
			demokit.Actor("Operator", "The person running the demo — you"),
			demokit.Actor("Nginx", "Frontdoor reverse proxy (localhost:9090)"),
			demokit.Actor("Server", "Event-server (introspection-mode auth wired)"),
			demokit.Actor("Drivers", "Operator-runnable synthetic producers (`make drive-chat`, `make drive-presence`)"),
			demokit.Actor("Keycloak", "OAuth AS — three realms pre-imported on first start (localhost:8180)"),
		)

	common.SetupRenderer(demo)

	demo.Section("Before you start",
		"Run `make predemo` once first — it gives you a clean Keycloak slate, brings up the backends + observability + events stacks fresh, and opens the Keycloak admin (`localhost:8180`) and Grafana (`localhost:3000`) in your browser. Optionally run `make alllogs` for a single iTerm window with 3 panes tailing each stack's logs.",
		"",
		"The walkthrough binary you're reading does **not** make MCP calls. Each Step tells you which window to open and exactly what command to run; the actual protocol traffic happens in those operator-run binaries.",
		"",
		"**Window plan** — at peak you'll have these open:",
		"",
		"| Label | Role | First step |",
		"|---|---|---|",
		"| Chat-Driver | synth producer | 1 |",
		"| A1-Poll, B1-Poll, C1-Poll | tenant poll subscribers (alice / bob / carol) | 2 / 3 / 4 |",
		"| A2-Webhook, B2-Webhook, C2-Webhook | tenant webhook subscribers (anand / bhavna / chandan) | 5 / 6 / 7 |",
		"| Admin | one-shot commands (inject, evctl, docker exec, psql) | 8 |",
		"| Monitor | Redis MONITOR | 9 |",
		"| Topology | events.topology meta-source subscriber | 13 |",
		"| Discord-Poll | discord.message poller | 15 |",
		"| Browser | Keycloak admin UI for revocation | 18 |",
	)

	// -----------------------------------------------------------------
	// Phase 1 — Stack is alive but silent.
	// -----------------------------------------------------------------

	demo.Section("Phase 1 — Stack is alive but silent",
		"Predemo finished and everything is healthy, but no events flow until you fire a producer.",
	)

	demo.Step("Chat-Driver — fire the synthetic producer.").
		Note("- **Demonstrates:** operator-controlled producers (stack does NOT auto-emit; events flow only when something pushes them).\n- **Expected:** terminal logs `[chat-driver] tenant=…` lines every 2s, rotating asgard / babylon / camelot round-robin. Leave it running for the rest of the demo.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make drive-chat`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 2 — Per-tenant isolation on pollers.
	// -----------------------------------------------------------------

	demo.Section("Phase 2 — Per-tenant isolation on pollers",
		"Three poll-mode subscribers, one per realm. Each sees only its tenant's events; this is the headline isolation claim of the demo.",
	)

	demo.Step("A1-Poll — Asgard poller (alice).").
		Note("- **Demonstrates:** per-tenant delivery scoping (realm-in-bearer is what gates delivery).\n- **Expected:** prints chat.message events tagged for asgard; babylon / camelot events never reach this window.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make poller TENANT=A USERNAME=alice`).Default(),
		)

	demo.Step("B1-Poll — Babylon poller (bob).").
		Note("- **Demonstrates:** the scoping claim holds across a second tenant.\n- **Expected:** prints only babylon events; A1 and (next) C1 remain silent for B's events.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make poller TENANT=B USERNAME=bob`).Default(),
		)

	demo.Step("C1-Poll — Camelot poller (carol).").
		Note("- **Demonstrates:** clean three-way isolation on the wire.\n- **Expected:** each event the chat driver fires lights up exactly one of A1 / B1 / C1, never two at once.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make poller TENANT=C USERNAME=carol`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 3 — Same isolation on webhook mode.
	// -----------------------------------------------------------------

	demo.Section("Phase 3 — Same isolation on webhook mode",
		"Webhook is the second delivery surface. Distinct users per role (anand / bhavna / chandan) keep Keycloak sessions clean and avoid bumping into the subscription cap demo later.",
	)

	demo.Step("A2-Webhook — Asgard webhook receiver (anand).").
		Note("- **Demonstrates:** push-based webhook delivery with the same tenant scoping that poll mode has.\n- **Expected:** webhook receiver logs an HMAC-verified delivery for every asgard chat.message; A1 prints the same events via poll mode.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make webhook TENANT=A USERNAME=anand`).Default(),
		)

	demo.Step("B2-Webhook — Babylon webhook receiver (bhavna).").
		Note("- **Demonstrates:** webhook scoping for a second tenant.\n- **Expected:** receives only babylon events; never sees asgard or camelot deliveries.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make webhook TENANT=B USERNAME=bhavna`).Default(),
		)

	demo.Step("C2-Webhook — Camelot webhook receiver (chandan).").
		Note("- **Demonstrates:** webhook scoping completes the 3×2 matrix (3 tenants × {poll, webhook}).\n- **Expected:** receives only camelot events. Every chat.message lights up exactly TWO windows (one poll + one webhook), both for the same tenant.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make webhook TENANT=C USERNAME=chandan`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 4 — Manual injects confirm tenant tag is the scope.
	// -----------------------------------------------------------------

	demo.Section("Phase 4 — Manual injects confirm tenant tag is the scope",
		"Up to now the chat driver rotates tenants. Direct injects prove the tenant tag on the event is what scopes delivery (not the producer or the connection).",
	)

	demo.Step("Admin — inject a single event per tenant.").
		Note("- **Demonstrates:** the per-event tenant tag is the authoritative scope; same producer can target any tenant.\n- **Expected:** A's inject lights up A1+A2 only; B's lights up B1+B2 only; C's (presence.changed) lights up C1+C2 only.").
		VerbatimVariants("Run these in turn",
			demokit.MakeVariant("shell", "bash", `make inject TENANT=A EVENT=chat.message TEXT='hi from Asgard'
make inject TENANT=B EVENT=chat.message TEXT='hi from Babylon'
make inject TENANT=C EVENT=presence.changed USER=carol STATE=online`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 5 — Multi-replica resilience.
	// -----------------------------------------------------------------

	demo.Section("Phase 5 — Multi-replica resilience",
		"Stack is N=3 by default. Redis Publisher/Subscriber fans every yielded event to every replica's local delivery loop, so killing a replica mid-stream doesn't drop subscriber state on the survivors.",
	)

	demo.Step("Monitor + Admin — kill a replica mid-stream.").
		Note("- **Demonstrates:** Redis pub/sub fan-out keeps deliveries flowing through surviving replicas; nginx round-robin routes new connections to the survivors.\n- **Expected:** Redis MONITOR shows `publish mcpkit.events.chat.message ...` on every event. After killing replica 1, A/B/C subscriber windows keep printing without gaps. Start replica 1 again when done.").
		VerbatimVariants("Open Monitor first, then run the kill / start in Admin",
			demokit.MakeVariant("shell", "bash", `# Monitor window — leave running:
docker exec -it mcpkit-redis redis-cli MONITOR | grep mcpkit.events

# Admin window — kill, then restore:
docker compose kill event-server-1
docker compose start event-server-1`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 6 — Cursor durability.
	// -----------------------------------------------------------------

	demo.Section("Phase 6 — Cursor durability",
		"Postgres-backed event buffer is the single source of truth across replicas. Poll-mode subscribers can stop, restart on a different replica, and resume gap-free.",
	)

	demo.Step("A1-Poll — restart with the last cursor; resume gap-free.").
		Note("- **Demonstrates:** cross-replica cursor durability (any replica reads the same Postgres buffer).\n- **Expected:** after Ctrl+C, restart with `--start-cursor=<N>` and the poller resumes exactly where it left off, even if nginx routes the new connection to a different replica.").
		VerbatimVariants("Stop, note the cursor, restart",
			demokit.MakeVariant("shell", "bash", `make poller TENANT=A USERNAME=alice
# Ctrl+C — note the last cursor printed (call it N)
make poller TENANT=A USERNAME=alice -- --start-cursor=<N>`).Default(),
		)

	demo.Step("Admin — observe buffer TTL truncation.").
		Note("- **Demonstrates:** bounded replay (`POSTGRES_BUFFER_TTL=10m` in the compose); stale cursor → server returns `truncated:true` and client resyncs from `latest`.\n- **Expected:** after waiting past the TTL, restarting the poller with the old cursor produces a `truncated:true` response visible in the poller logs; it then continues from `latest`.").
		VerbatimVariants("Run this in Admin",
			demokit.MakeVariant("shell", "bash", `docker exec mcpkit-postgres psql -U postgres -d events \
  -c "SELECT source_name, min(cursor), count(*) FROM event_buffer GROUP BY source_name;"`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 7 — Subscription quota enforcement.
	// -----------------------------------------------------------------

	demo.Section("Phase 7 — Subscription quota enforcement",
		"`EVENTS_QUOTA_CAPS=chat.message=3` is wired in compose. The Redis-backed QuotaStore enforces this per-principal globally — the 4th subscribe rejects even when it lands on a different replica.",
	)

	demo.Step("Aarti × 4 — trip the subscription cap.").
		Note("- **Demonstrates:** cap is enforced GLOBALLY (Redis Lua-atomic INCR-with-check) — replica-locality of subscribes doesn't help bypass it.\n- **Expected:** first three windows print steady delivery; the fourth exits immediately with `-32013 ResourceExhausted limit=subscriptions max=3`. We use `aarti` (not alice/anand) so the existing subscriptions from Phases 2-3 don't already count toward her cap.").
		VerbatimVariants("Run in four sibling windows; the 4th rejects",
			demokit.MakeVariant("shell", "bash", `make webhook TENANT=A USERNAME=aarti   # window 1 — succeeds
make webhook TENANT=A USERNAME=aarti   # window 2 — succeeds
make webhook TENANT=A USERNAME=aarti   # window 3 — succeeds (at cap)
make webhook TENANT=A USERNAME=aarti   # window 4 — rejects with -32013`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 8 — Dynamic source topology.
	// -----------------------------------------------------------------

	demo.Section("Phase 8 — Dynamic source topology",
		"The events SDK lets you AddSource / RemoveSource at runtime. mcpkit ships `events.topology` as a meta-source that yields one event for every lifecycle mutation — observe topology through the same subscription primitives clients already know.",
	)

	demo.Step("Topology — subscribe to the topology stream (alex).").
		Note("- **Demonstrates:** `events.topology` is a normal source — any client can subscribe to it.\n- **Expected:** window sits silent right now (no sources have been added since boot). Will print `source.added` / `source.removed` events the moment Phase 8 fires them.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make poller EVENT=events.topology TENANT=A USERNAME=alex`).Default(),
		)

	demo.Step("Admin — add a real Discord source on replicas 1 and 3 only.").
		Note("- **Demonstrates:** operator-controlled source topology (replicas 1+3 own the Discord WebSocket; replica 2 deliberately skipped to expose per-replica divergence).\n- **Expected:** evctl prints per-replica responses showing the source was registered on 1 and 3 only. Topology window immediately prints `{\"type\":\"source.added\",\"name\":\"discord.message\",...}`.").
		VerbatimVariants("Requires DISCORD_BOT_TOKEN + DISCORD_CHANNEL_IDS exported",
			demokit.MakeVariant("shell", "bash", `make add-discord TOKEN=$DISCORD_BOT_TOKEN CHANNELS=$DISCORD_CHANNEL_IDS REPLICAS=1,3 TENANTS=asgard,camelot`).Default(),
		)

	demo.Step("Discord-Poll — subscribe to discord.message as Asgard (alice).").
		Note("- **Demonstrates:** dynamic source events flow through the same SSE + tenant scoping; subscribers on replica 2 (no Discord adapter) still receive events via Redis pubsub.\n- **Expected:** real Discord messages from the configured channels arrive, tagged for asgard. Send a test message in Discord; it shows here within ~1s.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make poller EVENT=discord.message TENANT=A USERNAME=alice`).Default(),
		)

	demo.Step("Admin — compare per-replica source views.").
		Note("- **Demonstrates:** adapter configs are per-replica state (no cross-replica gossip); the topology stream is what unifies the view.\n- **Expected:** replica 1 and replica 3 list `discord.message` with config metadata; replica 2 does NOT.").
		VerbatimVariants("Run these in turn",
			demokit.MakeVariant("shell", "bash", `make list-sources REPLICAS=1   # includes discord.message
make list-sources REPLICAS=2   # does NOT include discord.message
make list-sources REPLICAS=3   # includes discord.message`).Default(),
		)

	demo.Step("Admin — remove the Discord source.").
		Note("- **Demonstrates:** `evctl sources rm` tears down both registry membership AND the upstream Discord WebSocket session.\n- **Expected:** topology window prints `{\"type\":\"source.removed\",\"name\":\"discord.message\",...}`. The Discord-Poll window terminates with NotFound on its next poll cycle.").
		VerbatimVariants("Run this",
			demokit.MakeVariant("shell", "bash", `make rm-source SOURCE=discord.message REPLICAS=1,3`).Default(),
		)

	// -----------------------------------------------------------------
	// Phase 9 — Token revocation kills only affected subscribers.
	// -----------------------------------------------------------------

	demo.Section("Phase 9 — Token revocation kills only affected subscribers",
		"One Keycloak admin click fires TWO distinct revocation paths: introspection-cache eviction for poll-mode subscribers (~5s) and OIDC Back-Channel Logout for webhook subscribers (immediate).",
	)

	demo.Step("Browser + Admin — sign alice out in Keycloak admin and watch the asgard windows die.").
		Note("- **Demonstrates:** synchronously revocable bearer tokens — the demo's headline win over plain JWT.\n- **Expected:** within ~5s, A1-Poll exits with `token invalidated by AS (401)`. A2-Webhook receives a `{type:terminated}` envelope on its webhook stream and disconnects. B and C windows are entirely untouched — revocation is per-realm.").
		VerbatimVariants("Open the browser, then tail logs in Admin",
			demokit.MakeVariant("shell", "bash", `# Browser:
#   http://localhost:8180/admin/master/console/#/asgard/users
#   admin / admin → click 'alice' → Sessions → Sign out

# Admin window — see the back-channel logout fire:
docker compose logs -f event-server-1 | grep BCL`).Default(),
		)

	demo.Section("That's the demo",
		"You've now seen: producer/consumer split, per-tenant scoping on both delivery modes, cross-replica fan-out and resilience, durable cursors with bounded replay, globally-enforced subscription quotas, runtime source topology with the SDK's self-aware meta-stream, and synchronous token revocation. Everything is operator-runnable from sibling terminals — `make predemo` re-runs the prep at any time.",
	)

	demo.Execute()
}

