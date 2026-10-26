package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gapsServer wraps srv's handler so a test can see each HTTP request's method
// and headers, and optionally answer DELETE itself.
type gapsServer struct {
	srv *server.Server
	url string

	mu       sync.Mutex
	deletes  int
	toolHdrs []http.Header
}

func newGapsServer(t *testing.T, onDelete http.HandlerFunc) *gapsServer {
	t.Helper()
	g := &gapsServer{srv: server.NewServer(core.ServerInfo{Name: "gaps", Version: "1"})}
	g.srv.RegisterTool(core.ToolDef{
		Name: "lookup",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"region": map[string]any{"type": "string", "x-mcp-header": "Region"},
			},
		},
	}, func(core.ToolContext, core.ToolRequest) (core.ToolResponse, error) {
		return core.TextResult("ok"), nil
	})
	inner := g.srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		if r.Method == http.MethodDelete {
			g.deletes++
		}
		if r.Header.Get("Mcp-Method") == "tools/call" {
			g.toolHdrs = append(g.toolHdrs, r.Header.Clone())
		}
		g.mu.Unlock()
		if r.Method == http.MethodDelete && onDelete != nil {
			onDelete(w, r)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	g.url = ts.URL + "/mcp"
	return g
}

func (g *gapsServer) deleteCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.deletes
}

func TestClose_TerminatesTheSessionSoCloseHooksRun(t *testing.T) {
	g := newGapsServer(t, nil)
	var closed atomic.Value
	g.srv.OnSessionClose(func(_ context.Context, id string) { closed.Store(id) })

	c := client.NewClient(g.url, core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))
	sid := c.SessionID()
	require.NotEmpty(t, sid)

	require.NoError(t, c.Close())
	assert.Equal(t, 1, g.deleteCount())
	got, _ := closed.Load().(string)
	assert.Equal(t, sid, got, "the server ran its close hook for this session when Close returned")
}

func TestClose_SendsNoDeleteOnTheStatelessWire(t *testing.T) {
	g := newGapsServer(t, nil)
	c := client.NewClient(g.url, core.ClientInfo{Name: "c", Version: "1"}, client.WithClientMode(client.ClientModeStateless))
	require.NoError(t, c.Connect(t.Context()))
	require.NoError(t, c.Close())
	assert.Equal(t, 0, g.deleteCount())
}

func TestClose_ToleratesAServerThatRefusesDelete(t *testing.T) {
	g := newGapsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	c := client.NewClient(g.url, core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))
	assert.NoError(t, c.Close())
	assert.Equal(t, 1, g.deleteCount())
}

func TestClose_DoesNotHangOnAServerThatNeverAnswersDelete(t *testing.T) {
	release := make(chan struct{})
	g := newGapsServer(t, func(http.ResponseWriter, *http.Request) { <-release })
	// After newGapsServer's cleanup in LIFO order, so the stuck handler is
	// released before the test server waits on it.
	t.Cleanup(func() { close(release) })
	c := client.NewClient(g.url, core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))

	start := time.Now()
	assert.NoError(t, c.Close())
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestToolCallWith_SendsMcpParamHeadersLikeToolCall(t *testing.T) {
	g := newGapsServer(t, nil)
	c := client.NewClient(g.url, core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })
	_, err := c.ListTools(t.Context())
	require.NoError(t, err)

	args := map[string]any{"region": "us-west"}
	_, err = c.ToolCall(t.Context(), "lookup", args)
	require.NoError(t, err)
	_, err = c.ToolCallWith(t.Context(), "lookup", args, client.WithProgress(func(core.ProgressNotification) {}))
	require.NoError(t, err)

	g.mu.Lock()
	defer g.mu.Unlock()
	require.Len(t, g.toolHdrs, 2)
	assert.Equal(t, "us-west", g.toolHdrs[0].Get("Mcp-Param-Region"), "ToolCall")
	assert.Equal(t, "us-west", g.toolHdrs[1].Get("Mcp-Param-Region"), "ToolCallWith")
}

type progressTokenKey struct{}

