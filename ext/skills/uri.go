package skills

import (
	"fmt"
	"net/url"
	"strings"
)

// URIParts is a parsed skill:// URI.
//
// SEP-2640 splits a skill URI into a skill path (locating the skill
// directory within the server's namespace) and a file path (the file inside
// the skill). For manifest URIs ending in /SKILL.md the boundary is fixed
// by the SKILL.md suffix, and ParseURI sets SkillPath, FilePath, SkillName,
// and IsManifest from it.
//
// For non-manifest URIs the boundary cannot be recovered from the URI
// alone. SEP-2640's claim that "the skill name is always recoverable from
// the URI alone, without reading frontmatter" is grounded in two
// manifest-URI examples where SKILL.md acts as the boundary. The spec also
// allows prefix segments to be any RFC 3986 path segment with no further
// constraint, so a prefix segment and an intermediate file-path segment
// share the same character class. In
// skill://acme/billing/refunds/templates/email.md the segments refunds,
// templates, billing, and acme all satisfy the Agent Skills name rules,
// and a URI-only scan cannot pick refunds over templates without external
// knowledge from the discovery index or a prior manifest read. SEP-2640's
// host workflow always supplies that knowledge, so the spec's claim holds
// operationally even though the URI string in isolation is ambiguous.
//
// ParseURI therefore returns parsed segments in AllSegments and leaves
// SkillPath, FilePath, SkillName, and IsManifest unset for non-manifest
// URIs. Callers establish the boundary by calling SplitAt(n) when the
// skill path length is known from an index entry or a prior manifest read,
// or by calling ResolveRelative from a known skill root.
type URIParts struct {
	// Scheme is always "skill" for URIs that pass ParseURI's validation.
	Scheme string

	// Raw is the original URI string ParseURI was called with.
	Raw string

	// AllSegments is the canonical segment view: the authority component
	// followed by the path component, decoded and split on "/". Empty
	// segments cause a parse error rather than appearing in the slice.
	AllSegments []string

	// SkillPath is the skill directory path. Populated for manifest URIs;
	// empty for non-manifest URIs parsed standalone.
	SkillPath []string

	// FilePath is the file path within the skill. Populated for manifest
	// URIs (always ["SKILL.md"]) and after a successful SplitAt or
	// ResolveRelative; empty otherwise.
	FilePath []string

	// SkillName is the final segment of SkillPath, equal to the skill's
	// frontmatter name per SEP-2640. Empty when SkillPath is unset.
	SkillName string

	// IsManifest is true when FilePath equals exactly ["SKILL.md"].
	IsManifest bool
}

// ParseURI parses a skill:// URI into a URIParts.
//
// Rejects any "." or ".." path segment with ErrPathTraversal — SEP-2640
// skill names use [a-z0-9-] only, so dot-segments cannot appear in a
// well-formed URI and their presence indicates a malformed or traversal
// probe. Production servers SHOULD validate inbound resources/read URIs
// against this parser before registry lookup so that traversal attempts
// produce a precise InvalidParams error instead of a generic
// "unknown resource" miss.
//
// Validation rules:
//   - Scheme must be exactly "skill" (ErrInvalidScheme otherwise).
//   - At least one path segment is required (ErrEmptySkillPath).
//   - No segment may be empty, e.g. from consecutive slashes
//     (ErrEmptyPathSegment).
//   - SKILL.md, when present, MUST be the final segment of the URI; it MUST
//     NOT appear at any non-terminal position (ErrManifestNotInRoot).
//   - For manifest URIs the final skill-path segment (i.e. the segment just
//     before SKILL.md) MUST be a valid Agent Skills name: lowercase letters,
//     digits, and hyphens, neither leading nor trailing hyphen
//     (ErrInvalidSkillName / ErrEmptySkillName).
//
// ParseURI does not require a manifest URI. Non-manifest URIs validate
// scheme, segments, and the no-nested-SKILL.md rule, but SkillPath and
// FilePath are left empty (see URIParts).
func ParseURI(s string) (URIParts, error) {
	if s == "" {
		return URIParts{}, fmt.Errorf("%w: empty URI", ErrInvalidScheme)
	}

	u, err := url.Parse(s)
	if err != nil {
		return URIParts{}, fmt.Errorf("%w: %v", ErrInvalidScheme, err)
	}
	if u.Scheme != Scheme {
		return URIParts{}, fmt.Errorf("%w: got %q", ErrInvalidScheme, u.Scheme)
	}

	segments, err := splitURIPath(u)
	if err != nil {
		return URIParts{}, err
	}
	if len(segments) == 0 {
		return URIParts{}, ErrEmptySkillPath
	}

	// SEP-2640: no SKILL.md may appear in a descendant directory. We allow
	// a single SKILL.md at the terminal position; any other position is a
	// nesting violation.
	for i, seg := range segments[:len(segments)-1] {
		if seg == ManifestFilename {
			return URIParts{}, fmt.Errorf("%w: SKILL.md at segment %d", ErrManifestNotInRoot, i)
		}
	}

	out := URIParts{
		Scheme:      u.Scheme,
		Raw:         s,
		AllSegments: segments,
	}

	if segments[len(segments)-1] == ManifestFilename {
		// Manifest URI: split before the trailing SKILL.md.
		if len(segments) < 2 {
			return URIParts{}, ErrEmptySkillPath
		}
		out.SkillPath = segments[:len(segments)-1]
		out.FilePath = []string{ManifestFilename}
		out.IsManifest = true
		name := out.SkillPath[len(out.SkillPath)-1]
		if err := ValidateSkillName(name); err != nil {
			return URIParts{}, err
		}
		out.SkillName = name
	}

	return out, nil
}

