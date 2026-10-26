package eventsclient

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// Receiver is a typed webhook receiver. Implements http.Handler so it can
// be hung off any net/http mux or wrapped by httptest.NewServer.
//
// Verifies the inbound signature against the configured secret —
// auto-detecting whether the request carries X-MCP-* (default) or
// webhook-* (Standard Webhooks) headers per the server's WebhookHeaderMode.
//
// Answers the server's endpoint-verification challenge (SEP-3415 §"Webhook security" (endpoint verification)) by echoing the nonce, but only when the challenge's
// signature verifies. That arrives while Subscribe is still in flight,
// so the secret must already be set, or empty, by then; a receiver
// holding the wrong secret refuses the challenge and Subscribe fails
// with -32027 challenge_failed.
//
// Refuses a delivery whose timestamp is outside the tolerance (see
// WithTimestampTolerance), so a captured POST cannot be replayed later, and
// drops a second delivery of the same event on the same subscription (see
// WithDeduper), answering it 200 so the server stops retrying.
//
// Decodes the wire envelope's Data field into the typed Data parameter
// and delivers Event[Data] values on the Events() channel.
type Receiver[Data any] struct {
	mu        sync.RWMutex
	secret    string
	out       chan Event[Data]
	closed    bool
	rejected  uint64
	tolerance time.Duration
	dedup     *Deduper
	now       func() time.Time

	// Control envelope callbacks per SEP-3415 §"Non-event webhook bodies"
	// L415-423. Both are optional. The discriminator is the top-level
	// `type` field: "gap" carries a fresh cursor; "terminated"
	// carries an error.
	onGap        func(cursor string)
	onTerminated func(err ControlError)
}

// ControlError mirrors the JSON-RPC error object carried in a
// type:terminated control envelope per spec L420.
type ControlError struct {
	Code    int
	Message string
}

// NewReceiver constructs a typed receiver with the given verification
// secret. Pass an empty string if you intend to call SetSecret later
// (e.g., when the receiver is constructed before the application has
// decided what value to subscribe with).
//
// Buffered to 64 events; callers should drain Events() promptly. A
// delivery that finds the channel full is dropped rather than blocking the
// inbound HTTP handler, and answered 503 so the server retries it. The
// server gives up after a few retries, so a consumer that can stall for
// longer (one waiting on a person, say) should drain into its own queue.
func NewReceiver[Data any](secret string, opts ...ReceiverOption) *Receiver[Data] {
	cfg := receiverConfig{tolerance: events.DefaultTimestampTolerance, now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	return &Receiver[Data]{
		secret:    secret,
		out:       make(chan Event[Data], 64),
		tolerance: cfg.tolerance,
		dedup:     dedupFor(cfg.dedup, cfg.allowDuplicates),
		now:       cfg.now,
	}
}

// ReceiverOption configures NewReceiver.
type ReceiverOption func(*receiverConfig)

type receiverConfig struct {
	tolerance       time.Duration
	dedup           *Deduper
	allowDuplicates bool
	now             func() time.Time
}

// WithTimestampTolerance sets how far the delivery's timestamp header may be
// from the receiver's clock (default events.DefaultTimestampTolerance). A
// delivery outside it gets 401 like a bad signature. Zero turns the check off.
func WithTimestampTolerance(d time.Duration) ReceiverOption {
	return func(c *receiverConfig) { c.tolerance = d }
}

// WithDeduper shares a Deduper with the receiver. Webhook deliveries are keyed
// on the X-MCP-Subscription-Id header together with eventId, so one event that
// matches two subscriptions to the same URL arrives twice, once per
// subscription. Poll and Stream key on eventId alone, so sharing a Deduper
// with them drops overlap only for deliveries that carry no subscription id.
func WithDeduper(d *Deduper) ReceiverOption {
	return func(c *receiverConfig) { c.dedup = d }
}

// WithoutDedup hands every verified delivery to the application, retries
// included, for callers who dedup themselves.
func WithoutDedup() ReceiverOption {
	return func(c *receiverConfig) { c.allowDuplicates = true }
}

// SetSecret updates the verification secret. Safe to call concurrently
// with deliveries. Useful for adopting the server-assigned secret after
// Subscribe returns.
func (r *Receiver[Data]) SetSecret(secret string) {
	r.mu.Lock()
	r.secret = secret
	r.mu.Unlock()
}

// Events returns the typed delivery channel. Closes when Close is called.
func (r *Receiver[Data]) Events() <-chan Event[Data] { return r.out }

// OnGap installs a callback invoked when the receiver gets a
// {type:"gap", cursor:<fresh>} control envelope (spec L415). Caller
// SHOULD reset its persisted cursor to the carried value and re-fetch
// authoritative state if the gap matters for correctness.
//
// Receiver-callback semantics: invoked from the inbound HTTP handler
// goroutine — must NOT block. Heavy work belongs in a separate
// goroutine inside the callback.
func (r *Receiver[Data]) OnGap(fn func(cursor string)) {
	r.mu.Lock()
	r.onGap = fn
	r.mu.Unlock()
}

// OnTerminated installs a callback invoked when the receiver gets a
// {type:"terminated", error:{...}} control envelope (spec L420). Caller
// SHOULD remove its local subscription state — the server has already
// stopped delivering. Same goroutine constraints as OnGap.
func (r *Receiver[Data]) OnTerminated(fn func(err ControlError)) {
	r.mu.Lock()
	r.onTerminated = fn
	r.mu.Unlock()
}

// Close stops accepting deliveries and closes the Events channel. Safe
// to call multiple times.
func (r *Receiver[Data]) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.out)
}

