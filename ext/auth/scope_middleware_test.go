package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLookup implements auth.ToolDefLookup with a pre-populated map.
type fakeLookup struct {
	tools map[string]core.ToolDef
}

func (f fakeLookup) ToolDef(name string) (core.ToolDef, bool) {
	def, ok := f.tools[name]
	return def, ok
}

// withClaims is a helper that injects auth claims into the context the same
// way the server transport layer does.
func withClaims(ctx context.Context, scopes ...string) context.Context {
	return core.ContextWithSession(ctx, nil, nil, nil, nil, &core.Claims{Subject: "test", Scopes: scopes})
}

// TestToolScopeMiddleware_403WithWWWAuth verifies that a tools/call request
// for a tool requiring scopes the caller doesn't have short-circuits with a
// *core.AuthError carrying HTTP 403 + WWW-Authenticate: Bearer
// error="insufficient_scope". Per SEP-2643 (FineGrainedAuth UC2): the scope
// challenge is conveyed via WWW-Authenticate, not via remediationHints.
func TestToolScopeMiddleware_403WithWWWAuth(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"update_doc": {Name: "update_doc", RequiredScopes: []string{"docs:write"}},
	}}

	mw := auth.NewToolScopeMiddleware(lookup)
	called := false
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		called = true
		return core.NewResponse(req.ID, "should not be reached"), nil
	})

	ctx := withClaims(context.Background(), "docs:read") // missing docs:write
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"update_doc"}`),
	}

	resp, err := mw(ctx, req, next)
	assert.False(t, called, "next must NOT be called when scope is missing")
	assert.Nil(t, resp, "no response when short-circuiting at transport layer")
	require.Error(t, err)

	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr), "error must be *core.AuthError, got %T", err)
	assert.Equal(t, http.StatusForbidden, authErr.Code)
	assert.Contains(t, authErr.WWWAuthenticate, `error="insufficient_scope"`)
	assert.Contains(t, authErr.WWWAuthenticate, `scope="docs:write"`)
}

// TestToolScopeMiddleware_PassThroughWhenSufficient verifies that requests
// with all required scopes pass through to next.
func TestToolScopeMiddleware_PassThroughWhenSufficient(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"update_doc": {Name: "update_doc", RequiredScopes: []string{"docs:write"}},
	}}

	mw := auth.NewToolScopeMiddleware(lookup)
	called := false
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		called = true
		return core.NewResponse(req.ID, "ok"), nil
	})

	ctx := withClaims(context.Background(), "docs:read", "docs:write")
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"update_doc"}`),
	}

	resp, err := mw(ctx, req, next)
	require.NoError(t, err)
	assert.True(t, called, "next must be called when all scopes are present")
	require.NotNil(t, resp)
}

// TestToolScopeMiddleware_NonToolsCallPassesThrough verifies that
// non-tools/call methods (e.g., resources/read, initialize) pass through
// without any scope check, even when the tool registry has scope requirements.
func TestToolScopeMiddleware_NonToolsCallPassesThrough(t *testing.T) {
	lookup := fakeLookup{}
	mw := auth.NewToolScopeMiddleware(lookup)
	called := false
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		called = true
		return core.NewResponse(req.ID, "ok"), nil
	})

	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "resources/read",
		Params: json.RawMessage(`{"uri":"test://x"}`),
	}

	_, err := mw(context.Background(), req, next)
	require.NoError(t, err)
	assert.True(t, called, "non-tools/call requests must pass through")
}

// TestToolScopeMiddleware_UnknownToolPassesThrough verifies that requests
// for tools not in the registry pass through (the dispatcher handles
// method-not-found, which is the right error for unknown tools — not 403).
func TestToolScopeMiddleware_UnknownToolPassesThrough(t *testing.T) {
	lookup := fakeLookup{} // empty registry
	mw := auth.NewToolScopeMiddleware(lookup)
	called := false
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		called = true
		return nil, nil
	})

	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"does_not_exist"}`),
	}

	_, err := mw(withClaims(context.Background()), req, next)
	require.NoError(t, err)
	assert.True(t, called, "unknown tool must pass through to dispatcher for method-not-found")
}

// TestToolScopeMiddleware_NoRequiredScopesPassesThrough verifies that tools
// without RequiredScopes set bypass the scope check entirely, even when no
// claims are present.
func TestToolScopeMiddleware_NoRequiredScopesPassesThrough(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"public_tool": {Name: "public_tool"}, // no RequiredScopes
	}}

	mw := auth.NewToolScopeMiddleware(lookup)
	called := false
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		called = true
		return core.NewResponse(req.ID, "ok"), nil
	})

	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"public_tool"}`),
	}

	_, err := mw(context.Background(), req, next)
	require.NoError(t, err)
	assert.True(t, called, "tool without RequiredScopes must pass through")
}

