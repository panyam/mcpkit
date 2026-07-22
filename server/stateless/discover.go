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
//	  "_meta": { "io.modelcontextprotocol/serverInfo": { "name": "...", "version": "..." } }
//	}
//
// Server identity moved from the result body to _meta in spec PR 3002;
// the _meta stamp is applied centrally by Dispatch, like every other
// stateless result.
//
// Per the spec, clients MAY call server/discover up front to negotiate
// version + capability shape, OR call any other RPC inline and handle
// UnsupportedProtocolVersionError as a probe. Both paths are supported.
type DiscoverResult struct {
	SupportedVersions []string                `json:"supportedVersions"`
	Capabilities      core.ServerCapabilities `json:"capabilities"`
}

// handleDiscover assembles the discover response from the backend. The
// dispatcher has already validated the _meta envelope; we just snapshot
// what the server is currently advertising and return it.
func (d *Dispatcher) handleDiscover(id json.RawMessage) *core.Response {
	return core.NewResponse(id, DiscoverResult{
		SupportedVersions: d.Backend.SupportedVersions(),
		Capabilities:      d.Backend.Capabilities(),
	})
}
