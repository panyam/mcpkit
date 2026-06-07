package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"strconv"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// runPresenceFeeder is the OnSubscribe-provisioned upstream pattern in
// action. Unlike runChatFeeder which yields onto a YieldingSource (and
// the library broadcasts + Match-filters), this feeder generates a
// random transition each tick and then uses the watch-list registry
// to compute exactly which subscriptions care about it, and calls
// EmitToSubscription per matched sub.
//
// Spec §"Server SDK Guidance" L630 frames the canonical example as
// on_subscribe joining a chat room and tagging upstream events with
// the originating sub id. Our demo collapses the "tagging" into a
// per-event lookup against the watch list registry, which is simpler
// to read and equivalent on the wire — the library never sees a
// fan-out, so Match / Transform are NOT invoked.
//
// nextEventID gives each emitted event a stable id for log diffing.
var presenceNextID = 0

func runPresenceFeeder(
	ctx context.Context,
	idx *events.SubscriptionIndex,
	registry *watchListRegistry,
	interval time.Duration,
) {
	users := []string{"alice", "bob", "carol", "dave"}
	states := []string{"online", "away", "offline"}
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + 13))

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
			emitPresence(idx, registry, ev)
		}
	}
}

// emitPresence is the per-event routing core. It is exported (lower-c
// but package-internal) so the walkthrough's deterministic
// inject-step calls it directly without waiting for the random
// feeder. Production code would do the same — the feeder loop is
// just a synthetic driver.
//
// Lookup is O(R*S) where R is registered subs and S is the average
// watch-list length; both small in any realistic per-tenant scope.
// EmitToSubscription itself does its own SubscriptionIndex lookup
// inside the library, so we tolerate concurrent unsubscribe races
// naturally (unknown subID drops with a debug log per the library).
func emitPresence(idx *events.SubscriptionIndex, registry *watchListRegistry, p PresenceChangedData) {
	subs := registry.matchingSubs(p.User)
	if len(subs) == 0 {
		return // no consumer cares; library never sees the event
	}
	presenceNextID++
	raw, err := json.Marshal(p)
	if err != nil {
		log.Printf("[presence] marshal: %v", err)
		return
	}
	ev := events.Event{
		EventID:   "presence-" + strconv.Itoa(presenceNextID),
		Name:      "presence.changed",
		Timestamp: p.Timestamp,
		Data:      raw,
		Cursor:    nil, // cursorless
	}
	for _, subID := range subs {
		events.EmitToSubscription(idx, ev, subID)
	}
}
