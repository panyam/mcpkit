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
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// filterFlags strips the dispatcher flags so the inner serve flag.Parse
// doesn't choke on them.
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

// runDemo drives a lighter walkthrough than the discord version — the
// protocol is the same, so this one focuses on the telegram-specific
// payload shape (chat_id, user, text) and the typed Go SDK at clients/go.
// For the full protocol exposition see examples/events/discord.
func runDemo() {
	serverURL := "http://localhost:8080"
	for i, arg := range os.Args[1:] {
		if arg == "--url" && i+2 < len(os.Args) {
			serverURL = os.Args[i+2]
		}
	}
	mcpURL := serverURL + "/mcp"
	injectURL := serverURL + "/inject"

	demo := demokit.New("MCP Events Extension — Telegram reference walkthrough").
		Dir("events/telegram").
		Description("A condensed walkthrough showing the same MCP Events extension wired against a Telegram-shaped event source. The protocol exposition lives in the discord walkthrough; this one focuses on the telegram-specific payload (chat_id, user, text) and the cursored vs cursorless distinction.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
			demokit.Actor("Receiver", "Local webhook receiver (this process)"),
		)

	demo.Section("Setup — two modes",
		"This walkthrough runs against either a test-mode server or a real Telegram bot.",
		"",
		"**Option A — Test mode** (no bot token needed). All steps run; the final live-interaction step skips with a 'no token' message. Drive synthetic events from a third terminal via `make inject` / `make inject-typing`.",
		"",
		"```",
		"Terminal 1:  make serve                                # server in test mode",
		"Terminal 2:  make demo                                 # this walkthrough",
		"Terminal 3:  make inject TEXT='hello'                  # message event",
		"             make inject-typing                        # typing event (cursorless, demo-only)",
		"```",
		"",
		"**Option B — Real bot mode** (requires `TELEGRAM_BOT_TOKEN`). Same walkthrough plus the live step captures real message events from a chat with the bot. Telegram's Bot API doesn't expose user typing events to bots, so the live step is message-only — see the live step's note for details.",
		"",
		"```",
		"Terminal 1:  TELEGRAM_BOT_TOKEN=... make serve         # server in bot mode",
		"Terminal 2:  make demo                                 # this walkthrough",
		"             # In Telegram: send a message to the bot. Live step captures it.",
		"```",
	)

	demo.Section("Why a separate telegram demo?",
		"The two demos share the same `experimental/ext/events` library and the same wire protocol. The differences are only in the payload shape (telegram has flat `chat_id` / `text`; discord has nested `author` and richer fields) and the bot SDK used to source events (telegram's `tgbotapi` long-poll vs discord's `discordgo` WebSocket).",
		"",
		"For the full protocol exposition (events/list, poll, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).",
	)

	var c *client.Client

	// --- Step 1: Connect ---
	demo.Step("Connect to the events server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("Plain MCP initialize over Streamable HTTP. Push delivery uses events/stream (a long-lived per-subscription POST that returns SSE), not the session GET stream — no transport-level wiring needed in the client.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(mcpURL,
				core.ClientInfo{Name: "telegram-events-host", Version: "1.0"},
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return
		})

	// --- Step 2: Push delivery via events/stream ---
	demo.Step("Push: open events/stream, inject a telegram message, observe per-call notifications").
		Arrow("Host", "Server", "events/stream { name: telegram.message }").
		DashedArrow("Server", "Host", "notifications/events/active { requestId, cursor }").
		Arrow("Receiver", "Server", "POST /inject (simulated telegram message)").
		DashedArrow("Server", "Host", "notifications/events/event { requestId, data: {chat_id, user, text, ...} }").
		Note("events/stream is a long-lived per-subscription POST returning SSE — see the discord walkthrough for the full protocol exposition. Telegram's flat payload (chat_id, user, text) wires through the same Stream() helper as discord's nested one; only the Data shape changes.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotEvent := make(chan events.Event, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "telegram.message",
				OnEvent:   func(ev events.Event) { gotEvent <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

			body := map[string]any{
				"chat_id": 100,
				"sender":  "alice",
				"text":    "hello from the telegram walkthrough",
			}
			if err := postInject(injectURL, "telegram.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case ev := <-gotEvent:
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    notifications/events/event params:\n%s\n", string(pretty))
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no push notification within 3s\n")
			}
			return
		})

	// --- Step 3: Cursorless typing source ---
	demo.Step("Cursorless: open events/stream for telegram.typing, observe cursor:null").
		Arrow("Host", "Server", "events/stream { name: telegram.typing }").
		DashedArrow("Server", "Host", "notifications/events/event { cursor: null }").
		Note("Telegram's typing chat-action is ephemeral — no replay value, no buffer. Same WithoutCursors() story as discord.typing. Wire-shape contract per spec L294: cursorless emits cursor:null, never an empty string or absent key.").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotEvent := make(chan events.Event, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "telegram.typing",
				OnEvent:   func(ev events.Event) { gotEvent <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

			body := map[string]any{
				"chat_id": 100,
				"user":    "alice",
			}
			if err := postInject(injectURL, "telegram.typing", body); err != nil {
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

	// --- Step 4: Webhook + auto-refresh via Go SDK ---
	demo.Step("Webhook: subscribe via the typed Go SDK, receive a TelegramEventData").
		Arrow("Receiver", "Receiver", "spin up local httptest receiver on :random").
		Arrow("Host", "Server", "events/subscribe { mode: webhook, url, secret: whsec_<client-supplied>, name: telegram.message }").
		DashedArrow("Server", "Host", "{ id, refreshBefore }   (response does NOT echo secret per spec)").
		Arrow("Receiver", "Server", "POST /inject (simulated message)").
		DashedArrow("Server", "Receiver", "POST <url> + HMAC signature headers (default: webhook-* per Standard Webhooks; opt-in: X-MCP-* via -webhook-header-mode mcp)").
		Note("The typed Receiver[TelegramEventData] decodes the wire envelope's Data field directly into TelegramEventData, so the consumer reads `ev.Data.Text` rather than re-parsing JSON. Same `Subscription` + `Receiver[Data]` pair as the discord webhook step — the only differences are the type parameter and the payload field names. Per spec, the SDK auto-generates a whsec_ secret when SubscribeOptions.Secret is empty (events.GenerateSecret).").
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			recv := eventsclient.NewReceiver[TelegramEventData]("")
			defer recv.Close()

			hookSrv := httptest.NewServer(recv)
			defer hookSrv.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
				EventName:   "telegram.message",
				CallbackURL: hookSrv.URL,
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
				// γ-2: unsubscribe by tuple (§"Unsubscribing" L509).
				_, _ = c.Call("events/unsubscribe", map[string]any{
					"name":     "telegram.message",
					"delivery": map[string]any{"url": hookSrv.URL},
				})
			}()
			recv.SetSecret(sub.Secret())
			fmt.Printf("    SDK-generated secret:    %s...\n", truncate(sub.Secret(), 16))
			fmt.Printf("    refreshBefore:           %s\n", sub.RefreshBefore().Format(time.RFC3339))

			body := map[string]any{
				"chat_id": 200,
				"sender":  "bob",
				"text":    "hello via webhook",
			}
			if err := postInject(injectURL, "telegram.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case ev := <-recv.Events():
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    webhook delivery (typed Event[TelegramEventData]):\n%s\n", string(pretty))
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no webhook delivery within 3s\n")
				return
			}
			return
		})

	// --- Step 5: Live Telegram interaction ---
	// Uses demokit's Timeout (safety upper bound) + Cancellable (press-enter
	// to end early in interactive non-TUI mode). Output streams live as
	// events arrive — demokit v0.0.8+ tees step stdout to the renderer in
	// real time rather than buffering until Run returns. Cancellable is
	// disabled in TUI mode because Bubble Tea owns stdin.
	demo.Step("Live Telegram interaction (real message from a Telegram chat)").
		Arrow("Telegram", "Server", "MessageCreate event (when you send a message to the bot)").
		DashedArrow("Server", "Host", "notifications/events/event { name: telegram.message, cursor: <new> }").
		Note("Requires the server to be running with -token + you having a chat open with the bot. No typing parallel here — Telegram's Bot API doesn't expose user typing events to bots (only the bot can send typing chat actions, not the other way around). Discord does have user-typing events; see ../discord/WALKTHROUGH.md for the live-typing demo. In --non-interactive mode this step skips the wait so CI runs aren't slowed.").
		Timeout(liveInteractionMaxWait).
		Cancellable(!tuiMode()).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if nonInteractive() {
				fmt.Printf("    Skipped in --non-interactive mode. Run without --non-interactive (and with the server in -token mode) to see live events.\n")
				return
			}

			fmt.Printf("    Open a chat with your bot in Telegram and send messages.\n")
			if tuiMode() {
				fmt.Printf("    Press Ctrl+C to exit when done. Will stop automatically after %s.\n\n", liveInteractionMaxWait)
			} else {
				fmt.Printf("    Press enter (here, in this terminal) when you're done capturing events.\n")
				fmt.Printf("    Will stop automatically after %s if you walk away.\n\n", liveInteractionMaxWait)
			}

			liveCtx, liveCancel := context.WithCancel(ctx.Ctx)
			defer liveCancel()

			gotMsg := make(chan events.Event, 16)
			stream, err := eventsclient.Stream(liveCtx, c, eventsclient.StreamOptions{
				EventName: "telegram.message",
				OnEvent:   func(ev events.Event) { gotMsg <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: open telegram.message stream: %v\n", err)
				return
			}
			defer stream.Stop()

			var msgSeen int
			for {
				select {
				case ev := <-gotMsg:
					msgSeen++
					var d map[string]any
					_ = json.Unmarshal(ev.Data, &d)
					fmt.Printf("    [message] %v in chat %v: %q\n", d["user"], d["chat_id"], d["text"])
				case <-ctx.Ctx.Done():
					if msgSeen == 0 {
						fmt.Printf("    No live events received. Make sure the server is started with -token and you have a chat open with the bot.\n")
					} else {
						fmt.Printf("\n    Captured %d message(s).\n", msgSeen)
					}
					return
				}
			}
		})

	demo.Section("More",
		"For the full protocol walkthrough (events/list, poll, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).",
		"",
		"Both demos share `experimental/ext/events` (library), `clients/go/` (Go SDK), and `clients/python/events_client.py` (Python SDK).",
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

