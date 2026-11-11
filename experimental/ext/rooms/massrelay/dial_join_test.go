package massrelay_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	pb "github.com/panyam/massrelay/gen/go/massrelay/v1/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/massrelay"
)

// earlyRelay is a relay that writes room traffic to a joining socket before its RoomJoined. The
// real relay can do the same: it adds the client to the room and starts forwarding the room's
// broadcasts, and only then writes the join's answer, so a broadcast can win the race. The
// events it sends first are real ones for the new member, made after the roster in RoomJoined
// was taken.
func earlyRelay(t *testing.T, early ...*pb.CollabEvent) string {
	t.Helper()
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var join map[string]any
		if err := conn.ReadJSON(&join); err != nil {
			return
		}
		send := func(ev *pb.CollabEvent) {
			data, err := protojson.Marshal(ev)
			require.NoError(t, err)
			require.NoError(t, conn.WriteJSON(map[string]any{"type": "data", "data": json.RawMessage(data)}))
		}
		for _, ev := range early {
			send(ev)
		}
		send(&pb.CollabEvent{Event: &pb.CollabEvent_RoomJoined{RoomJoined: &pb.RoomJoined{
			ClientId: "me",
			Room:     &pb.Room{SessionId: "r", Peers: map[string]*pb.PeerInfo{"page": {ClientId: "page"}}},
		}}})
		// Hold the socket open until the client leaves.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A join succeeds when room traffic reaches the socket ahead of RoomJoined, and the member
// reports that traffic rather than losing it.
func TestDial_JoinKeepsEventsThatArriveBeforeRoomJoined(t *testing.T) {
	url := earlyRelay(t,
		&pb.CollabEvent{FromClientId: "late", Event: &pb.CollabEvent_PeerJoined{PeerJoined: &pb.PeerJoined{
			Peer: &pb.PeerInfo{ClientId: "late"},
		}}},
		&pb.CollabEvent{FromClientId: "page", Event: &pb.CollabEvent_AppMessage{AppMessage: &pb.AppMessage{
			ToClientIds: []string{"me"}, Kind: "mcp", Payload: []byte(`{"id":7}`),
		}}},
	)
	r := join(t, massrelay.Dial(url, ""), "r")

	joined := next(t, r, rooms.PeerJoined)
	assert.Equal(t, rooms.PeerID("late"), joined.Peer.ID)
	msg := next(t, r, rooms.MessageReceived)
	assert.Equal(t, rooms.PeerID("page"), msg.Peer.ID)
	assert.JSONEq(t, `{"id":7}`, string(msg.Payload))
	assert.ElementsMatch(t, []rooms.PeerID{"page", "late"}, peerIDs(r.Roster()))
}

func peerIDs(ps []rooms.Peer) []rooms.PeerID {
	out := make([]rooms.PeerID, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}
