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
	"fmt"
	"os"
	"strings"

	"github.com/panyam/demokit"
	common "github.com/panyam/mcpkit/examples/common"
)

// expectedTokenEnvs is the set of bearer-token env vars the 4-terminal
// flow consumes. The walkthrough's first Step warns the operator about
// any that are missing — useful for catching "I forgot to run
// newtoken before make demo" early. Three tenants × {poller, webhook}
// = six terminals (T2..T7) with T1 being `make up` itself.
var expectedTokenEnvs = []string{
	"TOKEN_POLLER_TENANT_A",
	"TOKEN_POLLER_TENANT_B",
	"TOKEN_POLLER_TENANT_C",
	"TOKEN_WEBHOOK_TENANT_A",
	"TOKEN_WEBHOOK_TENANT_B",
	"TOKEN_WEBHOOK_TENANT_C",
}

// runDemo drives the demokit walkthrough against the running compose
// stack. serverURL / receiverURL are accepted for symmetry with the
// stage-1 signature; they are no longer used inside this binary
// because no MCP traffic originates here.
func runDemo(_ /*serverURL*/, _ /*receiverURL*/ string) {
	demo := demokit.New("MCP Events — whole-enchilada stage 2 walkthrough").
		Dir("events/whole-enchilada").
		Description("Production-shape multi-tier reference. nginx fronts the event-server tier; Keycloak provides three pre-configured OAuth realms (tenant-a, tenant-b, tenant-c). The stack comes up silent — operator-runnable synthetic drivers (`make drive-chat`, `make drive-presence`) start producing events from sibling terminals. This walkthrough guides you through a multi-terminal demo where each tenant gets its own poller and webhook receiver — per-tenant isolation is the headline.").
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

	demo.Step("Confirm your token env vars are loaded.").
		Note(buildTokenCheckMessage())

	demo.Step("Window 0 — start the synthetic chat driver.").
		Note("Stack came up silent. This is what makes events start flowing.\n\n```\nmake drive-chat\n```")

	demo.Step("Window A1 — Tenant A poller.").
		Note("Sees only `tenant-a` events; B and C never reach it.\n\n```\nmake poller TENANT=A TOKEN=$TOKEN_POLLER_TENANT_A\n```")

	demo.Step("Window B1 — Tenant B poller.").
		Note("Realm in the bearer is what scopes delivery.\n\n```\nmake poller TENANT=B TOKEN=$TOKEN_POLLER_TENANT_B\n```")

	demo.Step("Window C1 — Tenant C poller.").
		Note("Three terminals, three tenants — clean isolation on the wire.\n\n```\nmake poller TENANT=C TOKEN=$TOKEN_POLLER_TENANT_C\n```")

	demo.Step("Windows A2 / B2 / C2 — webhook receivers.").
		Note("Second delivery mode, same tenant scoping. An event for `tenant-a` lights up A1 and A2 only.\n\n```\nmake webhook TENANT=A TOKEN=$TOKEN_WEBHOOK_TENANT_A\nmake webhook TENANT=B TOKEN=$TOKEN_WEBHOOK_TENANT_B\nmake webhook TENANT=C TOKEN=$TOKEN_WEBHOOK_TENANT_C\n```")

	demo.Step("Manually inject from a sibling terminal — watch isolation in real time.").
		Note("A's inject lights up A1 + A2 only; B's lights up B1 + B2 only.\n\n```\nmake inject TENANT=A EVENT=chat.message TEXT='hi from A'\nmake inject TENANT=B EVENT=chat.message TEXT='hi from B'\nmake inject TENANT=C EVENT=presence.changed USER=carol STATE=online\n```")

	demo.Step("Kill a replica mid-stream — survivors keep delivering.").
		Note("Stack defaults to N=3. Kill replica 1; nginx round-robins to 2 + 3; Redis fan-out keeps every subscriber fed.\n\n```\ndocker exec -it mcpkit-redis redis-cli MONITOR | grep mcpkit.events    # in a sibling window\ndocker compose kill event-server-1\ndocker compose start event-server-1                                      # bring it back when done\n```")

	demo.Step("Cross-replica cursor — poll resumes on a different replica.").
		Note("Restart the poller with the last cursor; events resume gap-free even when nginx routes it elsewhere.\n\n```\nmake poller TENANT=A USERNAME=usera1 PASSWORD=usera1\n# Ctrl+C, note the last cursor printed\nmake poller TENANT=A USERNAME=usera1 PASSWORD=usera1 -- --start-cursor=<N>\n```")

	demo.Step("Buffer TTL — stale cursor returns `truncated:true`.").
		Note("Wait past `POSTGRES_BUFFER_TTL=10m`, restart with the old cursor — poller resyncs from `latest`.\n\n```\ndocker exec mcpkit-postgres psql -U postgres -d events \\\n  -c \"SELECT source_name, min(cursor), count(*) FROM event_buffer GROUP BY source_name;\"\n```")

	demo.Step("Trip the subscription cap — 4th subscribe rejects.").
		Note("Three webhook subscriptions for the same user succeed; the 4th gets `-32013 ResourceExhausted`.\n\n```\nmake webhook TENANT=A USERNAME=usera1 PASSWORD=usera1   # x3 in sibling windows; 4th rejects\n```")

	demo.Step("Window D — subscribe to the topology stream.").
		Note("Silent until a source is added / removed.\n\n```\nmake poller EVENT=events.topology TENANT=A TOKEN=$TOKEN_POLLER_TENANT_A\n```")

	demo.Step("Window E — add a real Discord source on replicas 1 and 3 only.").
		Note("Requires `DISCORD_BOT_TOKEN` + `DISCORD_CHANNEL_IDS` exported. Window D prints `source.added`; replicas 1 + 3 open Discord WebSocket sessions. Replica 2 is deliberately skipped to make the per-replica divergence demo-able.\n\n```\nmake add-discord TOKEN=$DISCORD_BOT_TOKEN CHANNELS=$DISCORD_CHANNEL_IDS REPLICAS=1,3 TENANTS=tenant-a,tenant-c\n```")

	demo.Step("Window G — poll discord.message as Tenant A.").
		Note("Sees real Discord traffic tagged for tenant-a. Subscribers on replica 2 (where Discord is NOT registered) ALSO see them — Redis pubsub fans cross-replica.\n\n```\nmake poller EVENT=discord.message TENANT=A TOKEN=$TOKEN_POLLER_TENANT_A\n```")

	demo.Step("Compare per-replica source views.").
		Note("Replicas 1 + 3 list `discord.message`; replica 2 does not. Adapter configs are per-replica state — the topology stream is what unifies them.\n\n```\nmake list-sources REPLICAS=1\nmake list-sources REPLICAS=2\nmake list-sources REPLICAS=3\n```")

	demo.Step("Remove the Discord source.").
		Note("Window D prints `source.removed`; the discord.message poller terminates with NotFound on its next cycle.\n\n```\nmake rm-source SOURCE=discord.message REPLICAS=1,3\n```")

	demo.Step("Revoke a token in Keycloak admin — affected windows die, others keep flowing.").
		Note("Open `http://localhost:8180/admin/master/console/#/tenant-a/users` (admin / admin), click `alice` → **Sessions** → **Sign out**. Within ~5s A1 (poller) exits with `token invalidated`; A2 (webhook) gets a `{type:terminated}` envelope via BCL. B and C are untouched.\n\n```\ndocker compose logs -f event-server-1 | grep BCL    # see the back-channel logout fire\n```")

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

// buildTokenCheckMessage scans os.Environ for the expected token env
// vars and returns either an "all set" confirmation or a clear list
// of missing variables with the acquisition commands. Called once per
// run from the Note text of Step 1 so the result is captured into the
// rendered doc — `make readme` shows operators what the check looks
// for even if they're reading the markdown out of context.
func buildTokenCheckMessage() string {
	var missing []string
	for _, name := range expectedTokenEnvs {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}

	if len(missing) == 0 {
		return "All six token env vars are set:\n\n```\n" +
			strings.Join(expectedTokenEnvs, "\n") + "\n```\n\n" +
			"Press Enter to start the 4-terminal demo."
	}

	var b strings.Builder
	b.WriteString("**Missing token env vars** — the 4-terminal demo below needs all six. ")
	b.WriteString("Open six terminals now and acquire tokens, then re-export them into THIS shell before continuing:\n\n```\n")
	for _, name := range missing {
		tenantLetter := strings.TrimPrefix(name, "TOKEN_POLLER_TENANT_")
		tenantLetter = strings.TrimPrefix(tenantLetter, "TOKEN_WEBHOOK_TENANT_")
		fmt.Fprintf(&b, "export %s=$(make newtoken TENANT=%s)\n", name, tenantLetter)
	}
	b.WriteString("```\n\n")
	b.WriteString("Each `make newtoken` opens a browser for the realm's login page; log in as ")
	b.WriteString("`alice@tenant-a` / `bob@tenant-b` / `carol@tenant-c` (passwords match the usernames in the demo realm JSONs).\n\n")
	b.WriteString("If you're scripting (CI / unattended), use the ROPC variant — same envs, no browser:\n\n```\n")
	for _, name := range missing {
		tenantLetter := strings.TrimPrefix(name, "TOKEN_POLLER_TENANT_")
		tenantLetter = strings.TrimPrefix(tenantLetter, "TOKEN_WEBHOOK_TENANT_")
		user := userForTenant(tenantLetter)
		fmt.Fprintf(&b, "export %s=$(make newtoken-ci TENANT=%s USER=%s PASSWORD=%s)\n", name, tenantLetter, user, user)
	}
	b.WriteString("```\n\nPress Enter once all six are exported — the walkthrough does NOT make MCP calls itself, so it will continue past this Step regardless; the subsequent Steps assume the envs exist when you copy/paste them into your terminals.")
	return b.String()
}

// userForTenant maps the trailing tenant letter ("A" / "B" / "C") to
// the pre-baked username in the realm JSON.
func userForTenant(letter string) string {
	switch letter {
	case "A":
		return "alice"
	case "B":
		return "bob"
	case "C":
		return "carol"
	default:
		return "alice"
	}
}
