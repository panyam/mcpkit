// walkthrough is the demokit-driven walkthrough binary for the
// whole-enchilada demo, stage 1. It runs against the docker-compose
// stack (or against a locally-running event-server + receiver, see
// the leaf README) and walks a host through every events-spec feature
// the stage-1 server supports.
//
// Stage 2 will add Keycloak-authenticated subscribe + tenant routing
// callouts; stage 3 will add cross-replica cursor replay callouts;
// stage 4 will add push survival across container restart.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

const liveCaptureWindow = 10 * time.Second

// runDemo drives the demokit walkthrough against the running compose
// stack. Server URL defaults to http://localhost:8080 (the nginx
// frontdoor); receiver URL defaults to http://localhost:9090 (only
// reachable from outside the compose network when -receiver-host is
// overridden, but the walkthrough subscribes via the in-compose name).
func runDemo(serverURL, receiverURL string) {
	mcpURL := serverURL + "/mcp"

	demo := demokit.New("MCP Events — whole-enchilada stage 2 walkthrough").
		Dir("events/whole-enchilada").
		Description("Production-shape multi-tier reference: nginx fronts the event-server tier; a push-server tier injects synthetic chat + presence events; a Keycloak service provides three pre-configured OAuth realms (tenant-a, tenant-b, tenant-c); each tenant gets its own pollers and webhook receivers running in sibling terminals. Stage 2 ships with in-memory stores. Stage 3 plugs in Postgres + Redis multi-replica without changing this directory shape.").
		Actors(
			demokit.Actor("Host", "MCP Host (this walkthrough)"),
			demokit.Actor("Nginx", "Frontdoor reverse proxy"),
			demokit.Actor("Server", "Event-server (single replica in stage 2)"),
			demokit.Actor("PushServer", "Push-server (synthetic chat + presence feeders)"),
			demokit.Actor("Receiver", "Example webhook consumer"),
			demokit.Actor("Keycloak", "OAuth AS — three realms pre-imported on first start"),
			demokit.Actor("Operator", "The person running the demo from their terminal"),
		)

	demo.Section("Architecture in one diagram",
		"```",
		"Host  ──[MCP / SSE]──>  Nginx  ──>  Event-server  <──[HTTP /events/<name>/inject]──  Push-server",
		"                                          │",
		"                                          └──[webhook POST]──>  Receiver  (example consumer)",
		"```",
		"",
		"All four services run as separate containers in `docker-compose.yaml`. The push-server is just one example of a source manager — production deployments scale push-servers and event-servers independently, route via the same nginx, and persist into Postgres + Redis (stage 3). The receiver here is a tiny Go binary demonstrating Standard Webhooks verification; in production, **your receivers are your own apps** deployed in your own infrastructure — they are NOT part of the event-server tier.",
	)

	demo.Section("Setup",
		"Run from a separate terminal in the leaf directory:",
		"",
		"```",
		"make demo-up        # docker compose up -d, waits for healthchecks",
		"make demo           # this walkthrough (interactive TUI)",
		"# OR:",
		"make demo-test      # non-interactive run for CI / scripting",
		"```",
		"",
		"`make demo-down` tears the stack down (`-v` removes named volumes).",
		"",
		"This binary demonstrates the **events protocol mechanics** end-to-end (poll, push/SSE, webhook). The **stage-2 tenant-isolation story** is best experienced by hand — see the next section.",
	)

	demo.Section("Stage-2 4-terminal demo (run by hand)",
		"The walkthrough binary you're reading runs as a single MCP host doing every protocol thing. The *operator-facing* stage-2 demo splits that work across separate terminals — one per tenant — so per-tenant isolation is visually obvious. From the leaf:",
		"",
		"```",
		"# T1: stack up",
		"make demo-up",
		"",
		"# T2: tenant A poller",
		"TA=$(make newtoken TENANT=A)         # browser opens, log in as alice@tenant-a",
		"make poller TENANT=A TOKEN=$TA       # long-running — only sees tenant-a events",
		"",
		"# T3: tenant B poller (in another terminal)",
		"TB=$(make newtoken TENANT=B)",
		"make poller TENANT=B TOKEN=$TB",
		"",
		"# T4: tenant A webhook receiver (in another terminal)",
		"make webhook TENANT=A TOKEN=$TA",
		"",
		"# T5: tenant B webhook receiver (in another terminal)",
		"make webhook TENANT=B TOKEN=$TB",
		"",
		"# T6: operator injects events from the host",
		"make inject TENANT=A EVENT=chat.message TEXT='hi from A'",
		"# → T2 and T4 print the event. T3 and T5 stay quiet.",
		"make inject TENANT=B EVENT=chat.message TEXT='hi from B'",
		"# → T3 and T5 print. T2 and T4 stay quiet.",
		"```",
		"",
		"**Revocation walkthrough**: open `http://localhost:8180/admin/master/console/#/tenant-a/users` in a browser (admin / admin), click `alice` → Sessions tab → \"Sign out\". Within ~5 seconds (`OAUTH_CACHE_TTL`), T2 and T4 die with `-32012 Forbidden`. T3 and T5 keep flowing — tenant-B unaffected.",
		"",
		"For tenant C (carol@tenant-c) repeat the pattern. The push-server also auto-rotates events across all three tenants at its configured cadence, so leave the terminals running and watch the rotation.",
	)

	demo.Section("CI regression for tenant isolation",
		"`make demo-test` runs THIS binary end-to-end against the docker stack — it covers the protocol mechanics. The **tenant-isolation contract** is regression-tested by the event-server's e2e suite, which runs in-process with a fake token-as-tenant validator (no Docker needed):",
		"",
		"```",
		"make test     # event-server/... e2e tests, includes 8 tenant-isolation cases",
		"```",
		"",
		"That suite verifies (1) tagged events deliver only to matching tenants, (2) untagged events still deliver to all, (3) interleaved cross-tenant events don't leak. The docker stack adds the Keycloak interop layer on top.",
	)

	var (
		c             *client.Client
		messageCursor *string
	)

	demo.Step("How do I open the conversation?").
		Arrow("Host", "Nginx", "POST /mcp — initialize").
		DashedArrow("Nginx", "Server", "proxy_pass").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("Vanilla MCP initialize over Streamable HTTP, proxied transparently by nginx. The events extension declares no new capability; events/* methods are registered server-side.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(mcpURL, core.ClientInfo{Name: "whole-enchilada-host", Version: "1.0"})
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the stack with: make demo-up\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s (via %s)\n", c.ServerInfo.Name, c.ServerInfo.Version, serverURL)
			return
		})

	demo.Step("Which events does this server publish?").
		Arrow("Host", "Server", "events/list").
		DashedArrow("Server", "Host", "[chat.message (cursored), presence.changed (cursorless)]").
		Note("Two synthetic event types, fed by the push-server tier. chat.message is cursored (subscribers can replay); presence.changed is cursorless (live-only — cursor:null on the wire). Both are fed via the events.HTTPSource pattern: the push-server POSTs to /events/<name>/inject on the event-server.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			raw, err := c.Call("events/list", nil)
			if err != nil {
				fmt.Printf("    events/list error: %v\n", err)
				return nil
			}
			pretty, _ := json.MarshalIndent(json.RawMessage(raw.Raw), "    ", "  ")
			fmt.Printf("    %s\n", pretty)
			return nil
		})

	demo.Step("How do I read past chat messages? (poll)").
		Arrow("Host", "Server", "events/poll{name:chat.message, cursor:\"0\"}").
		DashedArrow("Server", "Host", "{events:[...], cursor:<head>, hasMore:false}").
		Note("Single-subscription poll; events accumulated by the push-server's chat feeder since the stack started. The cursor advances on every yield.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			raw, err := c.Call("events/poll", map[string]any{
				"name":   "chat.message",
				"cursor": "0",
				"limit":  5,
			})
			if err != nil {
				fmt.Printf("    events/poll error: %v\n", err)
				return nil
			}
			var pr struct {
				Events  []json.RawMessage `json:"events"`
				Cursor  *string           `json:"cursor"`
				HasMore bool              `json:"hasMore"`
			}
			_ = json.Unmarshal(raw.Raw, &pr)
			fmt.Printf("    got %d events; head cursor=%v hasMore=%v\n", len(pr.Events), strOr(pr.Cursor, "<nil>"), pr.HasMore)
			messageCursor = pr.Cursor
			return nil
		})

	demo.Step("How do I follow chat live? (push via SSE)").
		Arrow("Host", "Server", "events/stream{name:chat.message, cursor:nil}").
		DashedArrow("Server", "Host", "SSE: notifications/events/event ...").
		Note("Long-lived stream. The push-server feeds new chat events into the event-server's HTTPSource every ~2s; the SSE stream surfaces each one as notifications/events/event. Heartbeats arrive on quiet sources to advance persisted cursors. Press enter (interactive) or wait 10s (test) to end capture.").
		Run(func(sctx demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), liveCaptureWindow)
			defer cancel()
			var seen int
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "chat.message",
				OnEvent: func(ev events.Event) {
					seen++
					fmt.Printf("    [%d] cursor=%s name=%s\n", seen, ev.CursorStr(), ev.Name)
				},
			})
			if err != nil {
				fmt.Printf("    events/stream error: %v\n", err)
				return nil
			}
			defer stream.Stop()
			<-ctx.Done()
			fmt.Printf("    captured %d chat events in %s\n", seen, liveCaptureWindow)
			return nil
		})

	demo.Step("How does cursorless presence look on the wire?").
		Arrow("Host", "Server", "events/poll{name:presence.changed, cursor:\"0\"}").
		DashedArrow("Server", "Host", "{events:[], cursor:null, hasMore:false}").
		Note("Cursorless sources always return empty + cursor:null from poll. Subscribers can listen for live presence transitions via push or webhook, but there is no replay — that matches the underlying semantics of presence state.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			raw, err := c.Call("events/poll", map[string]any{
				"name":   "presence.changed",
				"cursor": "0",
			})
			if err != nil {
				fmt.Printf("    events/poll error: %v\n", err)
				return nil
			}
			pretty, _ := json.MarshalIndent(json.RawMessage(raw.Raw), "    ", "  ")
			fmt.Printf("    %s\n", pretty)
			return nil
		})

	demo.Step("How do I subscribe a webhook? (server pushes to my own app)").
		Arrow("Host", "Server", "events/subscribe{name:chat.message, delivery:{type:webhook, url:..., secret:whsec_...}}").
		DashedArrow("Server", "Host", "{id, refreshBefore, ...}").
		Arrow("Server", "Receiver", "POST <url> + Standard Webhooks signature").
		Note("The receiver in this demo is just an example downstream consumer — a 30-line Go program verifying Standard Webhooks signatures. In production each tenant deploys their own receivers in their own infra. The walkthrough's URL points at the in-compose receiver via the host network.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
				EventName:   "chat.message",
				CallbackURL: receiverURL + "/webhook",
				Cursor:      messageCursor,
			})
			if err != nil {
				fmt.Printf("    subscribe error: %v\n", err)
				return nil
			}
			defer sub.Stop()
			fmt.Printf("    subscribed id=%s; waiting %s for deliveries...\n", sub.ID(), liveCaptureWindow)
			time.Sleep(liveCaptureWindow)
			received := pollReceiver(receiverURL)
			fmt.Printf("    receiver captured %d delivery(ies)\n", received)
			return nil
		})

	demo.Section("What stage 2 adds",
		"- Keycloak realm with multi-tenant subscriptions (events/subscribe rejected if not authenticated).",
		"- Tenant identifier flows from token claims into source/subscription scoping.",
		"- Anonymous principal demo escape removed.",
		"- Per-tenant quota with the canonical -32013 ResourceExhausted wire shape pinned by kitchen-sink ({limit:\"subscriptions\", max:N}; see experimental/ext/events/errors.go's ResourceExhaustedData godoc). Same shape, same two emission paths (Reserve failure vs on_subscribe rejection) — a single client switch over (code, data) works for both demos.",
	)
	demo.Section("What stage 3 adds",
		"- Postgres-backed cursor / webhook / quota stores. Restart-survival for the demo.",
		"- Redis EventBus for cross-replica fanout. event-server scaled to N=3 replicas via docker compose --scale event-server=3.",
		"- nginx routes round-robin; subscribers reconnect to any replica without losing delivery.",
	)
	demo.Section("What stage 4 adds",
		"- M push-server replicas with admin-frontend-driven source bindings.",
		"- Admin web UI for per-tenant caps + rate limits + webhook config.",
		"- OTel collector + Jaeger + Grafana — trace spans hop from push-server through event-server through webhook delivery, visible end-to-end.",
		"- Push survival walkthrough: kill an event-server replica during the live step; nginx routes new connections to a sibling; resumed cursor replays the missed window.",
	)

	demo.Execute()
}

func strOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// pollReceiver hits the receiver's /__received drain endpoint and
// returns how many webhook deliveries were captured.
func pollReceiver(receiverURL string) int {
	resp, err := http.Get(receiverURL + "/__received")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var arr []json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&arr)
	return len(arr)
}
