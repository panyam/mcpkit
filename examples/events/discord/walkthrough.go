package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// liveInteractionMaxWait is the upper-bound on the live-interaction step.
// User can press enter at any point to end the capture early (in
// interactive non-TUI mode); otherwise the step ends after this duration.
// Skipped entirely in --non-interactive mode.
const liveInteractionMaxWait = 30 * time.Second

// runDemo drives the demokit walkthrough against a server that the user
// started separately via `make serve`. Steps walk through every events-spec
// feature mcpkit currently supports: list, push, poll, cursorless, webhook
// + auto-refresh, all driven through the typed Go SDK at clients/go.
func runDemo() {
	serverURL := common.ServerURL()
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
	)

	// --- Step 1: Connect ---
	demo.Step("Connect to the events server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("Plain MCP initialize over Streamable HTTP. We're not declaring any extension capability — events/* are server-side custom methods registered via experimental/ext/events. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(mcpURL,
				core.ClientInfo{Name: "discord-events-host", Version: "1.0"},
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
			var v any
			_ = json.Unmarshal(res.Raw, &v)
			pretty, _ := json.MarshalIndent(v, "    ", "  ")
			fmt.Printf("    events/list response:\n%s\n", string(pretty))
			return
		})

	// --- Step 3: Push delivery via events/stream ---
	demo.Step("Push: open events/stream, inject a message, observe per-call notifications").
		Arrow("Host", "Server", "events/stream { name: discord.message }").
		DashedArrow("Server", "Host", "notifications/events/active { requestId, cursor }").
		Arrow("Receiver", "Server", "POST /inject (simulated Discord message)").
		DashedArrow("Server", "Host", "notifications/events/event { requestId, eventId, ... }").
		DashedArrow("Host", "Server", "(close request) → StreamEventsResult final frame").
		Note(
			"events/stream is a long-lived JSON-RPC request — one per subscription. Spec §\"Push-Based Delivery\" L223-296.",
			"",
			"- Server confirms with notifications/events/active, then delivers events as notifications/events/event on the call's own SSE response stream.",
			"- Heartbeats fire every ≥30s carrying the source's current cursor so the client's persisted cursor advances during quiet periods.",
			"- Replaces the broadcast-to-all-listeners model from Phase 1; per-stream isolation comes for free since each stream is its own POST.",
			"- Typed Go SDK Stream() helper (experimental/ext/events/clients/go) threads the per-call notification hook (client.CallContext.WithNotifyHook) so callbacks fire only for THIS stream's notifications.",
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotEvent := make(chan events.Event, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "discord.message",
				OnEvent:   func(ev events.Event) { gotEvent <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

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
			case ev := <-gotEvent:
				if ev.Cursor != nil {
					cc := *ev.Cursor
					messageCursor = &cc
				}
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    notifications/events/event params:\n%s\n", string(pretty))
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
			// Re-indent the raw response so the demo output shows the actual
			// wire shape — events/poll response per spec L139-149: flat
			// {events, cursor, hasMore, [truncated], [nextPollSeconds]}.
			var v any
			_ = json.Unmarshal(res.Raw, &v)
			pretty, _ := json.MarshalIndent(v, "    ", "  ")
			fmt.Printf("    events/poll response:\n%s\n", string(pretty))
			return
		})

	// --- Step 5: Cursorless source ---
	demo.Step("Cursorless: open events/stream for typing, observe cursor:null on the wire").
		Arrow("Host", "Server", "events/stream { name: discord.typing }").
		DashedArrow("Server", "Host", "notifications/events/active { cursor: null }").
		Arrow("Receiver", "Server", "POST /inject?event=discord.typing").
		DashedArrow("Server", "Host", "notifications/events/event { cursor: null }").
		Note(
			"WithoutCursors() sources don't buffer; the wire emits cursor:null.",
			"",
			"- Push delivery via events/stream still works — there's just nothing to replay.",
			"- Heartbeats also carry cursor:null (spec L294: \"null for event types that do not support replay\").",
			"- Useful for ephemeral state (typing indicators, presence, current readings).",
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotEvent := make(chan events.Event, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "discord.typing",
				OnEvent:   func(ev events.Event) { gotEvent <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

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
			case ev := <-gotEvent:
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    notifications/events/event params:\n%s\n", string(pretty))
				if ev.Cursor != nil {
					fmt.Printf("    UNEXPECTED: cursorless events should wire as cursor:null\n")
				}
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no typing event within 3s\n")
			}
			return
		})

	// --- Step 5.5: Source-side health signals (ζ-7) ---
	demo.Step("Health signals: source bubbles a transient upstream failure → notifications/events/error").
		Arrow("Host", "Server", "events/stream { name: discord.message }").
		DashedArrow("Server", "Host", "notifications/events/active").
		Arrow("Receiver", "Server", "POST /inject?action=error").
		DashedArrow("Server", "Host", "notifications/events/event/error { requestId, error: { code, message } }").
		Note(
			"Sources bubble health via YieldError(err) (transient, stream stays open) and YieldTerminated(err) (terminal, stream closes).",
			"",
			"- Stream subscribers map onto notifications/events/error (spec L255+L261, transient) and notifications/events/terminated (spec L783-795, terminal).",
			"- Webhook subscribers don't see error envelopes (errors are upstream-side, not delivery-side); they DO see {type:terminated} control envelopes when the suspend state machine flips Active=false (ζ-6) or when the source itself terminates (ζ-7.3).",
			"- This walkthrough step exercises only the transient error path — calling `inject?action=terminate` would one-shot terminate the discord.message source, breaking subsequent walkthrough steps that depend on it. Full terminate flow is covered by TestE2EHealthSignalsEndToEnd in this demo's e2e_test.go.",
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotError := make(chan error, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "discord.message",
				OnError:   func(e error) { gotError <- e },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

			// Inject a transient upstream error.
			injectErrURL := injectURL + "?action=error&code=-32603&message=demo+upstream+failure"
			req, _ := http.NewRequest("POST", injectErrURL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			resp.Body.Close()

			select {
			case e := <-gotError:
				fmt.Printf("    notifications/events/error fired:\n      %v\n", e)
				fmt.Printf("    Stream is still open (transient): subsequent events would still arrive.\n")
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no notifications/events/error within 3s\n")
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
		Note(
			"clients/go provides Subscription (subscribe + auto-refresh) plus Receiver[Data] (typed inbound channel).",
			"",
			"- HMAC signing secret is client-supplied per spec; SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty.",
			"- Subscription.Secret() returns the value the SDK ended up using, so the receiver can verify with the same secret.",
			"- Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape — consumer reads `ev.Data.Content` directly, no re-parsing JSON.",
		).
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
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    webhook delivery (typed Event[DiscordEventData]):\n%s\n", string(pretty))
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
		Note(
			"delivery.secret is REQUIRED on every events/subscribe — no server-side fallback per spec.",
			"",
			"- Server rejects at subscribe time so a subscription never exists that produces unverifiable deliveries.",
			"- The Go SDK auto-generates a conforming whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty.",
			"- This step makes a raw client.Call to bypass the SDK and demonstrate the server-side validator directly.",
		).
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
		Note(
			"The validator enforces the full Standard Webhooks format: `whsec_` followed by base64 of 24-64 random bytes.",
			"",
			"- A non-prefixed value, a too-short value, or non-base64 garbage all fail with -32602 InvalidParams.",
			"- Catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.",
		).
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
		Note(
			"Spec §\"Subscription Identity\" → \"Key composition\" L363: \"There is no client-generated id — a subscription is fully determined by what it listens for, where it delivers, and who asked.\"",
			"",
			"- Server derives the id from (principal, name, params, url) and returns it.",
			"- Old SDKs sending an id field get a loud -32602 instead of a silent mis-keying.",
		).
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
		Note(
			"Counter-test: a freshly-generated whsec_ value is accepted.",
			"",
			"- Response carries the server-derived id (sub_<base64>) per spec §\"Subscription Identity\" → \"Derived id\" L367.",
			"- The id is non-load-bearing for security; surfaced as X-MCP-Subscription-Id on delivery POSTs (γ-4 wires the header).",
			"- Response does NOT echo the secret — the client supplied it. Echoing would risk leaks via proxies / logs / IDE network panes.",
		).
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
			var v any
			_ = json.Unmarshal(res.Raw, &v)
			pretty, _ := json.MarshalIndent(v, "    ", "  ")
			fmt.Printf("    events/subscribe response (server-derived id, no secret echo per spec):\n%s\n", string(pretty))

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
		Note(
			"Setup: start the server with a Discord bot token and invite the bot to a channel you can post in.",
			"",
			"```",
			"DISCORD_BOT_TOKEN=<your-token> make serve",
			"```",
			"",
			"Bot setup (token + invite URL) is documented in this demo's README.md.",
			"",
			"- TypingStart handler in main.go yields a cursorless discord.typing event; MessageCreate yields the cursored discord.message.",
			"- Discord's typing indicator fires once when you start (then refires every ~8s if you keep typing), not per keystroke.",
			"- --non-interactive mode skips the wait so CI runs aren't slowed.",
		).
		Timeout(liveInteractionMaxWait).
		Cancellable(!demokit.IsTUI()).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if demokit.IsNonInteractive() {
				fmt.Printf("    Skipped in --non-interactive mode. Run without --non-interactive (and with the server in -token mode) to see live events.\n")
				return
			}

			fmt.Printf("    Go to your Discord channel and start typing — typing indicators will show up here.\n")
			if demokit.IsTUI() {
				fmt.Printf("    Press Ctrl+C to exit when done. Will stop automatically after %s.\n\n", liveInteractionMaxWait)
			} else {
				fmt.Printf("    Press enter (here, in this terminal) when you're done capturing events.\n")
				fmt.Printf("    Will stop automatically after %s if you walk away.\n\n", liveInteractionMaxWait)
			}

			// Two concurrent events/stream calls against the same MCP
			// session — each is a separate POST holding its own SSE
			// response. requestId is the demux key on stdio; on
			// Streamable HTTP each stream's notifications already
			// arrive on its own response so per-call hooks naturally
			// isolate. Per spec L271: "A client MAY hold multiple
			// concurrent events/stream requests open, one per
			// subscription."
			liveCtx, liveCancel := context.WithCancel(ctx.Ctx)
			defer liveCancel()

			gotMsg := make(chan events.Event, 16)
			gotTyping := make(chan events.Event, 16)

			msgStream, err := eventsclient.Stream(liveCtx, c, eventsclient.StreamOptions{
				EventName: "discord.message",
				OnEvent:   func(ev events.Event) { gotMsg <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: open discord.message stream: %v\n", err)
				return
			}
			defer msgStream.Stop()

			typingStream, err := eventsclient.Stream(liveCtx, c, eventsclient.StreamOptions{
				EventName: "discord.typing",
				OnEvent:   func(ev events.Event) { gotTyping <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: open discord.typing stream: %v\n", err)
				return
			}
			defer typingStream.Stop()

			var typingSeen, msgSeen int
			for {
				select {
				case ev := <-gotTyping:
					typingSeen++
					var d map[string]any
					_ = json.Unmarshal(ev.Data, &d)
					fmt.Printf("    [typing]  %v in channel %v\n", d["user"], d["channel_id"])
				case ev := <-gotMsg:
					msgSeen++
					var d map[string]any
					_ = json.Unmarshal(ev.Data, &d)
					fmt.Printf("    [message] %v: %q\n", d["author"], d["content"])
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

	common.SetupRenderer(demo)

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

