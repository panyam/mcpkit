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
func runChatFeeder(ctx context.Context, pusher *eventsclient.Pusher, interval time.Duration) {
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
			if err := pusher.PushNamed(ctx, "chat.message", msg); err != nil {
				log.Printf("[push] chat.message: %v", err)
				continue
			}
			chatPushed.Add(1)
		}
	}
}
