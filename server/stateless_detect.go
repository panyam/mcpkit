package server

import (
	"encoding/json"
	"net/http"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// wireKind classifies an incoming request to a Streamable HTTP endpoint
// into which dispatcher path it should hit.
type wireKind int

const (
	wireUnknown   wireKind = iota
	wireLegacy             // legacy session wire (initialize, Mcp-Session-Id)
	wireStateless          // SEP-2575 stateless wire (_meta envelope, server/discover)
)

func (k wireKind) String() string {
	switch k {
	case wireLegacy:
		return "legacy"
	case wireStateless:
		return "stateless"
	default:
		return "unknown"
	}
}

// detectWireKind classifies a parsed request under a given mode and
// returns the dispatch path. Pure: doesn't mutate the request or the
// response. The transport routes based on the returned kind; for the
// stateless path it additionally validates the MCP-Protocol-Version
// header / _meta cross-check via headerMatchesMetaVersion.
//
// Precedence (high → low; cheapest signal first):
//
//	1. Method == initialize / notifications/initialized → legacy
//	   (these methods don't exist on stateless)
//	2. Method == server/discover → stateless (doesn't exist on legacy)
//	3. MCP-Protocol-Version HTTP header present with value in
//	   core.SupportedStatelessVersions → stateless
//	4. params._meta protocolVersion present → stateless
//	5. Mcp-Session-Id HTTP header present → legacy (mid-session call)
//	6. Otherwise → legacy under Dual mode (backward compat)
//
// Pure-Stateless and Pure-Legacy modes short-circuit: an incoming
// shape that doesn't match the configured mode still routes to that
// mode's dispatcher, which then returns -32601 for whatever method
// arrived. Detection itself does not refuse traffic — refusal happens
// at the dispatcher with the correct error code/HTTP status.
func detectWireKind(r *http.Request, body []byte, req *core.Request, mode stateless.Mode) wireKind {
	switch mode {
	case stateless.ModeLegacyOnly:
		return wireLegacy
	case stateless.ModeStateless:
		return wireStateless
	}
	// stateless.ModeDual — full detection.

	// Signal 1: distinctly-legacy methods.
	switch req.Method {
	case "initialize", "notifications/initialized", "initialized":
		return wireLegacy
	}

	// Signal 2: distinctly-stateless method.
	if req.Method == "server/discover" {
		return wireStateless
	}

	// Signal 3: MCP-Protocol-Version HTTP header naming a stateless version.
	if hdr := r.Header.Get(mcpProtocolVersionHeader); hdr != "" {
		for _, sv := range core.SupportedStatelessVersions {
			if hdr == sv {
				return wireStateless
			}
		}
	}

	// Signal 4: params._meta carries a protocolVersion (stateless envelope).
	if hasStatelessMetaProtocolVersion(req.Params) {
		return wireStateless
	}

	// Signal 5: legacy session id present.
	if r.Header.Get(mcpSessionIDHeader) != "" {
		return wireLegacy
	}

	// Signal 6: Dual default — fall back to legacy for backward compat.
	// A stateless client that forgot all of MCP-Protocol-Version,
	// _meta, and server/discover will surface as a legacy-shaped
	// missing-session error, which is the right "wake up" signal.
	return wireLegacy
}

// hasStatelessMetaProtocolVersion peeks the params blob for the
// SEP-2575 _meta envelope's protocolVersion field. Returns true iff
// the field is present (any non-empty string). Tolerant: malformed
// JSON / missing keys return false rather than erroring.
func hasStatelessMetaProtocolVersion(params json.RawMessage) bool {
	if len(params) == 0 {
		return false
	}
	var probe struct {
		Meta struct {
			ProtocolVersion string `json:"io.modelcontextprotocol/protocolVersion"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &probe); err != nil {
		return false
	}
	return probe.Meta.ProtocolVersion != ""
}

// headerMismatchResponse returns a -32001 HeaderMismatch JSON-RPC
// response when the MCP-Protocol-Version HTTP header and the _meta
// protocolVersion field disagree. Both observed values surface on
// the structured data payload.
//
// Returns nil when the values agree OR when either is absent
// (absent header is handled upstream of detection; absent _meta
// is the dispatcher's -32602 path, not -32001).
func headerMismatchResponse(id json.RawMessage, headerVer, metaVer string) *core.Response {
	if headerVer == "" || metaVer == "" || headerVer == metaVer {
		return nil
	}
	return core.NewErrorResponseWithData(
		id,
		core.ErrCodeHeaderMismatch,
		"MCP-Protocol-Version header does not match _meta protocolVersion",
		core.HeaderMismatchData{
			HeaderProtocolVersion: headerVer,
			MetaProtocolVersion:   metaVer,
		},
	)
}
