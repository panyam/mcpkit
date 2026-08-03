package agentsclient_test

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workflowAgent() agents.AgentDef {
	return agents.AgentDef{
		AgentID:      "workflow-agent",
		Description:  "Pipelines, approval gates, run history, connectors",
		Capabilities: []string{"Pipeline catalog and execution", "Approval gates"},
		ExampleTasks: []string{"Show pending pipeline approvals"},
		DelegateTool: "invoke_workflow_agent",
		TasksEnabled: true,
		SkillURI:     "skill://workflow-agent/SKILL.md",
		Instructions: "You operate CI/CD pipelines.",
		Tools: []core.ToolDef{
			{Name: "list_pipelines", Description: "List pipelines", InputSchema: map[string]any{"type": "object"}},
		},
	}
}

func newClient(t *testing.T, defs ...agents.AgentDef) *agentsclient.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "agents-test", Version: "0.0.1"})
	_, err := agents.Register(agents.Config{Server: srv, Agents: defs})
	require.NoError(t, err)
	tc := testutil.NewTestClient(t, srv)
	return agentsclient.New(tc.Client)
}

// TestSupportsAgents confirms the client detects the advertised extension off
// the cached handshake.
func TestSupportsAgents(t *testing.T) {
	ac := newClient(t, workflowAgent())
	assert.True(t, ac.SupportsAgents())
}

// TestSupportsAgentsFalseWhenAbsent confirms a server that did not register the
// extension is reported unsupported, so a host skips discovery.
func TestSupportsAgentsFalseWhenAbsent(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "plain", Version: "0.0.1"})
	tc := testutil.NewTestClient(t, srv)
	ac := agentsclient.New(tc.Client)
	assert.False(t, ac.SupportsAgents())
}

// TestListAgentsRoundTrip drives agents/list through a real server and decodes
// the roster — the level-2 read.
func TestListAgentsRoundTrip(t *testing.T) {
	ac := newClient(t, workflowAgent())

	roster, err := ac.ListAgents(context.Background())
	require.NoError(t, err)
	require.Len(t, roster, 1)
	assert.Equal(t, "workflow-agent", roster[0].AgentID)
	assert.Equal(t, "invoke_workflow_agent", roster[0].DelegateTool)
	assert.True(t, roster[0].TasksEnabled)
}

// TestListAgentsEmptyIsNoError confirms the tolerant decode: a server with an
// empty roster returns an empty slice, not an error.
func TestListAgentsEmptyIsNoError(t *testing.T) {
	ac := newClient(t) // no agents registered

	roster, err := ac.ListAgents(context.Background())
	require.NoError(t, err)
	assert.Empty(t, roster)
}

// TestGetAgentRoundTrip drives agents/get and decodes the level-3 detail:
// instructions + scoped tools alongside the flattened summary fields.
func TestGetAgentRoundTrip(t *testing.T) {
	ac := newClient(t, workflowAgent())

	detail, err := ac.GetAgent(context.Background(), "workflow-agent")
	require.NoError(t, err)
	assert.Equal(t, "workflow-agent", detail.AgentID)
	assert.Equal(t, "invoke_workflow_agent", detail.DelegateTool)
	assert.Equal(t, "You operate CI/CD pipelines.", detail.Instructions)
	require.Len(t, detail.Tools, 1)
	assert.Equal(t, "list_pipelines", detail.Tools[0].Name)
}

// TestGetAgentUnknownPropagatesError confirms an unknown agentId surfaces the
// server's JSON-RPC error rather than a zero-value success.
func TestGetAgentUnknownPropagatesError(t *testing.T) {
	ac := newClient(t, workflowAgent())

	_, err := ac.GetAgent(context.Background(), "no-such-agent")
	require.Error(t, err)
}
