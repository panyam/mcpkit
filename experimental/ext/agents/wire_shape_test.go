package agents_test

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The result objects below are copied verbatim from agents-wg#22 at head
// 9b16840e (§Listing Agents and §Retrieving Agent Details). Each test checks
// both directions: the SEP example decodes into our types, and our types
// re-encode to the same JSON, so a renamed or dropped field fails either way.

const sepListResult = `{
  "resultType": "complete",
  "agents": [
    {
      "name": "workflow-agent",
      "description": "Investigates delivery pipelines and approvals",
      "capabilities": ["pipeline failures", "deployment readiness", "approval status"]
    },
    {
      "name": "research-agent",
      "description": "Researches vulnerabilities and technical evidence",
      "capabilities": ["vulnerability research", "evidence collection"]
    }
  ],
  "nextCursor": "next-page-cursor",
  "ttlMs": 60000,
  "cacheScope": "private"
}`

const sepGetResult = `{
  "resultType": "complete",
  "agent": "workflow-agent",
  "instructions": "Handle delivery readiness and approval workflows.",
  "modelRoutingHint": {"role": "medium", "vision": false},
  "tools": [
    {
      "name": "list_failed_pipelines",
      "description": "List failed pipelines for a service.",
      "inputSchema": {
        "type": "object",
        "properties": {"service": {"type": "string"}},
        "required": ["service"]
      }
    }
  ],
  "ttlMs": 60000,
  "cacheScope": "private"
}`

func TestListWireShapeMatchesSEP(t *testing.T) {
	var res agents.ListAgentsResult
	require.NoError(t, json.Unmarshal([]byte(sepListResult), &res))

	require.Len(t, res.Agents, 2)
	assert.Equal(t, "workflow-agent", res.Agents[0].Name)
	assert.Equal(t, "next-page-cursor", res.NextCursor)
	require.NotNil(t, res.TTLMs)
	assert.Equal(t, 60000, *res.TTLMs)

	got, err := json.Marshal(res)
	require.NoError(t, err)
	assert.JSONEq(t, sepListResult, string(got))
}

func TestGetWireShapeMatchesSEP(t *testing.T) {
	var res agents.GetAgentResult
	require.NoError(t, json.Unmarshal([]byte(sepGetResult), &res))

	assert.Equal(t, "workflow-agent", res.Agent)
	require.NotNil(t, res.ModelRoutingHint)
	assert.Equal(t, agents.ModelRoleMedium, res.ModelRoutingHint.Role)
	require.Len(t, res.Tools, 1)
	assert.Equal(t, "list_failed_pipelines", res.Tools[0].Name)

	got, err := json.Marshal(res)
	require.NoError(t, err)
	assert.JSONEq(t, sepGetResult, string(got))
}

// A served agents/get must reproduce the SEP example byte-for-byte in value,
// not just decode it. This drives the real handler with the example's agent
// and tool registered.
func TestServedGetMatchesSEPExample(t *testing.T) {
	ttl := 60000
	tc, _ := newAgentsServerWith(t, agents.Config{TTLMs: &ttl}, sepWorkflowAgent())

	res, err := tc.Client.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "workflow-agent"})
	require.NoError(t, err)
	assert.JSONEq(t, sepGetResult, string(res.Raw))
}

func TestCardAlwaysCarriesCapabilities(t *testing.T) {
	got, err := json.Marshal(agents.AgentDef{Name: "bare"}.Card())
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"bare","capabilities":[]}`, string(got),
		"capabilities is required on AgentCard, so an agent without labels sends []")
}

func TestRoutingHintAlwaysCarriesVision(t *testing.T) {
	got, err := json.Marshal(agents.ModelRoutingHint{Role: agents.ModelRoleSmall})
	require.NoError(t, err)
	assert.JSONEq(t, `{"role":"small","vision":false}`, string(got))
}
