package massrelay

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pb "github.com/panyam/massrelay/gen/go/massrelay/v1/models"
	"github.com/panyam/massrelay/services"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

// Embedded returns a RoomTransport that joins rooms on svc in this process, as
// principal. The relay records principal's Subject as the peer's identity and
// enforces its Rooms, exactly as for a WebSocket connection whose token
// carried them, so the caller is vouching for that identity: pass the subject
// the host authenticated, never one a client asserted. A nil principal joins
// anonymously, which leaves the peer with no Subject, so the bridge will pair
// it with no one.
//
// Each Join is a separate non-owner peer, so leaving never ends the room for
// the others.
func Embedded(svc *services.CollabService, principal *services.Principal) rooms.RoomTransport {
	return embedded{svc: svc, principal: principal}
}

type embedded struct {
	svc       *services.CollabService
	principal *services.Principal
}

func (e embedded) Join(ctx context.Context, room string) (rooms.Room, error) {
	actx := services.WithPrincipal(context.Background(), e.principal)
	resp, err := e.svc.HandleAction(actx, joinAction(room, subjectOf(e.principal), e.svc.ProtocolVersion))
	if err != nil {
		return nil, fmt.Errorf("massrelay: join %s: %w", room, err)
	}
	joined, err := joinedFrom(room, resp)
	if err != nil {
		return nil, err
	}
	m := newMember(joined)
	ch := e.svc.GetClientSendCh(room, string(m.self))
	if ch == nil {
		return nil, fmt.Errorf("massrelay: join %s: no channel for %s", room, m.self)
	}
	done := make(chan struct{})
	var once sync.Once
	m.leave = func() {
		once.Do(func() {
			close(done)
			_, _ = e.svc.HandleAction(actx, &pb.CollabAction{
				ClientId: string(m.self),
				Action:   &pb.CollabAction_Leave{Leave: &pb.LeaveRoom{Reason: "left"}},
			})
		})
	}
	m.send = func(_ context.Context, action *pb.CollabAction) error {
		resp, err := e.svc.HandleAction(actx, action)
		if err != nil {
			return err
		}
		if code := resp.GetError().GetCode(); code == "PEER_NOT_FOUND" {
			return &rooms.NotMembersError{Peers: missingPeers(resp.GetError().GetMessage())}
		} else if code != "" {
			return fmt.Errorf("massrelay: %s: %s", code, resp.GetError().GetMessage())
		}
		return nil
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case ev, ok := <-ch:
				if !ok {
					m.close()
					return
				}
				m.handle(ev)
			}
		}
	}()
	return m, nil
}

func subjectOf(p *services.Principal) string {
	if p == nil {
		return ""
	}
	return p.Subject
}

func joinAction(room, username string, protocolVersion int32) *pb.CollabAction {
	if username == "" {
		username = "mcp"
	}
	return &pb.CollabAction{Action: &pb.CollabAction_Join{Join: &pb.JoinRoom{
		SessionId:       room,
		Username:        username,
		ClientType:      "api",
		ProtocolVersion: protocolVersion,
	}}}
}

// joinedFrom reads the relay's answer to a join: RoomJoined, or an ErrorEvent
// such as ROOM_FULL or ROOM_FORBIDDEN, which becomes the join's error.
func joinedFrom(room string, ev *pb.CollabEvent) (*pb.RoomJoined, error) {
	if e := ev.GetError(); e != nil {
		return nil, fmt.Errorf("massrelay: join %s: %s: %s", room, e.GetCode(), e.GetMessage())
	}
	joined := ev.GetRoomJoined()
	if joined == nil {
		return nil, fmt.Errorf("massrelay: join %s: unexpected answer %T", room, ev.GetEvent())
	}
	return joined, nil
}

// missingPeers reads the ids out of the relay's PEER_NOT_FOUND message, "Not
// in this room: a, b", which is the only place v0.3 names them for a socket's
// action.
func missingPeers(msg string) []rooms.PeerID {
	_, list, ok := strings.Cut(msg, ": ")
	if !ok {
		return nil
	}
	var out []rooms.PeerID
	for _, id := range strings.Split(list, ", ") {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, rooms.PeerID(id))
		}
	}
	return out
}
