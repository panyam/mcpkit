package auth

import (
	"context"
	"net/http"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// ToolDefLookup is the minimal interface NewToolScopeMiddleware needs from
// the server's tool registry. *server.Registry satisfies this.
//
// Deprecated: use ScopeLookup with NewScopeMiddleware, which gates resources
// and prompts as well as tools.
type ToolDefLookup interface {
	ToolDef(name string) (core.ToolDef, bool)
}

// ScopeLookup is what NewScopeMiddleware needs from the server's registry.
// *server.Registry satisfies it.
type ScopeLookup interface {
	ToolDef(name string) (core.ToolDef, bool)
	ResourceDef(uri string) (core.ResourceDef, bool)
	ResourceTemplateDefFor(uri string) (core.ResourceTemplate, bool)
	PromptDef(name string) (core.PromptDef, bool)
}

// ToolScopeOption configures NewToolScopeMiddleware. Use the With* functions
// in this package; the underlying type is opaque so future fields can be
// added without breaking callers that pass zero options.
type ToolScopeOption func(*toolScopeConfig)

type toolScopeConfig struct {
	includeGrantedScopes bool
	resourceMetadataURL  string
}

// WithIncludeGrantedScopes opts the middleware into advertising the union of
// (caller's currently-granted scopes ∪ tool's required scopes) in the 403
// WWW-Authenticate scope parameter. Default is false (per-operation, matching
// SEP-2350 semantics).
//
// When to opt in: facing non-mcpkit clients that may overwrite their scope
// set on every challenge instead of accumulating it. The classic broken
// behavior is "client sees insufficient_scope=docs:write, re-requests a token
// for ONLY docs:write, loses the docs:read it already had." Union-on-challenge
// defends against that by re-stating the granted set every time. this option is
// the server-side counterpart for deployments that can't wait for every client
// to upgrade.
//
// When to leave off: mcpkit's own clients accumulate scopes correctly (the
// OAuthTokenSource.TokenForScopes contract enforces it), so mcpkit-on-mcpkit
// deployments gain nothing from the union. Leaving it off also keeps the
// challenge minimal, which suits least-privilege re-auth.
//
// Interaction with AcceptedScopes: AcceptedScopes (gate-only) NEVER appears
// in the challenge regardless of this option. The union is over granted
// scopes and required scopes; tolerated alternates stay private to the server.
func WithIncludeGrantedScopes(v bool) ToolScopeOption {
	return func(c *toolScopeConfig) { c.includeGrantedScopes = v }
}

// WithResourceMetadataURL plumbs the server's RFC 9728 Protected Resource
// Metadata URL into the 403 challenge the middleware emits. When set, the
// WWW-Authenticate header carries `resource_metadata="<URL>"` alongside
// `error="insufficient_scope"` and `scope="..."`, so clients hitting a
// first-call 403 on the stateless wire (no preceding 401) can discover the
// authorization server without falling back to the well-known path.
//
// Empty (default) omits the segment — the 401 path's PRM advertisement is
// the only discovery hint. This is correct when every client passes through
// a 401 before any 403, but breaks down on SEP-2575 stateless wire where the
// first tools/call can be a 403 with no prior context.
//
// Typically the same URL passed to JWTValidator.ResourceMetadataURL — the two
// configs live on separate components by design (JWTValidator emits 401s,
// NewToolScopeMiddleware emits 403s) but referencing the same PRM document.
func WithResourceMetadataURL(url string) ToolScopeOption {
	return func(c *toolScopeConfig) { c.resourceMetadataURL = url }
}

// NewToolScopeMiddleware returns a server middleware that enforces per-tool
// OAuth scopes (declared via core.ToolDef.RequiredScopes and optionally
// core.ToolDef.AcceptedScopes) for tools/call requests. It runs pre-dispatch
// and short-circuits with *core.AuthError (HTTP 403 + WWW-Authenticate:
// insufficient_scope) when the request's claims don't satisfy the gate.
//
// Gate semantics:
//   - RequiredScopes alone: AND — the caller must hold every listed scope.
//   - AcceptedScopes non-empty: OR — the caller satisfies the gate by holding
//     ANY scope in AcceptedScopes. Supports scope hierarchies (a parent `repo`
//     scope satisfies a tool nominally requiring `repo:read`). AcceptedScopes
//     is gate-only — it never appears in the 403 challenge, keeping re-auth
//     guidance least-privilege.
//   - AcceptedScopes nil or empty: falls back to the AND semantics above. The
//     two-state is deliberate so allocating []string{} cannot silently bypass
//     enforcement.
//
// Per SEP-2643 (FineGrainedAuth UC2): the scope step-up is fully described by
// the WWW-Authenticate challenge. The body is a JSON-RPC error with the
// authorization-denial classification metadata only — no remediationHints,
// because the scopes are already in the WWW-Authenticate header.
//
// Non-tools/call requests pass through unchanged. Unknown tools also pass
// through (the dispatcher returns method-not-found for those).
//
// Example:
//
//	srv := server.NewServer(info,
//	    server.WithAuth(jwtValidator),
//	    server.WithMiddleware(auth.NewToolScopeMiddleware(srv.Registry(),
//	        auth.WithIncludeGrantedScopes(true), // optional
//	    )),
//	)
//	srv.RegisterTool(core.ToolDef{
//	    Name:           "update_doc",
//	    RequiredScopes: []string{"docs:write"},
//	    AcceptedScopes: []string{"docs:write", "docs"}, // optional OR hierarchy
//	}, handler)
func NewToolScopeMiddleware(lookup ToolDefLookup, opts ...ToolScopeOption) server.Middleware {
	cfg := toolScopeConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		var params struct {
			Name string `json:"name"`
		}
		if err := req.Params.Bind(&params); err != nil {
			// Malformed params — let the dispatcher handle the parse error.
			return next(ctx, req)
		}

		def, ok := lookup.ToolDef(params.Name)
		if !ok {
			return next(ctx, req) // unknown tool → dispatcher returns method-not-found
		}

		if len(def.RequiredScopes) == 0 {
			return next(ctx, req) // no per-tool scope check
		}

		if !scopeGateSatisfied(ctx, def) {
			challengeScopes := def.RequiredScopes
			if cfg.includeGrantedScopes {
				challengeScopes = UnionScopes(core.GetScopes(ctx), def.RequiredScopes)
			}
			return nil, &core.AuthError{
				Code:            http.StatusForbidden,
				Message:         "insufficient scope",
				WWWAuthenticate: WWWAuth403(cfg.resourceMetadataURL, challengeScopes...),
			}
		}

		return next(ctx, req)
	}
}

