// Discord Events Reference Server + demokit walkthrough.
//
// Two-process architecture:
//
//	Terminal 1:  make serve         # discord-events server on :8080
//	Terminal 2:  make demo          # demokit walkthrough (--tui for the TUI)
//
// Without --serve, the binary runs the walkthrough against a server it
// expects at --url (default http://localhost:8080). Use --readme to
// regenerate WALKTHROUGH.md.
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
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
	token := flag.String("token", "", "Discord bot token (omit for test mode)")
	whTTL := flag.Duration("webhook-ttl", 0, "override webhook subscription TTL (default 60s; useful for driving the SDK refresh path in tests)")
	whSecretMode := flag.String("webhook-secret-mode", "server", "webhook secret mode: server | client | identity")
	whHeaderMode := flag.String("webhook-header-mode", "standard", "webhook header style: standard | mcp")
	whRootHex := flag.String("webhook-root", "", "hex-encoded master secret for identity mode (required when -webhook-secret-mode=identity)")
	flag.CommandLine.Parse(filterFlags(os.Args[1:]))

	secretMode, err := events.ParseSecretMode(*whSecretMode)
	if err != nil {
		log.Fatalf("invalid -webhook-secret-mode: %v", err)
	}
	headerMode, err := events.ParseHeaderMode(*whHeaderMode)
	if err != nil {
		log.Fatalf("invalid -webhook-header-mode: %v", err)
	}

	whOpts := []events.WebhookOption{
		events.WithWebhookSecretMode(secretMode),
		events.WithWebhookHeaderMode(headerMode),
	}
	if *whTTL > 0 {
		whOpts = append(whOpts, events.WithWebhookTTL(*whTTL))
		log.Printf("[server] webhook TTL overridden to %s", *whTTL)
	}
	if secretMode == events.WebhookSecretIdentity {
		if *whRootHex == "" {
			log.Fatalf("-webhook-root is required when -webhook-secret-mode=identity")
		}
		root, err := hex.DecodeString(*whRootHex)
		if err != nil {
			log.Fatalf("invalid -webhook-root: %v", err)
		}
		whOpts = append(whOpts, events.WithWebhookRoot(root))
	}
	log.Printf("[server] webhook modes: secret=%s headers=%s", secretMode, headerMode)

	webhooks := events.NewWebhookRegistry(whOpts...)
	source, yield := newDiscordSource()
	typingSource, yieldTyping := newDiscordTypingSource()

	var dg *discordgo.Session
	if *token != "" {
		var err error
		dg, err = discordgo.New("Bot " + *token)
		if err != nil {
			log.Fatalf("failed to create Discord session: %v", err)
		}

		dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
			if m.Author.ID == s.State.User.ID {
				return // ignore own messages
			}
			_ = yield(newDiscordEvent(m.GuildID, m.ChannelID, m.Author.Username, m.Content, time.Now()))
			// The yield above already broadcasts push + webhook via the
			// library's fanout hook. The resource notification stays explicit
			// because the events library doesn't know about MCP resources.
		})

		// TypingStart fires once when a user starts typing in a channel the
		// bot can see, then refires every ~8s if they keep typing. Powers the
		// live cursorless flow in the demokit walkthrough.
		dg.AddHandler(func(s *discordgo.Session, ts *discordgo.TypingStart) {
			if ts.UserID == s.State.User.ID {
				return // ignore the bot's own typing actions
			}
			username := "unknown"
			if member, err := s.State.Member(ts.GuildID, ts.UserID); err == nil && member != nil {
				if member.Nick != "" {
					username = member.Nick
				} else if member.User != nil {
					username = member.User.Username
				}
			}
			_ = yieldTyping(newDiscordTypingEvent(ts.GuildID, ts.ChannelID, username, time.Unix(int64(ts.Timestamp), 0)))
		})

		dg.Identify.Intents = discordgo.IntentsGuildMessages |
			discordgo.IntentsMessageContent |
			discordgo.IntentsGuildMessageTyping

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

	registerResources(srv, source)
	registerTools(srv, dg)

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

	// One endpoint, dispatch on ?event=<name>. Default is discord.message
	// for backwards compatibility with the older inject script. Body shape
	// varies per event — see the per-event branches below.
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
		eventName := r.URL.Query().Get("event")
		if eventName == "" {
			eventName = "discord.message"
		}

		switch eventName {
		case "discord.message":
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
			_ = yield(newDiscordEvent(msg.GuildID, msg.ChannelID, msg.Sender, msg.Text, time.Now()))
			srv.NotifyResourceUpdated("discord://messages/recent")

		case "discord.typing":
			var msg struct {
				GuildID   string `json:"guild_id"`
				ChannelID string `json:"channel_id"`
				User      string `json:"user"`
			}
			if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if msg.User == "" {
				msg.User = "injected-typing"
			}
			_ = yieldTyping(newDiscordTypingEvent(msg.GuildID, msg.ChannelID, msg.User, time.Now()))

		default:
			http.Error(w, fmt.Sprintf("unknown event %q (want discord.message or discord.typing)", eventName), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "event": eventName})
	})

	log.Printf("[server] discord-events listening on %s (MCP at /mcp)", *addr)

	go func() {
		if err := http.ListenAndServe(*addr, mux); err != nil {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("[server] shutting down")
}
