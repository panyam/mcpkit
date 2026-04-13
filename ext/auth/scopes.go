package auth

import (
	"context"
	"net/http"

	"github.com/panyam/mcpkit/core"
)

// RequireScope checks if the context's authenticated claims include the given scope.
// Returns nil if the scope is present, or an *core.AuthError with HTTP 403 and
// a WWW-Authenticate header with error="insufficient_scope" if missing.
//
// Per MCP spec (2025-11-25): servers SHOULD respond with 403 and WWW-Authenticate
// when a client has a valid token but insufficient scopes.
//
// Usage in a tool handler:
//
//	func adminTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
//	    if err := auth.RequireScope(ctx, "admin:write"); err != nil {
//	        return core.ErrorResult(err.Error()), nil
//	    }
//	    // ... admin operation ...
//	}
func RequireScope(ctx context.Context, scope string) error {
	if core.HasScope(ctx, scope) {
		return nil
	}
	return &core.AuthError{
		Code:            http.StatusForbidden,
		Message:         "insufficient scope: " + scope,
		WWWAuthenticate: WWWAuth403(scope),
	}
}

// RequireAllScopes checks that all given scopes are present in the context's claims.
func RequireAllScopes(ctx context.Context, scopes ...string) error {
	for _, scope := range scopes {
		if err := RequireScope(ctx, scope); err != nil {
			return err
		}
	}
	return nil
}
