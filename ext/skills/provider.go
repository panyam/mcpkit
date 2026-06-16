package skills

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Provider walks an io/fs.FS, identifies SEP-2640 skills by SKILL.md
// presence, and exposes each skill's files as MCP resources under the
// skill:// URI convention.
//
// A Provider is built once via NewProvider and is immutable thereafter.
// The walk that populates the resource map happens at construction time
// and surfaces any structural violation (name mismatch, nested skill,
// invalid skill name) as a typed error before any resource is registered
// with a server. The hot-reload path (ext/skills issue 564) is a separate
// affordance built on top of this one.
type Provider struct {
	cfg       providerConfig
	skills    []*skillEntry
	resources []*resourceEntry
	byURI     map[string]*resourceEntry

	versionMu sync.RWMutex
	version   uint64
	srv       *server.Server
	indexer   *Indexer
}

type skillEntry struct {
	dirPath string      // path within cfg.fsys, e.g. "git-workflow" or "acme/billing/refunds"
	uriSegs []string    // full skill-path segments including uriPrefix
	fm      Frontmatter // parsed SKILL.md frontmatter
	body    []byte      // SKILL.md body bytes
}

type resourceEntry struct {
	URI        string
	skill      *skillEntry
	fsPath     string // path within cfg.fsys; for archive resources this is the skill dir
	mimeType   string
	isManifest bool
	isArchive  bool
}

// NewProvider walks the configured fs.FS and returns a Provider with
// every skill's files mapped to MCP resources under the skill:// URI
// scheme. The walk is single-pass at construction time, then the
// resource map is fixed.
//
// Errors at construction:
//   - ErrProviderMissingFS when neither WithFS nor WithDirectory was
//     supplied.
//   - Frontmatter parse errors (ErrMissingFrontmatter, etc.) on a
//     malformed SKILL.md.
//   - ErrSkillNameMismatch when frontmatter name does not equal the
//     skill's parent directory base name.
//   - ErrNestedSkill when a SKILL.md is found inside another skill's
//     subtree.
//   - ErrInvalidSkillName when the final skill-path segment violates the
//     Agent Skills naming rules (the directory name and the matching
//     frontmatter name must both satisfy them).
func NewProvider(opts ...ProviderOption) (*Provider, error) {
	cfg := providerConfig{metaPrefix: MetaPrefix}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.fsys == nil {
		return nil, ErrProviderMissingFS
	}
	if cfg.root == "" {
		cfg.root = "."
	}

	p := &Provider{cfg: cfg, byURI: map[string]*resourceEntry{}}
	if err := p.walk(); err != nil {
		return nil, err
	}
	return p, nil
}

