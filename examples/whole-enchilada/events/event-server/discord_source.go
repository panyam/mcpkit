package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// DiscordSourceConfig is the runtime configuration the admin API hands
// to newDiscordSource. All fields are mandatory except SourceName +
// Tenants (Tenants nil → no tenant tagging; SourceName empty →
// "discord.message").
type DiscordSourceConfig struct {
	// BotToken is the Discord application's bot token. Never logged
	// or echoed in admin responses — list-sources redacts it.
	BotToken string
	// ChannelIDs is the allowlist of Discord channel IDs whose
	// MessageCreate events get yielded. Messages from other channels
	// the bot has visibility into are silently dropped.
	ChannelIDs []string
	// Tenants is the round-robin tenant tag set. Each yielded event
	// rotates one tenant from this slice into DiscordMessage.Tenant,
	// matching the synth driver pattern so per-tenant subscribers
	// see ~1/N of events. Nil / empty → no tenant tag (events
	// deliver to every subscriber regardless of tenant).
	Tenants []string
	// SourceName is the EventDef.Name registered with the Registry.
	// Defaults to "discord.message". Multiple Discord adapters with
	// distinct bot tokens / channels can coexist on the same server
	// by registering under distinct names.
	SourceName string
}

// DiscordMessage is the wire payload yielded for each filtered
// MessageCreate event. Mirrors the existing examples/events/discord
// DiscordEventData fields but stripped to the demo essentials —
// guild, channel, author, text, timestamp + the tenant tag the
// event-server's tenantMatchFunc routes on.
type DiscordMessage struct {
	Tenant    string `json:"tenant,omitempty"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	Author    string `json:"author"`
	Content   string `json:"content"`
	Timestamp string `json:"ts"`
}

// discordSource bundles the Discord gateway connection with the
// EventSource the Registry holds. Owns the session lifecycle; the
// Registry only manages the source's registry membership.
type discordSource struct {
	cfg     DiscordSourceConfig
	session *discordgo.Session
	source  events.EventSource

	mu        sync.Mutex
	tenantIdx int
	channels  map[string]bool
}

func newDiscordSource(cfg DiscordSourceConfig) (*discordSource, error) {
	if cfg.BotToken == "" {
		return nil, errors.New("discord: BotToken required")
	}
	if len(cfg.ChannelIDs) == 0 {
		return nil, errors.New("discord: at least one ChannelID required")
	}
	if cfg.SourceName == "" {
		cfg.SourceName = "discord.message"
	}

	session, err := discordgo.New("Bot " + cfg.BotToken)
	if err != nil {
		return nil, fmt.Errorf("discord session: %w", err)
	}

	src, yield := events.NewYieldingSource[DiscordMessage](events.EventDef{
		Name:        cfg.SourceName,
		Description: "Discord channel messages from a runtime-registered bot token. Per-event tenant tagging rotates across the configured tenants for multi-tenant routing.",
		Delivery:    []string{"push", "poll", "webhook"},
		Meta:        map[string]any{"category": "messaging", "upstream": "discord"},
	}, events.WithMaxSize(1000))

	ds := &discordSource{
		cfg:      cfg,
		session:  session,
		source:   src,
		channels: make(map[string]bool, len(cfg.ChannelIDs)),
	}
	for _, c := range cfg.ChannelIDs {
		ds.channels[c] = true
	}

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		// Discord delivers every message the bot can see; filter to
		// the operator's allowlist BEFORE yielding so noisy guilds
		// don't flood the event-server.
		if m.Author == nil || s.State == nil || s.State.User == nil {
			return
		}
		if m.Author.ID == s.State.User.ID {
			return // ignore own messages
		}
		if !ds.channels[m.ChannelID] {
			return
		}
		tenant := ds.nextTenant()
		_ = yield(context.Background(), DiscordMessage{
			Tenant:    tenant,
			GuildID:   m.GuildID,
			ChannelID: m.ChannelID,
			Author:    m.Author.Username,
			Content:   m.Content,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		})
	})

	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsMessageContent

	return ds, nil
}

// nextTenant returns the next round-robin tenant tag (empty string
// when no tenants are configured). Under a mutex because admin add
// callers and the Discord gateway handler can race.
func (ds *discordSource) nextTenant() string {
	if len(ds.cfg.Tenants) == 0 {
		return ""
	}
	ds.mu.Lock()
	defer ds.mu.Unlock()
	t := ds.cfg.Tenants[ds.tenantIdx%len(ds.cfg.Tenants)]
	ds.tenantIdx++
	return t
}

// open establishes the Discord gateway connection. Call before
// registry.AddSource so the source is producing events the moment
// it's discoverable.
func (ds *discordSource) open() error { return ds.session.Open() }

// close terminates the Discord gateway connection. Call after
// registry.RemoveSource so no orphan yields land on an unregistered
// source.
func (ds *discordSource) close() error { return ds.session.Close() }

// Source returns the EventSource the Registry takes via AddSource.
func (ds *discordSource) Source() events.EventSource { return ds.source }

// Name returns the EventDef.Name the source was registered under.
func (ds *discordSource) Name() string { return ds.cfg.SourceName }
