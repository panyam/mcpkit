package skills

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// MethodSkillsList and MethodSkillsGet are the two JSON-RPC methods the
// 2026-08-21 SEP-2640 revision introduced, replacing the retired
// skill://index.json well-known resource.
//
// Declaring the io.modelcontextprotocol/skills extension commits a server to
// both. A server with nothing to enumerate answers skills/list with an empty
// array rather than declining the method.
const (
	MethodSkillsList = "skills/list"
	MethodSkillsGet  = "skills/get"
)

// buildResources walks a skill's directory and returns its complete resource
// manifest: every regular file exactly once, including the skill's own
// SKILL.md, each with the digest and byte length of its raw content.
//
// SKILL.md is included rather than pinned separately. The pre-rewrite shape
// carried it on the entry and supporting files under _meta, which meant two
// code paths and two verification rules for what the SEP now treats as one
// list.
func (i *Indexer) buildResources(skill *skillEntry) (SkillResources, error) {
	skillDir := skill.dirPath
	var files []SkillResource

	err := fs.WalkDir(i.provider.cfg.fsys, skillDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		raw, err := fs.ReadFile(i.provider.cfg.fsys, p)
		if err != nil {
			return fmt.Errorf("skills: read %s for manifest: %w", p, err)
		}
		rel := strings.TrimPrefix(p, skillDir+"/")
		segs := append(append([]string{}, skill.uriSegs...), strings.Split(rel, "/")...)
		size := int64(len(raw))
		files = append(files, SkillResource{
			URI:    Scheme + "://" + joinSegments(segs),
			Digest: digestOf(raw),
			Size:   &size,
		})
		return nil
	})
	if err != nil {
		return SkillResources{}, err
	}

	sort.Slice(files, func(a, b int) bool { return files[a].URI < files[b].URI })
	return SkillResources{Files: files}, nil
}

// frontmatterJSON renders parsed frontmatter back to the verbatim object the
// SEP requires. Name and Description are hoisted onto the Frontmatter struct
// for convenience, so they are reinstated here alongside every other key the
// author wrote.
//
// A host compares this object field-by-field against the SKILL.md it fetches,
// so dropping or renaming a key here would make the skill unloadable rather
// than merely lossy.
func frontmatterJSON(fm Frontmatter) map[string]any {
	out := make(map[string]any, len(fm.Extra)+2)
	for k, v := range fm.Extra {
		out[k] = v
	}
	out["name"] = fm.Name
	out["description"] = fm.Description
	return out
}

// handleSkillsList serves skills/list.
//
// Entries are atomic: pagination never splits a skill's resource set across
// pages, which falls out of paginating over whole entries. An empty result is
// conformant and does not mean the server has no skills, so a host that gets
// one must still try skills/get for a URI it learned elsewhere.
func (i *Indexer) handleSkillsList(ctx core.MethodContext, id, params json.RawMessage) *core.Response {
	var req SkillsListRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
	}

	entries, err := i.Entries()
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInternal, err.Error())
	}

	page, next := paginateEntries(entries, req.Cursor, defaultSkillsListPageSize)
	res := SkillsListResult{Skills: page, NextCursor: next}
	if ttl := i.listTTLMs(); ttl > 0 {
		res.TTLMs = &ttl
	}
	res.CacheScope = i.cfg.listCacheScope
	res.Meta = map[string]any{MetaKeyVersion: i.provider.Version()}
	return core.NewResponse(id, res)
}

// handleSkillsGet serves skills/get.
//
// Returns -32602 for a URI this server does not serve, matching the
// resources/read error code for an unknown resource. The lookup runs over the
// full entry set rather than the current listing page, so a skill omitted
// from an empty or partial listing is still retrievable by URI.
func (i *Indexer) handleSkillsGet(ctx core.MethodContext, id, params json.RawMessage) *core.Response {
	var req SkillsGetRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
	}
	if req.URI == "" {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "skills: skills/get: uri is required")
	}

	entries, err := i.Entries()
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInternal, err.Error())
	}
	for _, e := range entries {
		if e.URI == req.URI {
			return core.NewResponse(id, SkillsGetResult{Skill: e})
		}
	}
	return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
		fmt.Sprintf("skills: skills/get: unknown skill URI: %s", req.URI))
}

// defaultSkillsListPageSize of 0 disables server-side paging, matching the
// directory-read default: mcpkit returns every entry in one page and never
// emits a cursor. Callers that need paging drive it from their own store.
const defaultSkillsListPageSize = 0

// paginateEntries slices entries by an integer offset cursor. Entries are
// whole, so a skill's resources never straddle a page boundary.
func paginateEntries(entries []SkillEntry, cursor string, pageSize int) ([]SkillEntry, string) {
	if pageSize <= 0 || len(entries) <= pageSize {
		if cursor != "" {
			// A cursor against an unpaginated listing addresses nothing.
			return []SkillEntry{}, ""
		}
		return entries, ""
	}
	start := 0
	if cursor != "" {
		if _, err := fmt.Sscanf(cursor, "%d", &start); err != nil || start < 0 || start > len(entries) {
			return []SkillEntry{}, ""
		}
	}
	end := start + pageSize
	if end >= len(entries) {
		return entries[start:], ""
	}
	return entries[start:end], fmt.Sprintf("%d", end)
}

// CacheScopePublic is the SEP-2549 scope an Indexer advertises by default: a
// Provider's catalog comes from one fs.FS and does not vary by caller, so a
// shared cache may serve it to everyone.
const CacheScopePublic = "public"

// DefaultListTTLMs is the ttlMs advertised on skills/list when the Indexer
// has no cache TTL of its own.
//
// One minute is short on purpose. A client holding a stale listing reads a
// stale digest, so the next verified read fails rather than silently serving
// wrong content, and the host refetches. The failure is self-correcting, but
// it is still a failure, so the window stays small.
const DefaultListTTLMs = 60_000

// listTTLMs resolves the ttlMs advertised on skills/list. An explicit
// WithListCacheHints value wins; otherwise it mirrors the Indexer's own cache
// TTL so a client is never told to hold a listing longer than the server
// considers it fresh, falling back to DefaultListTTLMs when the Indexer
// recomputes on every call.
func (i *Indexer) listTTLMs() int {
	if i.cfg.listTTLMs > 0 {
		return i.cfg.listTTLMs
	}
	if i.cfg.ttl > 0 {
		return int(i.cfg.ttl.Milliseconds())
	}
	return DefaultListTTLMs
}