// scopeGateSatisfied returns true when the caller's claims satisfy def's
// scope requirements. When AcceptedScopes is non-empty the gate is OR over
// AcceptedScopes; otherwise it is AND over RequiredScopes.
func scopeGateSatisfied(ctx context.Context, def core.ToolDef) bool {
	if len(def.AcceptedScopes) > 0 {
		for _, scope := range def.AcceptedScopes {
			if core.HasScope(ctx, scope) {
				return true
			}
		}
		return false
	}
	for _, scope := range def.RequiredScopes {
		if !core.HasScope(ctx, scope) {
			return false
		}
	}
	return true
}

// NewScopeMiddleware gates every scope-carrying primitive, not just tools:
// tools/call, resources/read (exact URIs and templates), and prompts/get.
// Anything else, and any primitive with no scope declared, passes straight
// through, so adding this to an existing server changes nothing until a
// definition opts in.
//
// It runs before dispatch, which is what lets a refusal become a real HTTP 403
// with a WWW-Authenticate header rather than a JSON-RPC error inside an
// already-committed 200 response. SDKs that resolve scope inside the handler
// cannot do this once streaming has started and the headers are flushed.
//
//	srv := server.New("app", "1.0",
//	    server.WithAuth(jwtValidator),
//	    server.WithMiddleware(auth.NewScopeMiddleware(srv.Registry(),
//	        auth.WithResourceMetadataURL(prmURL),
//	    )),
//	)
//	srv.RegisterTool(core.ToolDef{
//	    Name:           "update_doc",
//	    ScopeChallenge: core.RequireScopes("docs:write"),
//	}, handler)
func NewScopeMiddleware(lookup ScopeLookup, opts ...ToolScopeOption) server.Middleware {
	cfg := toolScopeConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
		challenge, ok := resolveChallenge(lookup, req)
		if !ok {
			return next(ctx, req)
		}

		result, err := challenge(ctx, req)
		if err != nil {
			// Fail closed. The callback could not decide, so the caller does
			// not get the benefit of the doubt.
			return nil, &core.AuthError{
				Code:            http.StatusForbidden,
				Message:         "insufficient scope",
				WWWAuthenticate: WWWAuth403(cfg.resourceMetadataURL),
			}
		}
		if result == nil {
			return next(ctx, req)
		}

		advertised := result.Scopes
		if cfg.includeGrantedScopes && len(advertised) > 0 {
			advertised = UnionScopes(core.GetScopes(ctx), advertised)
		}
		return nil, &core.AuthError{
			Code:            http.StatusForbidden,
			Message:         "insufficient scope",
			WWWAuthenticate: WWWAuth403Desc(cfg.resourceMetadataURL, result.ErrorDescription, advertised...),
		}
	}
}

