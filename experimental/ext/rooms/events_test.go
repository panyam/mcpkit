package rooms_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func eventRecorder() (chan rooms.PageEvent, rooms.Option) {
	ch := make(chan rooms.PageEvent, 16)
	return ch, rooms.WithPageEvents(func(e rooms.PageEvent) { ch <- e })
}

func nextEvent(t *testing.T, ch chan rooms.PageEvent) rooms.PageEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no page event reached the hook")
		return rooms.PageEvent{}
	}
}

func noEvent(t *testing.T, ch chan rooms.PageEvent) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected page event %+v", e)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestBridge_PageEventReachesHook(t *testing.T) {
	ch, opt := eventRecorder()
	st := newStack(t, opt)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := st.page(t, "alice", room)
	a.waitPage(t, p.ID(), func(rooms.PageInfo) bool { return true })

	p.SetLabel("Viewer")
	require.NoError(t, p.Ask("why is SDA flagged?"))
	e := nextEvent(t, ch)
	assert.Equal(t, room, e.Resource)
	assert.Equal(t, p.ID(), e.Page)
	assert.Equal(t, "alice", e.Subject, "the subject comes from the page's relay identity")
	assert.Equal(t, "Viewer", e.Label)
	assert.Equal(t, rooms.EventAsk, e.Name)
	assert.JSONEq(t, `{"text":"why is SDA flagged?"}`, string(e.Data))

	require.NoError(t, p.SelectionChanged(map[string]any{"net": "SDA"}))
	e = nextEvent(t, ch)
	assert.Equal(t, rooms.EventSelectionChanged, e.Name)
	assert.JSONEq(t, `{"selection":{"net":"SDA"}}`, string(e.Data))

	require.NoError(t, p.Emit("viewer.sheet_opened", []string{"s-power"}))
	e = nextEvent(t, ch)
	assert.Equal(t, "viewer.sheet_opened", e.Name, "a page may emit names of its own")
}

func TestPage_EmitRefusesInvalidNamesAndOversizedData(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room)
	for _, name := range []string{"", "Room.Ask", "room ask", "1room", "a.b.c.d.e", strings.Repeat("a", 65)} {
		assert.ErrorIs(t, p.Emit(name, nil), rooms.ErrInvalidEvent, "name %q", name)
	}
	assert.ErrorIs(t, p.Emit("room.ask", strings.Repeat("x", rooms.MaxEventDataBytes)), rooms.ErrInvalidEvent)
}

// The page side refuses bad events before they leave, but a page is any peer
// with a relay token, so the bridge checks again.
func TestBridge_DropsInvalidEventsFromARawPeer(t *testing.T) {
	ch, opt := eventRecorder()
	st := newStack(t, opt)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)

	raw, err := st.relay.Transport("mallory").Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Leave() })
	var bridge rooms.PeerID
	for ev := range raw.Events() {
		if ev.Type != rooms.MessageReceived {
			continue
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		require.NoError(t, json.Unmarshal(ev.Payload, &m))
		if m.Method == "tools/list" {
			bridge = ev.Peer.ID
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"tools": []any{}}})
			require.NoError(t, raw.Send(t.Context(), bridge, rooms.MessageKind, reply))
			break
		}
	}
	a.waitPage(t, raw.Self(), func(rooms.PageInfo) bool { return true })

	send := func(name string, data any) {
		params, _ := json.Marshal(map[string]any{"name": name, "data": data})
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": rooms.MethodPageEvent, "params": json.RawMessage(params)})
		require.NoError(t, raw.Send(t.Context(), bridge, rooms.MessageKind, msg))
	}
	send("Room Ask", "x")
	send("room.ask", strings.Repeat("y", rooms.MaxEventDataBytes+1))
	noEvent(t, ch)
	send("room.ask", map[string]any{"text": "ok"})
	assert.Equal(t, "mallory", nextEvent(t, ch).Subject)
}

func TestBridge_AttachedAndPageAgreed(t *testing.T) {
	st := newStack(t)
	a := st.agent(t, "alice", allScopes...)
	p := st.page(t, "alice", room)
	assert.False(t, st.bridge.Attached(a.SessionID(), room))

	a.attach(t, room)
	a.waitPage(t, p.ID(), func(rooms.PageInfo) bool { return true })
	assert.True(t, st.bridge.Attached(a.SessionID(), room))
	assert.False(t, st.bridge.PageAgreed(a.SessionID(), room, p.ID()), "listed is not agreed")

	p.SetAgentsAllowed(true)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })
	assert.True(t, st.bridge.PageAgreed(a.SessionID(), room, p.ID()))
	assert.False(t, st.bridge.PageAgreed(a.SessionID(), "design:other", p.ID()))
}

// HearsPage is Attached and PageAgreed together, and a detach ends it even
// though the page still agrees.
func TestBridge_HearsPageNeedsAttachAndAgreement(t *testing.T) {
	st := newStack(t)
	a := st.agent(t, "alice", allScopes...)
	p := st.page(t, "alice", room)
	p.SetAgentsAllowed(true)
	assert.False(t, st.bridge.HearsPage(a.SessionID(), room, p.ID()), "not attached")

	a.attach(t, room)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })
	assert.True(t, st.bridge.HearsPage(a.SessionID(), room, p.ID()))

	ok, out := call(t, a, "detach", map[string]any{"resource": room})
	require.True(t, ok, out)
	assert.True(t, st.bridge.PageAgreed(a.SessionID(), room, p.ID()), "the page still agrees")
	assert.False(t, st.bridge.HearsPage(a.SessionID(), room, p.ID()), "detached")
}
