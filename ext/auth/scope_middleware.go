package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// ToolDefLookup is the minimal interface NewToolScopeMiddleware needs from
// the server's tool registry. *server.Registry satisfies this.
type ToolDefLookup interface {
	ToolDef(name string) (core.ToolDef, bool)
}

// ToolScopeOption configures NewToolScopeMiddleware. Use the With* functions
// in this package; the underlying type is opaque so future fields can be
// added without breaking callers that pass zero options.
type ToolScopeOption func(*toolScopeConfig)

type toolScopeConfig struct {
	includeGrantedScopes bool
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
// defends against that by re-stating the granted set every time. The upstream
// TypeScript SDK PR modelcontextprotocol/typescript-sdk#1657 exists to fix
// the same bug client-side; this option is the server-side counterpart for
// deployments that can't wait for every client to upgrade.
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
		if err := json.Unmarshal(req.Params, &params); err != nil {
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
				WWWAuthenticate: WWWAuth403(challengeScopes...),
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
