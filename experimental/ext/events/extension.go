package events

import "github.com/panyam/mcpkit/core"

// ExtensionID is the MCP Events extension identifier, the key this extension
// occupies under `capabilities.extensions`.
const ExtensionID = "io.modelcontextprotocol/events"

// EventsExtension declares MCP Events support through SEP-2133 Extension
// Negotiation.
//
// Register declares it automatically as part of wiring the events/* handlers,
// so callers using Register do not need to add it; RegisterExtension is keyed
// by extension ID and idempotent, so doing both is safe. Declare it directly
// via server.WithExtension or srv.RegisterExtension only when wiring the
// handlers by hand.
//
// ListChanged reports whether the server sends
// notifications/events/list_changed. The spec makes this a promise rather than
// a hint: the notification is sent only by a server that declared true here.
// Register sets it true because AddSource and RemoveSource always broadcast.
type EventsExtension struct {
	ListChanged bool
}

// Extension implements core.ExtensionProvider.
//
// A false ListChanged yields an empty settings object rather than
// `{"listChanged": false}`. The spec defines false as the default and reads an
// empty object as "event support, no list-change notifications", so omitting it
// says the same thing in fewer bytes and keeps the wire identical to a server
// that never considered the question.
func (e EventsExtension) Extension() core.Extension {
	settings := map[string]any{}
	if e.ListChanged {
		settings["listChanged"] = true
	}
	return core.Extension{ID: ExtensionID, Settings: settings}
}
