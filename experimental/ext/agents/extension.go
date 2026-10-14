package agents

import "github.com/panyam/mcpkit/core"

// AgentsExtension declares support for the experimental agents discovery
// primitive (io.mcpkit/agents).
//
// Register it on a server construction-time via server.WithExtension, or
// post-construction via srv.RegisterExtension, mirroring the ext/skills and
// ext/tasks patterns. Register (this package) declares it automatically as
// part of wiring the agents/list and agents/get handlers, so callers that use
// Register do not need to add it explicitly; RegisterExtension is idempotent
// (keyed by extension ID), so combining the two is safe.
//
// The extension carries no Config yet, so the wire value is the empty object
// {}. agents-wg#22 defines one setting, listChanged, which this package will
// advertise once it can deliver notifications/agents/list_changed over
// subscriptions/listen (blocked by panyam/mcpkit#1504).
type AgentsExtension struct{}

// Extension implements core.ExtensionProvider. It returns the agents
// extension metadata at Experimental stability. Config stays nil (empty {} on
// the wire) — discovery happens through agents/list, not the capability body.
func (AgentsExtension) Extension() core.Extension {
	return core.Extension{ID: ExtensionID}
}
