package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cancelServer has one tool, "wait", that signals started and then blocks
// until its context ends (reporting that on cancelled) or five seconds pass.
type cancelServer struct {
	srv       *server.Server
	started   chan struct{}
	cancelled chan struct{}

	mu     sync.Mutex
	bodies []map[string]any
}

func newCancelServer() *cancelServer {
	b := &cancelServer{
		srv:       server.NewServer(core.ServerInfo{Name: "block", Version: "1"}),
		started:   make(chan struct{}, 1),
		cancelled: make(chan struct{}, 1),
	}
	b.srv.RegisterTool(core.ToolDef{Name: "wait"}, func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
		b.started <- struct{}{}
		select {
		case <-ctx.Done():
			b.cancelled <- struct{}{}
		case <-time.After(5 * time.Second):
		}
		return core.TextResult("done"), nil
	})
	return b
}

// serve records every JSON-RPC body POSTed to the server.
func (b *cancelServer) serve(t *testing.T, opts ...server.TransportOption) string {
	t.Helper()
	h := b.srv.Handler(opts...)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				b.mu.Lock()
				b.bodies = append(b.bodies, m)
				b.mu.Unlock()
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func (b *cancelServer) method(name string) []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, m := range b.bodies {
		if m["method"] == name {
			out = append(out, m)
		}
	}
	return out
}

// callAndCancel starts the wait tool and cancels its context once the
// handler is running, returning how long the call took and its error.
func callAndCancel(t *testing.T, c *client.Client, b *cancelServer) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := c.ToolCall(ctx, "wait", nil)
		done <- err
	}()
	select {
	case <-b.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	cancel()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled call did not return")
		return 0, nil
	}
}

func TestCancel_SendsNotificationsCancelledWithTheRequestID(t *testing.T) {
	b := newCancelServer()
	url := b.serve(t, server.WithStreamableHTTP(true), server.WithSSE(false))
	c := client.NewClient(url+"/mcp", core.ClientInfo{Name: "c", Version: "1"})
	require.NoError(t, c.Connect(t.Context()))

	_, err := callAndCancel(t, c, b)
	assert.ErrorIs(t, err, context.Canceled)
	require.Eventually(t, func() bool { return len(b.method("notifications/cancelled")) == 1 }, 2*time.Second, 10*time.Millisecond)

	calls := b.method("tools/call")
	require.Len(t, calls, 1)
	params := b.method("notifications/cancelled")[0]["params"].(map[string]any)
	assert.Equal(t, calls[0]["id"], params["requestId"])
	assert.Equal(t, context.Canceled.Error(), params["reason"])
}

// On the legacy SSE wire the response arrives on a separate stream, so the
// call must stop waiting on its own, and the notification is the only way
// the handler learns the caller left.
func TestCancel_SSECallReturnsAndTheHandlerIsCancelled(t *testing.T) {
	b := newCancelServer()
	url := b.serve(t, server.WithSSE(true), server.WithStreamableHTTP(false))
	c := client.NewClient(url+"/mcp/sse", core.ClientInfo{Name: "c", Version: "1"}, client.WithSSEClient())
	require.NoError(t, c.Connect(t.Context()))
	t.Cleanup(func() { c.Close() })

	took, err := callAndCancel(t, c, b)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, took, 2*time.Second)
	select {
	case <-b.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler's context was never cancelled")
	}
}

func TestCancel_InProcessTransportCancelsTheHandler(t *testing.T) {
	b := newCancelServer()
	c := client.NewClient("", core.ClientInfo{Name: "c", Version: "1"}, client.WithTransport(server.NewInProcessTransport(b.srv)))
	require.NoError(t, c.Connect(t.Context()))

	// The in-process call is synchronous, so the cancelled handler's own
	// result comes back; what matters is that the handler saw the cancel.
	callAndCancel(t, c, b)
	select {
	case <-b.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the handler's context was never cancelled")
	}
}

// SEP-2575 removed notifications/cancelled from the stateless wire; aborting
// the POST is the cancellation there.
func TestCancel_StatelessWireSendsNoNotification(t *testing.T) {
	b := newCancelServer()
	url := b.serve(t, server.WithStreamableHTTP(true), server.WithSSE(false))
	c := client.NewClient(url+"/mcp", core.ClientInfo{Name: "c", Version: "1"}, client.WithClientMode(client.ClientModeStateless))
	require.NoError(t, c.Connect(t.Context()))

	_, err := callAndCancel(t, c, b)
	assert.ErrorIs(t, err, context.Canceled)
	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, b.method("notifications/cancelled"))
}
