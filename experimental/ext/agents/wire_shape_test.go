package agents_test

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListWireShapeMatchesResearchDoc pins the agents/list roster JSON to the
// exact shape the agents-wg#20 research doc §6 proposes. This is the interop
// anchor: if mcpkit's field names or nesting drift from the WG payload, this
// fails, and any drift is a deliberate decision recorded in the diff rather
// than an accident. Compared as decoded values (not byte-for-byte) so key
// order and whitespace do not make it brittle.
func TestListWireShapeMatchesResearchDoc(t *testing.T) {
	res := agents.ListResult{Agents: []agents.AgentSummary{workflowAgent().Summary()}}

	got, err := json.Marshal(res)
	require.NoError(t, err)

	const want = `{
	  "agents": [{
	    "agentId": "workflow-agent",
	    "description": "Pipelines, approval gates, run history, connectors",
	    "capabilities": ["Pipeline catalog and execution", "Approval gates", "Run analysis"],
	    "exampleTasks": ["Show pending pipeline approvals", "Why did pipeline X fail?"],
	    "delegateTool": "invoke_workflow_agent",
	    "tasksEnabled": true,
	    "skillUri": "skill://workflow-agent/SKILL.md"
	  }]
	}`

	assert.JSONEq(t, want, string(got))
}

// TestGetWireShapeIsSummarySuperset verifies the agents/get detail object
// flattens the summary fields (embedded, not nested) and adds instructions +
// tools alongside them — a get response is a strict superset of a list entry.
func TestGetWireShapeIsSummarySuperset(t *testing.T) {
	res := agents.GetResult{Agent: workflowAgent().Detail()}
	got, err := json.Marshal(res)
	require.NoError(t, err)

	var envelope struct {
		Agent map[string]any `json:"agent"`
	}
	require.NoError(t, json.Unmarshal(got, &envelope))

	// Summary fields sit at the top level of the agent object, not under a
	// nested "agentSummary" key.
	assert.Equal(t, "workflow-agent", envelope.Agent["agentId"])
	assert.Equal(t, "invoke_workflow_agent", envelope.Agent["delegateTool"])
	assert.NotContains(t, envelope.Agent, "AgentSummary")
	assert.NotContains(t, envelope.Agent, "agentSummary")

	// Detail fields present.
	assert.Contains(t, envelope.Agent, "instructions")
	tools, ok := envelope.Agent["tools"].([]any)
	require.True(t, ok)
	assert.Len(t, tools, 2)
}
