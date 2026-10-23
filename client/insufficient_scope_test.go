package client_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixedToken string

func (f fixedToken) Token() (string, error) { return string(f), nil }

// scopeGatedServer refuses tools/call with core.InsufficientScope from plain
// server middleware, the way an extension that cannot import ext/auth does.
func scopeGatedServer(t *testing.T) *httptest.Server {
	t.Helper()
	gate := func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
		if req.Method == "tools/call" {
			return nil, core.InsufficientScope("docs:write")
		}
		return next(ctx, req)
	}
	srv := server.NewServer(core.ServerInfo{Name: "gated", Version: "1"}, server.WithMiddleware(gate))
	srv.RegisterTool(core.ToolDef{Name: "edit"}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("edited"), nil
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	t.Cleanup(ts.Close)
	return ts
}

// A static token cannot step up, so the 403 comes back to the caller. The
// challenged scopes are on the *client.ClientAuthError, which is how a caller
// learns what to ask its authorization server for.
func TestInsufficientScope_ReachesTheCallerWithItsScopes(t *testing.T) {
	for _, mode := range []client.ClientMode{client.ClientModeLegacyOnly, client.ClientModeStateless} {
		t.Run(mode.String(), func(t *testing.T) {
			ts := scopeGatedServer(t)
			c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "c", Version: "1"},
				client.WithTokenSource(fixedToken("t")), client.WithClientMode(mode))
			require.NoError(t, c.Connect(t.Context()))

			_, err := c.ToolCall(t.Context(), "edit", nil)
			var ae *client.ClientAuthError
			require.True(t, errors.As(err, &ae), "got %v", err)
			assert.Equal(t, 403, ae.StatusCode)
			assert.Equal(t, []string{"docs:write"}, ae.RequiredScopes)
			assert.Equal(t, `Bearer error="insufficient_scope", scope="docs:write"`, ae.WWWAuthenticate)
		})
	}
}

func TestScopeChallengeAuthError(t *testing.T) {
	e := (&core.ScopeChallenge{Scopes: []string{"a", "b"}, ErrorDescription: `needs "a"`}).AuthError("https://rs/.well-known/oauth-protected-resource")
	assert.Equal(t, 403, e.Code)
	assert.Equal(t, `Bearer error="insufficient_scope", resource_metadata="https://rs/.well-known/oauth-protected-resource", scope="a b", error_description="needs \"a\""`, e.WWWAuthenticate)

	var nilChallenge *core.ScopeChallenge
	assert.Equal(t, `Bearer error="insufficient_scope"`, nilChallenge.AuthError("").WWWAuthenticate, "a nil challenge fails closed with no remedy")
}
