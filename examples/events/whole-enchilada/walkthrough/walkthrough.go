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

	demo := demokit.New("MCP Events — whole-enchilada stage 1 walkthrough").
		Dir("events/whole-enchilada").
		Description("Production-shape multi-tier reference: nginx fronts an event-server replica; a push-server tier injects synthetic chat + presence events; a webhook receiver demonstrates downstream delivery. Stage 1 ships with in-memory stores, single tenant, anonymous principal. Stages 2/3/4 add Keycloak, Postgres+Redis multi-replica, and an admin frontend without changing this directory shape.").
		Actors(
			demokit.Actor("Host", "MCP Host (this walkthrough)"),
			demokit.Actor("Nginx", "Frontdoor reverse proxy"),
			demokit.Actor("Server", "Event-server (single replica in stage 1)"),
			demokit.Actor("PushServer", "Push-server (synthetic chat + presence feeders)"),
			demokit.Actor("Receiver", "Example webhook consumer"),
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
