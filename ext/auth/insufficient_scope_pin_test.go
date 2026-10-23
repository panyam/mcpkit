package auth

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	apiauth "github.com/panyam/oneauth/apiauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every insufficient_scope 403 ext/auth builds, byte for byte. These pin the
// responses across the move onto core.ScopeChallenge.AuthError (#1589), which
// must not change a header a client parses.

type pinLookup struct {
	tools map[string]mcpcore.ToolDef
}

func (l pinLookup) ToolDef(name string) (mcpcore.ToolDef, bool) {
	d, ok := l.tools[name]
	return d, ok
}
func (pinLookup) ResourceDef(string) (mcpcore.ResourceDef, bool) { return mcpcore.ResourceDef{}, false }
func (pinLookup) ResourceTemplateDefFor(string) (mcpcore.ResourceTemplate, bool) {
	return mcpcore.ResourceTemplate{}, false
}
func (pinLookup) PromptDef(string) (mcpcore.PromptDef, bool) { return mcpcore.PromptDef{}, false }

func pinClaims(scopes ...string) context.Context {
	return mcpcore.ContextWithSession(context.Background(), nil, nil, nil, nil, &mcpcore.Claims{Subject: "t", Scopes: scopes})
}

func pinCall(t *testing.T, mw server.Middleware, ctx context.Context, tool string) *mcpcore.AuthError {
	t.Helper()
	req := &mcpcore.Request{
		ID:     json.RawMessage(`1`),
		Method: "tools/call",
		Params: mcpcore.NewRawJSON(json.RawMessage(`{"name":"` + tool + `"}`)),
	}
	_, err := mw(ctx, req, func(context.Context, *mcpcore.Request) (*mcpcore.Response, error) {
		t.Fatal("next must not run")
		return nil, nil
	})
	var ae *mcpcore.AuthError
	require.True(t, errors.As(err, &ae), "want *core.AuthError, got %T: %v", err, err)
	return ae
}

func assertPinned(t *testing.T, ae *mcpcore.AuthError, message, header string) {
	t.Helper()
	assert.Equal(t, 403, ae.Code)
	assert.Equal(t, message, ae.Message)
	assert.Equal(t, header, ae.WWWAuthenticate)
}

func TestInsufficientScopePin_ToolScopeMiddleware(t *testing.T) {
	lookup := pinLookup{tools: map[string]mcpcore.ToolDef{
		"update": {Name: "update", RequiredScopes: []string{"docs:write", "docs:admin"}},
	}}
	mw := NewToolScopeMiddleware(lookup, WithResourceMetadataURL("https://rs/.well-known/prm"))
	assertPinned(t, pinCall(t, mw, pinClaims("docs:read"), "update"), "insufficient scope",
		`Bearer error="insufficient_scope", resource_metadata="https://rs/.well-known/prm", scope="docs:write docs:admin"`)
}

func TestInsufficientScopePin_ScopeMiddlewareChallenge(t *testing.T) {
	lookup := pinLookup{tools: map[string]mcpcore.ToolDef{
		"update": {Name: "update", ScopeChallenge: func(context.Context, *mcpcore.Request) (*mcpcore.ScopeChallenge, error) {
			return &mcpcore.ScopeChallenge{Scopes: []string{"docs:write"}, ErrorDescription: `needs "write"`}, nil
		}},
	}}
	mw := NewScopeMiddleware(lookup, WithResourceMetadataURL("https://rs/prm"))
	assertPinned(t, pinCall(t, mw, pinClaims(), "update"), "insufficient scope",
		`Bearer error="insufficient_scope", resource_metadata="https://rs/prm", scope="docs:write", error_description="needs \"write\""`)
}

func TestInsufficientScopePin_ScopeMiddlewareFailsClosed(t *testing.T) {
	lookup := pinLookup{tools: map[string]mcpcore.ToolDef{
		"update": {Name: "update", ScopeChallenge: func(context.Context, *mcpcore.Request) (*mcpcore.ScopeChallenge, error) {
			return nil, errors.New("can't decide")
		}},
	}}
	mw := NewScopeMiddleware(lookup, WithResourceMetadataURL("https://rs/prm"))
	assertPinned(t, pinCall(t, mw, pinClaims(), "update"), "insufficient scope",
		`Bearer error="insufficient_scope", resource_metadata="https://rs/prm"`)
}

func TestInsufficientScopePin_RequireScope(t *testing.T) {
	var ae *mcpcore.AuthError
	require.True(t, errors.As(RequireScope(pinClaims("docs:read"), "docs:write"), &ae))
	assertPinned(t, ae, "insufficient scope: docs:write", `Bearer error="insufficient_scope", scope="docs:write"`)
}

func TestInsufficientScopePin_IntrospectionValidator(t *testing.T) {
	_, asURL := newFakeAS(t, map[string]any{"active": true, "sub": "alice", "iss": "https://as", "scope": "read"})
	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL:    asURL,
		ClientID:            "c",
		ClientSecret:        "s",
		RequiredScopes:      []string{"read", "write"},
		ResourceMetadataURL: "https://rs/prm",
	})
	var ae *mcpcore.AuthError
	require.True(t, errors.As(v.Validate(newAuthedRequest("tok")), &ae))
	assertPinned(t, ae, "insufficient scope", `Bearer error="insufficient_scope", resource_metadata="https://rs/prm", scope="read write"`)
}

func TestInsufficientScopePin_JWTValidator(t *testing.T) {
	jwksURL, issuer, token, cleanup := setupTestJWKS(t, "alice", []string{"read"})
	defer cleanup()
	v := NewJWTValidator(JWTConfig{JWKSURL: jwksURL, Issuer: issuer, Audience: "https://mcp.test", ResourceMetadataURL: "https://rs/prm"})
	v.RequiredScopes = []string{"admin"}
	assertPinned(t, authErr(t, v.Validate(dpopRequest("Bearer", token, ""))), "insufficient scope",
		`Bearer error="insufficient_scope", resource_metadata="https://rs/prm", scope="admin"`)
}

func TestInsufficientScopePin_JWTValidatorDPoP(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	f.v.RequiredScopes = []string{"admin"}
	ae := authErr(t, f.v.Validate(dpopRequest("DPoP", f.token, f.proof(t, f.token))))
	assertPinned(t, ae, "insufficient scope", "DPoP "+trimBearer(mcpcore.InsufficientScopeChallenge(f.v.ResourceMetadataURL, "", "admin")))
}

func trimBearer(s string) string { return s[len("Bearer "):] }
