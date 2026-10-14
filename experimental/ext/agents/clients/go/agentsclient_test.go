package agentsclient_test

import (
	"context"
	"errors"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopTool(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
	return core.TextResult("ok"), nil
}

func workflowAgent() agents.AgentDef {
	return agents.AgentDef{
		Name:             "workflow-agent",
		Description:      "Investigates delivery pipelines and approvals",
		Capabilities:     []string{"pipeline failures", "approval status"},
		Instructions:     "Handle delivery readiness and approval workflows.",
		ModelRoutingHint: &agents.ModelRoutingHint{Role: agents.ModelRoleMedium},
		Tools:            []string{"list_failed_pipelines"},
	}
}

func newClientWith(t *testing.T, cfg agents.Config, defs ...agents.AgentDef) *agentsclient.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "agents-test", Version: "0.0.1"})
	srv.RegisterTool(core.ToolDef{Name: "list_failed_pipelines", Description: "List failed pipelines", InputSchema: map[string]any{"type": "object"}}, noopTool)
	cfg.Server = srv
	cfg.Agents = defs
	_, _ = agents.Register(cfg)
	tc := testutil.NewTestClient(t, srv)
	return agentsclient.New(tc.Client)
}

func newClient(t *testing.T, defs ...agents.AgentDef) *agentsclient.Client {
	t.Helper()
	return newClientWith(t, agents.Config{}, defs...)
}

func TestSupportsAgents(t *testing.T) {
	ac := newClient(t, workflowAgent())
	assert.True(t, ac.SupportsAgents())
}

// A server that did not register the extension is reported unsupported, so a
// host skips discovery.
func TestSupportsAgentsFalseWhenAbsent(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "plain", Version: "0.0.1"})
	tc := testutil.NewTestClient(t, srv)
	ac := agentsclient.New(tc.Client)
	assert.False(t, ac.SupportsAgents())
}

func TestListAgentsRoundTrip(t *testing.T) {
	ac := newClient(t, workflowAgent())

	roster, err := ac.ListAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, roster, 1)
	assert.Equal(t, agents.AgentCard{
		Name:         "workflow-agent",
		Description:  "Investigates delivery pipelines and approvals",
		Capabilities: []string{"pipeline failures", "approval status"},
	}, roster[0])
}

func TestListAgentsEmptyIsNoError(t *testing.T) {
	ac := newClient(t)

	roster, err := ac.ListAgents(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, roster)
	assert.Empty(t, roster)
}

func TestListAgentsWalksPages(t *testing.T) {
	ac := newClientWith(t, agents.Config{PageSize: 2},
		agents.AgentDef{Name: "alpha"}, agents.AgentDef{Name: "bravo"},
		agents.AgentDef{Name: "charlie"}, agents.AgentDef{Name: "delta"}, agents.AgentDef{Name: "echo"})

	first, err := ac.ListAgentsPage(context.Background(), "")
	require.NoError(t, err)
	assert.Len(t, first.Agents, 2)
	assert.NotEmpty(t, first.NextCursor)
	assert.Equal(t, core.CacheScopePrivate, first.CacheScope)

	roster, err := ac.ListAgents(context.Background())
	require.NoError(t, err)
	names := make([]string, len(roster))
	for i, a := range roster {
		names[i] = a.Name
	}
	assert.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, names)
}

func TestGetAgentRoundTrip(t *testing.T) {
	ac := newClient(t, workflowAgent())

	got, err := ac.GetAgent(context.Background(), "workflow-agent")
	require.NoError(t, err)
	assert.Equal(t, core.ResultTypeComplete, got.ResultType)
	assert.Equal(t, "workflow-agent", got.Agent)
	assert.Equal(t, "Handle delivery readiness and approval workflows.", got.Instructions)
	assert.Equal(t, &agents.ModelRoutingHint{Role: agents.ModelRoleMedium}, got.ModelRoutingHint)
	require.Len(t, got.Tools, 1)
	assert.Equal(t, "list_failed_pipelines", got.Tools[0].Name)
}

func TestGetAgentErrorsAreClassified(t *testing.T) {
	ac := newClient(t, workflowAgent(), agents.AgentDef{Name: "broken", Tools: []string{"no_such_tool"}})

	_, err := ac.GetAgent(context.Background(), "no-such-agent")
	require.Error(t, err)
	assert.True(t, agentsclient.IsAgentNotFound(err))
	assert.False(t, agentsclient.IsInvalidAgentDefinition(err))

	_, err = ac.GetAgent(context.Background(), "broken")
	require.Error(t, err)
	assert.True(t, agentsclient.IsInvalidAgentDefinition(err))
	assert.False(t, agentsclient.IsAgentNotFound(err))

	assert.False(t, agentsclient.IsAgentNotFound(errors.New("transport down")))
	assert.False(t, agentsclient.IsAgentNotFound(nil))
}
