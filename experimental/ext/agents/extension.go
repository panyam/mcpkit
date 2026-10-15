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
// ListChanged reports whether the server sends
// notifications/agents/list_changed, the one setting agents-wg#22 defines.
// Register sets it true, because AddAgent and RemoveAgent always broadcast.
// False yields the empty settings object {}.
type AgentsExtension struct {
	ListChanged bool
}

// Extension implements core.ExtensionProvider. It returns the agents
// extension metadata at Experimental stability. Discovery happens through
// agents/list, so the settings carry only listChanged.
func (e AgentsExtension) Extension() core.Extension {
	ext := core.Extension{ID: ExtensionID}
	if e.ListChanged {
		ext.Settings = map[string]any{"listChanged": true}
	}
	return ext
}
