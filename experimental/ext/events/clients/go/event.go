package eventsclient

// Event is a typed webhook event delivered to receivers. The Data field is
// the decoded application payload — distinct from the wire envelope's
// json.RawMessage. Cursor mirrors the wire field: nil for cursorless
// sources, non-nil for cursored sources.
type Event[Data any] struct {
	EventID   string
	Name      string
	Timestamp string
	Cursor    *string
	Data      Data
}

// HasCursor reports whether the event carries a cursor.
func (e Event[Data]) HasCursor() bool { return e.Cursor != nil }

// CursorStr returns the cursor string for cursored events, or "" for
// cursorless ones.
func (e Event[Data]) CursorStr() string {
	if e.Cursor == nil {
		return ""
	}
	return *e.Cursor
}
