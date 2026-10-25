package massrelay

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"

	pb "github.com/panyam/massrelay/gen/go/massrelay/v1/models"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

// ErrNotMember is returned by Send when the target is not in the room, or the
// sender has left it.
var ErrNotMember = errors.New("massrelay: peer is not in the room")

// eventBuffer bounds each member's undelivered events, as in memroom. A member
// that falls this far behind is disconnected rather than allowed to make the
// relay drop its messages, which it does silently past 64.
const eventBuffer = 1024

// member is one membership, whichever way it reaches the relay. send carries
// one action to the relay; leave tears the connection down. rateLimited, set
// only when dialled, handles the message of a RATE_LIMITED error.
type member struct {
	self        rooms.PeerID
	send        func(ctx context.Context, action *pb.CollabAction) error
	leave       func()
	rateLimited func(message string)
	events      chan rooms.RoomEvent

	mu     sync.Mutex
	roster map[rooms.PeerID]rooms.Peer
	gone   bool
}

func newMember(joined *pb.RoomJoined) *member {
	m := &member{
		self:   rooms.PeerID(joined.GetClientId()),
		events: make(chan rooms.RoomEvent, eventBuffer),
		roster: make(map[rooms.PeerID]rooms.Peer),
	}
	for _, p := range joined.GetRoom().GetPeers() {
		peer := peerOf(p)
		if peer.ID != m.self {
			m.roster[peer.ID] = peer
		}
	}
	return m
}

func peerOf(p *pb.PeerInfo) rooms.Peer {
	return rooms.Peer{ID: rooms.PeerID(p.GetClientId()), Subject: p.GetIdentity().GetSubject()}
}

// handle turns one relay event into what the room reports. Presence, scene and
// other collaboration traffic is not the bridge's and is dropped, as are app
// messages addressed to someone else or to the whole room: the transport
// promises messages addressed to this peer, and nothing the bridge sends is
// room-wide.
func (m *member) handle(ev *pb.CollabEvent) {
	switch {
	case ev.GetPeerJoined() != nil:
		peer := peerOf(ev.GetPeerJoined().GetPeer())
		m.mu.Lock()
		m.roster[peer.ID] = peer
		m.mu.Unlock()
		m.push(rooms.RoomEvent{Type: rooms.PeerJoined, Peer: peer})
	case ev.GetPeerLeft() != nil:
		id := rooms.PeerID(ev.GetPeerLeft().GetClientId())
		m.mu.Lock()
		peer, ok := m.roster[id]
		delete(m.roster, id)
		m.mu.Unlock()
		if !ok {
			peer = rooms.Peer{ID: id}
		}
		m.push(rooms.RoomEvent{Type: rooms.PeerLeft, Peer: peer})
	case ev.GetAppMessage() != nil:
		msg := ev.GetAppMessage()
		// The relay rewrites to_client_ids to hold only the receiver's own id,
		// and leaves it empty for a room-wide message, which the bridge never
		// sends and doesn't act on.
		if !slices.Contains(msg.GetToClientIds(), string(m.self)) {
			return
		}
		from := rooms.PeerID(ev.GetFromClientId())
		m.mu.Lock()
		peer, ok := m.roster[from]
		m.mu.Unlock()
		if !ok {
			// A message from outside the roster, e.g. the embedding server
			// itself, has no subject the relay vouched for.
			peer = rooms.Peer{ID: from}
		}
		m.push(rooms.RoomEvent{Type: rooms.MessageReceived, Peer: peer, Kind: msg.GetKind(), Payload: json.RawMessage(slices.Clone(msg.GetPayload()))})
	case ev.GetError().GetCode() == "RATE_LIMITED":
		if m.rateLimited != nil {
			m.rateLimited(ev.GetError().GetMessage())
		}
	case ev.GetSessionEnded() != nil:
		m.close()
	}
}

func (m *member) push(ev rooms.RoomEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gone {
		return
	}
	select {
	case m.events <- ev:
	default:
		m.gone = true
		close(m.events)
		go m.leave()
	}
}

// close ends the membership from the relay's side. Safe to call more than once.
func (m *member) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.gone {
		m.gone = true
		close(m.events)
	}
}

func (m *member) Self() rooms.PeerID { return m.self }

func (m *member) Roster() []rooms.Peer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]rooms.Peer, 0, len(m.roster))
	for _, p := range m.roster {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b rooms.Peer) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out
}

func (m *member) Send(ctx context.Context, to rooms.PeerID, kind string, payload json.RawMessage) error {
	m.mu.Lock()
	gone := m.gone
	_, known := m.roster[to]
	m.mu.Unlock()
	if gone || !known {
		return ErrNotMember
	}
	err := m.send(ctx, &pb.CollabAction{
		ClientId: string(m.self),
		Action: &pb.CollabAction_AppMessage{AppMessage: &pb.AppMessage{
			Kind: kind, Payload: slices.Clone(payload), ToClientIds: []string{string(to)},
		}},
	})
	var nm *rooms.NotMembersError
	if errors.As(err, &nm) {
		return ErrNotMember
	}
	return err
}

// SendEach sends one AppMessage listing every target the roster knows, which
// the relay delivers to each of them once. Targets the roster doesn't know
// are never sent; they come back in the *NotMembersError, merged with any the
// relay itself reports missing (a peer that left in between).
func (m *member) SendEach(ctx context.Context, to []rooms.PeerID, kind string, payload json.RawMessage) error {
	m.mu.Lock()
	gone := m.gone
	var ids []string
	var missing []rooms.PeerID
	seen := make(map[rooms.PeerID]bool, len(to))
	for _, id := range to {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, known := m.roster[id]; known {
			ids = append(ids, string(id))
		} else {
			missing = append(missing, id)
		}
	}
	m.mu.Unlock()
	if gone {
		return ErrNotMember
	}
	if len(ids) > 0 {
		err := m.send(ctx, &pb.CollabAction{
			ClientId: string(m.self),
			Action: &pb.CollabAction_AppMessage{AppMessage: &pb.AppMessage{
				Kind: kind, Payload: slices.Clone(payload), ToClientIds: ids,
			}},
		})
		var nm *rooms.NotMembersError
		switch {
		case errors.As(err, &nm):
			missing = append(missing, nm.Peers...)
		case err != nil:
			return err
		}
	}
	if len(missing) > 0 {
		return &rooms.NotMembersError{Peers: missing}
	}
	return nil
}

func (m *member) Events() <-chan rooms.RoomEvent { return m.events }

func (m *member) Leave() error {
	m.close()
	m.leave()
	return nil
}
