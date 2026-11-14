package groups

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Def is one group as the server defines it. Members are referenced, not
// copied: Tools and Prompts by name, Resources by URI, Groups by child group
// name. groups/get builds full definitions from the server's registry at call
// time.
type Def struct {
	Name         string
	Title        string
	Description  string
	Instructions string
	Tools        []string
	Prompts      []string
	Resources    []string
	Groups       []string
	Meta         map[string]any
}

// Errors from Add and RegisterWith.
var (
	// ErrInvalidName is returned for a group name outside the draft's
	// identifier rule.
	ErrInvalidName = errors.New("groups: invalid group name")
	// ErrDuplicate is returned when a name is added twice.
	ErrDuplicate = errors.New("groups: duplicate group name")
	// ErrEmpty is returned for a group with no instructions and no members.
	// The draft gives an empty groups/get result no meaning.
	ErrEmpty = errors.New("groups: group has no instructions and no members")
	// ErrUnknownMember is returned by RegisterWith when a group names a tool,
	// prompt, resource or child group that isn't registered.
	ErrUnknownMember = errors.New("groups: unknown member")
)

var nameRule = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName reports whether name follows the draft's rule: 1 to 64
// characters of lowercase letters, digits and single hyphens, with no hyphen
// at either end.
func ValidName(name string) bool {
	return len(name) >= 1 && len(name) <= 64 && nameRule.MatchString(name)
}

// Catalog holds a server's groups and serves groups/list and groups/get.
// Build it with NewCatalog, Add each group, then call RegisterWith once the
// primitives it names are registered.
//
// A Catalog is safe for concurrent use. Add after RegisterWith works, and
// takes effect on the next request.
type Catalog struct {
	mu    sync.RWMutex
	defs  map[string]Def
	order []string
	// scoped holds every grouped primitive, keyed "tool:", "prompt:" or
	// "resource:" plus its name or URI. The ListScope reads it on every
	// list call.
	scoped   map[string]bool
	srv      *server.Server
	pageSize int
	ttlMs    *int
}

// Option configures a Catalog.
type Option func(*Catalog)

// WithPageSize sets how many groups one groups/list page carries. The
// default is 250, matching the base list methods. n <= 0 turns paging off.
func WithPageSize(n int) Option { return func(c *Catalog) { c.pageSize = n } }

// WithTTL sets the ttlMs hint on groups/list and groups/get results. The
// default is 0, which tells a client to re-fetch whenever it needs the data.
func WithTTL(ms int) Option { return func(c *Catalog) { c.ttlMs = &ms } }

// NewCatalog returns an empty Catalog.
func NewCatalog(opts ...Option) *Catalog {
	zero := 0
	c := &Catalog{defs: map[string]Def{}, scoped: map[string]bool{}, pageSize: 250, ttlMs: &zero}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Add defines a group. It checks the name and that the group isn't empty;
// RegisterWith checks that its members exist.
func (c *Catalog) Add(d Def) error {
	if !ValidName(d.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, d.Name)
	}
	if d.Instructions == "" && len(d.Tools)+len(d.Prompts)+len(d.Resources)+len(d.Groups) == 0 {
		return fmt.Errorf("%w: %q", ErrEmpty, d.Name)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.defs[d.Name]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicate, d.Name)
	}
	c.defs[d.Name] = d
	c.order = append(c.order, d.Name)
	for _, t := range d.Tools {
		c.scoped["tool:"+t] = true
	}
	for _, p := range d.Prompts {
		c.scoped["prompt:"+p] = true
	}
	for _, r := range d.Resources {
		c.scoped["resource:"+r] = true
	}
	return nil
}

// Extension implements core.ExtensionProvider. The draft defines no
// settings, so the capability is an empty object.
func (c *Catalog) Extension() core.Extension {
	return core.Extension{ID: ExtensionID}
}

// RegisterWith declares the extension on srv, serves groups/list and
// groups/get, and installs a server.ListScope that hides grouped primitives
// from callers that declared the extension.
//
// Call it after registering every tool, prompt and resource the groups name.
// It returns ErrUnknownMember, and registers nothing, if a group names one
// that srv doesn't have, or names a child group the Catalog doesn't have.
func (c *Catalog) RegisterWith(srv *server.Server) error {
	if err := c.check(srv.Registry()); err != nil {
		return err
	}
	c.mu.Lock()
	c.srv = srv
	c.mu.Unlock()
	srv.RegisterExtension(c)
	srv.HandleMethod(MethodList, c.handleList)
	srv.HandleMethod(MethodGet, c.handleGet)
	srv.SetListScope(scope{c})
	return nil
}

