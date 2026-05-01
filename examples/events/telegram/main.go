// Telegram Events Reference Server + demokit walkthrough.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # telegram-events server on :8080
//	Terminal 2:  make demo          # demokit walkthrough (--tui for the TUI)
//
// Without --serve, the binary runs the walkthrough against a server it
// expects at --url (default http://localhost:8080). Use --readme to
// regenerate WALKTHROUGH.md.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	gohttp "github.com/panyam/servicekit/http"
)

const eventStoreCap = 1000

func main() {
	for _, arg := range os.Args[1:] {
		if strings.TrimSpace(arg) == "--serve" {
			serve()
			return
		}
	}
	runDemo()
}

func serve() {
	addr := flag.String("addr", ":8080", "listen address")
	token := flag.String("token", "", "Telegram bot token (omit for test mode)")
	whHeaderMode := flag.String("webhook-header-mode", "standard", "webhook header style: standard | mcp")
	flag.CommandLine.Parse(filterFlags(os.Args[1:]))

	headerMode, err := events.ParseHeaderMode(*whHeaderMode)
	if err != nil {
		log.Fatalf("invalid -webhook-header-mode: %v", err)
	}

	whOpts := []events.WebhookOption{
		events.WithWebhookHeaderMode(headerMode),
	}
	log.Printf("[server] webhook headers=%s; client-supplied secrets only", headerMode)

	webhooks := events.NewWebhookRegistry(whOpts...)
	source, yield := newTelegramSource()
	typingSource, yieldTyping := newTelegramTypingSource()

	var bot *tgbotapi.BotAPI
	if *token != "" {
		var err error
		bot, err = tgbotapi.NewBotAPI(*token)
		if err != nil {
			log.Fatalf("failed to create Telegram bot: %v", err)
		}
		log.Printf("[telegram] authorized as @%s", bot.Self.UserName)
	} else {
		log.Println("[telegram] no token provided — running in test mode")
	}

	srv := server.NewServer(
		core.ServerInfo{Name: "telegram-events", Version: "0.1.0"},
		server.WithSubscriptions(),
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
		server.WithRequestLogging(log.Default()),
	)

	registerResources(srv, source)
	(&ToolDelivery{Bot: bot}).Register(srv)

	events.Register(events.Config{
		Sources:  []events.EventSource{source, typingSource},
		Webhooks: webhooks,
		Server:   srv,
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
		server.WithEventStore(gohttp.NewMemoryEventStore(eventStoreCap)),
	))
	mux.HandleFunc("POST /webhook/telegram", func(w http.ResponseWriter, r *http.Request) {
		if handleTelegramWebhook(yield, r) {
			srv.NotifyResourceUpdated("telegram://messages/recent")
		}
		w.WriteHeader(http.StatusOK)
	})

	// One endpoint, dispatch on ?event=<name>. Default is telegram.message
	// for backwards compatibility with the older inject script. Body shape
	// varies per event — see the per-event branches below.
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
		eventName := r.URL.Query().Get("event")
		if eventName == "" {
			eventName = "telegram.message"
		}

		switch eventName {
		case "telegram.message":
			var msg struct {
				ChatID int64  `json:"chat_id"`
				Sender string `json:"sender"`
				Text   string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if msg.Sender == "" {
				msg.Sender = "injected"
			}
			now := time.Now()
			_ = yield(TelegramEventData{
				ChatID:    strconv.FormatInt(msg.ChatID, 10),
				User:      msg.Sender,
				Text:      msg.Text,
				Timestamp: now.Format(time.RFC3339),
			})
			srv.NotifyResourceUpdated("telegram://messages/recent")
			log.Printf("[inject] sender=%s text=%q", msg.Sender, msg.Text)

		case "telegram.typing":
			var msg struct {
				ChatID int64  `json:"chat_id"`
				User   string `json:"user"`
			}
			if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if msg.User == "" {
				msg.User = "injected-typing"
			}
			_ = yieldTyping(newTelegramTypingEvent(msg.ChatID, msg.User, time.Now()))

		default:
			http.Error(w, fmt.Sprintf("unknown event %q (want telegram.message or telegram.typing)", eventName), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "event": eventName})
	})

	log.Printf("[server] telegram-events listening on %s (MCP at /mcp)", *addr)
	if bot != nil {
		go startTelegramPolling(bot, yield)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// startTelegramPolling uses long-polling to receive updates from Telegram and
// yields each text message as a TelegramEventData event.
func startTelegramPolling(bot *tgbotapi.BotAPI, yield func(TelegramEventData) error) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	log.Println("[telegram] starting long-poll loop...")
	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		log.Printf("[telegram] update_id=%d has_message=%v", update.UpdateID, update.Message != nil)
		if update.Message == nil || update.Message.Text == "" {
			continue
		}
		ev := makeTelegramEvent(update.Message)
		if err := yield(ev); err != nil {
			log.Printf("[telegram] yield failed: %v", err)
			continue
		}
		log.Printf("[telegram] chat=%s sender=%s text=%q", ev.ChatID, ev.User, ev.Text)
	}
	log.Println("[telegram] poll loop ended")
}