// walk runs the three-phase discovery: find all SKILL.md locations,
// reject nested skills via sorted-prefix comparison, then per skill
// parse the manifest and register every file in its subtree as a
// resource.
func (p *Provider) walk() error {
	var skillDirs []string
	walkErr := fs.WalkDir(p.cfg.fsys, p.cfg.root, func(walkPath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if path.Base(walkPath) != ManifestFilename {
			return nil
		}
		dir := path.Dir(walkPath)
		if dir == "." {
			return fmt.Errorf("%w: SKILL.md at FS root", ErrManifestNotInRoot)
		}
		skillDirs = append(skillDirs, dir)
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("skills: walk: %w", walkErr)
	}

	sort.Strings(skillDirs)

	// Sorted-prefix nesting check. Any directory whose path is a strict
	// child of an earlier entry (i.e. starts with prev + "/") is nested
	// inside that earlier skill.
	for i := 1; i < len(skillDirs); i++ {
		prev := skillDirs[i-1]
		cur := skillDirs[i]
		if strings.HasPrefix(cur, prev+"/") {
			return fmt.Errorf("%w: %q nested inside %q", ErrNestedSkill, cur, prev)
		}
	}

	for _, dir := range skillDirs {
		if err := p.registerSkill(dir); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) registerSkill(dirPath string) error {
	skillFilePath := path.Join(dirPath, ManifestFilename)
	src, err := fs.ReadFile(p.cfg.fsys, skillFilePath)
	if err != nil {
		return fmt.Errorf("skills: read %s: %w", skillFilePath, err)
	}
	fm, body, err := ParseFrontmatter(src)
	if err != nil {
		return fmt.Errorf("skills: %s: %w", skillFilePath, err)
	}

	dirName := path.Base(dirPath)
	if fm.Name != dirName {
		return fmt.Errorf("%w: directory %q vs frontmatter name %q", ErrSkillNameMismatch, dirName, fm.Name)
	}
	if err := ValidateSkillName(dirName); err != nil {
		return fmt.Errorf("skills: %s: %w", skillFilePath, err)
	}

	uriSegs := make([]string, 0, len(p.cfg.uriPrefix)+strings.Count(dirPath, "/")+1)
	uriSegs = append(uriSegs, p.cfg.uriPrefix...)
	uriSegs = append(uriSegs, strings.Split(dirPath, "/")...)

	skill := &skillEntry{
		dirPath: dirPath,
		uriSegs: uriSegs,
		fm:      fm,
		body:    body,
	}
	p.skills = append(p.skills, skill)

	if p.cfg.archiveMode != ArchiveFormatUnknown {
		return p.registerArchive(skill)
	}

	return fs.WalkDir(p.cfg.fsys, dirPath, func(walkPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(walkPath, dirPath)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return nil
		}
		fileSegs := strings.Split(rel, "/")
		return p.registerResource(skill, fileSegs, walkPath)
	})
}

func (p *Provider) registerArchive(skill *skillEntry) error {
	suffix := p.cfg.archiveMode.Suffix()
	uri := Scheme + "://" + joinSegments(skill.uriSegs) + suffix
	if _, exists := p.byURI[uri]; exists {
		return fmt.Errorf("skills: duplicate archive URI %q", uri)
	}
	entry := &resourceEntry{
		URI:       uri,
		skill:     skill,
		fsPath:    skill.dirPath,
		mimeType:  p.cfg.archiveMode.MimeType(),
		isArchive: true,
	}
	p.resources = append(p.resources, entry)
	p.byURI[uri] = entry
	return nil
}

func (p *Provider) registerResource(skill *skillEntry, fileSegs []string, fsPath string) error {
	all := make([]string, 0, len(skill.uriSegs)+len(fileSegs))
	all = append(all, skill.uriSegs...)
	all = append(all, fileSegs...)
	uri := Scheme + "://" + strings.Join(all, "/")
	if _, exists := p.byURI[uri]; exists {
		return fmt.Errorf("skills: duplicate resource URI %q", uri)
	}
	entry := &resourceEntry{
		URI:        uri,
		skill:      skill,
		fsPath:     fsPath,
		mimeType:   detectMimeType(fileSegs),
		isManifest: len(fileSegs) == 1 && fileSegs[0] == ManifestFilename,
	}
	p.resources = append(p.resources, entry)
	p.byURI[uri] = entry
	return nil
}

// Resources returns the cataloged resource definitions in stable
// URI-sorted order, suitable for inspection or for callers that need a
// list view without going through a server registration.
func (p *Provider) Resources() []core.ResourceDef {
	sort.SliceStable(p.resources, func(i, j int) bool {
		return p.resources[i].URI < p.resources[j].URI
	})
	out := make([]core.ResourceDef, 0, len(p.resources))
	for _, r := range p.resources {
		out = append(out, p.defFor(r))
	}
	return out
}

// RegisterWith installs each cataloged resource onto srv via the
// existing RegisterResource path. The handler streams the file from
// the underlying fs.FS at request time. Resources are registered in
// stable URI-sorted order.
//
// RegisterWith also declares the io.modelcontextprotocol/skills
// extension on srv via srv.RegisterExtension so the capability appears
// in the initialize response. RegisterExtension is keyed by extension
// ID and idempotent, so an explicit srv.RegisterExtension or
// server.WithExtension call on the same SkillsExtension causes no
// double-emit.
//
// Unless WithoutDirectoryRead was supplied to NewProvider, RegisterWith
// also installs the SEP-2640 resources/directory/read handler (added by
// SEP commit 2e04c48d) and emits {"directoryRead": true} inside the
// extension's capability Config. The Provider walks its underlying
// fs.FS at request time to enumerate the requested directory's direct
// children.
//
// Unless WithoutIndex was supplied to NewProvider, RegisterWith also
// constructs an internal Indexer and registers skill://index.json on
// srv. Use WithIndexCacheTTL to tune the index's cache freshness or
// WithoutIndex to suppress registration entirely (when, for example,
// the caller wants to construct and register a custom Indexer).
func (p *Provider) RegisterWith(srv *server.Server) {
	p.versionMu.Lock()
	p.srv = srv
	p.versionMu.Unlock()

	sort.SliceStable(p.resources, func(i, j int) bool {
		return p.resources[i].URI < p.resources[j].URI
	})
	for _, r := range p.resources {
		srv.RegisterResource(p.defFor(r), p.makeHandler(r))
	}
	srv.RegisterExtension(SkillsExtension{DirectoryRead: !p.cfg.suppressDirectoryRead})
	if !p.cfg.suppressDirectoryRead {
		srv.HandleMethod(MethodResourcesDirectoryRead, p.handleDirectoryRead)
	}
	if !p.cfg.suppressIndex {
		var opts []IndexerOption
		if p.cfg.indexCacheTTL > 0 {
			opts = append(opts, WithIndexerCacheTTL(p.cfg.indexCacheTTL))
		}
		idx := NewIndexer(p, opts...)
		p.versionMu.Lock()
		p.indexer = idx
		p.versionMu.Unlock()
		idx.RegisterWith(srv)
	}
	srv.UseMiddleware(skillURIValidationMiddleware)
}

// Version returns the Provider's monotonic invalidation counter. The
// counter starts at zero and increments on every NotifyChanged /
// Refresh call. Consumers use Version() to drive cache freshness — the
// Indexer compares the counter at cache-build time against the current
// value and rebuilds on mismatch.
//
// Forward-compatible with the per-artifact counters proposed in #798:
// when those land, the global counter remains as the ceiling that any
// per-artifact counter increments alongside.
func (p *Provider) Version() uint64 {
	p.versionMu.RLock()
	defer p.versionMu.RUnlock()
	return p.version
}

// NotifyChanged is the Applier entry point: pass the relative fs.FS
// paths that changed and the Provider bumps its version counter,
// invalidates the Indexer cache (so the next skill://index.json read
// rebuilds), and — if RegisterWith has bound a server — broadcasts a
// notifications/resources/list_changed event to subscribed sessions on
// the stateful wire.
//
// For #795 scope the paths argument is advisory only — any call (with
// any path list, empty included) bumps the global counter once and
// invalidates the whole index cache. Issues #796 (sub-indexes) and
// #798 (per-artifact pack cache) will refine this to per-path
// invalidation via a dependency DAG, keying off the same NotifyChanged
// entry point so detector implementations don't change.
//
// Stateless wire honesty: this call alone cannot push to stateless
// clients (no persistent channel exists by construction). Stateless
// clients learn of changes by polling skill://index.json and observing
// the _meta.io.modelcontextprotocol.skills/version field bump.
//
// Safe to call before RegisterWith (the broadcast is a no-op) and from
// any goroutine.
func (p *Provider) NotifyChanged(paths ...string) error {
	p.versionMu.Lock()
	p.version++
	srv := p.srv
	idx := p.indexer
	p.versionMu.Unlock()

	if idx != nil {
		idx.Invalidate()
	}
	if srv != nil {
		srv.Broadcast(context.Background(), "notifications/resources/list_changed", nil)
	}
	return nil
}

// Refresh is sugar for NotifyChanged() with no paths — signals "the
// underlying content changed in some way; recompute everything." Use
// from adopter-driven hot-reload hooks (webhook handler, build
// pipeline, manual sweep) when the caller doesn't have a precise
// changed-path list.
func (p *Provider) Refresh() error {
	return p.NotifyChanged()
}

// skillURIValidationMiddleware short-circuits resources/read and
// resources/directory/read requests whose uri is shaped like a skill://
// URI but fails ParseURI — most importantly URIs that smuggle in dot
// segments (".", ".."). Without this gate, traversal probes fall
// through to the registry's exact-match lookup and surface as the
// generic "unknown resource" InvalidParams response, indistinguishable
// from a typo. With the gate, traversal probes return InvalidParams
// with an explicit ErrPathTraversal-derived message, so audit logs and
// host clients can act on the signal.
//
// Pass-through cases (next is invoked unchanged):
//   - any method other than resources/read or resources/directory/read
//   - resources/read of a non-skill:// URI (the server may host
//     additional resource schemes alongside Skills)
//   - params that don't parse as the read envelope (the dispatcher's
//     own error path handles that — duplicating here would compete)
//
// Registered outermost in Provider.RegisterWith because middleware is
// append-only and runs in registration order; placing the validator
// last keeps it innermost-but-still-before-routing, which is the right
// position for input shaping.
func skillURIValidationMiddleware(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
	switch req.Method {
	case "resources/read", MethodResourcesDirectoryRead:
	default:
		return next(ctx, req)
	}
	var envelope struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &envelope); err != nil {
		return next(ctx, req)
	}
	if !strings.HasPrefix(envelope.URI, Scheme+"://") {
		return next(ctx, req)
	}
	if _, err := ParseURI(envelope.URI); err != nil {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
			fmt.Sprintf("invalid skill URI %q: %v", envelope.URI, err)), nil
	}
	return next(ctx, req)
}

