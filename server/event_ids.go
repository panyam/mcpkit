package server

import (
	"encoding/json"

	gohttp "github.com/panyam/servicekit/http"
)

// newEventIDGen creates the default event ID generator for a session.
// Returns a servicekit AtomicIDGen which produces unique opaque string IDs.
func newEventIDGen() gohttp.IDGen {
	return &gohttp.AtomicIDGen{}
}

// emitSSEEvent generates an ID, sends the event via send, and persists it
// if a store is configured. All SSE event emission flows through this
// function to ensure consistent ID assignment and storage.
func emitSSEEvent(idGen gohttp.IDGen, store gohttp.EventStore, streamID string, data json.RawMessage, send func(id string, data json.RawMessage)) {
	id := idGen.Next()
	send(id, data)
	if store != nil {
		store.Store(streamID, gohttp.StoredEvent{ID: id, Event: "message", Data: data})
	}
}
