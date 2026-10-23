// Package rooms lets agents call tools that web pages offer.
//
// MCP only lets a client call a server, so a page the user opened somewhere
// else cannot be called by an agent directly. The bridge stands between them.
// It joins a relay room per resource as a peer, acts as an MCP client to every
// page in the room, and serves those pages' tools to the agents that attach to
// the room:
//
//	agent (MCP client) --MCP--> Bridge (MCP server) --room--> Page (tools)
//
// A page is a small MCP server whose transport is the room: it answers
// tools/list and tools/call as JSON-RPC inside messages of kind MessageKind.
// Page is the Go implementation; browser pages use the TypeScript client.
//
// Every call goes to exactly one page: the page the agent names, else the page
// paired with the session, else the only page that agreed to it, else an error
// carrying the roster. There is no broadcast.
//
// A page's user must agree before an agent can drive it: the agents_allowed
// switch for the user's own agents, or a pairing code for anyone else's.
// Subjects on both sides come from validated tokens, the relay's for pages and
// the MCP server's auth for agents.
//
// RoomTransport is the relay. memroom implements it in memory; a massrelay
// adapter is planned as its own module, so this one never depends on
// massrelay.
package rooms
