package server

import (
	"encoding/json"

	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
)

// sseWiring bundles the closures that SSE-based transports (SSE and
// Streamable HTTP GET SSE) install on a session dispatcher during
// OnStart. Both transports share the same pipeline:
//
//	notification → core.MarshalNotification → emitSSEEvent → hub.SendEventWithID
//
// and the same keepalive construction pattern. Extracting the shared
// wiring into this struct eliminates ~30 lines of near-identical code
// between mcpSSEConn.OnStart and streamableSSEConn.OnStart.
//
// Stdio and in-process transports have different I/O shapes (frame-based
// stdout, synchronous function calls) and are NOT wired through this
// helper — forcing them into the same abstraction would be worse than
// the duplication.
//
// Issue #199.
type sseWiring struct {
	dispatcher *Dispatcher
	hub        *gohttp.SSEHub[SSEData]
	sessionID  string
	store      gohttp.EventStore // nil when event persistence is not configured
	cfg        transportConfig   // for keepaliveInterval, keepaliveMaxFails
	onDeath    func()            // called when keepalive max failures reached (closeSession or expireSession)
	onPingFail func(int)         // called on each keepalive ping failure
}

// wire installs notifyFunc and pushRequest on the dispatcher, and
// optionally constructs a sessionKeepalive (nil if keepalive is not
// configured, i.e. cfg.keepaliveInterval == 0). Returns the pushFunc
// closure for callers that need it after wiring (e.g., for SSE event
// replay on reconnect).
//
// Idempotent: calling wire() on the same dispatcher overwrites the
// previous notifyFunc and pushRequest. This is the expected behavior
// on SSE reconnect (grace period), where the closures must be refreshed
// to point at the new hub connection.
func (w *sseWiring) wire() (pushFunc func(json.RawMessage), keepalive *sessionKeepalive) {
	sessionID := w.sessionID
	hub := w.hub
	store := w.store
	eventIDs := w.dispatcher.eventIDs

	// hubSend is the leaf writer: sends a single SSE event to the hub
	// connection identified by sessionID.
	hubSend := func(id string, data json.RawMessage) {
		hub.SendEventWithID(sessionID, "message", id, SSEJSON(data))
	}

	// notifyFunc: marshal a JSON-RPC notification → emit as an SSE event
	// with a unique ID (for Last-Event-ID replay on reconnect).
	w.dispatcher.SetNotifyFunc(func(method string, params any) {
		raw, err := core.MarshalNotification(method, params)
		if err != nil {
			return
		}
		emitSSEEvent(eventIDs, store, sessionID, raw, hubSend)
	})

	// pushFunc: write a raw JSON-RPC request/response as an SSE event.
	// Used by server-to-client requests (roots/list, sampling, elicitation,
	// keepalive ping) and by the caller for event replay after reconnect.
	pushFunc = func(raw json.RawMessage) {
		emitSSEEvent(eventIDs, store, sessionID, raw, hubSend)
	}
	w.dispatcher.SetPushRequest(pushFunc)

	// Keepalive: periodic ping via the push function. Only constructed
	// when the server has configured a keepalive interval.
	if w.cfg.keepaliveInterval > 0 {
		maxFails := w.cfg.keepaliveMaxFails
		if maxFails <= 0 {
			maxFails = 3
		}
		keepalive = &sessionKeepalive{
			interval:    w.cfg.keepaliveInterval,
			maxFailures: maxFails,
			requestFunc: w.dispatcher.makeRequestFunc(pushFunc),
			onDeath:     w.onDeath,
			onPingFail:  w.onPingFail,
		}
	}

	return pushFunc, keepalive
}
