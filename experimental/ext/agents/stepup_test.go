package agents_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenScopes is an AuthValidator that treats each bearer token as a caller
// holding a fixed scope set.
type tokenScopes map[string][]string

func (v tokenScopes) Validate(r *http.Request) error {
	if _, ok := v[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]; !ok {
		return &core.AuthError{Code: http.StatusUnauthorized, Message: "unauthorized"}
	}
	return nil
}

func (v tokenScopes) Claims(r *http.Request) *core.Claims {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return &core.Claims{Subject: tok, Scopes: v[tok]}
}

type staticToken string

func (s staticToken) Token() (string, error) { return string(s), nil }

var stepUpTokens = tokenScopes{
	"reader":   {"ops:read"},
	"deployer": {"ops:read", "ops:deploy"},
	"guest":    {},
}

// deployAgent is visible to reader and deployer but opens only for a caller
// holding ops:deploy.
func deployAgent() agents.AgentDef {
	d := opsAgent()
	d.Name = "deploy-agent"
	d.ScopeChallenge = core.RequireScopes("ops:deploy")
	return d
}

// newStepUpServer serves sepWorkflowAgent (no challenge) and deployAgent on
// the given wire, hiding deploy-agent from guest, and returns its URL.
func newStepUpServer(t *testing.T, statelessWire bool, mutate func(*agents.Config)) string {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "agents-stepup", Version: "0.0.1"}, server.WithAuth(stepUpTokens))
	for _, td := range catalog {
		srv.RegisterTool(td, noopTool)
	}
	cfg := agents.Config{
		Server:              srv,
		Agents:              []agents.AgentDef{sepWorkflowAgent(), deployAgent()},
		ResourceMetadataURL: "https://mcp.example.com/.well-known/oauth-protected-resource/mcp",
		Visible: func(ctx core.MethodContext, name string) bool {
			c := core.AuthClaims(ctx)
			return name != "deploy-agent" || (c != nil && c.Subject != "guest")
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	_, err := agents.Register(cfg)
	require.NoError(t, err)
	opts := []server.TransportOption{server.WithStreamableHTTP(true)}
	if statelessWire {
		opts = append(opts, server.WithStatelessMode(stateless.ModeStateless))
	}
	ts := httptest.NewServer(srv.Handler(opts...))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func connectAs(t *testing.T, url, token string, statelessWire bool) *client.Client {
	t.Helper()
	opts := []client.ClientOption{client.WithTokenSource(staticToken(token))}
	if statelessWire {
		opts = append(opts, client.WithClientMode(client.ClientModeStateless))
	}
	c := client.NewClient(url, core.ClientInfo{Name: "test", Version: "0.0.1"}, opts...)
	require.NoError(t, c.Connect(context.Background()))
	t.Cleanup(func() { c.Close() })
	return c
}

func forEachWire(t *testing.T, fn func(t *testing.T, statelessWire bool)) {
	t.Run("session", func(t *testing.T) { fn(t, false) })
	t.Run("stateless", func(t *testing.T) { fn(t, true) })
}

func getAgent(c *client.Client, name string) error {
	_, err := c.Call(context.Background(), agents.MethodGet, agents.GetAgentParams{Name: name})
	return err
}

func authError(t *testing.T, err error) *client.ClientAuthError {
	t.Helper()
	var ae *client.ClientAuthError
	require.True(t, errors.As(err, &ae), "want an HTTP auth refusal, got %T: %v", err, err)
	return ae
}

// A caller who may see an agent but lacks its scope still finds it in
// agents/list, and agents/get answers with a step-up challenge naming the
// scope. A caller holding the scope gets the detail.
func TestStepUpChallengesVisibleAgent(t *testing.T) {
	forEachWire(t, func(t *testing.T, statelessWire bool) {
		url := newStepUpServer(t, statelessWire, nil)

		reader := connectAs(t, url, "reader", statelessWire)
		roster := listAll(t, testCall(t, reader))
		assert.Contains(t, cardNames(roster.Agents), "deploy-agent", "a step-up agent stays listed")

		ae := authError(t, getAgent(reader, "deploy-agent"))
		assert.Equal(t, http.StatusForbidden, ae.StatusCode)
		assert.Equal(t, []string{"ops:deploy"}, ae.RequiredScopes)
		assert.Contains(t, ae.WWWAuthenticate, `error="insufficient_scope"`)
		assert.Contains(t, ae.WWWAuthenticate, `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`)

		require.NoError(t, getAgent(reader, "workflow-agent"), "an agent without a challenge is unaffected")
		require.NoError(t, getAgent(connectAs(t, url, "deployer", statelessWire), "deploy-agent"))
	})
}

// A hidden agent must not leak through a challenge: the caller gets the same
// agent_not_found as for a name that does not exist.
func TestStepUpNeverRevealsHiddenAgent(t *testing.T) {
	forEachWire(t, func(t *testing.T, statelessWire bool) {
		url := newStepUpServer(t, statelessWire, nil)
		guest := connectAs(t, url, "guest", statelessWire)

		hidden := rpcError(t, getAgent(guest, "deploy-agent"))
		missing := rpcError(t, getAgent(guest, "no-such-agent"))
		assert.Equal(t, agents.ReasonAgentNotFound, errorReason(t, hidden))
		assert.Equal(t, missing.Code, hidden.Code)
		assert.Equal(t, missing.Message, hidden.Message)
	})
}

// A challenge callback that errors refuses the request rather than letting it
// through, and the refusal names no scope.
func TestStepUpFailsClosed(t *testing.T) {
	url := newStepUpServer(t, false, func(cfg *agents.Config) {
		cfg.Agents[1].ScopeChallenge = func(context.Context, *core.Request) (*core.ScopeChallenge, error) {
			return nil, errors.New("policy store unreachable")
		}
	})
	ae := authError(t, getAgent(connectAs(t, url, "deployer", false), "deploy-agent"))
	assert.Equal(t, http.StatusForbidden, ae.StatusCode)
	assert.Empty(t, ae.RequiredScopes)
}

func cardNames(cards []agents.AgentCard) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Name
	}
	return out
}

// The gate covers agents/get alone. A tool that shares an agent's name is
// called under its own policy, not the agent's challenge.
func TestStepUpGatesOnlyAgentsGet(t *testing.T) {
	url := newStepUpServer(t, false, func(cfg *agents.Config) {
		cfg.Agents[1].Name = "approve_gate"
		cfg.Visible = nil
	})
	reader := connectAs(t, url, "reader", false)
	_, err := reader.Call(context.Background(), "tools/call", map[string]any{"name": "approve_gate", "arguments": map[string]any{}})
	require.NoError(t, err)
	authError(t, getAgent(reader, "approve_gate"))
}
