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

	demo.Section("Architecture in one diagram",
		"```",
		"Operator's terminals (poller, webhook, inject, drive-chat, drive-presence)",
		"           │",
		"           ▼",
		"     localhost:9090",
		"           │",
		"        Nginx ──────────────┐",
		"           │                │",
		"           ▼                ▼",
		"      Event-server     Keycloak",
		"                       (localhost:8180)",
		"```",
		"",
		"The walkthrough binary you're reading does **not** make MCP calls. The flow below has you run `make poller` / `make webhook` / `make inject` / `make drive-chat` / `make drive-presence` in sibling windows — those are the actual MCP clients + producers. This binary is the guide.",
	)

	demo.Step("Window 0 — start the synthetic chat driver.").
		Note("Stack came up silent. This is what makes events start flowing.\n\n```\nmake drive-chat\n```")

	demo.Step("Window A1 — Asgard poller (alice).").
		Note("Binary does its own ROPC login under USERNAME — PASSWORD defaults to USERNAME since realm seeds align. Sees only `asgard` events.\n\n```\nmake poller TENANT=A USERNAME=alice\n```")

	demo.Step("Window B1 — Babylon poller (bob).").
		Note("Realm in the bearer is what scopes delivery.\n\n```\nmake poller TENANT=B USERNAME=bob\n```")

	demo.Step("Window C1 — Camelot poller (carol).").
		Note("Three terminals, three tenants — clean isolation on the wire.\n\n```\nmake poller TENANT=C USERNAME=carol\n```")

	demo.Step("Windows A2 / B2 / C2 — webhook receivers (different users per tenant).").
		Note("Second delivery mode, same tenant scoping. An event for `asgard` lights up A1 and A2 only. Distinct users per role keep Keycloak's session table clean and avoid bumping into the subscription cap step later.\n\n```\nmake webhook TENANT=A USERNAME=anand\nmake webhook TENANT=B USERNAME=bhavna\nmake webhook TENANT=C USERNAME=chandan\n```")

	demo.Step("Manually inject from a sibling terminal — watch isolation in real time.").
		Note("A's inject lights up A1 + A2 only; B's lights up B1 + B2 only.\n\n```\nmake inject TENANT=A EVENT=chat.message TEXT='hi from A'\nmake inject TENANT=B EVENT=chat.message TEXT='hi from B'\nmake inject TENANT=C EVENT=presence.changed USER=carol STATE=online\n```")

	demo.Step("Kill a replica mid-stream — survivors keep delivering.").
		Note("Stack defaults to N=3. Kill replica 1; nginx round-robins to 2 + 3; Redis fan-out keeps every subscriber fed.\n\n```\ndocker exec -it mcpkit-redis redis-cli MONITOR | grep mcpkit.events    # in a sibling window\ndocker compose kill event-server-1\ndocker compose start event-server-1                                      # bring it back when done\n```")

	demo.Step("Cross-replica cursor — poll resumes on a different replica.").
		Note("Restart the poller with the last cursor; events resume gap-free even when nginx routes it elsewhere.\n\n```\nmake poller TENANT=A USERNAME=alice\n# Ctrl+C, note the last cursor printed\nmake poller TENANT=A USERNAME=alice -- --start-cursor=<N>\n```")

	demo.Step("Buffer TTL — stale cursor returns `truncated:true`.").
		Note("Wait past `POSTGRES_BUFFER_TTL=10m`, restart with the old cursor — poller resyncs from `latest`.\n\n```\ndocker exec mcpkit-postgres psql -U postgres -d events \\\n  -c \"SELECT source_name, min(cursor), count(*) FROM event_buffer GROUP BY source_name;\"\n```")

	demo.Step("Trip the subscription cap — 4th subscribe rejects.").
		Note("Three webhook subscriptions for the same user succeed; the 4th gets `-32013 ResourceExhausted`. Use aarti (a clean Asgard user not used elsewhere) so the existing alice/anand subscriptions don't interfere with the count.\n\n```\nmake webhook TENANT=A USERNAME=aarti   # x3 in sibling windows; 4th rejects\n```")

	demo.Step("Window D — subscribe to the topology stream.").
		Note("Silent until a source is added / removed. Tenant doesn't gate this stream (topology events carry no tenant tag).\n\n```\nmake poller EVENT=events.topology TENANT=A USERNAME=alex\n```")

	demo.Step("Window E — add a real Discord source on replicas 1 and 3 only.").
		Note("Requires `DISCORD_BOT_TOKEN` + `DISCORD_CHANNEL_IDS` exported. Window D prints `source.added`; replicas 1 + 3 open Discord WebSocket sessions. Replica 2 is deliberately skipped to make the per-replica divergence demo-able.\n\n```\nmake add-discord TOKEN=$DISCORD_BOT_TOKEN CHANNELS=$DISCORD_CHANNEL_IDS REPLICAS=1,3 TENANTS=asgard,camelot\n```")

	demo.Step("Window G — poll discord.message as Asgard.").
		Note("Sees real Discord traffic tagged for asgard. Subscribers on replica 2 (where Discord is NOT registered) ALSO see them — Redis pubsub fans cross-replica.\n\n```\nmake poller EVENT=discord.message TENANT=A USERNAME=alice\n```")

	demo.Step("Compare per-replica source views.").
		Note("Replicas 1 + 3 list `discord.message`; replica 2 does not. Adapter configs are per-replica state — the topology stream is what unifies them.\n\n```\nmake list-sources REPLICAS=1\nmake list-sources REPLICAS=2\nmake list-sources REPLICAS=3\n```")

	demo.Step("Remove the Discord source.").
		Note("Window D prints `source.removed`; the discord.message poller terminates with NotFound on its next cycle.\n\n```\nmake rm-source SOURCE=discord.message REPLICAS=1,3\n```")

	demo.Step("Revoke a token in Keycloak admin — affected windows die, others keep flowing.").
		Note("Open `http://localhost:8180/admin/master/console/#/asgard/users` (admin / admin), click `alice` → **Sessions** → **Sign out**. Within ~5s A1 (poller) exits with `token invalidated`; A2 (webhook) gets a `{type:terminated}` envelope via BCL. B and C are untouched.\n\n```\ndocker compose logs -f event-server-1 | grep BCL    # see the back-channel logout fire\n```")

	demo.Section("What stage 2 adds",
		"- Keycloak realm with multi-tenant subscriptions (every events/* method requires a real bearer token).",
		"- Tenant identifier flows from token claims (`core.Claims.Tenant`) into `OnSubscribe` scoping + the canonical webhook key.",
		"- Anonymous principal escape removed for the auth-wired path.",
		"- Per-tenant quota with the canonical `-32013 ResourceExhausted` wire shape pinned by kitchen-sink ({limit:\"subscriptions\", max:N}; see experimental/ext/events/errors.go's ResourceExhaustedData godoc).",
	)
	demo.Section("What stage 3 adds",
		"- Postgres-backed cursor / webhook / quota stores. Restart-survival for the demo.",
		"- Redis EventBus for cross-replica fanout. event-server scaled to N=3 replicas via `docker compose --scale event-server=3`.",
		"- nginx routes round-robin; subscribers reconnect to any replica without losing delivery.",
	)
	demo.Section("What stage 4 adds",
		"- M push-server replicas with admin-frontend-driven source bindings.",
		"- Admin web UI for per-tenant caps + rate limits + webhook config.",
		"- Push survival walkthrough: kill an event-server replica during the live step; nginx routes new connections to a sibling; resumed cursor replays the missed window.",
	)

	demo.Execute()
}

