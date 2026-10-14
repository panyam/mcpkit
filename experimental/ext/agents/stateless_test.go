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
// wire (no initialize handshake, no session) and connects a stateless-pinned
// client. The extension is captured via server/discover on this wire.
func newStatelessClient(t *testing.T, opts []client.ClientOption, defs ...agents.AgentDef) *client.Client {
	t.Helper()
	srv := newCatalogServer()
	_, err := agents.Register(agents.Config{Server: srv, Agents: defs})
	require.NoError(t, err)

	handler := srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeStateless))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	opts = append([]client.ClientOption{client.WithClientMode(client.ClientModeStateless)}, opts...)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "0.0.1"}, opts...)
	require.NoError(t, c.Connect(t.Context()))
	return c
}

// The primitive needs no stateless-specific code, because custom methods and
// extension advertising ride shared dispatcher seams. This keeps it that way:
// the recurring failure is a discovery feature that silently no-ops on the
// stateless wire.
func TestStatelessWireParity(t *testing.T) {
	c := newStatelessClient(t, nil, sepWorkflowAgent())
	call := testCall(t, c)

	assert.True(t, c.ServerSupportsExtension(agents.ExtensionID),
		"stateless wire must advertise %q", agents.ExtensionID)

	list := listAll(t, call)
	require.Len(t, list.Agents, 1)
	assert.Equal(t, "workflow-agent", list.Agents[0].Name)
	raw, err := c.Call(t.Context(), agents.MethodList, nil)
	require.NoError(t, err)
	assert.NotContains(t, string(raw.Raw), "list_failed_pipelines", "the roster carries no tool schemas")

	assertToolsMatchList(t, call, "workflow-agent")

	_, err = c.Call(t.Context(), agents.MethodGet, agents.GetAgentParams{Name: "nope"})
	rpcErr := rpcError(t, err)
	assert.Equal(t, core.ErrCodeInvalidParams, rpcErr.Code)
	assert.Equal(t, agents.ReasonAgentNotFound, errorReason(t, rpcErr))
}

// On the stateless wire the client declares fileInputs per request, and
// agents/get must read that declaration the same way tools/list does.
func TestStatelessToolsMatchWithFileInputs(t *testing.T) {
	c := newStatelessClient(t, []client.ClientOption{client.WithFileInputs()}, opsAgent())
	assertToolsMatchList(t, testCall(t, c), "ops-agent")
}
