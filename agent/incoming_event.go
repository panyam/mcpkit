package agent

import (
	"time"

	"github.com/panyam/mcpkit/core"
)

// IncomingEvent is the neutral event shape the policy engines consume,
// deliberately decoupled from any delivery mechanism: the events extension's
// stream, a webhook receiver, or a future page-bridge source all adapt into
// it. Wire-serializable per constraints A2/A5 (RawJSON payload) so wire
// surfaces can forward events verbatim.
type IncomingEvent struct {
	// Server identifies the source connection (the MultiSource id in
	// agentchat's wiring).
	Server string `json:"server"`

	// Name is the event name as declared in events/list.
	Name string `json:"name"`

	// ID is the delivery's event id, when the source provides one.
	ID string `json:"id,omitempty"`

	// Cursor is the replay cursor for cursored sources, empty otherwise.
	Cursor string `json:"cursor,omitempty"`

	// Time is when the host received the event (not the server's
	// timestamp; policies window on receipt time so an injected clock
	// governs tests).
	Time time.Time `json:"time"`

	// Data is the payload.
	Data core.RawJSON `json:"data"`

	// Meta is the occurrence-level _meta, opaque.
	Meta map[string]any `json:"meta,omitempty"`
}