func (c *Catalog) check(reg *server.Registry) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, name := range c.order {
		d := c.defs[name]
		for _, t := range d.Tools {
			if _, ok := reg.ToolDef(t); !ok {
				return fmt.Errorf("%w: group %q names tool %q", ErrUnknownMember, name, t)
			}
		}
		for _, p := range d.Prompts {
			if _, ok := reg.PromptDef(p); !ok {
				return fmt.Errorf("%w: group %q names prompt %q", ErrUnknownMember, name, p)
			}
		}
		for _, r := range d.Resources {
			if _, ok := reg.ResourceDef(r); !ok {
				return fmt.Errorf("%w: group %q names resource %q", ErrUnknownMember, name, r)
			}
		}
		for _, g := range d.Groups {
			if _, ok := c.defs[g]; !ok {
				return fmt.Errorf("%w: group %q names child group %q", ErrUnknownMember, name, g)
			}
		}
	}
	return nil
}

func summary(d Def) Group {
	return Group{Name: d.Name, Title: d.Title, Description: d.Description, Meta: d.Meta}
}

// topLevel returns the groups no other group lists as a child, in the order
// they were added. A group that is only reachable through a cycle has no
// top-level entry, so a client never sees it.
func (c *Catalog) topLevel() []Group {
	c.mu.RLock()
	defer c.mu.RUnlock()
	child := map[string]bool{}
	for _, d := range c.defs {
		for _, g := range d.Groups {
			child[g] = true
		}
	}
	out := make([]Group, 0, len(c.order))
	for _, name := range c.order {
		if !child[name] {
			out = append(out, summary(c.defs[name]))
		}
	}
	return out
}

func (c *Catalog) handleList(ctx core.MethodContext, id, params json.RawMessage) *core.Response {
	var req ListGroupsRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
	}
	page, next, err := pageOf(c.topLevel(), req.Cursor, c.pageSize)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}
	return core.NewResponse(id, ListGroupsResult{Groups: page, NextCursor: next, TTLMs: c.ttlMs, CacheScope: core.CacheScopePublic})
}

func (c *Catalog) handleGet(ctx core.MethodContext, id, params json.RawMessage) *core.Response {
	var req GetGroupRequest
	if err := json.Unmarshal(params, &req); err != nil || req.Name == "" {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "groups/get: name is required")
	}
	c.mu.RLock()
	d, ok := c.defs[req.Name]
	children := make([]Group, 0, len(d.Groups))
	for _, g := range d.Groups {
		children = append(children, summary(c.defs[g]))
	}
	srv := c.srv
	c.mu.RUnlock()
	if !ok {
		return core.NewErrorResponse(id, ErrCodeGroupNotFound, "groups/get: unknown group "+strconv.Quote(req.Name))
	}

	// Members are read from the registry on every call, so a group never
	// returns a primitive the server no longer has. That keeps the draft's
	// availability guarantee if a tool is removed after RegisterWith.
	reg := srv.Registry()
	fileInputs := core.HasFileInputs(ctx)
	res := GetGroupResult{Instructions: d.Instructions, Groups: children, TTLMs: c.ttlMs, CacheScope: core.CacheScopePrivate}
	for _, name := range d.Tools {
		if t, ok := reg.ToolDef(name); ok {
			if !fileInputs {
				t.InputSchema = core.StripFileInputKeywords(t.InputSchema)
			}
			res.Tools = append(res.Tools, t)
		}
	}
	for _, name := range d.Prompts {
		if p, ok := reg.PromptDef(name); ok {
			res.Prompts = append(res.Prompts, p)
		}
	}
	for _, uri := range d.Resources {
		if r, ok := reg.ResourceDef(uri); ok {
			res.Resources = append(res.Resources, r)
		}
	}
	return core.NewResponse(id, res)
}

// scope hides any primitive that some group lists, but only from callers that
// declared the extension. Everyone else gets the flat lists, which is the
// draft's default for clients without groups.
type scope struct{ c *Catalog }

func (s scope) hidden(ctx context.Context, key string) bool {
	if !core.ClientSupportsExtension(ctx, ExtensionID) {
		return false
	}
	s.c.mu.RLock()
	defer s.c.mu.RUnlock()
	return s.c.scoped[key]
}

func (s scope) HideTool(ctx context.Context, name string) bool {
	return s.hidden(ctx, "tool:"+name)
}

func (s scope) HidePrompt(ctx context.Context, name string) bool {
	return s.hidden(ctx, "prompt:"+name)
}

func (s scope) HideResource(ctx context.Context, uri string) bool {
	return s.hidden(ctx, "resource:"+uri)
}

// pageOf is an offset cursor, the same encoding the base list methods use.
func pageOf(items []Group, cursor string, size int) ([]Group, string, error) {
	off := 0
	if cursor != "" {
		b, err := base64.StdEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		if off, err = strconv.Atoi(string(b)); err != nil || off < 0 {
			return nil, "", fmt.Errorf("invalid cursor")
		}
	}
	if off >= len(items) {
		return []Group{}, "", nil
	}
	if size <= 0 {
		return items[off:], "", nil
	}
	end := min(off+size, len(items))
	next := ""
	if end < len(items) {
		next = base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return items[off:end], next, nil
}
