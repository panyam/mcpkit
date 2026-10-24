package rooms

import (
	"context"
	"slices"
	"time"

	"github.com/panyam/mcpkit/core"
)

// WithDynamicCatalogTTL serves the dynamic page-tool registry (no
// WithPageTools) to callers with no MCP session too. Their tools/list shows
// the page tools of every page that agreed to them, in the rooms the bridge
// has joined, with ttlMs set to ttl and cacheScope private, since the list
// differs per caller. The stateless wire has no list_changed, so ttl is how
// long a client may go without seeing a tool a page just registered: keep it
// to a few seconds, and expect every stateless client to re-list that often.
// The session wire is unchanged. ttl <= 0 leaves it off, which is the default:
// a caller with no session sees no page tools and is refused calling one.
func WithDynamicCatalogTTL(ttl time.Duration) Option {
	return func(b *Bridge) { b.dynamicTTL = ttl }
}

// WithScopeFilteredList makes tools/list show each caller only the bridge's
// tools its token's scopes let it call, on both wires, with cacheScope
// private. Off by default, since MCP's step-up flow expects a client to see a
// tool, call it, and get the insufficient_scope challenge naming the scope to
// ask for. With it on, list_pages carries stepUp, [{tool, scopes}], for each
// tool the list hid, so a client still learns what it could step up to.
// Calls are gated the same either way.
func WithScopeFilteredList() Option {
	return func(b *Bridge) { b.scopeFilter = true }
}

// StepUp is one tool the scope-filtered list hid from a caller, and the scopes
// its insufficient_scope challenge would ask for.
type StepUp struct {
	Tool   string   `json:"tool"`
	Scopes []string `json:"scopes"`
}

// servesDynamicStateless reports whether a call with session id sid gets the
// dynamic registry through WithDynamicCatalogTTL.
func (b *Bridge) servesDynamicStateless(sid string) bool {
	return b.declared == nil && b.dynamicTTL > 0 && sid == ""
}

// pageToolsFilterLocked is which page tools a caller may see, or nil for all
// of them, which is a declared catalog. In the dynamic registry it is the
// tools of pages that agreed to p, plus those of the session's default rooms;
// with no session it is nothing unless WithDynamicCatalogTTL is on.
func (b *Bridge) pageToolsFilterLocked(p Principal, s *session, stateless bool) map[string]bool {
	switch {
	case b.declared != nil:
		return nil
	case s != nil:
		return b.visibleToolsLocked(s.principal, s.attached)
	case stateless && b.dynamicTTL > 0:
		return b.visibleToolsLocked(p, nil)
	default:
		return map[string]bool{}
	}
}

func (b *Bridge) principalOfCtx(ctx context.Context) Principal {
	subject, client := b.subjectOfClaims(core.AuthClaims(ctx))
	return Principal{Subject: subject, Client: client}
}

func (b *Bridge) isPageTool(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.defs[name]
	return ok
}

// missingScopes is the challenge a call to the bridge's tool name would get
// from ctx's token, or nil when it would pass or the tool is not the bridge's.
func (b *Bridge) missingScopes(ctx context.Context, name string) *core.ScopeChallenge {
	if b.srv == nil || !b.isBridgeTool(name) {
		return nil
	}
	def, ok := b.srv.Registry().ToolDef(name)
	if !ok || def.ScopeChallenge == nil {
		return nil
	}
	challenge, err := def.ScopeChallenge(ctx, &core.Request{Method: "tools/call"})
	if err != nil && challenge == nil {
		return &core.ScopeChallenge{}
	}
	return challenge
}

// stepUpsFor is what list_pages reports with WithScopeFilteredList: every
// bridge tool the caller could see but lacks the scopes to call, by name.
func (b *Bridge) stepUpsFor(ctx context.Context, p Principal, s *session) []StepUp {
	b.mu.Lock()
	visible := b.pageToolsFilterLocked(p, s, s == nil)
	names := []string{"attach", "detach", "list_pages", "pair"}
	for _, name := range sortedKeys(b.defs) {
		if visible == nil || visible[name] {
			names = append(names, name)
		}
	}
	b.mu.Unlock()
	if b.fb != nil {
		names = append(names, "next_request")
	}
	slices.Sort(names)
	out := []StepUp{}
	for _, name := range names {
		if c := b.missingScopes(ctx, name); c != nil {
			out = append(out, StepUp{Tool: name, Scopes: slices.Clone(c.Scopes)})
		}
	}
	return out
}