func (p *Provider) defFor(r *resourceEntry) core.ResourceDef {
	def := core.ResourceDef{
		URI:      r.URI,
		Name:     path.Base(r.fsPath),
		MimeType: r.mimeType,
	}
	if r.isManifest || r.isArchive {
		def.Name = r.skill.fm.Name
		def.Description = r.skill.fm.Description
		if len(r.skill.fm.Extra) > 0 && p.cfg.metaPrefix != "" {
			def.Annotations = make(map[string]any, len(r.skill.fm.Extra))
			for k, v := range r.skill.fm.Extra {
				def.Annotations[p.cfg.metaPrefix+k] = v
			}
		}
	}
	return def
}

func (p *Provider) makeHandler(r *resourceEntry) core.ResourceHandler {
	return func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
		if r.isArchive {
			data, err := PackSkill(p.cfg.fsys, r.fsPath, p.cfg.archiveMode)
			if err != nil {
				return core.ResourceResult{}, fmt.Errorf("skills: pack %s: %w", r.fsPath, err)
			}
			return core.ResourceResult{
				Contents: []core.ResourceReadContent{{
					URI:      r.URI,
					MimeType: r.mimeType,
					Blob:     base64.StdEncoding.EncodeToString(data),
				}},
			}, nil
		}
		f, err := p.cfg.fsys.Open(r.fsPath)
		if err != nil {
			return core.ResourceResult{}, fmt.Errorf("skills: open %s: %w", r.fsPath, err)
		}
		defer f.Close()
		buf, err := io.ReadAll(f)
		if err != nil {
			return core.ResourceResult{}, fmt.Errorf("skills: read %s: %w", r.fsPath, err)
		}
		content := core.ResourceReadContent{URI: r.URI, MimeType: r.mimeType}
		if isTextMime(r.mimeType) {
			content.Text = string(buf)
		} else {
			content.Blob = base64.StdEncoding.EncodeToString(buf)
		}
		return core.ResourceResult{Contents: []core.ResourceReadContent{content}}, nil
	}
}