// splitURIPath joins authority + path into a single decoded segment list.
// RFC 3986 places the first <skill-path> segment in the authority component
// when the URI has the "skill://" form; the remainder lives in the path.
// Both contribute to the SEP-2640 skill path.
//
// Decoded segments are checked against ErrPathTraversal: "." and ".."
// are rejected even though they are syntactically valid path components,
// because SEP-2640 skill paths use [a-z0-9-] and dot-segments cannot
// appear legitimately. Rejecting at parse time prevents the registry
// miss path from masking traversal probes as ordinary typos.
func splitURIPath(u *url.URL) ([]string, error) {
	var segs []string
	if u.Host != "" {
		host, err := url.PathUnescape(u.Host)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid host: %v", ErrInvalidScheme, err)
		}
		if host == "." || host == ".." {
			return nil, fmt.Errorf("%w: at authority", ErrPathTraversal)
		}
		segs = append(segs, host)
	}
	p := u.Path
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return segs, nil
	}
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		// Trailing slash produces a trailing empty segment; treat as
		// terminator rather than empty segment.
		if seg == "" && i == len(parts)-1 {
			continue
		}
		if seg == "" {
			return nil, fmt.Errorf("%w: at index %d", ErrEmptyPathSegment, len(segs))
		}
		decoded, err := url.PathUnescape(seg)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidScheme, err)
		}
		if decoded == "." || decoded == ".." {
			return nil, fmt.Errorf("%w: %q at index %d", ErrPathTraversal, decoded, len(segs))
		}
		segs = append(segs, decoded)
	}
	return segs, nil
}

// SplitAt returns a copy of p with the skill/file boundary set after n
// segments. The first n segments become SkillPath, the remainder become
// FilePath. This is the explicit-boundary form callers use when the skill
// path is known from an index entry or a prior manifest read.
//
// SplitAt validates that:
//   - n is in range [1, len(AllSegments)],
//   - the segment at position n-1 is a valid skill name,
//   - no SKILL.md appears in FilePath at a non-root position (only allowed
//     when FilePath is exactly ["SKILL.md"]).
func (p URIParts) SplitAt(n int) (URIParts, error) {
	if n < 1 || n > len(p.AllSegments) {
		return URIParts{}, fmt.Errorf("%w: SplitAt n=%d, segments=%d", ErrEmptySkillPath, n, len(p.AllSegments))
	}
	skillPath := append([]string(nil), p.AllSegments[:n]...)
	filePath := append([]string(nil), p.AllSegments[n:]...)
	name := skillPath[len(skillPath)-1]
	if err := ValidateSkillName(name); err != nil {
		return URIParts{}, err
	}
	// SKILL.md only valid as the sole file-path segment.
	for i, seg := range filePath {
		if seg == ManifestFilename && !(len(filePath) == 1 && i == 0) {
			return URIParts{}, fmt.Errorf("%w: SKILL.md inside skill", ErrManifestNotInRoot)
		}
	}
	out := URIParts{
		Scheme:      p.Scheme,
		Raw:         p.Raw,
		AllSegments: p.AllSegments,
		SkillPath:   skillPath,
		FilePath:    filePath,
		SkillName:   name,
		IsManifest:  len(filePath) == 1 && filePath[0] == ManifestFilename,
	}
	return out, nil
}

