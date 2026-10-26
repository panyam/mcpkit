package stateless

import core "github.com/panyam/mcpkit/core"

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
// the server stamps it onto every stateless result, this one included.
//
// Per the spec, clients MAY call server/discover up front to negotiate
// version + capability shape, OR call any other RPC inline and handle
// UnsupportedProtocolVersionError as a probe. Both paths are supported.
type DiscoverResult struct {
	SupportedVersions []string                `json:"supportedVersions"`
	Capabilities      core.ServerCapabilities `json:"capabilities"`

	// Instructions is the server's guidance for the model, a top-level
	// field of DiscoverResult in the 2026-07-28 schema. Omitted when empty.
	Instructions string `json:"instructions,omitempty"`

	// TTLMs and CacheScope are required on DiscoverResult in the
	// 2026-07-28 final schema (the SEP-2549 hints extended to
	// server/discover). Always emitted; the defaults mirror the
	// conformant-by-default list-response posture from issue 496:
	// immediately stale, publicly cacheable.
	TTLMs      int    `json:"ttlMs"`
	CacheScope string `json:"cacheScope"`
}
