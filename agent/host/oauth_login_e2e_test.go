package host

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
	extauth "github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	oneauthclient "github.com/panyam/oneauth/client"
	"github.com/panyam/oneauth/testutil"
)

// authGatedServer stands up a oneauth test authorization server (which
// auto-approves the PKCE authorization-code flow) plus an mcpkit MCP server that
// validates the AS's JWTs and advertises PRM + a 401 challenge. Together they
// let the agentchat oauth auth type discover the AS and complete a real login
// against it — no browser, no Docker. Returns the MCP server URL (base, no /mcp).
func authGatedServer(t *testing.T) string {
	t.Helper()

	// Delegating handler: the auth middleware needs the server URL, which is
	// only known after httptest allocates it.
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handler == nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	as := testutil.NewTestAuthServer(t,
		testutil.WithAuthorizeEnabled(true),
		testutil.WithAuthorizeAutoApproveSubject("kitchen-user"),
		testutil.WithScopes([]string{"mcp:basic"}),
		testutil.WithAudience(ts.URL),
	)

	validator := extauth.NewJWTValidator(extauth.JWTConfig{
		JWKSURL:             as.JWKSURL(),
		Issuer:              as.Issuer(),
		Audience:            ts.URL,
		ResourceMetadataURL: ts.URL + "/.well-known/oauth-protected-resource/mcp",
		AllScopes:           []string{"mcp:basic"},
	})
	validator.Start()
	t.Cleanup(validator.Stop)

	srv := server.NewServer(
		core.ServerInfo{Name: "oauth-login-e2e", Version: "0.1.0"},
		server.WithAuth(validator),
		server.WithExtension(extauth.AuthExtension{}),
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "whoami",
			Description: "returns the authenticated subject (no scope required)",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			sub := ""
			if c := core.AuthClaims(ctx); c != nil {
				sub = c.Subject
			}
			return core.TextResult("hello " + sub), nil
		},
	)

	mux := http.NewServeMux()
	mcpHandler := srv.Handler(server.WithStreamableHTTP(true))
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)
	extauth.MountAuth(mux, extauth.AuthConfig{
		ResourceURI:          ts.URL,
		AuthorizationServers: []string{as.URL()},
		ScopesSupported:      []string{"mcp:basic"},
		MCPPath:              "/mcp",
	})
	handler = mux
	return ts.URL
}

func waitServerState(t *testing.T, app *App, id, want string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last string
	for time.Now().Before(deadline) {
		res, err := app.Dispatch(context.Background(), "/servers")
		if err == nil {
			for _, s := range res.Servers {
				if s.ID == id {
					last = s.State.String()
					if last == want {
						return
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server %q did not reach %q within %s (last=%q)", id, want, within, last)
}

// TestOAuthLoginFlowE2E is the end-to-end acceptance for issue 1123: an
// agentchat server configured with the interactive oauth auth type, driven
// through the real PKCE authorization-code flow against oneauth's test AS. The
// first login attempt is dismissed (opener errors), so the server parks at
// needs-login; App.LoginServer (the /mcp overlay's login action) forces a fresh
// flow that completes, the server reaches ready, and its tool becomes callable
// with the acquired token.
func TestOAuthLoginFlowE2E(t *testing.T) {
	mcpURL := authGatedServer(t)

	var mu sync.Mutex
	attempts := 0
	// The browser opener: the first call (initial connect) fails as if the user
	// dismissed the login; later calls complete the flow by following the AS's
	// redirects (the test AS auto-approves), as a headless browser stand-in.
	opener := func(authURL string) error {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n == 1 {
			return errors.New("simulated: user dismissed the login")
		}
		return oneauthclient.FollowRedirects(nil)(authURL)
	}

	// Pin a public client id (the test AS accepts an arbitrary public client for
	// the PKCE flow) rather than DCR, which the test AS does not serve.
	t.Setenv("OAUTH_E2E_CLIENT", "test-cli")
	cfg := testConfig(mcpURL) // one server at mcpURL+"/mcp", id "test"
	cfg.Servers[0].Auth = &AuthConfig{Type: "oauth", ClientIDEnv: "OAUTH_E2E_CLIENT", AllowInsecure: true}

	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""),
		WithProvider(agent.NewStubProvider()),
		WithBrowserOpener(opener))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	id := cfg.Servers[0].ID

	// initial connect: the dismissed login leaves the server at needs-login
	waitServerState(t, app, id, "needs-login", 15*time.Second)

	// the /mcp login action: force a fresh flow, which now completes
	app.LoginServer(id)
	waitServerState(t, app, id, "ready", 15*time.Second)

	// the authed connection is real: the server's tool lists with the token
	defs, found, err := app.ServerTools(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("ServerTools after login: found=%v err=%v", found, err)
	}
	if len(defs) != 1 || defs[0].Name != "whoami" {
		t.Fatalf("expected the whoami tool after login, got %+v", defs)
	}

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got < 2 {
		t.Fatalf("login did not re-run the browser flow (opener attempts=%d, want >=2)", got)
	}
}
