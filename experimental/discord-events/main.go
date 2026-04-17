// Discord Events Reference Server — demonstrates all three MCP event delivery
// modes (push, poll, webhook) using mcpkit with Discord as the event source.
//
// Run:
//
//	go run . -addr :8080 -token YOUR_BOT_TOKEN
//
// Without -token, the server runs in test mode (no Discord connection).
// Connect an MCP client to http://localhost:8080/mcp.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/server"
	gohttp "github.com/panyam/servicekit/http"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	token := flag.String("token", "", "Discord bot token (omit for test mode)")
	flag.Parse()

	store := NewMessageStore(1000)
	webhooks := events.NewWebhookRegistry()

	var dg *discordgo.Session
	if *token != "" {
		var err error
		dg, err = discordgo.New("Bot " + *token)
		if err != nil {
			log.Fatalf("failed to create Discord session: %v", err)
		}

		// Register handler for incoming messages
		dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
			if m.Author.ID == s.State.User.ID {
				return // ignore own messages
			}
			store.Add(m.GuildID, m.ChannelID, m.Author.Username, m.Content, time.Now())
		})

		dg.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

		if err := dg.Open(); err != nil {
			log.Fatalf("failed to open Discord connection: %v", err)
		}
		defer dg.Close()
		log.Printf("[discord] connected as %s", dg.State.User.Username)
	} else {
		log.Println("[discord] no token provided — running in test mode")
	}

	srv := server.NewServer(
		core.ServerInfo{Name: "discord-events", Version: "0.1.0"},
		server.WithSubscriptions(),
		server.WithMiddleware(server.LoggingMiddleware(log.Default())),
		server.WithRequestLogging(log.Default()),
	)

	registerResources(srv, store)
	registerTools(srv, dg)

	events.Register(events.Config{
		Sources:  []events.EventSource{newDiscordSource(store)},
		Webhooks: webhooks,
		Server:   srv,
	})

	store.OnMessage = func(msg Message) {
		event := messageToEvent(msg)
		events.Emit(srv, event)
		srv.NotifyResourceUpdated("discord://messages/recent")
		events.EmitToWebhooks(webhooks, event)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", srv.Handler(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
		server.WithEventStore(gohttp.NewMemoryEventStore(1000)),
	))

	// Direct injection endpoint
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			GuildID   string `json:"guild_id"`
			ChannelID string `json:"channel_id"`
			Sender    string `json:"sender"`
			Text      string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if msg.Sender == "" {
			msg.Sender = "injected"
		}
		id := store.Add(msg.GuildID, msg.ChannelID, msg.Sender, msg.Text, time.Now())
		log.Printf("[inject] id=%d sender=%s text=%q", id, msg.Sender, msg.Text)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "ok"})
	})

	log.Printf("[server] discord-events listening on %s (MCP at /mcp)", *addr)

	go func() {
		if err := http.ListenAndServe(*addr, mux); err != nil {
			log.Fatal(err)
		}
	}()

	// Wait for interrupt
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("[server] shutting down")
}
