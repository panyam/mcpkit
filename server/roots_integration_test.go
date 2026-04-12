package server_test

import (
	"context"
	"io"
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

// TestRootsRoundTrip exercises the full notifications/roots/list_changed →
// server-initiated roots/list → populated callback path through all 4
// transports (Streamable HTTP, SSE, in-process, stdio). This specifically
// stresses the persistent pushRequest wiring added by #26 for SSE and
// Streamable HTTP GET SSE streams — the unit tests use only the in-process
// transport and cannot reach those code paths.
func TestRootsRoundTrip(t *testing.T) {
	expected := []core.Root{
		{URI: "file:///workspace/app", Name: "app"},
		{URI: "file:///workspace/lib"},
	}

	forAllRootsTransports(t, expected, func(t *testing.T, c *client.Client, callbackCh chan []core.Root) {
		err := c.NotifyRootsChanged()
		require.NoError(t, err, "NotifyRootsChanged should succeed")

		select {
		case roots := <-callbackCh:
			assert.Equal(t, expected, roots,
				"callback must receive the exact roots returned by the client's handler")
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for OnRootsChanged callback")
		}
	})
}

// TestRootsRoundTripEmpty verifies that a client returning an empty roots
// list still triggers the callback with an empty (non-nil) slice.
func TestRootsRoundTripEmpty(t *testing.T) {
	forAllRootsTransports(t, []core.Root{}, func(t *testing.T, c *client.Client, callbackCh chan []core.Root) {
		err := c.NotifyRootsChanged()
		require.NoError(t, err)

		select {
		case roots := <-callbackCh:
			require.NotNil(t, roots, "callback should receive non-nil empty slice")
			assert.Empty(t, roots, "roots should be empty")
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for OnRootsChanged callback")
		}
	})
}

// TestRootsNoHandlerNoCap verifies that a client without WithRootsHandler
// does NOT declare roots.listChanged, so the server's capability gate
// prevents the fetch from firing. End-to-end negative test complementing
// the unit-level TestRootsNotFetchedWithoutCapability.
func TestRootsNoHandlerNoCap(t *testing.T) {
	callbackCh := make(chan []core.Root, 1)
	srv := newRootsTestServer(callbackCh)
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Client WITHOUT WithRootsHandler — no roots capability advertised.
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-no-roots", Version: "1.0"},
		client.WithGetSSEStream())
	require.NoError(t, c.Connect())
	t.Cleanup(func() { c.Close() })

	// Give the GET SSE stream a moment to connect.
	time.Sleep(100 * time.Millisecond)

	// The client can still send the notification (protocol doesn't forbid it),
	// but the server must not issue a roots/list request because the client
	// did not declare the capability.
	_ = c.NotifyRootsChanged()

	select {
	case roots := <-callbackCh:
		t.Fatalf("callback should NOT have fired, but got roots: %v", roots)
	case <-time.After(300 * time.Millisecond):
		// Expected: no callback.
	}
}

// --- Test helpers ---

// newRootsTestServer creates a server with a WithOnRootsChanged callback that
// sends the received roots to the provided channel. A no-op tool is registered
// so the initialize handshake succeeds.
func newRootsTestServer(callbackCh chan []core.Root) *server.Server {
	srv := server.NewServer(
		core.ServerInfo{Name: "roots-integration-test", Version: "1.0.0"},
		server.WithOnRootsChanged(func(roots []core.Root) {
			callbackCh <- roots
		}),
	)
	srv.RegisterTool(
		core.ToolDef{Name: "noop", Description: "noop", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)
	return srv
}

// forAllRootsTransports runs fn against all 4 transports (Streamable HTTP,
// SSE, in-process, stdio). Each subtest creates a fresh server + client with
// WithRootsHandler returning the given expectedRoots. A buffered callbackCh
// is passed to fn so it can wait for the WithOnRootsChanged callback.
//
// The Streamable HTTP and SSE subtests enable WithGetSSEStream so the server's
// persistent pushRequest is wired through the GET SSE connection — the primary
// code path this integration test stresses.
func forAllRootsTransports(t *testing.T, expectedRoots []core.Root, fn func(t *testing.T, c *client.Client, callbackCh chan []core.Root)) {
	t.Helper()

	rootsHandler := client.RootsHandler(func(ctx context.Context) ([]core.Root, error) {
		return expectedRoots, nil
	})

	t.Run("streamable", func(t *testing.T) {
		callbackCh := make(chan []core.Root, 1)
		srv := newRootsTestServer(callbackCh)
		handler := srv.Handler(server.WithStreamableHTTP(true))
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)

		c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithRootsHandler(rootsHandler),
			client.WithGetSSEStream())
		require.NoError(t, c.Connect())
		t.Cleanup(func() { c.Close() })
		// Give the GET SSE stream a moment to establish so pushRequest is wired.
		time.Sleep(100 * time.Millisecond)
		fn(t, c, callbackCh)
	})

	t.Run("sse", func(t *testing.T) {
		callbackCh := make(chan []core.Root, 1)
		srv := newRootsTestServer(callbackCh)
		handler := srv.Handler(server.WithSSE(true), server.WithStreamableHTTP(false))
		ts := httptest.NewServer(handler)

		c := client.NewClient(ts.URL+"/mcp/sse", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithSSEClient(),
			client.WithRootsHandler(rootsHandler))
		require.NoError(t, c.Connect())
		t.Cleanup(func() {
			c.Close()
			ts.Close()
		})
		fn(t, c, callbackCh)
	})

	t.Run("memory", func(t *testing.T) {
		callbackCh := make(chan []core.Root, 1)
		srv := newRootsTestServer(callbackCh)

		c := client.NewClient("memory://", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithRootsHandler(rootsHandler))
		transport := server.NewInProcessTransport(srv,
			server.WithServerRequestHandler(func(ctx context.Context, req *core.Request) *core.Response {
				return c.HandleServerRequest(req)
			}),
		)
		c.SetTransport(transport)
		require.NoError(t, c.Connect())
		t.Cleanup(func() { c.Close() })
		fn(t, c, callbackCh)
	})

	t.Run("stdio", func(t *testing.T) {
		callbackCh := make(chan []core.Root, 1)
		srv := newRootsTestServer(callbackCh)

		sr, cw := io.Pipe()
		cr, sw := io.Pipe()

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.RunStdio(ctx, server.WithStdioInput(sr), server.WithStdioOutput(sw))
			sw.Close()
		}()

		c := client.NewClient("stdio://", core.ClientInfo{Name: "test-client", Version: "1.0"},
			client.WithStdioTransport(cr, cw),
			client.WithRootsHandler(rootsHandler))
		require.NoError(t, c.Connect())

		t.Cleanup(func() {
			c.Close()
			cancel()
			wg.Wait()
		})
		fn(t, c, callbackCh)
	})
}
