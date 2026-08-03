package agents

import "github.com/panyam/mcpkit/core"

// AgentsExtension declares support for the experimental agents discovery
// primitive (io.modelcontextprotocol/agents).
//
// Register it on a server construction-time via server.WithExtension, or
// post-construction via srv.RegisterExtension, mirroring the ext/skills and
// ext/tasks patterns. Register (this package) declares it automatically as
// part of wiring the agents/list and agents/get handlers, so callers that use
// Register do not need to add it explicitly; RegisterExtension is idempotent
// (keyed by extension ID), so combining the two is safe.
//
// The extension carries no Config today — the roster itself is discovered via
// agents/list, not encoded in the capability. A future revision may add
// config flags (e.g. a maxAgents hint); until then the wire-level value is the
// bare empty object {}.
type AgentsExtension struct{}

// Extension implements core.ExtensionProvider. It returns the agents
// extension metadata at Experimental stability. Config stays nil (empty {} on
// the wire) — discovery happens through agents/list, not the capability body.
func (AgentsExtension) Extension() core.Extension {
	return core.Extension{
		ID:          ExtensionID,
		SpecVersion: SpecVersion,
		Stability:   core.Experimental,
	}
}
