// Package skills implements the Skills Extension binding for MCP (SEP-2640).
//
// Experimental: SEP-2640 is a Draft Extensions Track SEP. Its surface, URI
// grammar, index shape, and capability identifier may change while the
// Skills Over MCP Working Group iterates. This package tracks the current
// draft and breaking changes are expected on pre-1.0 tags. Pin to a
// specific version if you need stability.
//
// The extension defines a convention for serving Agent Skills over MCP
// using the existing Resources primitive. A skill is a directory containing
// a SKILL.md file at its root, addressed by the skill:// URI scheme. Files
// inside a skill are exposed as ordinary MCP resources; clients read them
// with resources/read and resolve relative references against the skill's
// root.
//
// This package provides the value types (Index, IndexEntry, Frontmatter,
// Metadata), the skill:// URI parser, and the SKILL.md frontmatter parser.
// Higher-level affordances (provider, index generator, archives, client
// helpers) live in sibling files in this package.
//
// See: https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2640
package skills

// ExperimentalNotice is a human-readable string examples and CLIs MAY emit
// to stderr on startup to signal that the package implements a Draft SEP.
const ExperimentalNotice = "ext/skills tracks Draft SEP-2640; the surface may change on pre-1.0 tags."

// ExtensionID is the SEP-2133 extension identifier declared by servers in
// their initialize response under capabilities.extensions.
const ExtensionID = "io.modelcontextprotocol/skills"

// Scheme is the URI scheme reserved for skill resources.
const Scheme = "skill"

// ManifestFilename is the required filename at the root of every skill.
const ManifestFilename = "SKILL.md"

// IndexPath is the well-known URI at which a server SHOULD expose its
// discovery index. The full URI is skill://index.json.
const IndexPath = "index.json"

// IndexURI is the full well-known URI for the discovery index.
const IndexURI = "skill://index.json"

// IndexSchemaURI is the JSON schema version URI the SEP currently pins to.
// Servers populate Index.Schema with this value; clients SHOULD compare
// against a known set before processing the rest of the document.
const IndexSchemaURI = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"

// MetaPrefix is the reverse-domain prefix recommended by the SEP for any
// SKILL.md frontmatter fields surfaced through a resource's _meta object.
const MetaPrefix = "io.modelcontextprotocol.skills/"

// ArchiveTarGz is the file suffix for gzip-compressed tar archive entries.
const ArchiveTarGz = ".tar.gz"

// ArchiveZip is the file suffix for zip archive entries.
const ArchiveZip = ".zip"
