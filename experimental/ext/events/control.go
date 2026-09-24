package events

// Control envelopes for non-event webhook bodies. Spec §"Non-event
// webhook bodies" L415-423: the server POSTs signed envelopes to
// webhook receivers when the event-delivery channel can't carry the
// signal:
//
//   - {type: "gap", cursor: "<fresh>"} — a gap was detected between
//     refreshes (e.g., a yield queue overflowed). Tells the receiver
//     to reset its cursor to the carried value.
//   - {type: "terminated", error: {code, message}} — the subscription
//     has ended (e.g., auth revoked). Receiver removes the subscription;
//     the server removes the registry target.
//
// Both use Standard Webhooks signature headers (webhook-id /
// webhook-timestamp / webhook-signature) plus the X-MCP-Subscription-Id
// header (spec §"Webhook Event Delivery" L390). webhook-id format is
// msg_<type>_<random> per spec L417, distinguishing control POSTs from
// event deliveries (which use the event's eventId so retries dedup
// correctly).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// ControlError is the error payload carried in a type:terminated
// envelope. Mirrors a JSON-RPC error object (code + message) so
// receivers can map the categorical reason consistently with how they
// already handle JSON-RPC failures elsewhere.
type ControlError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Data carries the code's typed discriminator, mirroring the `data`
	// field of a JSON-RPC error. Spec §"Event Type Removal and Breaking
	// Changes" requires it on both termination shapes: NotFoundData
	// {kind:"event"} for a removed type, UnsupportedData
	// {feature, reason:"schema_changed"} for one changed in place.
	// Without it a receiver sees -32011 and cannot tell a removed event
	// type from a missing subscription.
	Data any `json:"data,omitempty"`
}

// controlEnvelope is the wire shape of a non-event webhook body per
// spec L415-423. The top-level `type` field is the discriminator;
// payload-specific fields (cursor for gap, error for terminated) are
// optional and use omitempty so envelopes only carry the field they
// need.
type controlEnvelope struct {
	Type      string        `json:"type"`
	Cursor    string        `json:"cursor,omitempty"`
	Error     *ControlError `json:"error,omitempty"`
	Challenge string        `json:"challenge,omitempty"` // type:verification only, see verification.go
}

// PostGap delivers a {type:gap, cursor:<fresh>} envelope to the
// webhook target identified by canonicalKey. The receiver should reset
// its persisted cursor to the provided fresh value and resume from
// there. Per spec §"Non-event webhook bodies" L415.
//
// No-op if no target matches canonicalKey (e.g., the subscription has
// already expired or been unregistered). Logged via the registry's
// logf hook on best-effort failure; this method does not retry beyond
// the deliver-loop's existing exponential backoff.
//
// Also a no-op, logged, when freshCursor is empty. The envelope's cursor is
// the position the client persists and resumes from; a cursorless source or
// one that has yielded nothing has no position, and the spec's shape has no
// cursor-less gap. For a type without replay there is nothing to have
// skipped past (§"Replay is optional per event type").
func (r *WebhookRegistry) PostGap(canonicalKey []byte, freshCursor string) {
	if freshCursor == "" {
		r.logf("[webhook] PostGap: no cursor to send, skipping the gap envelope")
		return
	}
	r.mu.RLock()
	resp, _ := r.store.GetWebhook(context.Background(), GetWebhookRequest{CanonicalKey: canonicalKey})
	r.mu.RUnlock()
	if !resp.Found {
		return
	}
	body, err := json.Marshal(controlEnvelope{Type: "gap", Cursor: freshCursor})
	if err != nil {
		r.logf("[webhook] PostGap: marshal failed: %v", err)
		return
	}
	safeGo("events.control.gap", func() { r.deliverControl(resp.Target, "gap", body) })
}

