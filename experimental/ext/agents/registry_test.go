package agents_test

import (
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workflowAgent is the agents-wg#20 §6 example, carried in one place so the
// roster, detail, and wire-shape tests all assert against the same fixture.
func workflowAgent() agents.AgentDef {
	return agents.AgentDef{
		AgentID:      "workflow-agent",
		Description:  "Pipelines, approval gates, run history, connectors",
		Capabilities: []string{"Pipeline catalog and execution", "Approval gates", "Run analysis"},
		ExampleTasks: []string{"Show pending pipeline approvals", "Why did pipeline X fail?"},
		DelegateTool: "invoke_workflow_agent",
		TasksEnabled: true,
		SkillURI:     "skill://workflow-agent/SKILL.md",
		Instructions: "You operate CI/CD pipelines. Prefer read-only analysis before any mutation.",
		Tools: []core.ToolDef{
			{Name: "list_pipelines", Description: "List pipelines", InputSchema: map[string]any{"type": "object"}},
			{Name: "approve_gate", Description: "Approve a pending gate", InputSchema: map[string]any{"type": "object"}},
		},
	}
}

// newAgentsServer stands up a server with the agents extension registered and
// returns a connected TestClient plus the Registry.
func newAgentsServer(t *testing.T, defs ...agents.AgentDef) (*testutil.TestClient, *agents.Registry) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "agents-test", Version: "0.0.1"})
	reg, err := agents.Register(agents.Config{Server: srv, Agents: defs})
	require.NoError(t, err)
	return testutil.NewTestClient(t, srv), reg
}

// TestExtensionAdvertised confirms the server advertises the agents extension
// in its initialize capabilities so a host can gate discovery on it.
func TestExtensionAdvertised(t *testing.T) {
	tc, _ := newAgentsServer(t, workflowAgent())
	assert.True(t, tc.ServerSupportsExtension(agents.ExtensionID),
		"server must advertise %q in capabilities.extensions", agents.ExtensionID)

	cap, ok := tc.ServerExtensionCapability(agents.ExtensionID)
	require.True(t, ok)
	assert.Equal(t, agents.SpecVersion, cap.SpecVersion)
	assert.Equal(t, string(core.Experimental), cap.Stability)
}

// TestListReturnsRosterWithoutSchemas is the load-bearing progressive-
// disclosure assertion: agents/list returns routing summaries and carries NO
// instructions and NO tool schemas. If this regresses, a supervisor host would
// eager-load every specialist's tools at connect — the exact bloat the
// primitive exists to avoid.
func TestListReturnsRosterWithoutSchemas(t *testing.T) {
	tc, _ := newAgentsServer(t, workflowAgent())

	res, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	var out agents.ListResult
	require.NoError(t, res.Unmarshal(&out))

	require.Len(t, out.Agents, 1)
	got := out.Agents[0]
	assert.Equal(t, "workflow-agent", got.AgentID)
	assert.Equal(t, "invoke_workflow_agent", got.DelegateTool)
	assert.True(t, got.TasksEnabled)
	assert.Equal(t, "skill://workflow-agent/SKILL.md", got.SkillURI)
	assert.Equal(t, []string{"Pipeline catalog and execution", "Approval gates", "Run analysis"}, got.Capabilities)

	// The roster wire type has no field for instructions or tools at all; the
	// raw payload must not carry them either.
	assert.NotContains(t, string(res.Raw), "instructions")
	assert.NotContains(t, string(res.Raw), "list_pipelines")
}

// TestGetReturnsDetailWithTools confirms agents/get resolves the level-3
// payload: the summary fields PLUS instructions and the scoped tool schemas.
func TestGetReturnsDetailWithTools(t *testing.T) {
	tc, _ := newAgentsServer(t, workflowAgent())

	res, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetParams{AgentID: "workflow-agent"})
	require.NoError(t, err)
	var out agents.GetResult
	require.NoError(t, res.Unmarshal(&out))

	got := out.Agent
	assert.Equal(t, "workflow-agent", got.AgentID)             // summary field flattened in
	assert.Equal(t, "invoke_workflow_agent", got.DelegateTool) // summary field flattened in
	assert.Contains(t, got.Instructions, "read-only analysis") // detail
	require.Len(t, got.Tools, 2)                               // detail
	assert.Equal(t, "list_pipelines", got.Tools[0].Name)
	assert.Equal(t, "approve_gate", got.Tools[1].Name)
}

