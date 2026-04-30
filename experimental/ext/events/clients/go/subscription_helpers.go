package eventsclient

import (
	"encoding/json"
	"fmt"
)

// unmarshalResult extracts the JSON-RPC `result` envelope from a raw
// response body and decodes it into out. Helper because the mcpkit
// client.Call returns the full JSON-RPC envelope, not just the result.
func unmarshalResult(raw json.RawMessage, out any) error {
	// Some client.Call paths return just the result; others return the
	// full envelope. Handle both by trying envelope-first and falling
	// through to bare-result.
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return json.Unmarshal(raw, out)
}

// debugEvent is a small helper used internally for diagnostics; kept here
// to avoid import cycles between subscription.go and event.go.
func debugEvent[Data any](e Event[Data]) string {
	return fmt.Sprintf("Event{name=%s eventID=%s cursor=%v}", e.Name, e.EventID, e.Cursor)
}
