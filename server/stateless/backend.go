package stateless

import (
	"context"

	core "github.com/panyam/mcpkit/core"
)

// Backend is the server-metadata surface server/discover needs, plus the
// one call that runs every other method. The parent server package provides
// an adapter wrapping its Server; tests provide fakes.
//
// Defining the surface here (not in server) keeps the import direction
// one-way: server depends on stateless, never the reverse. When legacy
// goes away, this Backend interface stays — the future "stateless-only"
// server will still satisfy it.
type Backend interface {
	DiscoverSource

	// InvokeWithMiddleware runs the server's middleware chain around the
	// handler for req and returns the JSON-RPC response. The dispatcher calls
	// it once for every method that gets past the envelope checks, so no
	// method on this wire can skip the chain (#1625). Unknown methods come
	// back as -32601 from the backend's own method table.
	//
	// A non-nil error is a middleware short-circuit (typically
	// *core.AuthError) that the transport surfaces through writeAuthError,
	// so both wires send the same 403 + WWW-Authenticate (issue 815). When
	// err is nil the response is used verbatim.
	InvokeWithMiddleware(ctx context.Context, req *core.Request) (*core.Response, error)
}

// DiscoverSource is what server/discover reports: the server's identity,
// guidance, capabilities and versions. The server's method table builds the
// discover result from one, without needing a whole Backend.
type DiscoverSource interface {
	// ServerInfo returns the server's identity, stamped into every success
	// result's _meta[io.modelcontextprotocol/serverInfo].
	ServerInfo() core.Implementation

	// Instructions returns the server's natural-language guidance for the
	// model, sent as server/discover's top-level instructions field and
	// never inside serverInfo. Empty omits the field.
	Instructions() string

	// Capabilities returns the capability shape advertised by this server
	// in server/discover. The dispatcher does not mutate it; the backend
	// builds it from registered tools/resources/prompts/extensions.
	Capabilities() core.ServerCapabilities

	// SupportedVersions returns the protocol versions this server speaks
	// on the stateless wire. Always includes core.DraftProtocolVersion2026V1.
	SupportedVersions() []string
}