// resolveChallenge finds the challenge function guarding req, mirroring the
// dispatcher's own resolution so the gate and the handler always agree on
// which definition is in play. Returns false when nothing guards the request.
func resolveChallenge(lookup ScopeLookup, req *core.Request) (core.ScopeChallengeFunc, bool) {
	switch req.Method {
	case "tools/call":
		var p struct {
			Name string `json:"name"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return nil, false // malformed params; let the dispatcher report it
		}
		def, ok := lookup.ToolDef(p.Name)
		if !ok {
			return nil, false // unknown tool; dispatcher returns method-not-found
		}
		fn := toolChallenge(def)
		return fn, fn != nil

	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return nil, false
		}
		// Exact resources win over templates, matching the dispatcher.
		if def, ok := lookup.ResourceDef(p.URI); ok {
			return def.ScopeChallenge, def.ScopeChallenge != nil
		}
		if def, ok := lookup.ResourceTemplateDefFor(p.URI); ok {
			return def.ScopeChallenge, def.ScopeChallenge != nil
		}
		return nil, false

	case "prompts/get":
		var p struct {
			Name string `json:"name"`
		}
		if err := req.Params.Bind(&p); err != nil {
			return nil, false
		}
		def, ok := lookup.PromptDef(p.Name)
		if !ok {
			return nil, false
		}
		return def.ScopeChallenge, def.ScopeChallenge != nil

	default:
		return nil, false
	}
}

// toolChallenge honors ToolDef.ScopeChallenge when set, and otherwise adapts
// the deprecated RequiredScopes/AcceptedScopes pair so existing servers keep
// their behavior unchanged. Returns nil when the tool declares no gate.
func toolChallenge(def core.ToolDef) core.ScopeChallengeFunc {
	if def.ScopeChallenge != nil {
		return def.ScopeChallenge
	}
	if len(def.RequiredScopes) == 0 {
		return nil
	}
	required := def.RequiredScopes
	accepted := def.AcceptedScopes
	return func(ctx context.Context, _ *core.Request) (*core.ScopeChallenge, error) {
		// Legacy semantics preserved exactly: AcceptedScopes, when present,
		// replaces the AND-over-RequiredScopes gate with an OR over itself,
		// while the challenge still advertises only RequiredScopes.
		if len(accepted) > 0 {
			for _, s := range accepted {
				if core.HasScope(ctx, s) {
					return nil, nil
				}
			}
			return &core.ScopeChallenge{Scopes: required}, nil
		}
		for _, s := range required {
			if !core.HasScope(ctx, s) {
				return &core.ScopeChallenge{Scopes: required}, nil
			}
		}
		return nil, nil
	}
}
