package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Spec §"Gaps and truncated": for event types that do not support replay,
// truncated SHOULD be false, since there is no position to have advanced past.
// A source that can name no position (cursorless, or with cursors but nothing
// yielded) gets no fresh active{truncated:true} on push, matching what
// webhook does with PostGap (#1468, #1466).

// nextActiveOrEvent returns the next active or event notification, skipping
// heartbeats, so a stray active ahead of an event is caught.
func nextActiveOrEvent(t *testing.T, st *streamRoutingStack) (string, map[string]any) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case n := <-st.notifs:
			if n.method != "notifications/events/active" && n.method != "notifications/events/event" {
				continue
			}
			var p map[string]any
			require.NoError(t, json.Unmarshal(n.params, &p))
			return n.method, p
		case <-deadline:
			t.Fatal("no active or event notification within 1s")
			return "", nil
		}
	}
}

func startNoReplayStream(t *testing.T, def EventDef, latest string) (*fakeSubscribableSource, *streamRoutingStack, func()) {
	t.Helper()
	fake := &fakeSubscribableSource{def: def, ch: make(chan SubscriberEvent, 4), latest: latest}
	st := newStreamRoutingStack(t, []EventSource{fake}, 0)
	rs := st.startStreamID(t, json.RawMessage(`700`), def.Name)
	expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	return fake, st, func() { rs.endAndAwait(t, time.Second) }
}

func recoveryEvent(name string, cursor *string) Event {
	return Event{EventID: "evt_after", Name: name, Timestamp: "t", Data: json.RawMessage(`{}`), Cursor: cursor}
}

func TestStream_NoReplay_DroppedEventArrivesWithoutTruncatedActive(t *testing.T) {
	def := EventDef{Name: "presence.like", Delivery: []string{"push"}, Cursorless: true}
	fake, st, end := startNoReplayStream(t, def, "")
	defer end()

	fake.ch <- SubscriberEvent{Truncated: true, Event: recoveryEvent(def.Name, nil)}

	method, p := nextActiveOrEvent(t, st)
	require.Equal(t, "notifications/events/event", method, "a cursorless type must not get active{truncated:true}")
	assert.Equal(t, "evt_after", p["eventId"])
	assert.Nil(t, p["cursor"])
}

func TestStream_NoReplay_GapAloneSendsNothing(t *testing.T) {
	def := EventDef{Name: "presence.like", Delivery: []string{"push"}, Cursorless: true}
	fake, st, end := startNoReplayStream(t, def, "")
	defer end()

	fake.ch <- SubscriberEvent{Truncated: true}
	fake.ch <- SubscriberEvent{Event: recoveryEvent(def.Name, nil)}

	method, p := nextActiveOrEvent(t, st)
	require.Equal(t, "notifications/events/event", method)
	assert.Equal(t, "evt_after", p["eventId"])
}

func TestStream_EmptySourceWithCursors_GapAloneSendsNothing(t *testing.T) {
	def := EventDef{Name: "fresh.event", Delivery: []string{"push"}}
	fake, st, end := startNoReplayStream(t, def, "")
	defer end()

	fake.ch <- SubscriberEvent{Truncated: true}
	c := "1"
	fake.ch <- SubscriberEvent{Event: recoveryEvent(def.Name, &c)}

	method, _ := nextActiveOrEvent(t, st)
	assert.Equal(t, "notifications/events/event", method, "an empty cursor is no position to resume from")
}
