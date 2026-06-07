package main

import (
	"context"
	"log"
	"math/rand"
	"time"

	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// runPresenceFeeder emits a synthetic presence transition every interval.
// Each event flips one user's state to a randomly-selected value (potentially
// the same value — the demo doesn't dedupe because presence-changed events
// are inherently cursorless and subscribers see live transitions only).
func runPresenceFeeder(ctx context.Context, pusher *eventsclient.Pusher, interval time.Duration) {
	users := []string{"alice", "bob", "carol", "dave"}
	states := []string{"online", "away", "offline"}
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + 1))

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ts := <-t.C:
			ev := PresenceChangedData{
				User:      users[rng.Intn(len(users))],
				State:     states[rng.Intn(len(states))],
				Timestamp: ts.UTC().Format(time.RFC3339),
			}
			if err := pusher.PushNamed(ctx, "presence.changed", ev); err != nil {
				log.Printf("[push] presence.changed: %v", err)
				continue
			}
			presencePushed.Add(1)
		}
	}
}
