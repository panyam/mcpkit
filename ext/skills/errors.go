package skills

import "errors"

// URI parsing errors.
var (
	// ErrInvalidScheme is returned when a URI's scheme is not "skill".
	ErrInvalidScheme = errors.New("skills: scheme must be skill://")

	// ErrEmptySkillPath is returned when a skill:// URI has no path
	// segments at all (e.g., "skill://").
	ErrEmptySkillPath = errors.New("skills: empty skill path")

	// ErrEmptySkillName is returned when the final segment of the skill
	// path is empty (e.g., "skill://foo//SKILL.md").
	ErrEmptySkillName = errors.New("skills: empty skill name")

	// ErrInvalidSkillName is returned when the final segment of the skill
	// path violates the Agent Skills naming rules (lowercase letters,
	// digits, hyphens).
	ErrInvalidSkillName = errors.New("skills: invalid skill name")

	// ErrManifestNotInRoot is returned when a SKILL.md appears anywhere
	// other than the immediate root of a skill. SEP-2640 forbids nested
	// skills.
	ErrManifestNotInRoot = errors.New("skills: SKILL.md must be at skill root")

	// ErrEmptyPathSegment is returned when a URI contains an empty path
	// segment (consecutive slashes).
	ErrEmptyPathSegment = errors.New("skills: empty path segment")

	// ErrRelativeEscapesSkill is returned by ResolveRelative when the
	// resolved file path would escape the skill's root using "..".
	ErrRelativeEscapesSkill = errors.New("skills: relative reference escapes skill root")

	// ErrPathTraversal is returned by ParseURI when a URI contains a "."
	// or ".." segment. SEP-2640 skill paths use [a-z0-9-] segments only,
	// so dot-segments cannot appear legitimately — their presence
	// indicates either a malformed URI or an attempted traversal probe.
	// The strict rejection at parse time keeps the registry-miss path
	// (HTTP 200 + "unknown resource") from masking traversal-shaped
	// requests as ordinary typos in audit logs.
	ErrPathTraversal = errors.New("skills: URI contains traversal segment (. or ..)")

	// ErrNotManifestURI is returned when an operation requires a manifest
	// URI (ending in /SKILL.md) but received a different shape.
	ErrNotManifestURI = errors.New("skills: not a SKILL.md URI")
)

// Client-side errors.
var (
	// ErrDigestMismatch is returned by Client.ReadAndVerify when the
	// SHA-256 over the served bytes does not equal the expected digest
	// the caller supplied. Per SEP-2640's Integrity and Verification
	// section, hosts MUST NOT use unverified content; surfacing this as
	// a typed error makes the contract explicit at the call site.
	ErrDigestMismatch = errors.New("skills: digest mismatch — content MUST NOT be used")

	// ErrDirectoryReadNotSupported is returned by Client.ReadDirectory
	// when the connected server has not advertised the
	// io.modelcontextprotocol/skills.directoryRead capability. SEP-2640
	// commit 2e04c48d's normative wording: clients MUST NOT call
	// resources/directory/read against a server that has not declared
	// directoryRead: true. Returning a typed error from the pre-call
	// guard keeps the contract explicit at the call site.
	ErrDirectoryReadNotSupported = errors.New("skills: server does not advertise the directoryRead capability")
)

// Index validation errors.
var (
	ErrIndexMissingSchema           = errors.New("skills: index missing $schema")
	ErrUnknownSkillType             = errors.New("skills: unknown skill type")
	ErrIndexEntryMissingDescription = errors.New("skills: index entry missing description")
	ErrIndexEntryMissingURL         = errors.New("skills: index entry missing url")
	ErrIndexEntryMissingName        = errors.New("skills: index entry missing name")
	ErrIndexEntryMissingDigest      = errors.New("skills: index entry missing digest")
)

// Provider configuration and walk errors.
var (
	// ErrProviderMissingFS is returned by NewProvider when no fs.FS was
	// supplied via WithFS or WithDirectory.
	ErrProviderMissingFS = errors.New("skills: provider needs WithFS or WithDirectory")

	// ErrSkillNameMismatch is returned by NewProvider when a skill's
	// SKILL.md frontmatter name does not equal the parent directory base
	// name. SEP-2640 requires the two to match.
	ErrSkillNameMismatch = errors.New("skills: frontmatter name does not match directory")

	// ErrNestedSkill is returned by NewProvider when a SKILL.md is found
	// inside an existing skill's subtree. SEP-2640 forbids skill nesting.
	ErrNestedSkill = errors.New("skills: nested skill")
)

// Frontmatter parsing errors.
var (
	// ErrMissingFrontmatter is returned when a SKILL.md does not begin with
	// a "---" YAML delimiter.
	ErrMissingFrontmatter = errors.New("skills: missing YAML frontmatter")

	// ErrUnterminatedFrontmatter is returned when a SKILL.md opens with
	// "---" but never closes it.
	ErrUnterminatedFrontmatter = errors.New("skills: unterminated YAML frontmatter")

	// ErrNonMappingFrontmatter is returned when the frontmatter parses as
	// YAML but the top-level value is not a mapping (e.g., a list or
	// scalar).
	ErrNonMappingFrontmatter = errors.New("skills: frontmatter must be a YAML mapping")

	// ErrFrontmatterMissingName is returned when the frontmatter parses but
	// has no non-empty name field.
	ErrFrontmatterMissingName = errors.New("skills: frontmatter missing required field: name")

	// ErrFrontmatterMissingDescription is returned when the frontmatter
	// parses but has no non-empty description field.
	ErrFrontmatterMissingDescription = errors.New("skills: frontmatter missing required field: description")
)
