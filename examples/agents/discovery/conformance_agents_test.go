package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConformanceFixture pins the contract the sep-9998-agents-* scenarios
// rely on: an anonymous caller, one agent per page, the two named agents the
// fixture control reports, and an add control that changes the roster.
func TestConformanceFixture(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "agents-discovery", Version: "0.1.0"})
	buildServer(srv, true)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeStateless)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "harness", Version: "1"}, client.WithClientMode(client.ClientModeStateless))
	ctx := context.Background()
	require.NoError(t, c.Connect(ctx))
	t.Cleanup(func() { c.Close() })
	ac := agentsclient.New(c)

	page, err := ac.ListAgentsPage(ctx, "")
	require.NoError(t, err)
	assert.Len(t, page.Agents, 1)
	assert.NotEmpty(t, page.NextCursor, "one agent per page, so the roster paginates")

	text, err := c.ToolCall(ctx, "agents_conformance_fixture", map[string]any{})
	require.NoError(t, err)
	var names struct{ HiddenAgent, BrokenAgent string }
	require.NoError(t, json.Unmarshal([]byte(text), &names))

	roster, err := ac.ListAgents(ctx)
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, card := range roster {
		listed[card.Name] = true
	}
	assert.False(t, listed[names.HiddenAgent], "the hidden agent must not be listed")

	_, err = ac.GetAgent(ctx, names.HiddenAgent)
	assert.True(t, agentsclient.IsAgentNotFound(err), "hidden: %v", err)
	_, err = ac.GetAgent(ctx, names.BrokenAgent)
	assert.True(t, agentsclient.IsInvalidAgentDefinition(err), "broken: %v", err)

	_, err = c.ToolCall(ctx, "agents_conformance_add_agent", map[string]any{"name": "harness-added"})
	require.NoError(t, err)
	_, err = ac.GetAgent(ctx, "harness-added")
	assert.NoError(t, err)
}

// The demo server must not carry the conformance surface.
func TestConformanceControlsOffByDefault(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "agents-discovery", Version: "0.1.0"})
	buildServer(srv, false)
	for _, name := range []string{"agents_conformance_fixture", "agents_conformance_add_agent"} {
		_, ok := srv.Registry().ToolDef(name)
		assert.False(t, ok, "%s registered without --conformance-agents", name)
	}
}
