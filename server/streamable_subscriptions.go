package server

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
	"golang.org/x/time/rate"
)

// SEP-2575 subscriptions/listen — transport-level state machine.
//
// One *statelessSubscriber per open stream. Lives only for the duration
// of the open HTTP request; the subscriber unregisters on context
// cancellation (client disconnect). Server.Broadcast already fans out
// list-changed notifications through streamableTransport.broadcast;
// that method now also calls fanoutToStatelessSubs for every open
// stateless subscriber, gated on the per-subscription filter.

// statelessSubscriber holds the per-stream subscription state.
// frames is a small buffered channel — small enough that a misbehaving
// or paused client backpressures Broadcast quickly (we drop frames
// rather than block the producer).
type statelessSubscriber struct {
	id     string
	scope  string // populated by tryRegister; used by unregister to drop the count
	filter stateless.SubscribeFilter
	frames chan stateless.TaggedFrame
	done   chan struct{}
}

// newSubscriptionID generates a 128-bit base32-encoded subscriptionId.
// 26 char output; collision-resistant for the lifetime of the process.
func newSubscriptionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read failure on the platform crypto source is fatal-shaped;
		// the caller is opening a network stream so we degrade with a
		// best-effort id that's still uniquish via process address space.
		return "sub-fallback"
	}
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return "sub-" + enc.EncodeToString(b[:])
}

// statelessSubMap is the transport's set of open subscription streams.
// Keyed by subscriptionId. RWMutex because fanout (Broadcast path) is
// the dominant op; register/unregister are bursty but rare.
//
// Cap and rate-limit state is keyed by scope (typically remote-host
// derived) so a single client cannot exhaust the transport by churning
// subscription streams. The legacy registry (subscriptionRegistry on
// Server) uses sessionID for the same purpose; on the stateless wire
// there is no session, so the scope func extracts a stable key from the
// incoming *http.Request.
type statelessSubMap struct {
	mu   sync.RWMutex
	subs map[string]*statelessSubscriber

	// countsByScope holds the number of currently-open subscription
	// streams per scope key. limitersByScope are lazy per-scope token
	// buckets; both are cleared when a scope drops to zero open streams
	// so disconnected clients leave no state behind.
	countsByScope   map[string]int
	limitersByScope map[string]*rate.Limiter

	// Configured once at construction time; safe to read without the
	// mutex. cap == 0 / rateLimit == 0 disables the respective check.
	cap       int
	rateLimit rate.Limit
	rateBurst int
	onReject  SubscriptionRejectFunc
	scopeFn   StatelessSubscriptionScopeFunc
}

// StatelessSubscriptionScopeFunc derives a stable key from an inbound
// HTTP request to identify the calling client for cap / rate-limit
// bookkeeping. The legacy wire uses sessionID; the stateless wire has
// no session, so the operator picks the scoping key that makes sense
// for their deployment (proxy-supplied client header, source IP,
// auth-subject, etc.).
//
// Implementations MUST be deterministic: two requests from the same
// client must produce the same string, and two requests from different
// clients should not collide. An empty string is treated as a single
// shared scope (all anonymous requests count against one bucket); pass
// a function that returns a non-empty placeholder if that is not what
// you want.
type StatelessSubscriptionScopeFunc func(*http.Request) string

