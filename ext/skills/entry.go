package skills

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ResourcesDynamic is the sentinel a server publishes in place of a resource
// array when a skill's files are generated at read time and cannot be pinned
// ahead of a fetch.
//
// SEP-2640 makes the distinction explicit rather than letting an absent array
// mean "unknown": an entry carrying neither an array nor this sentinel is
// invalid, and hosts MUST NOT load it. Treating absence as permission would
// let a server opt out of integrity by omission.
const ResourcesDynamic = "dynamic"

// MaxResourcesPerSkill is the per-skill resource-entry ceiling SEP-2640
// advises servers not to exceed, counted over SkillEntry.Resources with
// SKILL.md included.
//
// SHOULD NOT, not MUST NOT: exceeding it is a warning at registration rather
// than a rejection, because a server that genuinely serves a larger skill is
// non-conformant in a way only its operator can weigh.
const MaxResourcesPerSkill = 512

// MaxSkillTotalBytes is the per-skill total-size ceiling SEP-2640 advises
// servers not to exceed, summed over SkillResource.Size. Same SHOULD NOT
// treatment as MaxResourcesPerSkill.
const MaxSkillTotalBytes = 16 * 1024 * 1024

// ErrInvalidResources is returned when a decoded entry's resources field is
// neither an array nor the "dynamic" sentinel. SEP-2640 makes such an entry
// invalid outright, and a host MUST NOT load the skill it describes.
var ErrInvalidResources = errors.New("skills: resources must be an array or \"dynamic\"")

// SkillResource pins one file of a skill to the bytes a host should receive.
//
// Digest and Size cover the same bytes. Size is not redundant with Digest: a
// host can reject a mismatched length before hashing, and SEP-2640 makes a
// length mismatch a verification failure in its own right, whether or not the
// digest is computed.
type SkillResource struct {
	// URI is the file's full skill:// URI. It is the skill's own SKILL.md or
	// a file inside the skill's directory, never outside it.
	URI string `json:"uri"`

	// Digest is the SHA-256 of the file's raw bytes as "sha256:{hex}", with
	// 64 lowercase hex characters.
	Digest string `json:"digest"`

	// Size is the length in bytes of the raw content the digest covers.
	Size int64 `json:"size"`
}

// SkillResources is a skill's resource manifest: either a complete list of
// files or the ResourcesDynamic sentinel.
//
// The two forms are one JSON field with two shapes, so this type carries its
// own codec. A zero value marshals as the empty array rather than null,
// because null is the one thing SEP-2640 treats as invalid.
type SkillResources struct {
	// Files is the complete file list. When Resources is complete it lists
	// every file of the skill exactly once, including an entry whose URI
	// equals the skill's own uri.
	Files []SkillResource

	// Dynamic reports the sentinel form. When true, Files is ignored.
	Dynamic bool
}

// DynamicResources returns the sentinel form, for servers that generate skill
// content at read time.
func DynamicResources() SkillResources { return SkillResources{Dynamic: true} }

// MarshalJSON emits either the sentinel string or the file array. A nil Files
// slice becomes [] rather than null, since SEP-2640 gives null no meaning and
// an absent array invalidates the entry.
func (r SkillResources) MarshalJSON() ([]byte, error) {
	if r.Dynamic {
		return json.Marshal(ResourcesDynamic)
	}
	if r.Files == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(r.Files)
}

// UnmarshalJSON accepts the array form or the exact string "dynamic".
// Anything else, including null and any other string, is ErrInvalidResources:
// the SEP makes such an entry invalid rather than merely unpinned.
func (r *SkillResources) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s != ResourcesDynamic {
			return fmt.Errorf("%w: got %q", ErrInvalidResources, s)
		}
		*r = SkillResources{Dynamic: true}
		return nil
	}
	var files []SkillResource
	if err := json.Unmarshal(b, &files); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidResources, err)
	}
	if files == nil {
		return ErrInvalidResources
	}
	*r = SkillResources{Files: files}
	return nil
}

// TotalBytes sums Size across Files. Returns 0 for the dynamic form, whose
// size is unknowable before a fetch.
func (r SkillResources) TotalBytes() int64 {
	if r.Dynamic {
		return 0
	}
	var n int64
	for _, f := range r.Files {
		n += f.Size
	}
	return n
}

// SkillEntry is one skill as it appears in a skills/list or skills/get
// result. All three fields are required.
//
// This replaces the pre-2026-08-21 index entry, which carried a flattened
// name and description plus a type discriminator. Frontmatter is now the
// verbatim block rather than two hoisted fields, because SEP-2640 requires a
// host to compare the entry's frontmatter field-by-field against the SKILL.md
// it later fetches. Flattening would make that comparison impossible for
// every field the struct did not name.
type SkillEntry struct {
	// URI is the full resource URI of the skill's SKILL.md. Its final
	// skill-path segment equals Frontmatter["name"].
	URI string `json:"uri"`

	// Frontmatter is the SKILL.md YAML frontmatter rendered verbatim as
	// JSON. The Agent Skills specification requires name and description, so
	// both are always present; any other key the author wrote is carried
	// through unchanged.
	Frontmatter map[string]any `json:"frontmatter"`

	// Resources is the complete file manifest or the dynamic sentinel.
	Resources SkillResources `json:"resources"`
}

// Name returns the skill's frontmatter name, or "" when absent. Convenience
// for the common lookup now that the field is no longer hoisted onto the
// struct.
func (e SkillEntry) Name() string {
	s, _ := e.Frontmatter["name"].(string)
	return s
}

// Description returns the skill's frontmatter description, or "" when absent.
func (e SkillEntry) Description() string {
	s, _ := e.Frontmatter["description"].(string)
	return s
}

// SkillsListRequest is the skills/list request payload.
type SkillsListRequest struct {
	// Cursor is an opaque pagination cursor from a prior result's
	// nextCursor. Empty requests the first page.
	Cursor string `json:"cursor,omitempty"`
}

// SkillsListResult is the skills/list response.
//
// A server MAY return an empty or partial listing, so an empty Skills is a
// conformant answer and never proof that the server has no skills. Hosts must
// fall back to skills/get for a URI they learned elsewhere.
type SkillsListResult struct {
	Skills []SkillEntry `json:"skills"`

	// NextCursor is present when more entries remain. Entries are atomic: a
	// skill's resource set is never split across pages.
	NextCursor string `json:"nextCursor,omitempty"`

	// TTLMs and CacheScope are the SEP-2549 list-caching attributes, carried
	// on protocol 2026-07-28 and later. They sit at the result's top level
	// rather than under _meta, matching the base-protocol list methods.
	TTLMs      *int   `json:"ttlMs,omitempty"`
	CacheScope string `json:"cacheScope,omitempty"`
}

// SkillsGetRequest is the skills/get request payload.
type SkillsGetRequest struct {
	// URI names the skill by its SKILL.md URI.
	URI string `json:"uri"`
}

// SkillsGetResult is the skills/get response. The entry is identical in shape
// and meaning to one from skills/list, and the result carries no cursor
// because a single entry is not a list.
//
// A server MAY serve a skill here that its listing omits, which is how a
// server with an unenumerable or partial catalog stays useful.
type SkillsGetResult struct {
	Skill SkillEntry `json:"skill"`
}
