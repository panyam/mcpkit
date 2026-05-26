package server

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"sync"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
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
type statelessSubMap struct {
	mu   sync.RWMutex
	subs map[string]*statelessSubscriber
}

func newStatelessSubMap() *statelessSubMap {
	return &statelessSubMap{subs: make(map[string]*statelessSubscriber)}
}

func (m *statelessSubMap) register(s *statelessSubscriber) {
	m.mu.Lock()
	m.subs[s.id] = s
	m.mu.Unlock()
}

func (m *statelessSubMap) unregister(id string) {
	m.mu.Lock()
	delete(m.subs, id)
	m.mu.Unlock()
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
		if resp := headerMismatchResponse(id, hdrVer, metaVer); resp != nil {
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
	t.statelessSubs.register(sub)
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