// SkillRootURI returns the URI of the skill's root directory (the URI
// obtained by stripping the trailing SKILL.md, with a trailing slash).
// Returns the empty string if SkillPath is not populated.
func (p URIParts) SkillRootURI() string {
	if len(p.SkillPath) == 0 {
		return ""
	}
	return Scheme + "://" + strings.Join(escapeSegments(p.SkillPath), "/") + "/"
}

// ManifestURI returns the URI of the skill's SKILL.md. Returns the empty
// string if SkillPath is not populated.
func (p URIParts) ManifestURI() string {
	if len(p.SkillPath) == 0 {
		return ""
	}
	return Scheme + "://" + strings.Join(escapeSegments(p.SkillPath), "/") + "/" + ManifestFilename
}

// String reconstructs the URI from the parsed segments. If FilePath is
// populated it joins SkillPath + FilePath; otherwise it falls back to
// AllSegments. The result is canonical, with each segment percent-encoded
// per RFC 3986.
func (p URIParts) String() string {
	if len(p.SkillPath) > 0 {
		root := strings.Join(escapeSegments(p.SkillPath), "/")
		if len(p.FilePath) == 0 {
			return Scheme + "://" + root + "/"
		}
		return Scheme + "://" + root + "/" + strings.Join(escapeSegments(p.FilePath), "/")
	}
	if len(p.AllSegments) > 0 {
		return Scheme + "://" + strings.Join(escapeSegments(p.AllSegments), "/")
	}
	return p.Raw
}

func escapeSegments(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = url.PathEscape(s)
	}
	return out
}

// ValidateSkillName checks that name satisfies the Agent Skills naming
// rules: 1+ characters, lowercase letters / digits / hyphens, no leading or
// trailing hyphen, no consecutive hyphens.
//
// The SEP delegates the format to the Agent Skills specification but states
// that names cannot collide with the reserved well-known path "index.json"
// because "." is not permitted.
func ValidateSkillName(name string) error {
	if name == "" {
		return ErrEmptySkillName
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("%w: %q", ErrInvalidSkillName, name)
	}
	prevHyphen := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			prevHyphen = false
		case r >= '0' && r <= '9':
			prevHyphen = false
		case r == '-':
			if prevHyphen {
				return fmt.Errorf("%w: consecutive hyphens in %q", ErrInvalidSkillName, name)
			}
			prevHyphen = true
		default:
			return fmt.Errorf("%w: %q", ErrInvalidSkillName, name)
		}
	}
	return nil
}

