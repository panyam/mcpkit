package skills

import "fmt"

// SkillType is the discriminator for an entry in skill://index.json.
type SkillType string

const (
	// SkillTypeSkillMD points at an individual SKILL.md resource. Supporting
	// files are siblings under the same skill path.
	SkillTypeSkillMD SkillType = "skill-md"

	// SkillTypeArchive points at a packed skill directory served as a single
	// resource. The archive's URL suffix (.tar.gz or .zip) determines the
	// expected format.
	SkillTypeArchive SkillType = "archive"

	// SkillTypeResourceTemplate describes a parameterized skill namespace as
	// an RFC 6570 URI template. Hosts surface these as interactive discovery
	// points, not as concrete skills.
	SkillTypeResourceTemplate SkillType = "mcp-resource-template"
)

// Valid reports whether t is one of the SkillType values defined by SEP-2640.
func (t SkillType) Valid() bool {
	switch t {
	case SkillTypeSkillMD, SkillTypeArchive, SkillTypeResourceTemplate:
		return true
	}
	return false
}

// HasManifestFields reports whether entries of this type carry a Name and
// Digest. The mcp-resource-template type omits both because no concrete
// SKILL.md is materialized at index time.
func (t SkillType) HasManifestFields() bool {
	return t == SkillTypeSkillMD || t == SkillTypeArchive
}

// IndexEntry is a single skill entry in a server's skill://index.json.
//
// Per SEP-2640, Name and Digest are required for the skill-md and archive
// types and omitted for mcp-resource-template. The JSON encoding uses
// omitempty so unmarshalled documents round-trip cleanly when those fields
// are absent.
type IndexEntry struct {
	Type        SkillType `json:"type"`
	Name        string    `json:"name,omitempty"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	Digest      string    `json:"digest,omitempty"`
}

// Validate checks the per-type field requirements from SEP-2640's index
// table. It does not check digest format (use ValidateDigest separately)
// or that URL is well-formed (use ParseURI).
func (e IndexEntry) Validate() error {
	if !e.Type.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownSkillType, e.Type)
	}
	if e.Description == "" {
		return ErrIndexEntryMissingDescription
	}
	if e.URL == "" {
		return ErrIndexEntryMissingURL
	}
	if e.Type.HasManifestFields() {
		if e.Name == "" {
			return ErrIndexEntryMissingName
		}
		if e.Digest == "" {
			return ErrIndexEntryMissingDigest
		}
	}
	return nil
}

// Index is the document served at the well-known IndexURI.
type Index struct {
	Schema string       `json:"$schema"`
	Skills []IndexEntry `json:"skills"`
}

// NewIndex returns an Index pre-populated with the schema URI defined by
// IndexSchemaURI.
func NewIndex(entries ...IndexEntry) Index {
	return Index{Schema: IndexSchemaURI, Skills: entries}
}

// Lookup returns the IndexEntry whose URL exactly matches uri. The
// second return value reports whether a match was found.
//
// Client-side use: a host that receives a skill:// URI from server
// instructions, the user, or another skill can call Lookup against
// the index it fetched via ListSkills. A hit gives the host
// digest-verifiable metadata; a miss is the SEP-2640-sanctioned
// "skill exists but is not enumerated" case where the host falls back
// to a bare ReadSkillURI.
//
// Comparison is exact-string. A trailing slash or differing percent
// encoding on the input is the caller's bug, not Lookup's concern.
func (i Index) Lookup(uri string) (IndexEntry, bool) {
	for _, e := range i.Skills {
		if e.URL == uri {
			return e, true
		}
	}
	return IndexEntry{}, false
}

// Validate checks every entry and the top-level shape. It is intended for
// servers preparing an index for publication; clients receiving an index
// SHOULD skip entries with unrecognized types rather than reject the
// document, so they MAY validate individually.
func (i Index) Validate() error {
	if i.Schema == "" {
		return ErrIndexMissingSchema
	}
	for n, e := range i.Skills {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("skills[%d]: %w", n, err)
		}
	}
	return nil
}

// Frontmatter is the YAML block at the head of a SKILL.md file.
//
// SEP-2640 requires only Name and Description; the Agent Skills
// specification (delegated to by the SEP) may require additional fields,
// and individual servers MAY surface arbitrary fields via the resource's
// _meta object. Extra captures anything the parser sees beyond Name and
// Description so callers can inspect or republish without losing data.
type Frontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Extra       map[string]any `yaml:"-"`
}

// Get returns the value of a frontmatter field by key. Name and Description
// are looked up directly; everything else falls through to Extra.
func (f Frontmatter) Get(key string) (any, bool) {
	switch key {
	case "name":
		if f.Name == "" {
			return nil, false
		}
		return f.Name, true
	case "description":
		if f.Description == "" {
			return nil, false
		}
		return f.Description, true
	}
	if f.Extra == nil {
		return nil, false
	}
	v, ok := f.Extra[key]
	return v, ok
}

// Metadata is the host-side view of a skill, populated from its SKILL.md
// frontmatter plus the URI it was loaded from. It is what a SkillProvider
// surfaces to higher layers and what a client receives from helpers like
// ListSkills.
type Metadata struct {
	Name        string
	Description string
	Extra       map[string]any
	SourceURI   string
}

// MetadataFromFrontmatter constructs a Metadata from a parsed Frontmatter
// and the URI of the SKILL.md it was loaded from.
func MetadataFromFrontmatter(fm Frontmatter, sourceURI string) Metadata {
	return Metadata{
		Name:        fm.Name,
		Description: fm.Description,
		Extra:       fm.Extra,
		SourceURI:   sourceURI,
	}
}

