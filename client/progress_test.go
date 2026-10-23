package client_test

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// progressServer's "count" tool reports progress 1..3 of 3, and also reports
// under a foreign token, which no caller's callback should see.
func progressServer(t *testing.T, opts ...server.TransportOption) string {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "progress", Version: "1"})
	srv.RegisterTool(core.ToolDef{Name: "count"}, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
		ctx.EmitProgress("someone-else", 9, 9, "not yours")
		for i := 1; i <= 3; i++ {
			ctx.Progress(float64(i), 3, "step")
		}
		return core.TextResult("counted"), nil
	})
	ts := httptest.NewServer(srv.Handler(opts...))
	t.Cleanup(ts.Close)
	return ts.URL
}

type progressLog struct {
	mu  sync.Mutex
	got []core.ProgressNotification
}

func (l *progressLog) add(p core.ProgressNotification) {
	l.mu.Lock()
	l.got = append(l.got, p)
	l.mu.Unlock()
}

func (l *progressLog) progress() []float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []float64
	for _, p := range l.got {
		out = append(out, p.Progress)
	}
	return out
}

func TestToolCallWith_ProgressReachesOnlyItsCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		srv  []server.TransportOption
		cli  []client.ClientOption
	}{
		{"streamable", "/mcp", []server.TransportOption{server.WithStreamableHTTP(true), server.WithSSE(false)}, nil},
		{"stateless", "/mcp", []server.TransportOption{server.WithStreamableHTTP(true), server.WithSSE(false)}, []client.ClientOption{client.WithClientMode(client.ClientModeStateless)}},
		{"sse", "/mcp/sse", []server.TransportOption{server.WithSSE(true), server.WithStreamableHTTP(false)}, []client.ClientOption{client.WithSSEClient()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			url := progressServer(t, tc.srv...)
			var all []string
			var allMu sync.Mutex
			opts := append([]client.ClientOption{client.WithNotificationCallback(func(method string, _ any) {
				allMu.Lock()
				all = append(all, method)
				allMu.Unlock()
			})}, tc.cli...)
			c := client.NewClient(url+tc.path, core.ClientInfo{Name: "c", Version: "1"}, opts...)
			require.NoError(t, c.Connect(t.Context()))
			t.Cleanup(func() { c.Close() })

			var log progressLog
			res, err := c.ToolCallWith(t.Context(), "count", nil, client.WithProgress(log.add))
			require.NoError(t, err)
			assert.False(t, res.IsError)
			assert.Equal(t, []float64{1, 2, 3}, log.progress(), "progress for this call, delivered before it returns")
			for _, p := range log.got {
				assert.Equal(t, float64(3), p.Total)
			}

			allMu.Lock()
			defer allMu.Unlock()
			n := 0
			for _, m := range all {
				if m == "notifications/progress" {
					n++
				}
			}
			assert.Equal(t, 4, n, "the session callback still sees every progress notification, foreign token included")
		})
	}
}

// Without WithProgress no token is sent, so the server reports nothing.
func TestToolCallWith_NoOptionsSendsNoToken(t *testing.T) {
	url := progressServer(t, server.WithStreamableHTTP(true), server.WithSSE(false))
	var n int
	var mu sync.Mutex
	c := client.NewClient(url+"/mcp", core.ClientInfo{Name: "c", Version: "1"}, client.WithNotificationCallback(func(method string, _ any) {
		if method == "notifications/progress" {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}))
	require.NoError(t, c.Connect(t.Context()))

	_, err := c.ToolCallWith(t.Context(), "count", nil)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, n, "only the tool's foreign-token report, no progress for this call")
}

// A client with no session callback still routes progress: the callback is
// registered per call, after the transport was built.
func TestToolCallWith_ProgressWithoutASessionCallback(t *testing.T) {
	url := progressServer(t, server.WithStreamableHTTP(true), server.WithSSE(false))
	c := client.NewClient(url+"/mcp", core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))

	var log progressLog
	_, err := c.ToolCallWith(t.Context(), "count", nil, client.WithProgress(log.add))
	require.NoError(t, err)
	assert.Equal(t, []float64{1, 2, 3}, log.progress())
}
