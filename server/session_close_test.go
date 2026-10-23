package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type closeRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *closeRecorder) hook(_ context.Context, id string) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
}

func (r *closeRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func closeHookServer(t *testing.T, opts ...server.TransportOption) (*server.Server, *httptest.Server) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "close-hook", Version: "1.0"})
	ts := httptest.NewServer(srv.Handler(opts...))
	t.Cleanup(ts.Close)
	return srv, ts
}

func connect(t *testing.T, url string, opts ...client.ClientOption) *client.Client {
	t.Helper()
	c := client.NewClient(url, core.ClientInfo{Name: "test", Version: "1.0"}, opts...)
	require.NoError(t, c.Connect(t.Context()))
	return c
}

func TestOnSessionClose_FiresOnceOnDelete(t *testing.T) {
	srv, ts := closeHookServer(t, server.WithStreamableHTTP(true), server.WithSSE(false))
	rec := &closeRecorder{}
	srv.OnSessionClose(rec.hook)

	c := connect(t, ts.URL+"/mcp")
	sid := c.SessionID()
	require.NotEmpty(t, sid)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	srv.CloseAllSessions()
	assert.Equal(t, []string{sid}, rec.seen())
}

func TestOnSessionClose_FiresOnIdleExpiry(t *testing.T) {
	srv, ts := closeHookServer(t, server.WithStreamableHTTP(true), server.WithSSE(false), server.WithSessionTimeout(50*time.Millisecond))
	rec := &closeRecorder{}
	srv.OnSessionClose(rec.hook)

	sid := connect(t, ts.URL+"/mcp").SessionID()
	require.Eventually(t, func() bool { return len(rec.seen()) == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{sid}, rec.seen())
}

// A hook registered after a session exists still fires for it, which is the
// case for an extension wired up after the server is serving.
func TestOnSessionClose_LateRegistrationCoversLiveSessions(t *testing.T) {
	srv, ts := closeHookServer(t, server.WithStreamableHTTP(true), server.WithSSE(false))
	sid := connect(t, ts.URL+"/mcp").SessionID()

	rec := &closeRecorder{}
	srv.OnSessionClose(rec.hook)
	require.True(t, srv.CloseSession(sid))
	assert.Equal(t, []string{sid}, rec.seen())
}

func TestOnSessionClose_FiresWhenSSEStreamCloses(t *testing.T) {
	srv, ts := closeHookServer(t, server.WithSSE(true), server.WithStreamableHTTP(false))
	rec := &closeRecorder{}
	srv.OnSessionClose(rec.hook)

	c := connect(t, ts.URL+"/mcp/sse", client.WithSSEClient())
	sid := c.SessionID()
	require.NoError(t, c.Close())
	require.Eventually(t, func() bool { return len(rec.seen()) == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{sid}, rec.seen())
}

func TestOnSessionClose_PanickingHookDoesNotStopTheRest(t *testing.T) {
	srv, ts := closeHookServer(t, server.WithStreamableHTTP(true), server.WithSSE(false))
	rec := &closeRecorder{}
	srv.OnSessionClose(func(context.Context, string) { panic("boom") })
	srv.OnSessionClose(rec.hook)

	sid := connect(t, ts.URL+"/mcp").SessionID()
	require.True(t, srv.CloseSession(sid))
	assert.Equal(t, []string{sid}, rec.seen())
}
