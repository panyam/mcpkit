package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEvent_NilCursorSerializesAsNull pins the wire shape: an Event with
// nil Cursor must marshal to JSON with `"cursor": null`, NOT omitted.
// Receivers that key on cursor presence depend on this distinction.
func TestEvent_NilCursorSerializesAsNull(t *testing.T) {
	e := Event{EventID: "evt_1", Name: "x", Timestamp: "t", Data: json.RawMessage(`{}`)}
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"cursor":null`,
		"cursorless events must wire as cursor:null, not omit the field")
}

// TestEvent_NonNilCursorSerializesAsString pins the cursored wire shape.
func TestEvent_NonNilCursorSerializesAsString(t *testing.T) {
	c := "abc123"
	e := Event{EventID: "evt_1", Name: "x", Timestamp: "t", Data: json.RawMessage(`{}`), Cursor: &c}
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"cursor":"abc123"`)
}

// TestEvent_HasCursorAndCursorStr exercises the helper accessors so internal
// callers don't have to do `*event.Cursor` themselves and risk a nil deref.
func TestEvent_HasCursorAndCursorStr(t *testing.T) {
	none := Event{}
	assert.False(t, none.HasCursor())
	assert.Equal(t, "", none.CursorStr())

	c := "abc"
	with := Event{Cursor: &c}
	assert.True(t, with.HasCursor())
	assert.Equal(t, "abc", with.CursorStr())
}

// TestMakeEvent_EmptyCursorYieldsNil verifies the constructor's empty-string
// → nil pointer convention. Source authors use empty string for "no cursor"
// and the wire layer translates to null without a separate API.
func TestMakeEvent_EmptyCursorYieldsNil(t *testing.T) {
	e := MakeEvent[map[string]string]("name", "evt_1", "", testTime(), map[string]string{"k": "v"})
	assert.False(t, e.HasCursor(), "empty cursor must produce nil pointer")
	assert.Nil(t, e.Cursor)
}

// TestMakeEvent_NonEmptyCursorYieldsPointer verifies the cursored case.
func TestMakeEvent_NonEmptyCursorYieldsPointer(t *testing.T) {
	e := MakeEvent[map[string]string]("name", "evt_1", "abc", testTime(), map[string]string{"k": "v"})
	require.True(t, e.HasCursor())
	assert.Equal(t, "abc", e.CursorStr())
}

// TestYieldingSource_WithoutCursorsEmitsNilCursor verifies that yielded
// events from a cursorless source carry a nil Cursor. This is the source
// author-side API for ephemeral-state sources (typing, presence, etc.).
func TestYieldingSource_WithoutCursorsEmitsNilCursor(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"}, WithoutCursors())

	var captured Event
	src.SetEmitHook(func(e Event) { captured = e })

	require.NoError(t, yield(fakePayload{Msg: "x"}))
	assert.False(t, captured.HasCursor(),
		"cursorless source must emit events with nil cursor")
}

// TestYieldingSource_WithoutCursorsDoesNotBuffer verifies the source skips
// buffering — Len, Recent, ByCursor all show no retained state. Saves
// memory and signals to operators that replay isn't supported.
func TestYieldingSource_WithoutCursorsDoesNotBuffer(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"}, WithoutCursors())
	require.NoError(t, yield(fakePayload{Msg: "a"}))
	require.NoError(t, yield(fakePayload{Msg: "b"}))

	assert.Equal(t, 0, src.Len(), "cursorless source must not buffer")
	assert.Empty(t, src.Recent(50), "Recent on cursorless source must be empty")
	_, ok := src.ByCursor("anything")
	assert.False(t, ok)
}

// TestYieldingSource_WithoutCursorsPollIsAlwaysEmpty verifies Poll on a
// cursorless source returns no events regardless of the requested cursor —
// caller knows there's nothing to replay.
func TestYieldingSource_WithoutCursorsPollIsAlwaysEmpty(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"}, WithoutCursors())
	for i := 0; i < 3; i++ {
		require.NoError(t, yield(fakePayload{Msg: "x"}))
	}

	pr := src.Poll("", 100)
	assert.Empty(t, pr.Events)
	assert.Equal(t, "", pr.Cursor)
}

// TestYieldingSource_WithoutCursorsLatestIsEmpty verifies Latest returns ""
// for cursorless sources. The poll handler keys on this to decide whether
// `cursor: null` should resolve to a real value or stay null.
func TestYieldingSource_WithoutCursorsLatestIsEmpty(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"}, WithoutCursors())
	require.NoError(t, yield(fakePayload{Msg: "x"}))
	assert.Equal(t, "", src.Latest())
}

// TestYieldingSource_DefIsCursorless verifies the cursorless flag flows
// onto the EventDef advertised via events/list. Clients use this to decide
// whether to bother passing a real cursor on subscribe / poll.
func TestYieldingSource_DefIsCursorless(t *testing.T) {
	cursored, _ := NewYieldingSource[fakePayload](EventDef{Name: "a"})
	cursorless, _ := NewYieldingSource[fakePayload](EventDef{Name: "b"}, WithoutCursors())

	assert.False(t, cursored.Def().Cursorless)
	assert.True(t, cursorless.Def().Cursorless)
}

// TestYieldingSource_LatestReturnsHeadCursor verifies cursored sources
// expose the head cursor for `cursor: null` subscribe resolution. The
// subscribe handler stamps this onto the response so the client polls
// from the right point onward.
func TestYieldingSource_LatestReturnsHeadCursor(t *testing.T) {
	src, yield := NewYieldingSource[fakePayload](EventDef{Name: "fake"})
	assert.Equal(t, "", src.Latest(), "empty source has no head")
	require.NoError(t, yield(fakePayload{Msg: "a"}))
	require.NoError(t, yield(fakePayload{Msg: "b"}))
	assert.NotEmpty(t, src.Latest())
	assert.Equal(t, "2", src.Latest(), "memory store assigns monotonic int cursors starting at 1")
}

// testTime returns a fixed timestamp for deterministic tests.
func testTime() (t time.Time) {
	t, _ = time.Parse(time.RFC3339, "2026-04-30T00:00:00Z")
	return
}
