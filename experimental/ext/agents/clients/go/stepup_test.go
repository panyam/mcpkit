package agentsclient_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenScopes maps each bearer token to the scopes it grants. Every token
// belongs to the same user, as a stepped-up token does, since the session
// wire binds a session to its principal.
type tokenScopes map[string][]string

func (v tokenScopes) Validate(r *http.Request) error {
	if _, ok := v[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]; !ok {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "unauthorized"}
	}
	return nil
}

func (v tokenScopes) Claims(r *http.Request) *core.Claims {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return &core.Claims{Subject: "alice", Scopes: v[tok]}
}

// swapToken is a token source a test can upgrade mid-run, standing in for a
// host that obtained a broader token.
type swapToken struct {
	mu  sync.Mutex
	tok string
}

func (s *swapToken) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok, nil
}

func (s *swapToken) set(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tok = tok
}

// A step-up refusal is reported with its scopes, is not cached, and the same
// cache serves the agent once the token carries the scope.
func TestStepUpScopesAndCacheRetry(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "agents-test", Version: "0.0.1"},
		server.WithAuth(tokenScopes{"reader": {"ops:read"}, "deployer": {"ops:read", "ops:deploy"}}))
	srv.RegisterTool(core.ToolDef{Name: "list_failed_pipelines", InputSchema: map[string]any{"type": "object"}}, noopTool)
	deploy := workflowAgent()
	deploy.ScopeChallenge = core.RequireScopes("ops:deploy")
	_, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{deploy}, TTLMs: ptr(60_000)})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	tok := &swapToken{tok: "reader"}
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, client.WithTokenSource(tok))
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })
	cache := agentsclient.NewCache(agentsclient.New(c))

	_, err = cache.GetAgent(context.Background(), deploy.Name)
	scopes, ok := agentsclient.StepUpScopes(err)
	require.True(t, ok, "want a step-up refusal, got %v", err)
	assert.Equal(t, []string{"ops:deploy"}, scopes)
	assert.False(t, agentsclient.IsAgentNotFound(err))

	tok.set("deployer")
	got, err := cache.GetAgent(context.Background(), deploy.Name)
	require.NoError(t, err, "the refusal must not have been cached")
	assert.Equal(t, deploy.Name, got.Agent)
}

func TestStepUpScopesIgnoresOtherErrors(t *testing.T) {
	_, ok := agentsclient.StepUpScopes(&client.RPCError{Code: core.ErrCodeInvalidParams})
	assert.False(t, ok)
	_, ok = agentsclient.StepUpScopes(&client.ClientAuthError{StatusCode: http.StatusUnauthorized})
	assert.False(t, ok, "a 401 is re-authentication, not step-up")
	_, ok = agentsclient.StepUpScopes(nil)
	assert.False(t, ok)
}