// TestGetUnknownAgentIsError verifies an unknown agentId maps to InvalidParams
// rather than an empty success — a host that typos an id gets a clear failure.
func TestGetUnknownAgentIsError(t *testing.T) {
	tc, _ := newAgentsServer(t, workflowAgent())

	_, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetParams{AgentID: "no-such-agent"})
	require.Error(t, err)
	rpcErr, ok := err.(*client.RPCError)
	require.True(t, ok, "want a JSON-RPC error, got %T", err)
	assert.Equal(t, core.ErrCodeInvalidParams, rpcErr.Code)
}

// TestGetEmptyAgentIsError verifies a missing agentId is rejected as
// InvalidParams.
func TestGetEmptyAgentIsError(t *testing.T) {
	tc, _ := newAgentsServer(t, workflowAgent())

	_, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetParams{})
	require.Error(t, err)
	rpcErr, ok := err.(*client.RPCError)
	require.True(t, ok, "want a JSON-RPC error, got %T", err)
	assert.Equal(t, core.ErrCodeInvalidParams, rpcErr.Code)
}

// TestRegisterDuplicateReportsError verifies a duplicate AgentID in the initial
// roster is reported (first wins) rather than silently overwriting.
func TestRegisterDuplicateReportsError(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "agents-test", Version: "0.0.1"})
	first := workflowAgent()
	dup := workflowAgent()
	dup.Description = "SHOULD NOT WIN"
	reg, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{first, dup}})
	require.Error(t, err, "duplicate agentId must be reported")
	require.NotNil(t, reg)

	tc := testutil.NewTestClient(t, srv)
	res, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	var out agents.ListResult
	require.NoError(t, res.Unmarshal(&out))
	require.Len(t, out.Agents, 1)
	assert.Equal(t, "Pipelines, approval gates, run history, connectors", out.Agents[0].Description,
		"first registration must win the duplicate")
}

// TestListOrderIsStable verifies agents/list preserves insertion order across
// the initial roster and a runtime AddAgent, so the wire roster is
// deterministic rather than map-random.
func TestListOrderIsStable(t *testing.T) {
	a := agents.AgentDef{AgentID: "alpha"}
	b := agents.AgentDef{AgentID: "bravo"}
	c := agents.AgentDef{AgentID: "charlie"}
	tc, reg := newAgentsServer(t, a, b)
	require.NoError(t, reg.AddAgent(c))

	res, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	var out agents.ListResult
	require.NoError(t, res.Unmarshal(&out))

	ids := []string{out.Agents[0].AgentID, out.Agents[1].AgentID, out.Agents[2].AgentID}
	assert.Equal(t, []string{"alpha", "bravo", "charlie"}, ids)
}

// TestAddRemoveAgent exercises the runtime roster mutators: AddAgent rejects a
// duplicate, RemoveAgent drops a known id and no-ops an unknown one.
func TestAddRemoveAgent(t *testing.T) {
	tc, reg := newAgentsServer(t, workflowAgent())

	require.Error(t, reg.AddAgent(workflowAgent()), "AddAgent must reject a duplicate")

	require.NoError(t, reg.AddAgent(agents.AgentDef{AgentID: "second"}))
	reg.RemoveAgent("workflow-agent")
	reg.RemoveAgent("nonexistent") // no-op, must not panic

	res, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	var out agents.ListResult
	require.NoError(t, res.Unmarshal(&out))
	require.Len(t, out.Agents, 1)
	assert.Equal(t, "second", out.Agents[0].AgentID)
}
