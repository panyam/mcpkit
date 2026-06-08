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
		Description("Production-shape multi-tier reference. nginx fronts the event-server tier; a push-server tier injects synthetic chat + presence events; Keycloak provides three pre-configured OAuth realms (tenant-a, tenant-b, tenant-c). This walkthrough guides you through a 4-terminal demo where each tenant gets its own poller and webhook receiver — per-tenant isolation is the headline.").
		Actors(
			demokit.Actor("Operator", "The person running the demo — you"),
			demokit.Actor("Nginx", "Frontdoor reverse proxy (localhost:9090)"),
			demokit.Actor("Server", "Event-server (introspection-mode auth wired)"),
			demokit.Actor("PushServer", "Push-server (synthetic chat + presence feeders)"),
			demokit.Actor("Keycloak", "OAuth AS — three realms pre-imported on first start (localhost:8180)"),
		)

	common.SetupRenderer(demo)

	demo.Section("Architecture in one diagram",
		"```",
		"Operator's terminals (poller, webhook, inject)",
		"           │",
		"           ▼",
		"     localhost:9090",
		"           │",
		"        Nginx ──────────────┐",
		"           │                │",
		"           ▼                ▼",
		"      Event-server     Keycloak",
		"           ▲           (localhost:8180)",
		"           │",
		"      Push-server   (auto-rotates events across tenants)",
		"```",
		"",
		"The walkthrough binary you're reading does **not** make MCP calls. The 4-terminal flow below has you run `make poller` / `make webhook` / `make inject` in sibling windows — those are the actual MCP clients. This binary is the guide.",
	)

	demo.Step("Confirm your token env vars are loaded.").
		Note(buildTokenCheckMessage())

	demo.Step("Window A1 — start the Tenant A poller.").
		Note("In a NEW terminal at this leaf, run:\n\n```\nmake poller TENANT=A TOKEN=$TOKEN_POLLER_TENANT_A\n```\n\nThe poller authenticates as Tenant A, polls `events/chat.message`, and prints every event it receives with the tenant tag visible. It only sees events whose tenant tag is `tenant-a`; events tagged for B or C never reach it. Leave this terminal visible — within a few seconds the push-server's synthetic chat feeder will rotate to tenant-a and you'll see events appear.")

	demo.Step("Window B1 — start the Tenant B poller.").
		Note("Same pattern, different realm:\n\n```\nmake poller TENANT=B TOKEN=$TOKEN_POLLER_TENANT_B\n```\n\nNow A1 and B1 sit side-by-side. A1 prints only tenant-a events; B1 prints only tenant-b. Same MCP server, same wire, same nginx — the realm in the bearer token is what scopes delivery.")

	demo.Step("Window C1 — start the Tenant C poller.").
		Note("```\nmake poller TENANT=C TOKEN=$TOKEN_POLLER_TENANT_C\n```\n\nThree pollers, three tenants. As the push-server cycles through tenants, each event lights up exactly one of your three terminals — clean isolation across the wire.")

	demo.Step("Windows A2 / B2 / C2 — start the webhook receivers.").
		Note("Webhook is the second delivery mode. It runs in parallel with poll; same tenant routing applies:\n\n```\n# Window A2\nmake webhook TENANT=A TOKEN=$TOKEN_WEBHOOK_TENANT_A\n\n# Window B2\nmake webhook TENANT=B TOKEN=$TOKEN_WEBHOOK_TENANT_B\n\n# Window C2\nmake webhook TENANT=C TOKEN=$TOKEN_WEBHOOK_TENANT_C\n```\n\nEach receiver registers a webhook subscription on a random local port, the event-server signs every delivery with the per-subscription HMAC secret, and the receivers verify + print. You now have six terminals: 3 pollers × 3 webhooks. An event for `tenant-a` lights up A1 and A2 only.")

	demo.Step("Inject events from a 7th terminal and watch isolation in real time.").
		Note("```\nmake inject TENANT=A EVENT=chat.message TEXT='hi from A'\n```\n\nA1 + A2 print; B1/B2/C1/C2 stay quiet.\n\n```\nmake inject TENANT=B EVENT=chat.message TEXT='hi from B'\nmake inject TENANT=C EVENT=presence.changed USER=carol STATE=online\n```\n\nB events go only to B's windows; C events only to C's. The push-server is also cycling synthetic events through all three tenants in the background, so leave the windows running and you'll see the rotation interleave with your manual injects.")

	demo.Step("Revoke a token in Keycloak admin and watch the affected windows die.").
		Note("Open `http://localhost:8180/admin/master/console/#/tenant-a/users` (admin / admin) in a browser. Click `alice` → **Sessions** tab → **Sign out**.\n\nWithin ~5 seconds (`OAUTH_CACHE_TTL`), A1 + A2 (Tenant A's poller + webhook) exit with `-32012 Forbidden`. B1/B2/C1/C2 keep flowing — revocation is per-realm; isolation holds.\n\nThis is the **load-bearing introspection demo**: revocation is synchronously visible to the resource server because the event-server's `IntrospectionValidator` calls Keycloak's `/introspect` on every request (cached for `OAUTH_CACHE_TTL`). JWT validation can't make this claim — JWT tokens stay valid until natural expiry.\n\nRe-acquire a token (`TOKEN_POLLER_TENANT_A=$(make newtoken TENANT=A)`) and restart the poller to reconnect.")

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