// TestToolScopeMiddleware_AllRequiredScopesChecked verifies that when a tool
// requires multiple scopes, ALL must be present (not just any one of them),
// and the WWW-Authenticate header lists all required scopes for client guidance.
func TestToolScopeMiddleware_AllRequiredScopesChecked(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"sensitive": {Name: "sensitive", RequiredScopes: []string{"docs:write", "admin"}},
	}}

	mw := auth.NewToolScopeMiddleware(lookup)
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		return nil, nil
	})

	// Token has docs:write but not admin → must be denied.
	ctx := withClaims(context.Background(), "docs:write")
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"sensitive"}`),
	}

	_, err := mw(ctx, req, next)
	require.Error(t, err)
	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr))
	// WWW-Authenticate must include both required scopes so the client can
	// request them all in a single re-auth.
	assert.True(t,
		strings.Contains(authErr.WWWAuthenticate, "docs:write") &&
			strings.Contains(authErr.WWWAuthenticate, "admin"),
		"WWW-Authenticate must list all required scopes; got %q", authErr.WWWAuthenticate)
}

// TestToolScopeMiddleware_AcceptedScopesHierarchyPasses verifies that when
// AcceptedScopes is set, the gate passes if the caller holds ANY scope in
// AcceptedScopes — even when RequiredScopes would otherwise demand a more
// specific scope. Use case: a `repo` scope satisfies a tool that nominally
// requires `repo:read`.
func TestToolScopeMiddleware_AcceptedScopesHierarchyPasses(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"read_repo": {
			Name:           "read_repo",
			RequiredScopes: []string{"repo:read"},
			AcceptedScopes: []string{"repo:read", "repo"},
		},
	}}

	mw := auth.NewToolScopeMiddleware(lookup)
	called := false
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		called = true
		return core.NewResponse(req.ID, "ok"), nil
	})

	// Token has the hierarchy parent `repo`, not the specific `repo:read`.
	ctx := withClaims(context.Background(), "repo")
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"read_repo"}`),
	}

	_, err := mw(ctx, req, next)
	require.NoError(t, err)
	assert.True(t, called, "hierarchy parent `repo` must satisfy AcceptedScopes gate")
}

// TestToolScopeMiddleware_AcceptedScopesEmptyFallsBackToAND verifies that
// AcceptedScopes is two-state: only non-empty turns on the OR semantics. An
// explicit empty slice (vs nil) behaves identically to nil — the gate falls
// back to the AND-on-RequiredScopes default. This prevents a footgun where
// allocating `[]string{}` would silently disable the scope check.
func TestToolScopeMiddleware_AcceptedScopesEmptyFallsBackToAND(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"update_doc": {
			Name:           "update_doc",
			RequiredScopes: []string{"docs:write"},
			AcceptedScopes: []string{},
		},
	}}

	mw := auth.NewToolScopeMiddleware(lookup)
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		return core.NewResponse(req.ID, "ok"), nil
	})

	// Token missing docs:write — empty AcceptedScopes must NOT bypass the check.
	ctx := withClaims(context.Background(), "docs:read")
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"update_doc"}`),
	}

	_, err := mw(ctx, req, next)
	require.Error(t, err, "empty AcceptedScopes must fall back to AND, not bypass the gate")
	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr))
	assert.Equal(t, http.StatusForbidden, authErr.Code)
}

// TestToolScopeMiddleware_AcceptedScopesDeniedAdvertisesRequiredOnly captures
// the gate-only contract: AcceptedScopes participates in the satisfaction
// check but NEVER appears in the WWW-Authenticate challenge. Re-auth guidance
// stays least-privilege — the client is told to request RequiredScopes, not
// the broader tolerated set.
func TestToolScopeMiddleware_AcceptedScopesDeniedAdvertisesRequiredOnly(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"read_repo": {
			Name:           "read_repo",
			RequiredScopes: []string{"repo:read"},
			AcceptedScopes: []string{"repo:read", "repo", "admin"},
		},
	}}

	mw := auth.NewToolScopeMiddleware(lookup)
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		return nil, nil
	})

	// Token has neither required nor any accepted scope.
	ctx := withClaims(context.Background(), "unrelated")
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"read_repo"}`),
	}

	_, err := mw(ctx, req, next)
	require.Error(t, err)
	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr))
	assert.Contains(t, authErr.WWWAuthenticate, `scope="repo:read"`,
		"challenge must advertise RequiredScopes only")
	assert.NotContains(t, authErr.WWWAuthenticate, "admin",
		"accepted-but-tolerated scopes must NOT leak into the challenge (gate-only contract)")
	assert.NotContains(t, authErr.WWWAuthenticate, `scope="repo:read repo`,
		"accepted hierarchy parents must NOT leak into the challenge")
}

