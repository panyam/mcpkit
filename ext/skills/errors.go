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

	// ErrNotManifestURI is returned when an operation requires a manifest
	// URI (ending in /SKILL.md) but received a different shape.
	ErrNotManifestURI = errors.New("skills: not a SKILL.md URI")
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
