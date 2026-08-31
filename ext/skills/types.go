package skills

import (
	"time"

	"github.com/panyam/mcpkit/core"
)

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
// surfaces to higher layers.
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
