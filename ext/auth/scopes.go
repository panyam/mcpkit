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
//	func adminTool(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
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

// UnionScopes returns the set union of a and b preserving first-seen order:
// every element of a comes first in its original order, then every element
// of b that did not already appear in a. Duplicates within either input are
// collapsed. Returns nil when both inputs are empty so callers can treat the
// "no scopes anywhere" case uniformly. The returned slice is always a fresh
// allocation — callers may safely retain and mutate it without affecting the
// inputs.
//
// Primarily used by NewToolScopeMiddleware to compose granted-and-required
// scope sets for the WWW-Authenticate challenge under WithIncludeGrantedScopes.
// Custom middleware that builds its own scope challenges can use it directly.
func UnionScopes(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
