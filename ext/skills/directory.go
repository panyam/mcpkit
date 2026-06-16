package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// defaultDirectoryReadPageSize matches the page size server/dispatch.go
// uses for resources/list. Kept local rather than imported because the
// upstream helper is package-private and one-line duplication beats a
// promotion to the public surface for a single off-package consumer.
const defaultDirectoryReadPageSize = 100

// handleDirectoryRead serves SEP-2640's resources/directory/read method.
//
// SEP semantics (per commit 2e04c48d on 2026-06-09):
//
//   - The URI MUST resolve to a directory resource — a node inside one of
//     the skills this Provider serves, with no trailing slash. Unknown or
//     non-directory URIs return -32602 (Invalid params), matching the
//     resources/read error code for unknown resources.
//   - The result enumerates only the directory's DIRECT children. Files
//     carry their ordinary resource metadata; subdirectories carry
//     MimeTypeDirectory ("inode/directory") so clients descend by issuing
//     a follow-up call against the child URI.
//   - Pagination mirrors resources/list: callers may pass a cursor, the
//     handler returns NextCursor when more entries remain.
//
// Resolution rule: the URI must start with one of the Provider's skill
// roots (Scheme + "://" + uriPrefix.../<skill>). The remainder of the
// URI is the relative path inside that skill's fs.FS subtree. The skill
// root itself is a valid directory URI; deeper directories work too as
// long as the on-FS path is a directory.
func (p *Provider) handleDirectoryRead(ctx core.MethodContext, id, params json.RawMessage) *core.Response {
	var req DirectoryReadRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
	}
	if req.URI == "" {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "skills: resources/directory/read: uri is required")
	}

	skill, relPath, err := p.resolveDirectoryURI(req.URI)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}

	children, err := p.enumerateDirectory(skill, relPath, req.URI)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}

	page, next := paginateDirectoryRead(children, req.Cursor, defaultDirectoryReadPageSize)
	return core.NewResponse(id, DirectoryReadResult{Resources: page, NextCursor: next})
}

// resolveDirectoryURI maps a skill:// URI to (owning skill, relative
// path inside the skill's fs.FS subtree). Returns a typed error suitable
// for -32602 propagation when the URI does not address a directory the
// Provider knows about.
func (p *Provider) resolveDirectoryURI(uri string) (*skillEntry, string, error) {
	parsed, err := ParseURI(uri)
	if err != nil {
		return nil, "", fmt.Errorf("skills: directory URI: %w", err)
	}
	if parsed.IsManifest {
		// Manifest URIs (terminal SKILL.md) are file resources, not
		// directories. resources/read handles them.
		return nil, "", fmt.Errorf("skills: %q is a file resource, not a directory", uri)
	}
	segs := parsed.AllSegments
	if len(segs) == 0 {
		return nil, "", fmt.Errorf("skills: directory URI has no segments")
	}

	// Find the skill whose uriSegs is the longest prefix of segs. Each
	// skillEntry.uriSegs holds the full skill path (with any URI prefix
	// applied at registration time).
	var best *skillEntry
	bestLen := -1
	for _, s := range p.skills {
		if hasPrefixSegments(segs, s.uriSegs) && len(s.uriSegs) > bestLen {
			best = s
			bestLen = len(s.uriSegs)
		}
	}
	if best == nil {
		return nil, "", fmt.Errorf("skills: %q is outside every served skill subtree", uri)
	}

	// Compute the relative path inside the skill's fs.FS subtree.
	// best.dirPath is the FS root for the skill; segs[bestLen:] is the
	// in-skill path.
	relSegs := segs[bestLen:]

	// Reject path-traversal segments BEFORE joining. ParseURI does not
	// forbid "." or ".." segments at the URI level (the SEP is silent on
	// them) and a naive path.Join would let
	// `skill://acme/billing/refunds/..` cleanly resolve to
	// `acme/billing`, enumerating the parent tree — exactly the surface
	// the directoryRead capability is meant to scope away. The
	// containment check in enumerateDirectory is defense-in-depth; this
	// is the load-bearing guard.
	for _, s := range relSegs {
		if s == ".." || s == "." || strings.ContainsAny(s, `/\`) {
			return nil, "", fmt.Errorf("skills: %q contains an invalid path segment %q", uri, s)
		}
	}

	relPath := strings.Join(relSegs, "/")
	return best, relPath, nil
}

// enumerateDirectory reads one level of fs entries beneath skill.dirPath
// + relPath and converts them to ResourceDefs. Files use detectMimeType
// for their ordinary MIME; subdirectories use MimeTypeDirectory.
//
// parentURI is the original requested URI; used to construct each
// child's URI by joining "<parentURI>/<entry-name>".
func (p *Provider) enumerateDirectory(skill *skillEntry, relPath, parentURI string) ([]core.ResourceDef, error) {
	fsRoot := skill.dirPath
	if relPath != "" {
		fsRoot = path.Join(skill.dirPath, relPath)
	}

	// Defense in depth: even with the segment guard in resolveDirectoryURI,
	// confirm the post-Clean path still names skill.dirPath or a descendant
	// before touching the FS. Future changes to splitURIPath or the
	// segment guard surface here as a typed error instead of a silent
	// out-of-scope enumeration.
	cleaned := path.Clean(fsRoot)
	if cleaned != skill.dirPath && !strings.HasPrefix(cleaned, skill.dirPath+"/") {
		return nil, fmt.Errorf("skills: %q resolves outside the skill subtree", parentURI)
	}

	// Verify the target is a directory before reading. fs.Stat surfaces
	// fs.ErrNotExist for unknown paths, which we want to surface as
	// "no such directory."
	info, err := fs.Stat(p.cfg.fsys, fsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("skills: %q does not exist", parentURI)
		}
		return nil, fmt.Errorf("skills: stat %q: %w", parentURI, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skills: %q is not a directory", parentURI)
	}

	entries, err := fs.ReadDir(p.cfg.fsys, fsRoot)
	if err != nil {
		return nil, fmt.Errorf("skills: read dir %q: %w", parentURI, err)
	}

	// Trim a trailing slash on the parent URI if any caller supplied one
	// even though the SEP says directory URIs MUST NOT have one — be
	// permissive on input, strict on output.
	parent := strings.TrimRight(parentURI, "/")

	out := make([]core.ResourceDef, 0, len(entries))
	for _, e := range entries {
		childURI := parent + "/" + e.Name()
		if e.IsDir() {
			out = append(out, core.ResourceDef{
				URI:      childURI,
				Name:     e.Name(),
				MimeType: MimeTypeDirectory,
			})
			continue
		}
		mt := detectMimeType([]string{e.Name()})
		out = append(out, core.ResourceDef{
			URI:      childURI,
			Name:     e.Name(),
			MimeType: mt,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].URI < out[j].URI })
	return out, nil
}

// paginateDirectoryRead returns a contiguous slice of items starting at
// the cursor offset, plus the next-cursor string (empty when the page
// includes the tail). Cursor encoding is a base-10 offset; opaque to
// callers per the resources/list contract.
func paginateDirectoryRead(items []core.ResourceDef, cursor string, pageSize int) ([]core.ResourceDef, string) {
	start := parseDirectoryReadCursor(cursor)
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end >= len(items) {
		return items[start:], ""
	}
	return items[start:end], formatDirectoryReadCursor(end)
}

func parseDirectoryReadCursor(s string) int {
	if s == "" {
		return 0
	}
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func formatDirectoryReadCursor(n int) string {
	if n == 0 {
		return ""
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