// DefaultStatelessSubscriptionScope returns the host portion of
// r.RemoteAddr — what the OS reports for the TCP peer — and falls back
// to RemoteAddr verbatim if the port split fails. This is the
// conservative default: it cannot be spoofed by a malicious client
// (headers are not consulted), but it lumps every client behind a
// single reverse-proxy IP into the same scope.
//
// For a deployment where the operator trusts a header like
// X-Forwarded-For to identify clients, supply a custom
// [StatelessSubscriptionScopeFunc] via
// [WithStatelessSubscriptionScope].
//
// Stability: the wire surface this guards (SEP-2575
// subscriptions/listen) is in the 2026-07-28 release candidate, not
// Final. The cap and rate-limit mechanism is mcpkit-internal and not
// spec-defined — if a future SEP standardizes either, callers may
// need to migrate.
func DefaultStatelessSubscriptionScope(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func newStatelessSubMap(cap int, rl rate.Limit, burst int, onReject SubscriptionRejectFunc, scopeFn StatelessSubscriptionScopeFunc) *statelessSubMap {
	if scopeFn == nil {
		scopeFn = DefaultStatelessSubscriptionScope
	}
	return &statelessSubMap{
		subs:            make(map[string]*statelessSubscriber),
		countsByScope:   make(map[string]int),
		limitersByScope: make(map[string]*rate.Limiter),
		cap:             cap,
		rateLimit:       rl,
		rateBurst:       burst,
		onReject:        onReject,
		scopeFn:         scopeFn,
	}
}

// tryRegister enforces the per-scope cap and rate limit, then registers
// s under its subscription id. Returns [ErrStatelessStreamCapExceeded]
// or [ErrStatelessStreamRateLimited] without registering when refused.
// scope is the caller-supplied scope key (typically extracted from the
// incoming HTTP request); it is recorded so unregister can decrement
// the right bucket.
func (m *statelessSubMap) tryRegister(s *statelessSubscriber, scope string) error {
	m.mu.Lock()
	if m.cap > 0 && m.countsByScope[scope] >= m.cap {
		m.mu.Unlock()
		if m.onReject != nil {
			m.onReject(scope, "subscriptions/listen", "cap_exceeded")
		}
		return fmt.Errorf("%w: scope at %d/%d streams", ErrStatelessStreamCapExceeded, m.cap, m.cap)
	}
	if m.rateLimit > 0 {
		lim, ok := m.limitersByScope[scope]
		if !ok {
			lim = rate.NewLimiter(m.rateLimit, m.rateBurst)
			m.limitersByScope[scope] = lim
		}
		if !lim.Allow() {
			m.mu.Unlock()
			if m.onReject != nil {
				m.onReject(scope, "subscriptions/listen", "rate_limited")
			}
			return fmt.Errorf("%w: %v opens/sec, burst %d", ErrStatelessStreamRateLimited, m.rateLimit, m.rateBurst)
		}
	}
	s.scope = scope
	m.subs[s.id] = s
	m.countsByScope[scope]++
	m.mu.Unlock()
	return nil
}

// unregister removes a subscriber and decrements its scope's count. The
// limiter for the scope is dropped once the count hits zero so an idle
// scope leaves no state in the map.
func (m *statelessSubMap) unregister(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.subs[id]
	if !ok {
		return
	}
	delete(m.subs, id)
	if s.scope != "" && m.countsByScope[s.scope] > 0 {
		m.countsByScope[s.scope]--
		if m.countsByScope[s.scope] == 0 {
			delete(m.countsByScope, s.scope)
			delete(m.limitersByScope, s.scope)
		}
	}
}

// fanout pushes a notification onto every open subscriber whose filter
// admits the method. Non-blocking: if a subscriber's channel is full
// the frame drops for that subscriber (the conformance contract is
// about server-side discipline, not in-flight queueing).
func (m *statelessSubMap) fanout(method string, params any) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, sub := range m.subs {
		if !sub.filter.Matches(method) {
			continue
		}
		frame := stateless.NewTaggedFrame(method, params, sub.id)
		select {
		case sub.frames <- frame:
		default:
			// drop — see doc comment
		}
	}
}

// handleStatelessSubscribe runs the SEP-2575 subscriptions/listen stream
// for one client. Special-cased by handleStatelessPost when the request
// method matches; never goes through the dispatcher (which is request/
// response only).
//
// Flow:
//  1. Validate _meta + protocol version (reuse dispatcher's validators).
//  2. Decode the SubscribeParams filter.
//  3. Mint a subscriptionId; build the subscriber and register it.
//  4. Open the SSE stream (Content-Type: text/event-stream).
//  5. Emit the ack frame with subscriptionId in _meta.
//  6. Loop on the subscriber's frame channel, write each as an SSE
//     data: line. Exit on client disconnect.
//  7. Unregister on exit.
func (t *streamableTransport) handleStatelessSubscribe(w http.ResponseWriter, r *http.Request, req *core.Request) {
	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}

	// Header / _meta cross-check (same precedence as handleStatelessPost).
	if hdrVer := r.Header.Get(mcpProtocolVersionHeader); hdrVer != "" {
		metaVer := peekMetaProtocolVersion(req.Params)
		if resp := statelessVersionMismatch(id, hdrVer, metaVer); resp != nil {
			writeStatelessResponse(w, resp)
			return
		}
	}

	// _meta envelope validation.
	if _, err := core.DecodeRequestMeta(req.Params); err != nil {
		writeStatelessResponse(w, core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error()))
		return
	}

	// Filter decode.
	var params stateless.SubscribeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeStatelessResponse(w, core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"invalid subscriptions/listen params: "+err.Error()))
		return
	}

	// Verify the writer supports streaming. http.test ResponseRecorder
	// does not implement http.Flusher; clients that hit this path
	// must use a real net/http server (or an httptest.Server).
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeStatelessResponse(w, core.NewErrorResponse(id, core.ErrCodeInternal,
			"transport does not support streaming"))
		return
	}

	subID := newSubscriptionID()
	sub := &statelessSubscriber{
		id:     subID,
		filter: params.Notifications,
		frames: make(chan stateless.TaggedFrame, 16),
		done:   make(chan struct{}),
	}
	scope := t.statelessSubs.scopeFn(r)
	if err := t.statelessSubs.tryRegister(sub, scope); err != nil {
		reason := "cap_exceeded"
		if errors.Is(err, ErrStatelessStreamRateLimited) {
			reason = "rate_limited"
		}
		writeStatelessResponse(w, core.NewErrorResponseWithData(id,
			core.ErrCodeSubscriptionLimitExceeded,
			err.Error(),
			map[string]any{"reason": reason, "scope": scope},
		))
		return
	}
	defer t.statelessSubs.unregister(subID)

	// SSE response headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Ack frame — MUST be the first thing on the stream.
	ack := stateless.NewAcknowledgedFrame(subID)
	if err := writeSSEFrame(w, ack); err != nil {
		return
	}
	flusher.Flush()

	// Stream loop.
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-sub.frames:
			if err := writeSSEFrame(w, frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEFrame marshals v and writes it as a single SSE data: line
// terminated by a blank line. Returns the underlying write error so
// the caller bails on client disconnect.
func writeSSEFrame(w http.ResponseWriter, v any) error {
	raw, err := marshalJSON(v)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}