// TestToolScopeMiddleware_IncludeGrantedScopesOffByDefault verifies that the
// existing 403 challenge shape is unchanged when the new option is not passed.
// Together with the other unmodified tests above, this is the back-compat gate
// for callers who haven't opted in.
func TestToolScopeMiddleware_IncludeGrantedScopesOffByDefault(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"update_doc": {Name: "update_doc", RequiredScopes: []string{"docs:write"}},
	}}

	mw := auth.NewToolScopeMiddleware(lookup) // no options
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		return nil, nil
	})

	ctx := withClaims(context.Background(), "docs:read") // partial token
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"update_doc"}`),
	}

	_, err := mw(ctx, req, next)
	require.Error(t, err)
	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr))
	assert.Contains(t, authErr.WWWAuthenticate, `scope="docs:write"`,
		"default behavior: challenge advertises RequiredScopes only")
	assert.NotContains(t, authErr.WWWAuthenticate, "docs:read",
		"granted scopes must NOT appear in the challenge without opt-in")
}

// TestToolScopeMiddleware_IncludeGrantedScopesUnionsInChallenge verifies the
// opt-in path: when WithIncludeGrantedScopes(true) is set, the 403 challenge
// advertises the union of (caller's already-held scopes ∪ tool's required
// scopes). This defends against non-mcpkit clients that overwrite granted
// scopes on every challenge (mirrors typescript-sdk PR 1657's workaround).
func TestToolScopeMiddleware_IncludeGrantedScopesUnionsInChallenge(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"update_doc": {Name: "update_doc", RequiredScopes: []string{"docs:write"}},
	}}

	mw := auth.NewToolScopeMiddleware(lookup, auth.WithIncludeGrantedScopes(true))
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		return nil, nil
	})

	ctx := withClaims(context.Background(), "docs:read")
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"update_doc"}`),
	}

	_, err := mw(ctx, req, next)
	require.Error(t, err)
	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr))
	assert.Contains(t, authErr.WWWAuthenticate, "docs:read",
		"opt-in: granted scope must appear in the challenge")
	assert.Contains(t, authErr.WWWAuthenticate, "docs:write",
		"opt-in: required scope still appears in the challenge")
}

// TestToolScopeMiddleware_IncludeGrantedScopesEmptyGrantedSameAsOff verifies
// the degenerate path: when WithIncludeGrantedScopes(true) but the caller
// holds no scopes, the union collapses to RequiredScopes alone — identical
// to the default-off behavior. Useful because the opt-in is set per-server,
// not per-request; servers with the flag on still see unauthenticated-ish
// calls (valid token, zero scopes).
func TestToolScopeMiddleware_IncludeGrantedScopesEmptyGrantedSameAsOff(t *testing.T) {
	lookup := fakeLookup{tools: map[string]core.ToolDef{
		"update_doc": {Name: "update_doc", RequiredScopes: []string{"docs:write"}},
	}}

	mw := auth.NewToolScopeMiddleware(lookup, auth.WithIncludeGrantedScopes(true))
	next := server.MiddlewareFunc(func(ctx context.Context, req *core.Request) (*core.Response, error) {
		return nil, nil
	})

	ctx := withClaims(context.Background()) // no scopes
	req := &core.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: json.RawMessage(`{"name":"update_doc"}`),
	}

	_, err := mw(ctx, req, next)
	require.Error(t, err)
	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr))
	assert.Contains(t, authErr.WWWAuthenticate, `scope="docs:write"`,
		"empty granted: challenge advertises RequiredScopes only (degenerate union)")
}
