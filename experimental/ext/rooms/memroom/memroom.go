// Package memroom is an in-memory relay implementing rooms.RoomTransport.
//
// It does what the bridge needs from a relay and nothing more: membership,
// join and leave events, and addressed delivery. Subjects are whatever the
// caller hands Transport, standing in for the token a real relay validates.
// Use it in tests and when the bridge and its pages share a process.
package memroom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

// ErrNotMember is returned by Send when the target is not in the room.
var ErrNotMember = errors.New("memroom: peer is not in the room")

// eventBuffer bounds each member's undelivered events. A member that falls
// this far behind is disconnected rather than allowed to stall senders.
const eventBuffer = 1024

// Relay is a set of rooms. The zero value is not usable; call New.
type Relay struct {
	mu     sync.Mutex
	rooms  map[string]map[rooms.PeerID]*member
	nextID atomic.Uint64
	log    []Delivery
}

// Delivery records one message the relay carried, for tests that assert who
// received what.
type Delivery struct {
	Room    string
	From    rooms.PeerID
	To      rooms.PeerID
	Kind    string
	Payload json.RawMessage
}

// New returns an empty relay.
func New() *Relay {
	return &Relay{rooms: make(map[string]map[rooms.PeerID]*member)}
}

// Transport returns a RoomTransport that joins as subject. Each Join gets a
// fresh peer id.
func (r *Relay) Transport(subject string) rooms.RoomTransport {
	return transport{relay: r, subject: subject}
}

// Deliveries returns every message carried so far, in order.
func (r *Relay) Deliveries() []Delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.log)
}

type transport struct {
	relay   *Relay
	subject string
}

func (t transport) Join(_ context.Context, room string) (rooms.Room, error) {
	r := t.relay
	m := &member{
		relay:  r,
		room:   room,
		peer:   rooms.Peer{ID: rooms.PeerID(fmt.Sprintf("peer-%d", r.nextID.Add(1))), Subject: t.subject},
		events: make(chan rooms.RoomEvent, eventBuffer),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	members := r.rooms[room]
	if members == nil {
		members = make(map[rooms.PeerID]*member)
		r.rooms[room] = members
	}
	for _, other := range members {
		other.push(rooms.RoomEvent{Type: rooms.PeerJoined, Peer: m.peer})
	}
	members[m.peer.ID] = m
	return m, nil
}

type member struct {
	relay  *Relay
	room   string
	peer   rooms.Peer
	events chan rooms.RoomEvent
	gone   bool // guarded by relay.mu
}

// push must be called with relay.mu held.
func (m *member) push(ev rooms.RoomEvent) {
	if m.gone {
		return
	}
	select {
	case m.events <- ev:
	default:
		m.removeLocked()
	}
}

func (m *member) Self() rooms.PeerID { return m.peer.ID }

func (m *member) Roster() []rooms.Peer {
	m.relay.mu.Lock()
	defer m.relay.mu.Unlock()
	var out []rooms.Peer
	for id, other := range m.relay.rooms[m.room] {
		if id != m.peer.ID {
			out = append(out, other.peer)
		}
	}
	slices.SortFunc(out, func(a, b rooms.Peer) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out
}

func (m *member) Send(_ context.Context, to rooms.PeerID, kind string, payload json.RawMessage) error {
	r := m.relay
	r.mu.Lock()
	defer r.mu.Unlock()
	if m.gone {
		return ErrNotMember
	}
	target, ok := r.rooms[m.room][to]
	if !ok {
		return ErrNotMember
	}
	r.log = append(r.log, Delivery{Room: m.room, From: m.peer.ID, To: to, Kind: kind, Payload: slices.Clone(payload)})
	target.push(rooms.RoomEvent{Type: rooms.MessageReceived, Peer: m.peer, Kind: kind, Payload: slices.Clone(payload)})
	return nil
}

func (m *member) Events() <-chan rooms.RoomEvent { return m.events }

func (m *member) Leave() error {
	m.relay.mu.Lock()
	defer m.relay.mu.Unlock()
	m.removeLocked()
	return nil
}

func (m *member) removeLocked() {
	if m.gone {
		return
	}
	m.gone = true
	members := m.relay.rooms[m.room]
	delete(members, m.peer.ID)
	close(m.events)
	for _, other := range members {
		other.push(rooms.RoomEvent{Type: rooms.PeerLeft, Peer: m.peer})
	}
}