// progressPrimitivesServer serves a resource and a prompt that each report
// progress 1..2 of 2 against the request's progress token. Resource and prompt
// handlers aren't handed the token, so a middleware lifts it into ctx.
func progressPrimitivesServer(t *testing.T) string {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "p", Version: "1"})
	srv.UseMiddleware(func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
		var p struct {
			Meta struct {
				ProgressToken any `json:"progressToken"`
			} `json:"_meta"`
		}
		if req.Params.Bind(&p) == nil && p.Meta.ProgressToken != nil {
			ctx = context.WithValue(ctx, progressTokenKey{}, p.Meta.ProgressToken)
		}
		return next(ctx, req)
	})
	report := func(bc core.BaseContext) {
		token := bc.Value(progressTokenKey{})
		for i := 1; i <= 2; i++ {
			bc.Notify("notifications/progress", core.ProgressNotification{ProgressToken: token, Progress: float64(i), Total: 2})
		}
	}
	srv.RegisterResource(core.ResourceDef{URI: "file:///r", Name: "r"}, func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
		report(ctx.BaseContext)
		return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: req.URI, Text: "body"}}}, nil
	})
	srv.RegisterPrompt(core.PromptDef{Name: "p"}, func(ctx core.PromptContext, _ core.PromptRequest) (core.PromptResponse, error) {
		report(ctx.BaseContext)
		return core.PromptResult{Messages: []core.PromptMessage{{Role: "user", Content: core.Content{Type: "text", Text: "hi"}}}}, nil
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithSSE(false)))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func TestReadResourceWith_DeliversItsProgress(t *testing.T) {
	c := client.NewClient(progressPrimitivesServer(t), core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })

	var log progressLog
	res, err := c.ReadResourceWith(t.Context(), "file:///r", client.WithProgress(log.add))
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "body", res.Contents[0].Text)
	assert.Equal(t, []float64{1, 2}, log.progress())
}

func TestGetPromptWith_DeliversItsProgress(t *testing.T) {
	c := client.NewClient(progressPrimitivesServer(t), core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })

	var log progressLog
	res, err := c.GetPromptWith(t.Context(), "p", nil, client.WithProgress(log.add))
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, []float64{1, 2}, log.progress())
}

func TestToolCallWith_ProgressOverTheInProcessTransport(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "progress", Version: "1"})
	srv.RegisterTool(core.ToolDef{Name: "count"}, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
		for i := 1; i <= 3; i++ {
			ctx.Progress(float64(i), 3, "step")
		}
		return core.TextResult("counted"), nil
	})
	var seenByTransport atomic.Int32
	tr := server.NewInProcessTransport(srv, server.WithNotificationHandler(func(method string, _ []byte) {
		if method == "notifications/progress" {
			seenByTransport.Add(1)
		}
	}))
	c := client.NewClient("memory://", core.ClientInfo{Name: "c", Version: "1"}, client.WithTransport(tr))
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })

	var log progressLog
	_, err := c.ToolCallWith(t.Context(), "count", nil, client.WithProgress(log.add))
	require.NoError(t, err)
	assert.Equal(t, []float64{1, 2, 3}, log.progress())
	assert.Equal(t, int32(3), seenByTransport.Load(), "the transport's own handler still sees every notification")
}

// Close's DELETE must not race requests still in flight on other goroutines.
// Run under -race; it reported a write to the session id in terminateSession
// against the read in notify.
func TestClose_DoesNotRaceRequestsInFlight(t *testing.T) {
	g := newGapsServer(t, nil)
	c := client.NewClient(g.url, core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				_ = c.NotifyRootsChanged(context.Background())
			}
		})
	}
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, c.Close())
	wg.Wait()
	assert.Equal(t, 1, g.deleteCount(), "one DELETE, however many requests were in flight")
}

func TestToolCall_WithMcpParamHeadersStillHonoursCancellation(t *testing.T) {
	g := newGapsServer(t, nil)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	g.srv.RegisterTool(core.ToolDef{
		Name: "slow",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"region": map[string]any{"type": "string", "x-mcp-header": "Region"}},
		},
	}, func(tc core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
		select {
		case <-release:
		case <-tc.Done():
		}
		return core.TextResult("late"), nil
	})
	c := client.NewClient(g.url, core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })
	_, err := c.ListTools(t.Context())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.ToolCall(ctx, "slow", map[string]any{"region": "us-west"})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}