// ServeHTTP implements http.Handler.
func (r *Receiver[Data]) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	r.mu.RLock()
	secret := r.secret
	closed := r.closed
	r.mu.RUnlock()

	if closed {
		http.Error(w, "receiver closed", http.StatusGone)
		return
	}

	if !verifySignature(req.Header, body, secret) || !r.timestampFresh(req.Header, secret) {
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
		return
	}

	// Top-level `type` field discriminates control envelopes from
	// event deliveries (SEP-3415 §"Non-event webhook bodies".
	// Probe with a minimal struct before unmarshaling as Event so we
	// don't double-decode the common case (no `type` → event).
	var probe struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &probe)
	switch probe.Type {
	case "verification":
		events.AnswerVerificationChallenge(w, body)
		return
	case "gap":
		var env struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(body, &env)
		r.mu.RLock()
		cb := r.onGap
		r.mu.RUnlock()
		if cb != nil {
			cb(env.Cursor)
		}
		w.WriteHeader(http.StatusOK)
		return
	case "terminated":
		var env struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &env)
		r.mu.RLock()
		cb := r.onTerminated
		r.mu.RUnlock()
		if cb != nil {
			ce := ControlError{}
			if env.Error != nil {
				ce.Code = env.Error.Code
				ce.Message = env.Error.Message
			}
			cb(ce)
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	var wire events.Event
	if err := json.Unmarshal(body, &wire); err != nil {
		http.Error(w, "decode envelope: "+err.Error(), http.StatusBadRequest)
		return
	}

	var data Data
	if len(wire.Data) > 0 {
		if err := json.Unmarshal(wire.Data, &data); err != nil {
			http.Error(w, "decode payload: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	ev := Event[Data]{
		EventID:   wire.EventID,
		Name:      wire.Name,
		Timestamp: wire.Timestamp,
		Cursor:    wire.Cursor,
		Data:      data,
		Meta:      wire.Meta,

		SubscriptionID: req.Header.Get("X-MCP-Subscription-Id"),
	}

	key := dedupKey(req.Header, wire.EventID)
	if r.dedup != nil && r.dedup.Seen(key) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Non-blocking send. A full channel signals a slow consumer; we'd
	// rather drop than back up the registry's delivery loop. A dropped
	// event is forgotten and answered 503, so the server's retry of it is
	// delivered. The send happens under the lock Close takes, so a Close
	// racing this request cannot close the channel between the check and
	// the send.
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		if r.dedup != nil {
			r.dedup.Forget(key)
		}
		http.Error(w, "receiver closed", http.StatusGone)
		return
	}
	select {
	case r.out <- ev:
	default:
		if r.dedup != nil {
			r.dedup.Forget(key)
		}
		r.rejected++
		r.mu.Unlock()
		http.Error(w, "receiver busy", http.StatusServiceUnavailable)
		return
	}
	r.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

// Rejected returns the count of deliveries dropped because Events() was
// not being drained fast enough. Useful for tests / diagnostics.
func (r *Receiver[Data]) Rejected() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rejected
}

// dedupKey scopes eventId by subscription, so two subscriptions to one URL
// each get their copy of an event that matches both.
func dedupKey(h http.Header, eventID string) string {
	if eventID == "" {
		return ""
	}
	if sub := h.Get("X-MCP-Subscription-Id"); sub != "" {
		return sub + "\x00" + eventID
	}
	return eventID
}

// timestampFresh applies the tolerance to whichever timestamp header the
// delivery carries. The empty-secret test mode skips it along with the
// signature.
func (r *Receiver[Data]) timestampFresh(h http.Header, secret string) bool {
	if secret == "" || r.tolerance <= 0 {
		return true
	}
	ts := h.Get("X-MCP-Timestamp")
	if ts == "" {
		ts = h.Get("webhook-timestamp")
	}
	return events.TimestampWithinTolerance(ts, r.now(), r.tolerance)
}

// verifySignature auto-detects the header set on the inbound request and
// verifies against the appropriate scheme. Returns true on a valid sig;
// false if neither header set verifies (or the secret is empty, in which
// case we accept — useful for ad-hoc test mode).
func verifySignature(h http.Header, body []byte, secret string) bool {
	if secret == "" {
		// Empty-secret receiver is "accept anything" — a deliberate test
		// shortcut. Production callers should always set a secret.
		return true
	}
	if sig := h.Get("X-MCP-Signature"); sig != "" {
		ts := h.Get("X-MCP-Timestamp")
		return events.VerifyMCPSignature(body, secret, ts, sig)
	}
	if sig := h.Get("webhook-signature"); sig != "" {
		msgID := h.Get("webhook-id")
		ts := h.Get("webhook-timestamp")
		return events.VerifyStandardWebhooksSignature(body, secret, msgID, ts, sig)
	}
	return false
}
