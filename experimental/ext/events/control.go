package events

// ζ-4 — control envelopes for non-event webhook bodies.
//
// Spec §"Non-event webhook bodies" L415-423: the server POSTs signed
// envelopes to webhook receivers when the event-delivery channel
// can't carry the signal:
//
//   - {type: "gap", cursor: "<fresh>"} — a gap was detected between
//     refreshes (e.g., a yield queue overflowed). Tells the receiver
//     to reset its cursor to the carried value.
//   - {type: "terminated", error: {code, message}} — the subscription
//     has ended (e.g., auth revoked). Receiver removes the subscription;
//     the server removes the registry target.
//
// Both use Standard Webhooks signature headers (webhook-id /
// webhook-timestamp / webhook-signature) plus the γ-4
// X-MCP-Subscription-Id header. webhook-id format is
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
}

// controlEnvelope is the wire shape of a non-event webhook body per
// spec L415-423. The top-level `type` field is the discriminator;
// payload-specific fields (cursor for gap, error for terminated) are
// optional and use omitempty so envelopes only carry the field they
// need.
type controlEnvelope struct {
	Type   string        `json:"type"`
	Cursor string        `json:"cursor,omitempty"`
	Error  *ControlError `json:"error,omitempty"`
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
func (r *WebhookRegistry) PostGap(canonicalKey []byte, freshCursor string) {
	r.mu.RLock()
	target, ok := r.targets[string(canonicalKey)]
	r.mu.RUnlock()
	if !ok {
		return
	}
	body, err := json.Marshal(controlEnvelope{Type: "gap", Cursor: freshCursor})
	if err != nil {
		r.logf("[webhook] PostGap: marshal failed: %v", err)
		return
	}
	go r.deliverControl(target, "gap", body)
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
	target, ok := r.targets[string(canonicalKey)]
	if ok {
		delete(r.targets, string(canonicalKey))
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	body, err := json.Marshal(controlEnvelope{Type: "terminated", Error: &controlErr})
	if err != nil {
		r.logf("[webhook] PostTerminated: marshal failed: %v", err)
		return
	}
	go r.deliverControl(target, "terminated", body)
}

// postTerminatedSilent POSTs a {type:terminated} envelope to a target
// WITHOUT removing it from the registry. Distinct from the public
// PostTerminated which removes — the suspend transition (ζ-6 →
// ζ-7.3) needs the target to remain observable as Active=false so the
// spec's "successful refresh reactivates" path stays available.
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
	go r.deliverControl(target, "terminated", body)
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
