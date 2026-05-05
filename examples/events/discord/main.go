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
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/ext/auth"
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
	whHeaderMode := flag.String("webhook-header-mode", "standard", "webhook header style: standard | mcp")
	flag.CommandLine.Parse(demokit.FilterArgs(os.Args[1:],
		demokit.BoolFlag("--serve"),
		demokit.ValueFlag("--url"),
		demokit.ValueFlag("--token"),
		demokit.ValueFlag("--webhook-ttl"),
		demokit.ValueFlag("--webhook-header-mode"),
		demokit.ValueFlag("--addr"),
	))

	logger := common.NewMCPLogger("[mcp] ")

	headerMode, err := events.ParseHeaderMode(*whHeaderMode)
	if err != nil {
		log.Fatalf("invalid -webhook-header-mode: %v", err)
	}

	whOpts := []events.WebhookOption{
		events.WithWebhookHeaderMode(headerMode),
		// ζ-1 demo escape: the demo's webhook step subscribes to local
		// httptest receivers (127.0.0.1:N), which the production-default
		// dial-time SSRF guard would block. Production deployments leave
		// this OFF so loopback/private-IP webhook URLs are rejected at
		// dial per spec §"Webhook Security" L464.
		events.WithWebhookAllowPrivateNetworks(true),
	}
	if *whTTL > 0 {
		whOpts = append(whOpts, events.WithWebhookTTL(*whTTL))
		log.Printf("[server] webhook TTL overridden to %s", *whTTL)
	}
	log.Printf("[server] webhook headers=%s; client-supplied secrets only", headerMode)

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

	// γ-5: auto-detect auth posture.
	//
	// If OAUTH_ISSUER is set, wire real OIDC auth via server.WithAuth(...)
	// and follow the spec strictly — anonymous webhook subscribes are
	// rejected with -32012 per §"Subscription Identity" → "Authentication
	// required" L361. Otherwise fall back to the demo escape hatch so
	// `make demo` works end-to-end without an auth provider.
	srvOpts := []server.Option{
		server.WithListen(*addr),
		server.WithSubscriptions(),
	}
	srvOpts = append(srvOpts, common.WithMCPLogging(logger)...)
	authPosture := "demo (anonymous → UnsafeAnonymousPrincipal)"
	if validator := tryEnableAuth(); validator != nil {
		srvOpts = append(srvOpts, server.WithAuth(validator))
		authPosture = "real OIDC (" + os.Getenv("OAUTH_ISSUER") + ") — anonymous webhook subscribes rejected per spec"
	}

	srv := server.NewServer(
		core.ServerInfo{Name: "discord-events", Version: "0.1.0"},
		srvOpts...,
	)
	log.Printf("[server] auth: %s", authPosture)

	registerResources(srv, source)
	registerTools(srv, dg)

	cfg := events.Config{
		Sources:  []events.EventSource{source, typingSource},
		Webhooks: webhooks,
		Server:   srv,
	}
	// Demo escape hatch — only when no real auth is wired. Production
	// deployments leave UnsafeAnonymousPrincipal empty AND configure
	// real auth via server.WithAuth(...).
	if !hasOAuthEnv() {
		cfg.UnsafeAnonymousPrincipal = "demo-user"
	}
	events.Register(cfg)

	injectHandler := func(w http.ResponseWriter, r *http.Request) {
		eventName := r.URL.Query().Get("event")
		if eventName == "" {
			eventName = "discord.message"
		}

		// ζ-7.4: source-side health-signal injection. Demo's stand-in
		// for a real source bubbling upstream errors. Triggers
		// notifications/events/error or /terminated on push subscribers,
		// per spec §"Push-Based Delivery" → "Event Delivery" L255-271.
		// `?action=error` is transient (stream stays); `?action=terminate`
		// is one-shot terminal (stream closes; webhook subscribers get a
		// type:terminated control envelope).
		if action := r.URL.Query().Get("action"); action != "" {
			code := -32603
			if c := r.URL.Query().Get("code"); c != "" {
				if parsed, err := strconv.Atoi(c); err == nil {
					code = parsed
				}
			}
			message := r.URL.Query().Get("message")
			if message == "" {
				message = "demo-injected " + action
			}
			derr := events.EventDeliveryError{Code: code, Message: message}
			switch action {
			case "error":
				_ = source.YieldError(derr)
			case "terminate":
				_ = source.YieldTerminated(derr)
			default:
				http.Error(w, fmt.Sprintf("unknown action %q (want error or terminate)", action), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"status": "ok", "event": eventName, "action": action,
				"code": code, "message": message,
			})
			return
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
	}

	log.Printf("[server] discord-events listening on %s (MCP at /mcp)", *addr)

	if err := srv.ListenAndServe(
		server.WithStreamableHTTP(true),
		server.WithSSE(true),
		server.WithEventStore(gohttp.NewMemoryEventStore(eventStoreCap)),
		server.WithMux(func(mux *http.ServeMux) {
			mux.HandleFunc("POST /inject", injectHandler)
		}),
	); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

// hasOAuthEnv reports whether OAUTH_ISSUER is set (real-auth posture).
// Used by the events.Config wiring to decide whether to set the
// UnsafeAnonymousPrincipal demo escape.
func hasOAuthEnv() bool {
	return os.Getenv("OAUTH_ISSUER") != ""
}

// tryEnableAuth builds a JWTValidator from environment variables and
// returns it (so the caller can pass server.WithAuth(validator)).
// Returns nil if OAUTH_ISSUER is not set — caller falls back to the
// UnsafeAnonymousPrincipal demo escape.
//
// Recognized env vars:
//   OAUTH_ISSUER    REQUIRED. The OIDC issuer URL.
//                   For Keycloak: http://localhost:8081/realms/<realm>
//   OAUTH_JWKS_URL  Optional. Defaults to <issuer>/protocol/openid-connect/certs
//                   (Keycloak convention). Override for non-Keycloak providers.
//   OAUTH_AUDIENCE  Optional. Defaults to "mcp-events". Tokens MUST have
//                   this audience claim to be accepted.
func tryEnableAuth() *auth.JWTValidator {
	issuer := os.Getenv("OAUTH_ISSUER")
	if issuer == "" {
		return nil
	}
	jwksURL := os.Getenv("OAUTH_JWKS_URL")
	if jwksURL == "" {
		jwksURL = issuer + "/protocol/openid-connect/certs"
	}
	audience := os.Getenv("OAUTH_AUDIENCE")
	if audience == "" {
		audience = "mcp-events"
	}
	v := auth.NewJWTValidator(auth.JWTConfig{
		JWKSURL:  jwksURL,
		Issuer:   issuer,
		Audience: audience,
	})
	v.Start()
	return v
}
