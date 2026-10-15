package agentsclient_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/require"
)

func names(cards []agents.AgentCard) []string {
	out := make([]string, len(cards))
	for i, c := range cards {
		out[i] = c.Name
	}
	return out
}

// With a minute-long roster TTL, the only way the cache sees a new agent
// within the test is the list_changed notification invalidating it.
func TestCacheFollowsListChanged(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "agents-test", Version: "0.0.1"})
	srv.RegisterTool(core.ToolDef{Name: "list_failed_pipelines", InputSchema: map[string]any{"type": "object"}}, noopTool)
	reg, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{workflowAgent()}, TTLMs: ptr(60_000)})
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(stateless.ModeStateless)))
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "t", Version: "1"}, client.WithClientMode(client.ClientModeStateless))
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })

	cache := agentsclient.NewCache(agentsclient.New(c))
	l, err := agentsclient.WatchListChanged(t.Context(), c, cache.Invalidate)
	require.NoError(t, err)
	t.Cleanup(l.Stop)

	roster, err := cache.ListAgents(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"workflow-agent"}, names(roster))

	require.NoError(t, reg.AddAgent(agents.AgentDef{Name: "ops-agent"}))
	require.Eventually(t, func() bool {
		roster, err := cache.ListAgents(context.Background())
		return err == nil && slices.Contains(names(roster), "ops-agent")
	}, 3*time.Second, 20*time.Millisecond, "the cache never saw the new agent")
}

func ptr(n int) *int { return &n }
