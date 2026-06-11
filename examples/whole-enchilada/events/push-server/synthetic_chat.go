package main

import (
	"context"
	"log"
	"math/rand"
	"time"

	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// runChatFeeder emits a synthetic chat message every interval on a
// rotating set of senders + channels until ctx is done. The cadence
// is configurable so the demo can show "high-volume" behavior by
// turning it up without changing code.
//
// tenants is a list of tenant tags to rotate through (round-robin) on
// successive events. The stage-2 demo passes ["asgard", "babylon",
// "camelot"] so subscribers from one tenant only see ~1/N events.
// Nil / empty tenants means "no tag" — the stage-1 single-tenant
// behavior where every subscriber sees every event.
func runChatFeeder(ctx context.Context, pusher *eventsclient.Pusher, interval time.Duration, tenants []string) {
	senders := []string{"alice", "bob", "carol", "dave"}
	channels := []string{"general", "random", "alerts"}
	templates := []string{
		"shipping new build",
		"anyone tried the new dashboard?",
		"meeting at 3pm",
		"+1",
		"merged",
		"reviewing now",
		"PSA: the staging env is down",
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	t := time.NewTicker(interval)
	defer t.Stop()

	tenantIdx := 0
	for {
		select {
		case <-ctx.Done():
			return
		case ts := <-t.C:
			msg := ChatMessageData{
				Channel:   channels[rng.Intn(len(channels))],
				Sender:    senders[rng.Intn(len(senders))],
				Text:      templates[rng.Intn(len(templates))],
				Timestamp: ts.UTC().Format(time.RFC3339),
			}
			if len(tenants) > 0 {
				msg.Tenant = tenants[tenantIdx%len(tenants)]
				tenantIdx++
			}
			if err := pusher.PushNamed(ctx, "chat.message", msg); err != nil {
				log.Printf("[push] chat.message: %v", err)
				continue
			}
			chatPushed.Add(1)
		}
	}
}
