package agents_test

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// declareAgents is what agentsclient.WithExtension returns. The tests here
// cannot import that module, since it depends on this one.
var declareAgents = client.WithExtension(agents.ExtensionID, core.ClientExtensionCap{})

func newRegisteredServer(t *testing.T, defs ...agents.AgentDef) *server.Server {
	t.Helper()
	srv := newCatalogServer()
	_, err := agents.Register(agents.Config{Server: srv, Agents: defs})
	require.NoError(t, err)
	return srv
}

func assertMissingAgentsCapability(t *testing.T, err error, label string) {
	t.Helper()
	require.Error(t, err, label)
	rpcErr, ok := err.(*client.RPCError)
	require.True(t, ok, "%s: want a JSON-RPC error, got %T: %v", label, err, err)
	assert.Equal(t, core.ErrCodeMissingRequiredClientCapability, rpcErr.Code, label)
	raw, mErr := json.Marshal(rpcErr.Data)
	require.NoError(t, mErr)
	var data core.MissingRequiredClientCapabilityData
	require.NoError(t, json.Unmarshal(raw, &data), "%s: -32021 must carry requiredCapabilities", label)
	assert.Contains(t, data.RequiredCapabilities.Extensions, agents.ExtensionID, label)
}

// Step-up must not reveal anything to a client that never declared the
// extension, so the declaration check answers first.
func TestUndeclaredClientGetsMissingCapabilityBeforeStepUp(t *testing.T) {
	forEachWire(t, func(t *testing.T, statelessWire bool) {
		reader := dialAs(t, newStepUpServer(t, statelessWire, nil), "reader", statelessWire)
		assertMissingAgentsCapability(t, getAgent(reader, "deploy-agent"), "agents/get deploy-agent")
	})
}

// An empty roster is valid (Luca, Agents WG Discord 2026-10-16): the server
// still declares the extension and lists no agents.
func TestEmptyRosterDeclaresAndListsNothing(t *testing.T) {
	tc := testutil.NewTestClient(t, newRegisteredServer(t), declareAgents)
	assert.True(t, tc.Client.ServerSupportsExtension(agents.ExtensionID))

	res, err := tc.Client.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, res.Unmarshal(&raw))
	assert.JSONEq(t, `[]`, string(raw["agents"]))
	assert.NotContains(t, raw, "nextCursor")
}