// PostGapByEventName sends a {type:gap, cursor:<fresh>} envelope to every
// webhook subscription for event name and returns how many it signalled.
// Nothing is removed; a gap is not an ending.
//
// This is what YieldingSource.YieldGap calls when the source is registered
// with a WebhookRegistry. A source that fans out itself (a TypedSource with
// EmitToWebhooks, say) calls it directly on detecting a loss, alongside
// whatever it tells its push subscribers.
//
// Every subscription gets the same cursor, the position the source can serve
// from now, which is also what push subscribers receive in their fresh
// notifications/events/active. name == "" is a no-op, and so is an empty
// cursor (see PostGap).
func (r *WebhookRegistry) PostGapByEventName(name, freshCursor string) int {
	if name == "" || freshCursor == "" {
		return 0
	}
	r.mu.RLock()
	listResp, _ := r.store.ListWebhooks(context.Background(), ListWebhooksRequest{})
	r.mu.RUnlock()

	var signalled int
	for _, t := range listResp.Targets {
		if t.EventName != name {
			continue
		}
		r.PostGap(t.CanonicalKey, freshCursor)
		signalled++
	}
	return signalled
}

// PostTerminated delivers a {type:terminated, error:...} envelope to
// the webhook target and then removes the target from the registry —
// the receiver is being told the subscription is dead, so subsequent
// event yields shouldn't try to deliver to it anyway. Per spec
// §"Non-event webhook bodies" L420 + §"Authorization" L783-795.
//
// No-op if no target matches canonicalKey. The target removal happens
// regardless of whether the POST succeeded; if the receiver was
// unreachable when we tried to notify it, we still don't want a zombie
// entry in the registry.
func (r *WebhookRegistry) PostTerminated(canonicalKey []byte, controlErr ControlError) {
	r.mu.Lock()
	resp, _ := r.store.DeleteWebhook(context.Background(), DeleteWebhookRequest{CanonicalKey: canonicalKey})
	r.mu.Unlock()
	if !resp.Found {
		return
	}
	target := resp.Removed
	// PostTerminated is server-initiated subscription death;
	// onRemove fires on actual registry deletion (per spec §"Server
	// SDK Guidance" → "Unsubscribe timing by mode" L707). Suspend
	// (handled via postTerminatedSilent) deliberately does NOT fire
	// — the target stays in the registry as paused.
	r.fireOnRemove(target)
	body, err := json.Marshal(controlEnvelope{Type: "terminated", Error: &controlErr})
	if err != nil {
		r.logf("[webhook] PostTerminated: marshal failed: %v", err)
		return
	}
	safeGo("events.control.terminated", func() { r.deliverControl(target, "terminated", body) })
}

// TerminateBySession fires {type:terminated} envelopes to every
// subscription whose stored SessionID matches sid, removes those
// subscriptions from the registry, and fires onRemove per match.
// Returns the count of subscriptions terminated.
//
// Spec §"Authorization" L783-795 designates this exact shape — when
// the AS revokes a session, the server SHOULD terminate every
// subscription that the session authorized. The trigger comes from an
// OIDC Back-Channel Logout 1.0 receiver (ext/auth.BackChannelLogoutHandler);
// this method is the events-lib side of the wire-up. See issue 709.
//
// Implementation: O(N) scan of the webhook store today — the demo and
// most current deployments have N small enough that a secondary index
// would be premature. When N gets large enough to matter the same
// method body can call a future store.ListBySessionID hook on the
// WebhookStore interface without changing the public API.
//
// sid == "" is a no-op (anonymous subscriptions can't carry a session
// to revoke).
func (r *WebhookRegistry) TerminateBySession(sid string, controlErr ControlError) int {
	if sid == "" {
		return 0
	}
	r.mu.RLock()
	listResp, _ := r.store.ListWebhooks(context.Background(), ListWebhooksRequest{})
	r.mu.RUnlock()

	var killed int
	for _, t := range listResp.Targets {
		if t.SessionID != sid {
			continue
		}
		r.PostTerminated(t.CanonicalKey, controlErr)
		killed++
	}
	return killed
}

