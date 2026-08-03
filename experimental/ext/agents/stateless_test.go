package agents_test

import (
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newStatelessClient stands up the agents server on the SEP-2575 stateless
// wire (ModeStateless — no initialize handshake, no session) and connects a
// stateless-pinned client. The extension is captured via server/discover
// rather than initialize on this wire.
func newStatelessClient(t *testing.T, defs ...agents.AgentDef) *client.Client {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "agents-stateless", Version: "0.0.1"})
	_, err := agents.Register(agents.Config{Server: srv, Agents: defs})
	require.NoError(t, err)

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeStateless))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "0.0.1"},
		client.WithClientMode(client.ClientModeStateless))
	require.NoError(t, c.Connect())
	return c
}

// TestStatelessWireParity is the parity guard: the whole primitive — extension
// advertising, agents/list, and agents/get — must work on the SEP-2575
// stateless wire, not just the legacy initialize path. Custom methods flow
// through the same dispatcher.customHandlers map on both wires and the
// stateless caps builder advertises extensions, so this needs no
// stateless-specific code; the test exists to keep it that way (the recurring
// failure mode is a discovery feature that silently no-ops on stateless).
func TestStatelessWireParity(t *testing.T) {
	c := newStatelessClient(t, workflowAgent())

	// 1. Extension advertised — captured via server/discover on this wire.
	assert.True(t, c.ServerSupportsExtension(agents.ExtensionID),
		"stateless wire must advertise %q", agents.ExtensionID)

	// 2. agents/list dispatches and returns the roster without tool schemas.
	listRes, err := c.Call(agents.MethodList, nil)
	require.NoError(t, err)
	var list agents.ListResult
	require.NoError(t, listRes.Unmarshal(&list))
	require.Len(t, list.Agents, 1)
	assert.Equal(t, "workflow-agent", list.Agents[0].AgentID)
	assert.NotContains(t, string(listRes.Raw), "list_pipelines")

	// 3. agents/get dispatches and returns the level-3 detail.
	getRes, err := c.Call(agents.MethodGet, agents.GetParams{AgentID: "workflow-agent"})
	require.NoError(t, err)
	var get agents.GetResult
	require.NoError(t, getRes.Unmarshal(&get))
	require.Len(t, get.Agent.Tools, 2)
	assert.Equal(t, "list_pipelines", get.Agent.Tools[0].Name)

	// 4. Unknown agentId still errors on the stateless wire.
	_, err = c.Call(agents.MethodGet, agents.GetParams{AgentID: "nope"})
	require.Error(t, err)
	rpcErr, ok := err.(*client.RPCError)
	require.True(t, ok, "want a JSON-RPC error, got %T", err)
	assert.Equal(t, core.ErrCodeInvalidParams, rpcErr.Code)
}
