package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// notifBroker is a tiny multiplexer for `notifications/events/event`. The
// MCP client only accepts a single notification callback at construction
// time, so the walkthrough sets one dispatcher that fans events out by
// name to per-step channels.
type notifBroker struct {
	mu      sync.Mutex
	byName  map[string]chan map[string]any
}

func newNotifBroker() *notifBroker { return &notifBroker{byName: map[string]chan map[string]any{}} }

// Subscribe returns a buffered channel for notifications/events/event with
// matching name. Caller is responsible for reading; the broker drops if
// the channel is full so it can never block the dispatcher.
func (b *notifBroker) Subscribe(name string, buf int) <-chan map[string]any {
	ch := make(chan map[string]any, buf)
	b.mu.Lock()
	b.byName[name] = ch
	b.mu.Unlock()
	return ch
}

func (b *notifBroker) dispatch(method string, params any) {
	if method != "notifications/events/event" {
		return
	}
	raw, _ := json.Marshal(params)
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	name, _ := p["name"].(string)
	b.mu.Lock()
	ch, ok := b.byName[name]
	b.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- p:
	default:
	}
}

// filterFlags strips the dispatcher flags so the inner flag.Parse on -addr
// (or the demo's flag.Parse) doesn't choke on them.
func filterFlags(args []string) []string {
	out := make([]string, 0, len(args))
	skip := false
	for _, a := range args {
		if skip {
			skip = false
			continue
		}
		switch a {
		case "--serve", "--tui", "--readme", "--non-interactive":
			continue
		case "--url":
			skip = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// liveInteractionMaxWait is the upper-bound on the live-interaction step.
// User can press enter at any point to end the capture early (in
// interactive non-TUI mode); otherwise the step ends after this duration.
// Skipped entirely in --non-interactive mode.
const liveInteractionMaxWait = 30 * time.Second

func nonInteractive() bool {
	for _, a := range os.Args[1:] {
		if strings.TrimSpace(a) == "--non-interactive" {
			return true
		}
	}
	return false
}

// tuiMode reports whether the walkthrough is running in TUI mode (Bubble
// Tea). In TUI Bubble Tea owns stdin, so demokit's Cancellable
// (press-enter cancel) would conflict — we keep Timeout but skip
// Cancellable in that mode.
func tuiMode() bool {
	for _, a := range os.Args[1:] {
		if strings.TrimSpace(a) == "--tui" {
			return true
		}
	}
	return false
}

// runDemo drives the demokit walkthrough against a server that the user
// started separately via `make serve`. Steps walk through every events-spec
// feature mcpkit currently supports: list, push, poll, cursorless, webhook
// + auto-refresh, all driven through the typed Go SDK at clients/go.
func runDemo() {
	serverURL := "http://localhost:8080"
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+2 < len(os.Args) {
			serverURL = os.Args[i+2]
		}
	}
	mcpURL := serverURL + "/mcp"
	injectURL := serverURL + "/inject"

	demo := demokit.New("MCP Events Extension — Discord reference walkthrough").
		Dir("events/discord").
		Description("Walks through the four delivery modes of the experimental MCP Events extension (events/list, push via SSE, poll, webhook with TTL refresh) plus the cursored vs cursorless source distinction. Webhook subscriber uses the typed Go SDK at experimental/ext/events/clients/go.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
			demokit.Actor("Receiver", "Local webhook receiver (this process)"),
		)

	demo.Section("Setup — two modes",
		"This walkthrough runs against either a test-mode server or a real Discord bot.",
		"",
		"**Option A — Test mode** (no bot token needed). All steps run; the final live-interaction step skips with a 'no token' message. Drive synthetic events from a third terminal via `make inject` / `make inject-typing`.",
		"",
		"```",
		"Terminal 1:  make serve                                # server in test mode",
		"Terminal 2:  make demo                                 # this walkthrough",
		"Terminal 3:  make inject TEXT='hello'                  # message event",
		"             make inject-typing                        # typing event (cursorless)",
		"```",
		"",
		"**Option B — Real bot mode** (requires `DISCORD_BOT_TOKEN`). Same walkthrough plus the live step captures real typing + message events from your Discord channel. Token setup in the demo's README.",
		"",
		"```",
		"Terminal 1:  DISCORD_BOT_TOKEN=... make serve          # server in bot mode",
		"Terminal 2:  make demo                                 # this walkthrough",
		"             # In Discord: type, then send. Live step captures both.",
		"```",
	)

	demo.Section("What this demo covers",
		"- **events/list** — the source catalog, including the new `cursorless` flag.",
		"- **Push** — long-lived SSE stream; `notifications/events/event` arrives in real time.",
		"- **Poll** — single-subscription `events/poll` (multi-sub batching is not supported).",
		"- **Cursorless source** — typing indicators that wire as `cursor: null`. Subscribers can't replay, only see live events.",
		"- **Webhook + auto-refresh** — `events/subscribe` with the typed `Subscription` + `Receiver[Data]` from `clients/go`.",
		"",
		"Identity-mode subscribe and Standard Webhooks header naming are exercised by the unit tests in `experimental/ext/events/` and by `discord-events`'s e2e tests; they require the server to be started with mode flags so they're documented in the README rather than driven from this walkthrough.",
	)

	var (
		c             *client.Client
		messageCursor *string
		broker        = newNotifBroker()
	)

	// --- Step 1: Connect ---
	demo.Step("Connect to the events server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("The mcpkit client opens a GET SSE stream so push notifications reach us during later steps. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. A small in-process broker fans out `notifications/events/event` by name so each step can subscribe just to what it cares about.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(mcpURL,
				core.ClientInfo{Name: "discord-events-host", Version: "1.0"},
				client.WithGetSSEStream(),
				client.WithNotificationCallback(broker.dispatch),
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return
		})

	// --- Step 2: events/list ---
	demo.Step("events/list — see the source catalog").
		Arrow("Host", "Server", "events/list").
		DashedArrow("Server", "Host", "[discord.message (cursored), discord.typing (cursorless)]").
		Note("The new `cursorless` flag (added in PR B) tells subscribers whether the source supports cursor-based replay. discord.message buffers events and accepts cursors; discord.typing emits ephemerally and always wires cursor:null.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			res, err := c.Call("events/list", map[string]any{})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var resp struct {
				Events []struct {
					Name        string   `json:"name"`
					Description string   `json:"description"`
					Delivery    []string `json:"delivery"`
					Cursorless  bool     `json:"cursorless"`
				} `json:"events"`
			}
			if err := json.Unmarshal(res.Raw, &resp); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			for _, e := range resp.Events {
				flag := "cursored"
				if e.Cursorless {
					flag = "CURSORLESS"
				}
				fmt.Printf("    %-20s %-12s delivery=%v\n", e.Name, "["+flag+"]", e.Delivery)
			}
			return
		})

	// --- Step 3: Push delivery ---
	demo.Step("Push: inject a message, observe SSE notification").
		Arrow("Receiver", "Server", "POST /inject (simulated Discord message)").
		DashedArrow("Server", "Host", "notifications/events/event (via SSE)").
		Note("The /inject endpoint is the demo's stand-in for a real Discord WebSocket handler; in production the bot's MessageCreate handler calls yield(). Either way, the YieldingSource fans out: the library installs an emit hook that broadcasts the event to all SSE subscribers.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			gotEvent := broker.Subscribe("discord.message", 4)

			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"sender":     "alice",
				"text":       "hello from the walkthrough",
			}
			if err := postInject(injectURL, "discord.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case p := <-gotEvent:
				if cur, ok := p["cursor"].(string); ok {
					cc := cur
					messageCursor = &cc
				}
				fmt.Printf("    name:    %v\n", p["name"])
				fmt.Printf("    eventId: %v\n", p["eventId"])
				fmt.Printf("    cursor:  %v\n", p["cursor"])
				if d, ok := p["data"].(map[string]any); ok {
					fmt.Printf("    text:    %v (from %v)\n", d["content"], d["author"])
				}
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no push notification within 3s\n")
			}
			return
		})

	// --- Step 4: Poll delivery ---
	demo.Step("Poll: events/poll with the cursor we just saw").
		Arrow("Host", "Server", "events/poll {subscriptions: [{name: discord.message, cursor: <head>}]}").
		DashedArrow("Server", "Host", "{events: [], cursor: <head>, hasMore: false}").
		Note("Single-subscription per call (PR B removed batching). Polling at the head returns no new events but advances the cursor — the same response shape that would carry events if any had arrived since the last poll.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			cursor := "0"
			if messageCursor != nil {
				cursor = *messageCursor
			}
			// δ-1: flat top-level shape per spec §"Poll-Based Delivery"
			// → "Request: events/poll" L139-149.
			res, err := c.Call("events/poll", map[string]any{
				"name":   "discord.message",
				"cursor": cursor,
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var resp struct {
				Cursor  *string `json:"cursor"`
				HasMore bool    `json:"hasMore"`
				Events  []any   `json:"events"`
			}
			_ = json.Unmarshal(res.Raw, &resp)
			cur := "(none)"
			if resp.Cursor != nil {
				cur = *resp.Cursor
			}
			fmt.Printf("    events:  %d  cursor: %s  hasMore: %v\n", len(resp.Events), cur, resp.HasMore)
			return
		})

	// --- Step 5: Cursorless source ---
	demo.Step("Cursorless: inject a typing event, observe cursor:null on the wire").
		Arrow("Receiver", "Server", "POST /inject?event=discord.typing").
		DashedArrow("Server", "Host", "notifications/events/event { cursor: null }").
		Note("WithoutCursors() sources don't buffer and emit cursor:null. Push and webhook fanout still work — there's just nothing to replay. Useful for ephemeral state (typing indicators, presence, current readings).").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			gotEvent := broker.Subscribe("discord.typing", 4)

			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"user":       "alice",
			}
			if err := postInject(injectURL, "discord.typing", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case p := <-gotEvent:
				cursorVal, present := p["cursor"]
				fmt.Printf("    name:        %v\n", p["name"])
				fmt.Printf("    cursor:      %v (present=%v)\n", cursorVal, present)
				if cursorVal != nil {
					fmt.Printf("    UNEXPECTED: cursorless events should wire as cursor:null\n")
				}
				if d, ok := p["data"].(map[string]any); ok {
					fmt.Printf("    user:        %v\n", d["user"])
					fmt.Printf("    started_at:  %v\n", d["started_at"])
				}
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no typing event within 3s\n")
			}
			return
		})

	// --- Step 6: Webhook + auto-refresh via Go SDK ---
	demo.Step("Webhook: subscribe via the typed Go SDK, observe HMAC delivery + auto-refresh").
		Arrow("Receiver", "Receiver", "spin up local httptest receiver on :random").
		Arrow("Host", "Server", "events/subscribe { mode: webhook, url, secret: whsec_<client-supplied> }").
		DashedArrow("Server", "Host", "{ id, refreshBefore }   (response does NOT echo secret per spec)").
		Arrow("Receiver", "Server", "POST /inject (simulated message)").
		DashedArrow("Server", "Receiver", "POST <url> + HMAC signature headers (default: webhook-* per Standard Webhooks; opt-in: X-MCP-* via -webhook-header-mode mcp)").
		DashedArrow("Host", "Host", "background loop: re-subscribe at 0.5 × TTL").
		Note("clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel). Per spec, the HMAC signing secret is client-supplied — the SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty, and exposes it via Subscription.Secret() so the receiver can verify with the same value. Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape, so the consumer reads `ev.Data.Content` rather than re-parsing JSON.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			recv := eventsclient.NewReceiver[DiscordEventData]("")
			defer recv.Close()

			hookSrv := httptest.NewServer(recv)
			defer hookSrv.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var refreshes int32
			sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
				EventName:     "discord.message",
				CallbackURL:   hookSrv.URL,
				RefreshFactor: 0.5,
				OnRefresh:     func() { atomic.AddInt32(&refreshes, 1) },
				// δ-3: bound worst-case replay on reconnect to 5 minutes
				// (§"Cursor Lifecycle" L529). Stored on WebhookTarget
				// for ζ's reconnect-with-replay logic.
				MaxAge: 5 * time.Minute,
			})
			if err != nil {
				fmt.Printf("    ERROR: subscribe failed: %v\n", err)
				return
			}
			// sub.Stop() ends the refresh loop but does NOT unsubscribe
			// server-side — the registry retains the subscription for its
			// TTL (~5 min) and keeps retrying delivery to the now-closed
			// httptest port. Eager unsubscribe avoids the noisy "connection
			// refused" retry log on the server side after the demo ends.
			defer func() {
				sub.Stop()
				// γ-2: unsubscribe by tuple (§"Unsubscribing" L509) —
				// (name, params, delivery.url). The derived id is not
				// accepted as input.
				_, _ = c.Call("events/unsubscribe", map[string]any{
					"name":     "discord.message",
					"delivery": map[string]any{"url": hookSrv.URL},
				})
			}()
			recv.SetSecret(sub.Secret())
			fmt.Printf("    SDK-generated secret:    %s...\n", truncate(sub.Secret(), 16))
			fmt.Printf("    refreshBefore:           %s\n", sub.RefreshBefore().Format(time.RFC3339))

			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"sender":     "bob",
				"text":       "hello via webhook",
			}
			if err := postInject(injectURL, "discord.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case ev := <-recv.Events():
				fmt.Printf("    Receiver got typed event:\n")
				fmt.Printf("      name:    %s\n", ev.Name)
				fmt.Printf("      eventId: %s\n", ev.EventID)
				fmt.Printf("      cursor:  %s\n", ev.CursorStr())
				fmt.Printf("      content: %q (from %s)\n", ev.Data.Content, ev.Data.Author.Username)
				// δ-4: per-event _meta from the source's metaFunc.
				if ev.Meta != nil {
					fmt.Printf("      _meta:   %v\n", ev.Meta)
				}
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no webhook delivery within 3s\n")
				return
			}

			fmt.Printf("    on_refresh callbacks fired so far: %d (initial subscribe + any auto-refreshes)\n",
				atomic.LoadInt32(&refreshes))
			return
		})

	// --- Step 7: Spec validation — empty secret rejected ---
	demo.Step("Spec validation: empty delivery.secret is rejected").
		Arrow("Host", "Server", "events/subscribe { delivery: { ... } }   (no secret)").
		DashedArrow("Server", "Host", "-32602 InvalidParams: delivery.secret is required").
		Note("Per spec, delivery.secret is REQUIRED on every events/subscribe — there is no server-side fallback. The server rejects at subscribe time so a subscription never exists that produces unverifiable deliveries. The Go SDK auto-generates a conforming whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty; this step makes a raw client.Call to bypass that and demonstrate the validator.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			_, err := c.Call("events/subscribe", map[string]any{
				"name": "discord.message",
				"delivery": map[string]any{
					"mode": "webhook",
					"url":  "http://localhost:1/sink",
				},
			})
			rpcErr, ok := err.(*client.RPCError)
			if !ok {
				fmt.Printf("    UNEXPECTED: want *client.RPCError, got %v\n", err)
				return
			}
			fmt.Printf("    code=%d  message=%q\n", rpcErr.Code, rpcErr.Message)
			if rpcErr.Code != -32602 {
				fmt.Printf("    UNEXPECTED: want code=-32602\n")
			}
			return
		})

	// --- Step 8: Spec validation — malformed secret rejected ---
	demo.Step("Spec validation: malformed delivery.secret is rejected").
		Arrow("Host", "Server", "events/subscribe { delivery: { secret: 'wrong' } }").
		DashedArrow("Server", "Host", "-32602 InvalidParams: delivery.secret invalid: must start with the whsec_ prefix").
		Note("The validator enforces the full Standard Webhooks format: whsec_ followed by base64 of 24-64 random bytes. A non-prefixed value, a too-short value, or non-base64 garbage all fail with -32602 InvalidParams. This is what catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			_, err := c.Call("events/subscribe", map[string]any{
				"name": "discord.message",
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    "http://localhost:1/sink",
					"secret": "wrong",
				},
			})
			rpcErr, ok := err.(*client.RPCError)
			if !ok {
				fmt.Printf("    UNEXPECTED: want *client.RPCError, got %v\n", err)
				return
			}
			fmt.Printf("    code=%d  message=%q\n", rpcErr.Code, rpcErr.Message)
			if rpcErr.Code != -32602 {
				fmt.Printf("    UNEXPECTED: want code=-32602\n")
			}
			return
		})

	// --- Step 9: Spec validation — client-supplied id is rejected (γ-3) ---
	demo.Step("Spec validation: client-supplied id is rejected").
		Arrow("Host", "Server", "events/subscribe { id: 'mine', ... }").
		DashedArrow("Server", "Host", "-32602 InvalidParams: client-supplied id is not accepted").
		Note("Per spec §\"Subscription Identity\" → \"Key composition\" L363: \"There is no client-generated id — a subscription is fully determined by what it listens for, where it delivers, and who asked.\" The server derives the id from (principal, name, params, url) and returns it; old SDKs sending an id field get a loud -32602 instead of a silent mis-keying.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			_, err := c.Call("events/subscribe", map[string]any{
				"id":   "client-picked-id",
				"name": "discord.message",
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    "http://localhost:1/sink",
					"secret": events.GenerateSecret(),
				},
			})
			rpcErr, ok := err.(*client.RPCError)
			if !ok {
				fmt.Printf("    UNEXPECTED: want *client.RPCError, got %v\n", err)
				return
			}
			fmt.Printf("    code=%d  message=%q\n", rpcErr.Code, rpcErr.Message)
			if rpcErr.Code != -32602 {
				fmt.Printf("    UNEXPECTED: want code=-32602\n")
			}
			return
		})

	// --- Step 10: Spec validation — valid secret accepted, response omits secret ---
	demo.Step("Spec validation: valid whsec_ accepted; response carries server-derived id, no secret").
		Arrow("Host", "Host", "events.GenerateSecret() → whsec_<base64 of 32 bytes>").
		Arrow("Host", "Server", "events/subscribe { delivery: { secret: whsec_<valid> } }").
		DashedArrow("Server", "Host", "{ id: sub_<base64-of-16-bytes>, cursor, refreshBefore }   (no secret per spec)").
		Note("Counter-test: a freshly-generated whsec_ value is accepted. The response carries the server-derived id (sub_<base64>) per spec §\"Subscription Identity\" → \"Derived id\" L367 — non-load-bearing for security, surfaced as X-MCP-Subscription-Id on delivery POSTs (γ-4 wires the header). The response does NOT echo the secret because the client already supplied it; echoing would risk leaks via proxies / logs / IDE network panes.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			supplied := events.GenerateSecret()
			fmt.Printf("    supplied secret:  %s...\n", truncate(supplied, 16))

			res, err := c.Call("events/subscribe", map[string]any{
				"name": "discord.message",
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    "http://localhost:1/sink",
					"secret": supplied,
				},
			})
			if err != nil {
				fmt.Printf("    UNEXPECTED: want success, got %v\n", err)
				return
			}
			var resp struct {
				ID            string `json:"id"`
				Secret        string `json:"secret"`
				RefreshBefore string `json:"refreshBefore"`
			}
			_ = json.Unmarshal(res.Raw, &resp)
			fmt.Printf("    response.id:            %q  (server-derived sub_<base64>)\n", resp.ID)
			fmt.Printf("    response.refreshBefore: %q\n", resp.RefreshBefore)
			fmt.Printf("    response.secret:        %q  (empty per spec)\n", resp.Secret)

			// Eagerly unsubscribe (γ-2: tuple form, no id) so the
			// subscription doesn't tie up a delivery target for a TTL
			// window after the demo ends.
			_, _ = c.Call("events/unsubscribe", map[string]any{
				"name":     "discord.message",
				"delivery": map[string]any{"url": "http://localhost:1/sink"},
			})
			return
		})

	// --- Step 11: Live Discord interaction ---
	// Uses demokit's Timeout (safety upper bound) + Cancellable (press-enter
	// to end early in interactive non-TUI mode). Output streams live as
	// events arrive — demokit v0.0.8+ tees step stdout to the renderer in
	// real time rather than buffering until Run returns. Cancellable is
	// disabled in TUI mode because Bubble Tea owns stdin.
	demo.Step("Live Discord interaction (typing + message from a real Discord channel)").
		Arrow("Discord", "Server", "TypingStart event (when you start typing in the channel)").
		DashedArrow("Server", "Host", "notifications/events/event { name: discord.typing, cursor: null }").
		Arrow("Discord", "Server", "MessageCreate event (when you press enter)").
		DashedArrow("Server", "Host", "notifications/events/event { name: discord.message, cursor: <new> }").
		Note("Requires the server to be running with -token + the bot invited to a channel you can post in. The TypingStart handler in main.go yields a cursorless discord.typing event; MessageCreate yields the cursored discord.message. Discord's typing indicator fires once when you start (then refires every ~8s if you keep typing), not per keystroke. In --non-interactive mode this step skips the wait so CI runs aren't slowed.").
		Timeout(liveInteractionMaxWait).
		Cancellable(!tuiMode()).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if nonInteractive() {
				fmt.Printf("    Skipped in --non-interactive mode. Run without --non-interactive (and with the server in -token mode) to see live events.\n")
				return
			}

			fmt.Printf("    Go to your Discord channel and start typing — typing indicators will show up here.\n")
			if tuiMode() {
				fmt.Printf("    Press Ctrl+C to exit when done. Will stop automatically after %s.\n\n", liveInteractionMaxWait)
			} else {
				fmt.Printf("    Press enter (here, in this terminal) when you're done capturing events.\n")
				fmt.Printf("    Will stop automatically after %s if you walk away.\n\n", liveInteractionMaxWait)
			}

			gotMsg := broker.Subscribe("discord.message", 16)
			gotTyping := broker.Subscribe("discord.typing", 16)

			var typingSeen, msgSeen int
			for {
				select {
				case p := <-gotTyping:
					typingSeen++
					if d, ok := p["data"].(map[string]any); ok {
						fmt.Printf("    [typing]  %v in channel %v\n", d["user"], d["channel_id"])
					}
				case p := <-gotMsg:
					msgSeen++
					if d, ok := p["data"].(map[string]any); ok {
						fmt.Printf("    [message] %v: %q\n", d["author"], d["content"])
					}
				case <-ctx.Ctx.Done():
					if typingSeen+msgSeen == 0 {
						fmt.Printf("    No live events received. Make sure the server is started with -token and the bot is invited to a channel you can post in.\n")
					} else {
						fmt.Printf("\n    Captured %d typing event(s) + %d message(s).\n", typingSeen, msgSeen)
					}
					return
				}
			}
		})

	demo.Section("Where each piece lives in mcpkit",
		"- Events library: `experimental/ext/events/`",
		"- Go client SDK (`Subscription`, `Receiver[Data]`): `experimental/ext/events/clients/go/`",
		"- Python client SDK (`WebhookSubscription` class + `webhook` CLI): `experimental/ext/events/clients/python/events_client.py`",
		"- Demo source: `examples/events/discord/`",
		"- Companion demo: `examples/events/telegram/` (lighter walkthrough — same protocol, different bot SDK)",
	)

	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--tui" {
			demo.WithRenderer(tui.New())
			break
		}
	}

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

// postInject is a tiny helper to POST a JSON body to /inject?event=<name>.
// Used by the steps that simulate an external event source.
func postInject(injectURL, eventName string, body map[string]any) error {
	raw, _ := json.Marshal(body)
	url := injectURL + "?event=" + eventName
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("inject returned %d", resp.StatusCode)
	}
	return nil
}

// truncate returns a short prefix for display.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

