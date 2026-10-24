package massrelay_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	pb "github.com/panyam/massrelay/gen/go/massrelay/v1/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
)

// The roster carries the subject the relay validated, from the token for a
// dialled peer and from the Principal for an embedded one.
func TestTransport_RosterCarriesValidatedSubject(t *testing.T) {
	for _, kind := range transportKinds {
		t.Run(kind, func(t *testing.T) {
			r := newRelay(t)
			alice := join(t, r.transport(t, kind, "alice"), room)
			bob := join(t, r.transport(t, kind, "bob"), room)

			ev := next(t, alice, rooms.PeerJoined)
			assert.Equal(t, rooms.Peer{ID: bob.Self(), Subject: "bob"}, ev.Peer)
			assert.Equal(t, []rooms.Peer{{ID: bob.Self(), Subject: "bob"}}, alice.Roster())
			assert.Equal(t, []rooms.Peer{{ID: alice.Self(), Subject: "alice"}}, bob.Roster(), "a later joiner sees who was there")
		})
	}
}

func TestTransport_SendReachesOnlyItsTarget(t *testing.T) {
	for _, kind := range transportKinds {
		t.Run(kind, func(t *testing.T) {
			r := newRelay(t)
			alice := join(t, r.transport(t, kind, "alice"), room)
			bob := join(t, r.transport(t, kind, "bob"), room)
			carol := join(t, r.transport(t, kind, "carol"), room)
			require.Eventually(t, func() bool { return len(alice.Roster()) == 2 }, 3e9, 1e7)

			payload := json.RawMessage(`{"jsonrpc":"2.0","method":"tools/list","id":1}`)
			require.NoError(t, alice.Send(t.Context(), bob.Self(), rooms.MessageKind, payload))

			ev := next(t, bob, rooms.MessageReceived)
			assert.Equal(t, rooms.Peer{ID: alice.Self(), Subject: "alice"}, ev.Peer)
			assert.Equal(t, rooms.MessageKind, ev.Kind)
			assert.JSONEq(t, string(payload), string(ev.Payload))
			quiet(t, carol)
		})
	}
}

// A room-wide AppMessage is someone else's traffic, so it is not reported;
// the transport carries only messages addressed to this peer.
func TestTransport_IgnoresRoomWideMessages(t *testing.T) {
	for _, kind := range transportKinds {
		t.Run(kind, func(t *testing.T) {
			r := newRelay(t)
			alice := join(t, r.transport(t, kind, "alice"), room)
			bob := join(t, r.transport(t, kind, "bob"), room)
			require.Eventually(t, func() bool { return len(alice.Roster()) == 1 }, 3e9, 1e7)

			require.NoError(t, r.svc.DeliverAppMessage(room, string(bob.Self()), &pb.AppMessage{Kind: rooms.MessageKind, Payload: []byte(`{"jsonrpc":"2.0","method":"x"}`)}))
			quiet(t, alice)
		})
	}
}

func TestTransport_LeaveReportsPeerLeftAndEndsSends(t *testing.T) {
	for _, kind := range transportKinds {
		t.Run(kind, func(t *testing.T) {
			r := newRelay(t)
			alice := join(t, r.transport(t, kind, "alice"), room)
			bob := join(t, r.transport(t, kind, "bob"), room)
			next(t, alice, rooms.PeerJoined)

			require.NoError(t, bob.Leave())
			ev := next(t, alice, rooms.PeerLeft)
			assert.Equal(t, rooms.Peer{ID: bob.Self(), Subject: "bob"}, ev.Peer)
			assert.Empty(t, alice.Roster())
			assert.ErrorIs(t, alice.Send(t.Context(), bob.Self(), rooms.MessageKind, json.RawMessage(`{}`)), massrelay.ErrNotMember)

			_, open := <-bob.Events()
			for open {
				_, open = <-bob.Events()
			}
			assert.NoError(t, bob.Leave(), "Leave is safe to call twice")
		})
	}
}

// An embedded bridge and a dialled page meet in the same room, which is how
// a host serving both runs.
func TestTransport_EmbeddedAndDialledPeersMeet(t *testing.T) {
	r := newRelay(t)
	bridge := join(t, r.embedded("bridge"), room)
	page := join(t, r.dial(t, "alice"), room)
	next(t, bridge, rooms.PeerJoined)

	require.NoError(t, bridge.Send(t.Context(), page.Self(), rooms.MessageKind, json.RawMessage(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)))
	ev := next(t, page, rooms.MessageReceived)
	assert.Equal(t, rooms.Peer{ID: bridge.Self(), Subject: "bridge"}, ev.Peer)

	require.NoError(t, page.Send(t.Context(), bridge.Self(), rooms.MessageKind, json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{}}`)))
	ev = next(t, bridge, rooms.MessageReceived)
	assert.Equal(t, rooms.Peer{ID: page.Self(), Subject: "alice"}, ev.Peer)
}

func TestDial_RefusesABadToken(t *testing.T) {
	r := newRelay(t)
	_, err := massrelay.Dial(r.url, "not-a-jwt").Join(t.Context(), room)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
}

// A token whose rooms claim does not cover the room cannot join it.
func TestDial_RoomsClaimLimitsTheJoin(t *testing.T) {
	r := newRelay(t)
	tok := token(t, "alice", map[string]any{"rooms": []string{"review:*"}})
	_, err := massrelay.Dial(r.url, tok).Join(t.Context(), room)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ROOM_FORBIDDEN")

	tok = token(t, "alice", map[string]any{"rooms": []string{"design:*"}})
	ok, err := massrelay.Dial(r.url, tok).Join(t.Context(), room)
	require.NoError(t, err)
	_ = ok.Leave()
}

// The relay drops what a WebSocket sends past 30 messages a second, without
// telling the sender, so a dialled transport paces itself under that and a
// burst arrives whole, only later (#1615).
func TestTransport_DialledBurstArrivesWhole(t *testing.T) {
	r := newRelay(t)
	alice := join(t, r.transport(t, "dial", "alice"), room)
	bob := join(t, r.transport(t, "embedded", "bob"), room)
	require.Eventually(t, func() bool { return len(alice.Roster()) == 1 }, 3e9, 1e7)

	const burst = 60
	go func() {
		for i := range burst {
			_ = alice.Send(t.Context(), bob.Self(), rooms.MessageKind, json.RawMessage(`{"jsonrpc":"2.0","method":"n","params":{"i":`+strconv.Itoa(i)+`}}`))
		}
	}()
	got := 0
	deadline := time.After(6 * time.Second)
	for got < burst {
		select {
		case ev := <-bob.Events():
			if ev.Type == rooms.MessageReceived {
				got++
			}
		case <-deadline:
			t.Fatalf("%d of %d messages arrived; the relay dropped the rest", got, burst)
		}
	}
}
