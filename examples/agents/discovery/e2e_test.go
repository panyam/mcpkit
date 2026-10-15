package main

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWalkthrough runs every claim the walkthrough prints, on both wires.
func TestWalkthrough(t *testing.T) {
	for _, wire := range []string{"session", "stateless"} {
		t.Run(wire, func(t *testing.T) {
			srv := server.NewServer(core.ServerInfo{Name: "agents-discovery", Version: "0.1.0"}, server.WithAuth(tokenAuth{}))
			buildServer(srv)
			topts := []server.TransportOption{server.WithStreamableHTTP(true)}
			var copts []client.ClientOption
			if wire == "stateless" {
				topts = append(topts, server.WithStatelessMode(stateless.ModeStateless))
				copts = append(copts, client.WithClientMode(client.ClientModeStateless))
			}
			ts := httptest.NewServer(srv.Handler(topts...))
			t.Cleanup(ts.Close)
			url := ts.URL + "/mcp"
			ctx := context.Background()

			alice, err := connect(ctx, url, "alice-read", copts...)
			require.NoError(t, err)
			t.Cleanup(alice.close)
			require.True(t, alice.agents.SupportsAgents())

			names, err := alice.rosterNames(ctx)
			require.NoError(t, err)
			assert.Equal(t, []string{"pipeline-agent", "deploy-agent"}, names, "billing-agent is hidden from Alice")

			pipeline, err := alice.cache.GetAgent(ctx, "pipeline-agent")
			require.NoError(t, err)
			match, err := toolsMatchList(ctx, alice, pipeline)
			require.NoError(t, err)
			assert.Equal(t, map[string]bool{"list_failed_pipelines": true, "get_pipeline_log": true}, match)
			model, how := models.Resolve(pipeline.ModelRoutingHint)
			assert.Equal(t, "claude-haiku-5-5", model)
			assert.Equal(t, agentsclient.ResolvedHinted, how)

			out, err := alice.runAgent(ctx, pipeline)
			require.NoError(t, err)
			assert.Contains(t, out["get_pipeline_log"], "build-1841")

			bob, err := connect(ctx, url, "bob-billing", copts...)
			require.NoError(t, err)
			t.Cleanup(bob.close)
			bobNames, err := bob.rosterNames(ctx)
			require.NoError(t, err)
			assert.Contains(t, bobNames, "billing-agent")
			for _, name := range []string{"billing-agent", "no-such-agent"} {
				_, err := alice.agents.GetAgent(ctx, name)
				assert.True(t, agentsclient.IsAgentNotFound(err), "%s: %v", name, err)
			}

			_, err = alice.cache.GetAgent(ctx, "deploy-agent")
			scopes, ok := agentsclient.StepUpScopes(err)
			require.True(t, ok, "want a step-up refusal, got %v", err)
			assert.Equal(t, []string{"ops:deploy"}, scopes)
			alice.token.set("alice-deploy")
			deploy, err := alice.cache.GetAgent(ctx, "deploy-agent")
			require.NoError(t, err)
			model, _ = models.Resolve(deploy.ModelRoutingHint)
			assert.Equal(t, "claude-opus-5-5", model)

			heard := make(chan struct{}, 1)
			stop, err := alice.watch(ctx, func() {
				select {
				case heard <- struct{}{}:
				default:
				}
			})
			require.NoError(t, err)
			t.Cleanup(stop)
			_, err = alice.mcp.ToolCall(ctx, "publish_release_agent", map[string]any{})
			require.NoError(t, err)
			select {
			case <-heard:
			case <-time.After(3 * time.Second):
				t.Fatal("no notifications/agents/list_changed within 3s")
			}
			names, err = alice.rosterNames(ctx)
			require.NoError(t, err)
			assert.True(t, slices.Contains(names, "release-agent"), "roster after list_changed: %v", names)
		})
	}
}