// Catalog returns the index entries for every cataloged skill, suitable
// for marshalling into skill://index.json by ext/skills issue 560.
// The Digest field is left empty here. SHA-256 over the canonical
// artifact (the SKILL.md bytes, or the archive bytes for archive-mode
// skills) is computed at index generation time so its source of truth
// is colocated with the canonicalization rule.
func (p *Provider) Catalog() []IndexEntry {
	out := make([]IndexEntry, 0, len(p.skills))
	for _, s := range p.skills {
		uri := Scheme + "://" + strings.Join(s.uriSegs, "/") + "/" + ManifestFilename
		out = append(out, IndexEntry{
			Type:        SkillTypeSkillMD,
			Name:        s.fm.Name,
			Description: s.fm.Description,
			URL:         uri,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].URL < out[j].URL
	})
	return out
}

func detectMimeType(segs []string) string {
	last := segs[len(segs)-1]
	if last == ManifestFilename {
		return "text/markdown"
	}
	ext := path.Ext(last)
	switch ext {
	case ".md":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".py":
		return "text/x-python"
	case ".js":
		return "application/javascript"
	case ".html":
		return "text/html"
	case ".css":
		return "text/css"
	case ".txt":
		return "text/plain"
	}
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	return "application/octet-stream"
}

func isTextMime(mt string) bool {
	if strings.HasPrefix(mt, "text/") {
		return true
	}
	switch {
	case strings.HasPrefix(mt, "application/json"):
		return true
	case strings.HasPrefix(mt, "application/javascript"):
		return true
	case strings.HasPrefix(mt, "application/xml"):
		return true
	}
	return false
}

func splitNonEmpty(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
