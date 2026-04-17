// Telegram Events Reference Server — demonstrates all three MCP event delivery
// modes (push, poll, webhook) using mcpkit with Telegram as the event source.
//
// Run:
//
//	go run . -addr :8080 -token YOUR_BOT_TOKEN
//
// Without -token, the server runs in test mode (no Telegram connection).
// Connect an MCP client to http://localhost:8080/mcp.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	gohttp "github.com/panyam/servicekit/http"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	token := flag.String("token", "", "Telegram bot token (omit for test mode)")
	flag.Parse()

	store := NewMessageStore(1000)
	webhooks := events.NewWebhookRegistry()

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

	// Register MCP resources (telegram://messages/*)
	registerResources(srv, store)

	// Register send_message tool (Telegram action, not an event operation)
	(&ToolDelivery{Bot: bot}).Register(srv, store)

	// Register event protocol methods via the events library
	events.Register(events.Config{
		Sources:  []events.EventSource{newTelegramSource(store)},
		Webhooks: webhooks,
		Server:   srv,
	})

	// Wire fan-out: when a message arrives, push to SSE clients, notify
	// resource subscribers, and deliver to outbound webhooks.
	store.OnMessage = func(msg Message) {
		event := messageToEvent(msg)
		events.Emit(srv, event)
		srv.NotifyResourceUpdated("telegram://messages/recent")
		events.EmitToWebhooks(webhooks, event)
	}

	// Build HTTP mux: MCP server at /mcp, Telegram webhook at /webhook/telegram
	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
		server.WithEventStore(gohttp.NewMemoryEventStore(1000)),
	))
	mux.HandleFunc("POST /webhook/telegram", func(w http.ResponseWriter, r *http.Request) {
		handleTelegramWebhook(store, r)
		w.WriteHeader(http.StatusOK)
	})

	// Direct injection endpoint
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
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
		id := store.Add(msg.ChatID, msg.Sender, msg.Text, time.Now())
		log.Printf("[inject] id=%d sender=%s text=%q", id, msg.Sender, msg.Text)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "ok"})
	})

	log.Printf("[server] telegram-events listening on %s (MCP at /mcp)", *addr)
	if bot != nil {
		go startTelegramPolling(bot, store)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// startTelegramPolling uses long-polling to receive updates from Telegram.
func startTelegramPolling(bot *tgbotapi.BotAPI, store *MessageStore) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	log.Println("[telegram] starting long-poll loop...")
	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		log.Printf("[telegram] update_id=%d has_message=%v", update.UpdateID, update.Message != nil)
		if update.Message == nil || update.Message.Text == "" {
			continue
		}
		msg := update.Message
		sender := "unknown"
		if msg.From != nil {
			if msg.From.UserName != "" {
				sender = msg.From.UserName
			} else {
				sender = msg.From.FirstName
			}
		}
		id := store.Add(msg.Chat.ID, sender, msg.Text, time.Unix(int64(msg.Date), 0))
		log.Printf("[telegram] id=%d chat=%d sender=%s text=%q", id, msg.Chat.ID, sender, msg.Text)
	}
	log.Println("[telegram] poll loop ended")
}
