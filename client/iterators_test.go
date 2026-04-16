package client_test

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTools_IteratesAll verifies that c.Tools(ctx) yields all registered tools
// from the server. With default page size (0 = all items), this exercises the
// single-page path where nextCursor is empty after the first call.
func TestTools_IteratesAll(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "iter-test", Version: "1.0"})
	for _, name := range []string{"alpha", "beta", "gamma"} {
		n := name
		srv.Register(core.TextTool[struct{}](n, "tool "+n,
			func(ctx core.ToolContext, _ struct{}) (string, error) {
				return n, nil
			},
		))
	}

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		var names []string
		for tool, err := range c.Tools(context.Background()) {
			require.NoError(t, err)
			names = append(names, tool.Name)
		}
		assert.ElementsMatch(t, []string{"alpha", "beta", "gamma"}, names)
	})
}

// TestResources_IteratesAll verifies that c.Resources(ctx) yields all registered
// resources.
func TestResources_IteratesAll(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "iter-test", Version: "1.0"})
	for _, uri := range []string{"test://a", "test://b"} {
		u := uri
		srv.RegisterResource(
			core.ResourceDef{URI: u, Name: u, MimeType: "text/plain"},
			func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
				return core.ResourceResult{Contents: []core.ResourceReadContent{{
					URI: req.URI, MimeType: "text/plain", Text: "data",
				}}}, nil
			},
		)
	}

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		var uris []string
		for res, err := range c.Resources(context.Background()) {
			require.NoError(t, err)
			uris = append(uris, res.URI)
		}
		assert.ElementsMatch(t, []string{"test://a", "test://b"}, uris)
	})
}

// TestPrompts_IteratesAll verifies that c.Prompts(ctx) yields all registered
// prompts.
func TestPrompts_IteratesAll(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "iter-test", Version: "1.0"})
	for _, name := range []string{"summarize", "translate"} {
		n := name
		srv.RegisterPrompt(
			core.PromptDef{Name: n, Description: "prompt " + n},
			func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
				return core.PromptResult{}, nil
			},
		)
	}

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		var names []string
		for prompt, err := range c.Prompts(context.Background()) {
			require.NoError(t, err)
			names = append(names, prompt.Name)
		}
		assert.ElementsMatch(t, []string{"summarize", "translate"}, names)
	})
}

// TestTools_EmptyServer verifies that iterating tools on a server with no tools
// produces zero items and no errors.
func TestTools_EmptyServer(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "empty-test", Version: "1.0"})

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		count := 0
		for _, err := range c.Tools(context.Background()) {
			require.NoError(t, err)
			count++
		}
		assert.Equal(t, 0, count)
	})
}

// TestTools_BreakStopsIteration verifies that breaking out of a for-range loop
// over the iterator cleanly stops iteration without panic or goroutine leak.
func TestTools_BreakStopsIteration(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "break-test", Version: "1.0"})
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		n := name
		srv.Register(core.TextTool[struct{}](n, "tool",
			func(ctx core.ToolContext, _ struct{}) (string, error) { return n, nil },
		))
	}

	testutil.ForAllTransports(t, srv, func(t *testing.T, c *client.Client) {
		count := 0
		for _, err := range c.Tools(context.Background()) {
			require.NoError(t, err)
			count++
			if count >= 2 {
				break // stop early
			}
		}
		assert.Equal(t, 2, count, "should stop after 2 items")
	})
}