// TerminateBySubject is the broader sibling of TerminateBySession —
// fires {type:terminated} envelopes to every subscription whose stored
// Subject matches sub. Used when the BCL logout_token carries only
// `sub` (the spec allows AS to omit `sid` when the revocation applies
// to all of the subject's sessions). sub == "" is a no-op.
//
// O(N) scan, same caveats as TerminateBySession.
func (r *WebhookRegistry) TerminateBySubject(sub string, controlErr ControlError) int {
	if sub == "" {
		return 0
	}
	r.mu.RLock()
	listResp, _ := r.store.ListWebhooks(context.Background(), ListWebhooksRequest{})
	r.mu.RUnlock()

	var killed int
	for _, t := range listResp.Targets {
		if t.Subject != sub {
			continue
		}
		r.PostTerminated(t.CanonicalKey, controlErr)
		killed++
	}
	return killed
}

// TerminateByEventName fires {type:terminated} envelopes to every
// subscription for a given event name, removes them from the registry,
// and fires onRemove per match. Returns the count terminated.
//
// Spec §"Event Type Removal and Breaking Changes" (commit 28ec35e9)
// designates this shape: when a server stops offering an event type, the
// subscriptions to it no longer hold a valid contract and SHOULD be ended
// rather than left waiting on a name the server will never emit again.
// Registry.RemoveSource is the caller.
//
// Same O(N) store scan as TerminateBySession, and the same note applies:
// a future store.ListByEventName hook can replace the body without
// touching the public API. name == "" is a no-op.
func (r *WebhookRegistry) TerminateByEventName(name string, controlErr ControlError) int {
	if name == "" {
		return 0
	}
	r.mu.RLock()
	listResp, _ := r.store.ListWebhooks(context.Background(), ListWebhooksRequest{})
	r.mu.RUnlock()

	var killed int
	for _, t := range listResp.Targets {
		if t.EventName != name {
			continue
		}
		r.PostTerminated(t.CanonicalKey, controlErr)
		killed++
	}
	return killed
}

// postTerminatedSilent POSTs a {type:terminated} envelope to a target
// WITHOUT removing it from the registry. Distinct from the public
// PostTerminated which removes — the suspend transition (per spec
// §"Webhook Delivery Status" L460) needs the target to remain
// observable as Active=false so the spec's "successful refresh
// reactivates" path stays available.
//
// Caller (recordDeliveryFailure on the suspend transition) passes the
// target snapshot so this method doesn't need to re-acquire the lock.
// Async delivery via deliverControl in a goroutine.
func (r *WebhookRegistry) postTerminatedSilent(target WebhookTarget, controlErr ControlError) {
	body, err := json.Marshal(controlEnvelope{Type: "terminated", Error: &controlErr})
	if err != nil {
		r.logf("[webhook] postTerminatedSilent: marshal failed: %v", err)
		return
	}
	safeGo("events.control.terminated", func() { r.deliverControl(target, "terminated", body) })
}

// deliverControl POSTs a control envelope synchronously (caller starts
// the goroutine). Uses newControlMessageID for the webhook-id so the
// type prefix appears in the header. Reuses the per-mode signing
// machinery from headers.go so the wire signature matches what event
// deliveries use; receivers verify with the same code path.
//
// Single-attempt delivery: control envelopes are best-effort signals.
// Retrying a `terminated` envelope after the target was removed would
// race the next subscribe; retrying a `gap` envelope would compound a
// racing condition the receiver is already being told to recover from.
func (r *WebhookRegistry) deliverControl(target WebhookTarget, typ string, body []byte) {
	msgID := newControlMessageID(typ)
	signed := signFor(r.headerMode, msgID, body, target.Secret, time.Now()).
		withSubscriptionID(target.ID)

	req, err := http.NewRequestWithContext(context.Background(), "POST", target.URL, bytes.NewReader(signed.body))
	if err != nil {
		r.logf("[webhook] deliverControl(%s) build-request failed for %s: %v", typ, target.URL, err)
		return
	}
	signed.applyHeaders(req)

	resp, err := r.client.Do(req)
	if err != nil {
		r.logf("[webhook] deliverControl(%s) to %s failed: %v", typ, target.URL, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		r.logf("[webhook] deliverControl(%s) to %s returned %d", typ, target.URL, resp.StatusCode)
	}
}