// ResolveRelative resolves a relative file reference against a known skill
// root URI, returning a fully-populated URIParts for the referenced file.
//
// SEP-2640 specifies that relative references within a skill resolve like
// filesystem paths against the skill's root directory (the directory
// containing SKILL.md). Resolution delegates to RFC 3986 reference
// resolution via net/url, then checks the result stays inside the skill's
// scope.
//
// Behavior:
//   - skillRoot must have SkillPath populated (typically a manifest URI
//     produced by ParseURI). ResolveRelative returns ErrNotManifestURI if
//     not.
//   - rel is a relative reference per RFC 3986. Dot-segment normalization
//     (". ", "..") is delegated to the stdlib resolver.
//   - References that carry their own scheme or authority, an absolute path,
//     or an empty path are rejected before resolution because RFC 3986
//     would let any of them short-circuit out of the skill scope.
//   - After resolution, the result MUST still start with
//     skillRoot.SkillPath. Otherwise the reference escaped (e.g., excess
//     ".." segments) and ErrRelativeEscapesSkill is returned.
//   - A resolved URI whose final segment is SKILL.md at a deeper position
//     than the skill root is rejected with ErrManifestNotInRoot because
//     SEP-2640 forbids skill nesting.
//   - The result reuses skillRoot.SkillPath. FilePath holds the resolved
//     file path segments. IsManifest is set when the resolution lands on
//     the skill's own SKILL.md (the idempotent case).
func ResolveRelative(skillRoot URIParts, rel string) (URIParts, error) {
	if len(skillRoot.SkillPath) == 0 {
		return URIParts{}, ErrNotManifestURI
	}
	if rel == "" {
		return URIParts{}, fmt.Errorf("%w: empty relative reference", ErrEmptyPathSegment)
	}

	base, err := url.Parse(skillRoot.ManifestURI())
	if err != nil {
		return URIParts{}, fmt.Errorf("skills: re-parse skill root: %w", err)
	}
	ref, err := url.Parse(rel)
	if err != nil {
		return URIParts{}, fmt.Errorf("%w: invalid reference %q: %v", ErrInvalidScheme, rel, err)
	}
	// RFC 3986 reference resolution honors a reference's scheme or
	// authority and treats an absolute path as overriding. All three would
	// silently short-circuit out of the skill scope, so reject them before
	// handing off to ResolveReference.
	if ref.Scheme != "" || ref.Host != "" {
		return URIParts{}, fmt.Errorf("%w: reference carries its own scheme or authority %q", ErrRelativeEscapesSkill, rel)
	}
	if strings.HasPrefix(ref.Path, "/") {
		return URIParts{}, fmt.Errorf("%w: absolute path %q", ErrRelativeEscapesSkill, rel)
	}
	if ref.Path == "" {
		return URIParts{}, fmt.Errorf("%w: empty relative reference", ErrEmptyPathSegment)
	}

	resolved := base.ResolveReference(ref)

	// Re-parse the resolved URI through ParseURI so it inherits the
	// canonical segment shape, percent decoding, and the
	// no-nested-SKILL.md rule that ParseURI already enforces on the URI
	// string. Any error from ParseURI propagates with its named sentinel.
	parsed, err := ParseURI(resolved.String())
	if err != nil {
		return URIParts{}, err
	}

	// RFC 3986 dot-segment removal silently collapses excess ".."
	// segments against the path root, producing a URI that no longer
	// starts with skillRoot.SkillPath. The prefix check below is the
	// canonical escape detector.
	if !hasPrefixSegments(parsed.AllSegments, skillRoot.SkillPath) {
		return URIParts{}, fmt.Errorf("%w: %q", ErrRelativeEscapesSkill, rel)
	}

	// Resolution landed back on the skill root directory itself (e.g.,
	// rel normalized to no path segments). Treat as a caller mistake.
	if len(parsed.AllSegments) == len(skillRoot.SkillPath) {
		return URIParts{}, fmt.Errorf("%w: resolves to skill root", ErrEmptyPathSegment)
	}

	// ParseURI treats any terminal SKILL.md as a manifest URI. After
	// resolution, a manifest deeper than the original skill root means the
	// reference pointed at a nested skill, which SEP-2640 forbids. The
	// idempotent case (resolving back to the same manifest) is allowed.
	if parsed.IsManifest && len(parsed.SkillPath) > len(skillRoot.SkillPath) {
		return URIParts{}, fmt.Errorf("%w: nested SKILL.md in resolved path", ErrManifestNotInRoot)
	}

	// Re-split at the original skill boundary so the result reflects this
	// skill's identity rather than whatever ParseURI inferred from the
	// terminal segment.
	return parsed.SplitAt(len(skillRoot.SkillPath))
}

// hasPrefixSegments reports whether segs begins with prefix, segment by
// segment.
func hasPrefixSegments(segs, prefix []string) bool {
	if len(segs) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if segs[i] != p {
			return false
		}
	}
	return true
}

// IsIndexURI reports whether s is the reserved skill://index.json URI.
// The check is exact-match per SEP-2640's reservation rule.
func IsIndexURI(s string) bool {
	return s == IndexURI
}
