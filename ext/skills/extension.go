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
type SkillsExtension struct {
	// DirectoryRead reports the server's support for the SEP-2640
	// resources/directory/read method (added by SEP commit 2e04c48d on
	// 2026-06-09). When true, the extension's wire-level Config carries
	// {"directoryRead": true}; when false the Config is omitted and clients
	// MUST NOT call the method per the SEP's normative wording.
	//
	// Provider.RegisterWith sets this to true automatically because a
	// Provider can always enumerate directories from its underlying fs.FS.
	// Direct callers of RegisterExtension keep the default (false) and must
	// opt in explicitly when they wire their own directory handler.
	DirectoryRead bool
}

// CapabilityDirectoryRead is the SEP-2640 setting key inside the
// io.modelcontextprotocol/skills extension capability that gates
// resources/directory/read. Exported so client code can decode the same
// key without re-stringing the literal.
const CapabilityDirectoryRead = "directoryRead"

// Extension implements core.ExtensionProvider. It returns the SEP-2640
// extension metadata. When DirectoryRead is set, the Config map carries
// the directoryRead capability flag; otherwise Config stays nil and the
// wire-level value is the empty JSON object {} (not [] — see SEP-2640 PR
// discussion).
func (e SkillsExtension) Extension() core.Extension {
	ext := core.Extension{
		ID:          ExtensionID,
		SpecVersion: SpecVersion,
		Stability:   core.Experimental,
	}
	if e.DirectoryRead {
		ext.Config = map[string]any{CapabilityDirectoryRead: true}
	}
	return ext
}

// SpecVersion is the SEP-2640 draft version this implementation tracks.
// Updated when the SEP advances; the date matches the SEP's Created field
// when the spec is still in Draft.
const SpecVersion = "2026-04-23"
