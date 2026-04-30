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
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/demokit/tui"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
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

// notifBroker fans `notifications/events/event` out to per-name channels
// so each step can subscribe just to what it cares about.
type notifBroker struct {
	mu     sync.Mutex
	byName map[string]chan map[string]any
}

func newNotifBroker() *notifBroker { return &notifBroker{byName: map[string]chan map[string]any{}} }

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

	demo.Section("Setup",
		"Start the events server in a separate terminal first:",
		"",
		"```",
		"Terminal 1:  make serve         # telegram-events server on :8080",
		"Terminal 2:  make demo          # this walkthrough",
		"```",
	)

	demo.Section("Why a separate telegram demo?",
		"The two demos share the same `experimental/ext/events` library and the same wire protocol. The differences are only in the payload shape (telegram has flat `chat_id` / `text`; discord has nested `author` and richer fields) and the bot SDK used to source events (telegram's `tgbotapi` long-poll vs discord's `discordgo` WebSocket).",
		"",
		"For the full protocol exposition (events/list, poll, secret modes, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).",
	)

	var (
		c      *client.Client
		broker = newNotifBroker()
	)

	// --- Step 1: Connect ---
	demo.Step("Connect to the events server").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("Same connection setup as discord. The notification broker fans `notifications/events/event` out by source name so each step subscribes to just what it cares about.").
		Run(func() (result *demokit.StepResult) {
			c = client.NewClient(mcpURL,
				core.ClientInfo{Name: "telegram-events-host", Version: "1.0"},
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

	// --- Step 2: Push delivery — telegram payload shape ---
	demo.Step("Push: inject a telegram message, observe SSE notification").
		Arrow("Receiver", "Server", "POST /inject (simulated telegram message)").
		DashedArrow("Server", "Host", "notifications/events/event { data: {chat_id, user, text, ...} }").
		Note("Telegram's payload is flat — chat_id, user, text — vs discord's nested author + content. Same library, same wire envelope, different Data shape (auto-derived from TelegramEventData).").
		Run(func() (result *demokit.StepResult) {
			gotEvent := broker.Subscribe("telegram.message", 4)

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
			case p := <-gotEvent:
				fmt.Printf("    name:    %v\n", p["name"])
				fmt.Printf("    cursor:  %v\n", p["cursor"])
				if d, ok := p["data"].(map[string]any); ok {
					fmt.Printf("    chat_id: %v\n", d["chat_id"])
					fmt.Printf("    user:    %v\n", d["user"])
					fmt.Printf("    text:    %q\n", d["text"])
				}
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no push notification within 3s\n")
			}
			return
		})

	// --- Step 3: Cursorless typing source ---
	demo.Step("Cursorless: telegram.typing emits cursor:null").
		Arrow("Receiver", "Server", "POST /inject?event=telegram.typing").
		DashedArrow("Server", "Host", "notifications/events/event { cursor: null }").
		Note("Telegram's typing chat-action is ephemeral — no replay value, no buffer. Same WithoutCursors() story as discord.typing; the wire payload differs only in shape.").
		Run(func() (result *demokit.StepResult) {
			gotEvent := broker.Subscribe("telegram.typing", 4)

			body := map[string]any{
				"chat_id": 100,
				"user":    "alice",
			}
			if err := postInject(injectURL, "telegram.typing", body); err != nil {
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
					fmt.Printf("    chat_id:    %v\n", d["chat_id"])
					fmt.Printf("    user:       %v\n", d["user"])
					fmt.Printf("    started_at: %v\n", d["started_at"])
				}
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no typing event within 3s\n")
			}
			return
		})

	// --- Step 4: Webhook + auto-refresh via Go SDK ---
	demo.Step("Webhook: subscribe via the typed Go SDK, receive a TelegramEventData").
		Arrow("Receiver", "Receiver", "spin up local httptest receiver on :random").
		Arrow("Host", "Server", "events/subscribe { mode: webhook, url, name: telegram.message }").
		DashedArrow("Server", "Host", "{ id, secret: <server-assigned>, refreshBefore }").
		Arrow("Receiver", "Server", "POST /inject (simulated message)").
		DashedArrow("Server", "Receiver", "POST <url> + HMAC signature headers (default: webhook-* per Standard Webhooks; opt-in: X-MCP-* via -webhook-header-mode mcp)").
		Note("The typed Receiver[TelegramEventData] decodes the wire envelope's Data field directly into TelegramEventData, so the consumer reads `ev.Data.Text` rather than re-parsing JSON. Same `Subscription` + `Receiver[Data]` pair as the discord webhook step — the only differences are the type parameter and the payload field names.").
		Run(func() (result *demokit.StepResult) {
			recv := eventsclient.NewReceiver[TelegramEventData]("")
			defer recv.Close()

			hookSrv := httptest.NewServer(recv)
			defer hookSrv.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
				EventName:   "telegram.message",
				CallbackURL: hookSrv.URL,
				SubID:       "demo-telegram-webhook",
			})
			if err != nil {
				fmt.Printf("    ERROR: subscribe failed: %v\n", err)
				return
			}
			defer sub.Stop()
			recv.SetSecret(sub.Secret())
			fmt.Printf("    server-assigned secret:  %s...\n", truncate(sub.Secret(), 16))
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
				fmt.Printf("    Receiver got typed event:\n")
				fmt.Printf("      name:    %s\n", ev.Name)
				fmt.Printf("      eventId: %s\n", ev.EventID)
				fmt.Printf("      cursor:  %s\n", ev.CursorStr())
				fmt.Printf("      chat_id: %s\n", ev.Data.ChatID)
				fmt.Printf("      user:    %s\n", ev.Data.User)
				fmt.Printf("      text:    %q\n", ev.Data.Text)
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no webhook delivery within 3s\n")
				return
			}
			return
		})

	demo.Section("More",
		"For the full protocol walkthrough (events/list, poll, secret modes, header modes, the spec's design rationale) see [`examples/events/discord/WALKTHROUGH.md`](../discord/WALKTHROUGH.md).",
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
