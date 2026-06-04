package skills

import "github.com/panyam/mcpkit/core"

// SkillsExtension declares support for the SEP-2640 Skills extension
// (io.modelcontextprotocol/skills).
//
// Register it on a server one of two ways. Construction-time:
//
//	srv := server.NewServer(info,
//	    server.WithExtension(skills.SkillsExtension{}),
//	)
//
// Or post-construction, mirroring the ext/tasks pattern:
//
//	srv.RegisterExtension(skills.SkillsExtension{})
//
// Provider.RegisterWith auto-declares the extension during its own
// registration pass, so callers that use a Provider do not need to call
// either form explicitly. RegisterExtension is idempotent (keyed by
// extension ID on the server's dispatcher), so combining auto-declaration
// with an explicit registration is safe.
type SkillsExtension struct{}

// Extension implements core.ExtensionProvider. It returns the SEP-2640
// extension metadata. SEP-2640 defines no extension-specific settings
// today, so the Config field is left nil and the wire-level value is
// the empty JSON object {} (not [] — see SEP-2640 PR discussion).
func (SkillsExtension) Extension() core.Extension {
	return core.Extension{
		ID:          ExtensionID,
		SpecVersion: SpecVersion,
		Stability:   core.Experimental,
	}
}

// SpecVersion is the SEP-2640 draft version this implementation tracks.
// Updated when the SEP advances; the date matches the SEP's Created field
// when the spec is still in Draft.
const SpecVersion = "2026-04-23"
