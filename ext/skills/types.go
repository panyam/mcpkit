package skills

import (
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
)

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
)

// Valid reports whether t is one of the SkillType values defined by SEP-2640.
// The previously valid "mcp-resource-template" type was dropped from the SEP
// on 2026-06-04; entries that carry it are now invalid.
func (t SkillType) Valid() bool {
	switch t {
	case SkillTypeSkillMD, SkillTypeArchive:
		return true
	}
	return false
}

// HasManifestFields reports whether entries of this type carry a Name and
// Digest. After the 2026-06-04 SEP HEAD removal of mcp-resource-template,
// both surviving types require both fields; the helper is retained for
// callers that still want to dispatch on the type symbolically.
func (t SkillType) HasManifestFields() bool {
	return t == SkillTypeSkillMD || t == SkillTypeArchive
}

// IndexEntry is a single skill entry in a server's skill://index.json.
//
// Per SEP-2640, Name and Digest are required for both the skill-md and
// archive types. The JSON encoding keeps `omitempty` on both fields so
// future spec revisions that re-introduce a manifest-less entry type can
// be parsed without a struct-shape change.
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

// MetaKeyVersion is the reverse-domain key under skill://index.json's
// _meta map that carries Provider.Version() at index build time.
// Stateless polling clients read this field to detect changes without
// a persistent push channel (issue #795).
const MetaKeyVersion = MetaPrefix + "version"

// MetaKeyPathsChanged is the reverse-domain key under the
// notifications/resources/list_changed params._meta map that carries
// a PathsChangedPayload. Subscribers that decode this payload get the
// deduplicated set of paths that changed plus the version counter at
// broadcast time; subscribers that ignore _meta still receive the
// standard list_changed signal and can re-read at their leisure.
const MetaKeyPathsChanged = MetaPrefix + "paths-changed"

// ChangeAction discriminates how a path entered the pending set in
// PathsChangedPayload. The zero value (empty string) is treated as
// ChangeActionModified on decode, so omitempty on the wire produces
// the most compact representation for the common case.
type ChangeAction string

const (
	// ChangeActionModified signals the path's content changed in place.
	// Subscribers should re-fetch and re-verify the digest.
	ChangeActionModified ChangeAction = "modified"

	// ChangeActionCreated signals the path is newly served. Subscribers
	// add the URI to their local cache; re-fetch on first access.
	ChangeActionCreated ChangeAction = "created"

	// ChangeActionDeleted signals the path is no longer served.
	// Subscribers prune the URI from their local cache without
	// re-fetching (a fetch would return a not-found error).
	ChangeActionDeleted ChangeAction = "deleted"
)

// PathChange is the typed event a Detector passes to
// Provider.NotifyChangedEvents. Detectors that have richer signals
// than "this path is dirty" (fsnotify producing CREATE / WRITE /
// REMOVE; webhooks carrying mtime + content hash) populate the action
// + timestamp + digest fields; the Applier preserves them through
// coalesce + dedup into the broadcast payload. Detectors that only
// know "something changed" can use the simpler
// Provider.NotifyChanged(paths ...string) sugar; all entries default
// to ChangeActionModified with the call-time timestamp.
type PathChange struct {
	// Path is the fs.FS-relative path that changed. Required.
	Path string

	// Action is one of Created / Modified / Deleted. Empty value is
	// treated as Modified.
	Action ChangeAction

	// Timestamp is when the Detector observed the change. Empty value
	// is replaced with the Applier's call-time timestamp.
	Timestamp time.Time

	// Digest is the optional SHA-256 ("sha256:" + 64-hex) of the
	// post-change content. Detectors that already have the digest
	// supply it; the Applier does not compute it. Subscribers compare
	// against their cached digest to avoid unnecessary re-fetches
	// when the content matches what they already have.
	Digest string
}

// PathChangeEntry is the per-path value in PathsChangedPayload.Paths.
// Wire-format counterpart of PathChange minus the Path itself (which
// is the map key).
type PathChangeEntry struct {
	Action    ChangeAction `json:"action,omitempty"`
	Timestamp time.Time    `json:"timestamp"`
	Digest    string       `json:"digest,omitempty"`
}

// PathsChangedPayload is the structured _meta hint mcpkit attaches to
// notifications/resources/list_changed when ext/skills's Applier has
// path-level information about what changed (issue #795). Decode with
// DecodeListChangedNotification.
//
// Paths is a map of fs.FS-relative path → PathChangeEntry, covering
// every path reported via Provider.NotifyChangedEvents (or
// NotifyChanged) in the coalesce window leading up to this broadcast.
// Repeated reports for the same path collapse to one entry under
// latest-wins semantics: the most recent action / timestamp / digest
// supersedes earlier ones. Empty when only opaque "something changed"
// signals were available (e.g., Refresh() called with no arguments) —
// subscribers should treat an empty Paths map as "re-read everything"
// and rely on Version alone.
//
// Version is Provider.Version() at the moment this broadcast was
// constructed — the same counter the index will carry when a
// subscriber re-reads it within the same instant. Two contractual
// uses:
//
//   - Idempotency: a duplicate broadcast carrying the same Version
//     can be silently dropped.
//   - ETag-like staleness check: if lastKnownVersion >= payload.Version
//     the subscriber is already current and may skip the re-read.
//
// Race note: between broadcast and re-read, another bump may land. The
// subscriber's re-read may legitimately return Version >
// payload.Version. Treat the re-read as the new ground truth; do not
// assume equality with the broadcast's Version.
type PathsChangedPayload struct {
	Paths   map[string]PathChangeEntry `json:"paths,omitempty"`
	Version uint64                     `json:"version"`
}

// Index is the document served at the well-known IndexURI.
//
// Meta carries opt-in extension metadata under the `_meta` key per the
// MCP convention. Keys are reverse-domain-namespaced
// (io.modelcontextprotocol.skills/...) so they will not collide with
// any field the SEP may add in the future. mcpkit populates
// "io.modelcontextprotocol.skills/version" with Provider.Version() at
// index build time; stateless clients poll the index and observe this
// field bumping when content changes (issue #795).
type Index struct {
	Schema string         `json:"$schema"`
	Skills []IndexEntry   `json:"skills"`
	Meta   map[string]any `json:"_meta,omitempty"`
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

// DirectoryReadRequest is the typed params shape for the SEP-2640
// resources/directory/read method.
//
// Cursor mirrors the resources/list pagination contract: empty on the
// first request, then the NextCursor returned by the prior response.
type DirectoryReadRequest struct {
	URI    string `json:"uri"`
	Cursor string `json:"cursor,omitempty"`
}

// DirectoryReadResult is the typed result shape for the SEP-2640
// resources/directory/read method.
//
// Resources are the directory's direct children — files carry their
// ordinary resource metadata; subdirectories carry MimeTypeDirectory and
// a URI without trailing slash. The listing is not recursive: clients
// descend by calling the method again on a child directory.
//
// NextCursor follows the resources/list contract: present and non-empty
// when more entries remain, omitted when the listing is complete.
type DirectoryReadResult struct {
	Resources  []core.ResourceDef `json:"resources"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

