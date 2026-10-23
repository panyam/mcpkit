package rooms

import (
	"context"
	"encoding/json"
)

// MessageKind is the AppMessage kind that carries MCP JSON-RPC between the
// bridge and a page. Messages of any other kind are not the bridge's and are
// ignored, so an app can share the room with its own traffic.
const MessageKind = "mcp/1"

// PeerID identifies one connection in a room. The relay assigns it, and it is
// unique across the relay, so a page id from one room never names a page in
// another.
type PeerID string

// Peer is a member of a room as the relay reports it.
//
// Subject is the `sub` of the token the peer joined with, as the relay
// validated it. The bridge compares subjects from here and from the agent's
// validated claims, and never from anything a peer says about itself.
type Peer struct {
	ID      PeerID
	Subject string
}

// RoomEventType says what a RoomEvent reports.
type RoomEventType int

// The three things a room reports. A peer's own join and leave are not
// reported to it.
const (
	PeerJoined RoomEventType = iota + 1
	PeerLeft
	MessageReceived
)

// RoomEvent is one membership change or one message addressed to this peer.
// For MessageReceived, Peer is the sender and Kind and Payload are the
// message; for the other two only Peer is set.
type RoomEvent struct {
	Type    RoomEventType
	Peer    Peer
	Kind    string
	Payload json.RawMessage
}

// RoomTransport joins rooms on a relay as one fixed identity. The bridge holds
// one, joined as itself, and each page holds its own.
//
// mcpkit ships an in-memory implementation (memroom) for tests and
// single-process use. A massrelay adapter is planned as its own module, so
// this module, and mcpkit's root, never depend on massrelay.
type RoomTransport interface {
	// Join enters the named room. The room name is the resource the app
	// chose, e.g. "design:demo/board.kicad_sch". ctx bounds the join itself,
	// not the membership, which lasts until Leave.
	Join(ctx context.Context, room string) (Room, error)
}

// Room is one membership. Events delivers until Leave is called or the relay
// drops the connection, and is then closed.
type Room interface {
	// Self is this connection's peer id.
	Self() PeerID

	// Roster lists the other peers in the room, sorted by id.
	Roster() []Peer

	// Send delivers one message to one peer and no one else. There is no
	// broadcast, by design; see the package doc.
	Send(ctx context.Context, to PeerID, kind string, payload json.RawMessage) error

	// Events reports membership changes and messages addressed to this peer.
	Events() <-chan RoomEvent

	// Leave ends the membership. Safe to call more than once.
	Leave() error
}
