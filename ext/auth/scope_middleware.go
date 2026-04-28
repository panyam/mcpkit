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

// NewToolScopeMiddleware returns a server middleware that enforces per-tool
// OAuth scopes (declared via core.ToolDef.RequiredScopes) for tools/call
// requests. It runs pre-dispatch and short-circuits with *core.AuthError
// (HTTP 403 + WWW-Authenticate: insufficient_scope) when the request's
// claims don't include all required scopes.
//
// Per SEP-2643 (FineGrainedAuth UC2): scope step-up is fully described by
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
//	    server.WithMiddleware(auth.NewToolScopeMiddleware(srv.Registry())),
//	)
//	srv.RegisterTool(core.ToolDef{
//	    Name: "update_doc",
//	    RequiredScopes: []string{"docs:write"},
//	}, handler)
func NewToolScopeMiddleware(lookup ToolDefLookup) server.Middleware {
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

		// Verify all required scopes are present.
		for _, scope := range def.RequiredScopes {
			if !core.HasScope(ctx, scope) {
				return nil, &core.AuthError{
					Code:            http.StatusForbidden,
					Message:         "insufficient scope: " + scope,
					WWWAuthenticate: WWWAuth403(def.RequiredScopes...),
				}
			}
		}

		return next(ctx, req)
	}
}
