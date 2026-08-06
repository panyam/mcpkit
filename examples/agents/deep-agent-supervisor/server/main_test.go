package main

import (
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRosterClient boots the demo server (no tracing) and returns a connected
// test client.
func newRosterClient(t *testing.T) *testutil.TestClient {
	t.Helper()
	srv, _, err := buildServer(core.NoopTracerProvider{})
	require.NoError(t, err)
	return testutil.NewTestClient(t, srv)
}

// TestRosterListsThreeAgentsWithoutSchemas is the progressive-disclosure guard:
// agents/list advertises the three specialists as routing tuples and carries NO
// instructions and NO tool schemas. If this regresses the supervisor would
// eager-load every specialist's tools at connect — the bloat the primitive
// exists to avoid.
func TestRosterListsThreeAgentsWithoutSchemas(t *testing.T) {
	tc := newRosterClient(t)

	res, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	var out agents.ListResult
	require.NoError(t, res.Unmarshal(&out))

	ids := make([]string, len(out.Agents))
	for i, a := range out.Agents {
		ids[i] = a.AgentID
	}
	assert.Equal(t, []string{"research", "workflow", "insights"}, ids)

	assert.NotContains(t, string(res.Raw), "instructions")
	assert.NotContains(t, string(res.Raw), "web_search",
		"agents/list must not carry a specialist's tool schemas")
}

// TestGetResearchReturnsScopedTools proves agents/get resolves the lead
// specialist's instructions plus its scoped tool set — the tools the child
// Runner advertises, matching the handlers the server actually installs.
func TestGetResearchReturnsScopedTools(t *testing.T) {
	tc := newRosterClient(t)

	res, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetParams{AgentID: "research"})
	require.NoError(t, err)
	var out agents.GetResult
	require.NoError(t, res.Unmarshal(&out))

	assert.Equal(t, "research", out.Agent.AgentID)
	assert.NotEmpty(t, out.Agent.Instructions)

	names := make([]string, len(out.Agent.Tools))
	for i, tl := range out.Agent.Tools {
		names[i] = tl.Name
	}
	assert.ElementsMatch(t, []string{"web_search", "fetch_page", "summarize"}, names)
}

// TestScopedToolLoopsBackToServer proves a specialist's scoped tool name is a
// real server handler: the supervisor host loops a resolved agent's tool call
// back to this server via tools/call, so the names must dispatch.
func TestScopedToolLoopsBackToServer(t *testing.T) {
	tc := newRosterClient(t)

	out := tc.ToolCall("web_search", map[string]any{"query": "mcp subagents"})
	assert.Contains(t, out, "mcp subagents",
		"web_search should dispatch to the demo handler and echo the query")
}
