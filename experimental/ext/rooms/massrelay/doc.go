// Package massrelay implements rooms.RoomTransport over a massrelay relay
// (github.com/panyam/massrelay), so the room bridge and its pages can meet in
// a real relay rather than memroom.
//
// Two ways to reach the relay:
//
//   - Embedded joins rooms on a services.CollabService in the same process,
//     as the relay's own WebSocket stream does, with the identity the caller
//     vouches for. Use it when the bridge is the relay's host, e.g. a design
//     viewer serving both.
//   - Dial joins over the relay's WebSocket endpoint with a token, one
//     connection per room. Use it when the relay runs elsewhere.
//
// Either way a room is a massrelay session whose id is the resource name, a
// message is an AppMessage addressed to one peer (to_client_ids), and a peer's
// Subject is the `sub` the relay validated (PeerInfo.identity). This module
// has its own go.mod so neither mcpkit nor experimental/ext/rooms depends on
// massrelay.
package massrelay
