package events

import (
	"context"
	"log"
)

// EmitToSubscription delivers an event to exactly one subscription
// identified by sub id, bypassing the broadcast fanout.
//
// Spec §"Server SDK Guidance" L630: targeted emit is appropriate when
// the server has set up a per-subscription upstream listener and
// already knows which subscription this event belongs to — the
// canonical example is on_subscribe joining a chat room and tagging
// upstream events with the originating sub id, so when an upstream
// event arrives the server emits straight to that sub rather than
// fanning out and Match-filtering.
//
// Match / Transform are NOT applied — the spec model is "the author
// has already shaped this event for this specific subscription."
// Authors that want filtering on a targeted emit should do it before
// calling.
//
// Unknown subID: drops with a debug log. The subscription may have
// been torn down between the author's check and this call (e.g., the
// stream client disconnected); racing this is normal, not an error.
//
// Poll subscriptions are NOT addressable by sub id — poll has no sub
// id (the lease tuple is the routing identity per Q4). Calling
// EmitToSubscription with a poll-mode id is a no-op of the
// "unknown subID" variety.
func EmitToSubscription(idx SubscriptionIndexStore, event Event, subID string) {
	if idx == nil {
		log.Printf("[events] EmitToSubscription: nil index; drop subID=%q event=%q",
			subID, event.EventID)
		return
	}
	resp, _ := idx.LookupSubscription(context.Background(), LookupSubscriptionRequest{SubscriptionID: subID})
	if !resp.Found || resp.Deliver == nil {
		log.Printf("[events] EmitToSubscription: unknown subID=%q (already unsubscribed?); drop event=%q",
			subID, event.EventID)
		return
	}
	resp.Deliver(event)
}
