package eventsclient

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// Receiver is a typed webhook receiver. Implements http.Handler so it can
// be hung off any net/http mux or wrapped by httptest.NewServer.
//
// Verifies the inbound signature against the configured secret —
// auto-detecting whether the request carries X-MCP-* (default) or
// webhook-* (Standard Webhooks) headers per the server's WebhookHeaderMode.
//
// Decodes the wire envelope's Data field into the typed Data parameter
// and delivers Event[Data] values on the Events() channel.
type Receiver[Data any] struct {
	mu       sync.RWMutex
	secret   string
	out      chan Event[Data]
	closed   bool
	rejected uint64
}

// NewReceiver constructs a typed receiver with the given verification
// secret. Pass an empty string if you intend to call SetSecret later
// (e.g., when the receiver is constructed before the application has
// decided what value to subscribe with).
//
// Buffered to 64 events; callers should drain Events() promptly. A full
// channel causes the receiver to drop new deliveries silently rather
// than block the inbound HTTP handler.
func NewReceiver[Data any](secret string) *Receiver[Data] {
	return &Receiver[Data]{
		secret: secret,
		out:    make(chan Event[Data], 64),
	}
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

	if !verifySignature(req.Header, body, secret) {
		http.Error(w, "signature verification failed", http.StatusUnauthorized)
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
	}

	// Non-blocking send. A full channel signals a slow consumer; we'd
	// rather drop than back up the registry's delivery loop.
	select {
	case r.out <- ev:
	default:
		r.mu.Lock()
		r.rejected++
		r.mu.Unlock()
	}

	w.WriteHeader(http.StatusOK)
}

// Rejected returns the count of deliveries dropped because Events() was
// not being drained fast enough. Useful for tests / diagnostics.
func (r *Receiver[Data]) Rejected() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rejected
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
