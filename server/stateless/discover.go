package stateless

import (
	"encoding/json"

	core "github.com/panyam/mcpkit/core"
)

// DiscoverResult is the SEP-2575 server/discover response payload.
//
// Wire shape:
//
//	{
//	  "supportedVersions": ["2026-07-28"],
//	  "capabilities":      { ...ServerCapabilities... },
//	  "serverInfo":        { "name": "...", "version": "..." }
//	}
//
// Per the spec, clients MAY call server/discover up front to negotiate
// version + capability shape, OR call any other RPC inline and handle
// UnsupportedProtocolVersionError as a probe. Both paths are supported.
type DiscoverResult struct {
	SupportedVersions []string                `json:"supportedVersions"`
	Capabilities      core.ServerCapabilities `json:"capabilities"`
	ServerInfo        core.ServerInfo         `json:"serverInfo"`
}

// handleDiscover assembles the discover response from the backend. The
// dispatcher has already validated the _meta envelope; we just snapshot
// what the server is currently advertising and return it.
func (d *Dispatcher) handleDiscover(id json.RawMessage) *core.Response {
	return core.NewResponse(id, DiscoverResult{
		SupportedVersions: d.Backend.SupportedVersions(),
		Capabilities:      d.Backend.Capabilities(),
		ServerInfo:        d.Backend.ServerInfo(),
	})
}
