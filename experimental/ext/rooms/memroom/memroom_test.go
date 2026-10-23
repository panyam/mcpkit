package memroom_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/memroom"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func next(t *testing.T, r rooms.Room) rooms.RoomEvent {
	t.Helper()
	select {
	case ev := <-r.Events():
		return ev
	case <-time.After(time.Second):
		t.Fatal("no event")
		return rooms.RoomEvent{}
	}
}

func TestRelay_AddressedDeliveryReachesOnlyTheTarget(t *testing.T) {
	relay := memroom.New()
	a, err := relay.Transport("alice").Join(t.Context(), "r")
	require.NoError(t, err)
	b, _ := relay.Transport("bob").Join(t.Context(), "r")
	c, _ := relay.Transport("carol").Join(t.Context(), "r")
	assert.Equal(t, rooms.PeerJoined, next(t, a).Type) // b
	assert.Equal(t, rooms.PeerJoined, next(t, a).Type) // c
	assert.Equal(t, rooms.PeerJoined, next(t, b).Type) // c

	require.NoError(t, a.Send(t.Context(), b.Self(), "mcp/1", json.RawMessage(`{"x":1}`)))
	ev := next(t, b)
	assert.Equal(t, rooms.MessageReceived, ev.Type)
	assert.Equal(t, rooms.Peer{ID: a.Self(), Subject: "alice"}, ev.Peer)
	assert.JSONEq(t, `{"x":1}`, string(ev.Payload))
	select {
	case ev := <-c.Events():
		t.Fatalf("carol received a message addressed to bob: %+v", ev)
	default:
	}
}

func TestRelay_LeaveIsReportedAndClosesEvents(t *testing.T) {
	relay := memroom.New()
	a, _ := relay.Transport("alice").Join(t.Context(), "r")
	b, _ := relay.Transport("bob").Join(t.Context(), "r")
	next(t, a)
	require.NoError(t, b.Leave())
	ev := next(t, a)
	assert.Equal(t, rooms.PeerLeft, ev.Type)
	assert.Equal(t, b.Self(), ev.Peer.ID)
	_, open := <-b.Events()
	assert.False(t, open)
	assert.ErrorIs(t, a.Send(t.Context(), b.Self(), "mcp/1", nil), memroom.ErrNotMember)
	assert.Empty(t, a.Roster())
}
