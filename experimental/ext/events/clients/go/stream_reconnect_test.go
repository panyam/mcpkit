package eventsclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scriptHeartbeat = 30 * time.Millisecond

// streamScript decides how the scripted server answers the n-th events/stream
// call (n starts at 1). It runs inside the server's handler, so it can send
// notifications and block until the client hangs up.
type streamScript func(n int, ctx core.MethodContext, id json.RawMessage) *core.Response

// streamScriptServer serves events/stream from a script and records the params of
// every call, so a test can see whether and how the client reconnected.
type streamScriptServer struct {
	mu    sync.Mutex
	calls []map[string]any
	seen  chan int
}

func (s *streamScriptServer) call(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[i]
}

func (s *streamScriptServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// awaitCall waits for the n-th events/stream call to arrive.
func (s *streamScriptServer) awaitCall(t *testing.T, n int, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		if s.count() >= n {
			return
		}
		select {
		case <-s.seen:
		case <-deadline:
			t.Fatalf("events/stream call %d never arrived within %v (got %d)", n, within, s.count())
		}
	}
}

func scriptedStack(t *testing.T, script streamScript) (*client.Client, *streamScriptServer) {
	t.Helper()
	ss := &streamScriptServer{seen: make(chan int, 16)}
	srv := server.NewServer(core.ServerInfo{Name: "scripted-stream", Version: "0.1.0"})
	srv.HandleMethod("events/stream", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		ss.mu.Lock()
		ss.calls = append(ss.calls, p)
		n := len(ss.calls)
		ss.mu.Unlock()
		select {
		case ss.seen <- n:
		default:
		}
		return script(n, ctx, id)
	})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect(t.Context()))
	return c, ss
}

// heartbeatThenSilence sends active, then heartbeats carrying cursors[1:] one
// interval apart, then stops sending anything while holding the call open,
// the way a stream dies behind a proxy that never closes the connection.
// A nil entry is a null cursor.
func heartbeatThenSilence(ctx core.MethodContext, id json.RawMessage, cursors []*string) *core.Response {
	ctx.Notify("notifications/events/active", map[string]any{"cursor": cursors[0], "truncated": false})
	for _, c := range cursors[1:] {
		select {
		case <-time.After(scriptHeartbeat):
		case <-ctx.Done():
			return core.NewResponse(id, map[string]any{"_meta": map[string]any{}})
		}
		ctx.Notify("notifications/events/heartbeat", map[string]any{"cursor": c})
	}
	<-ctx.Done()
	return core.NewResponse(id, map[string]any{"_meta": map[string]any{}})
}

// holdOpen sends active and keeps heartbeating until the client hangs up.
func holdOpen(ctx core.MethodContext, id json.RawMessage, cursor *string) *core.Response {
	ctx.Notify("notifications/events/active", map[string]any{"cursor": cursor, "truncated": false})
	for {
		select {
		case <-time.After(scriptHeartbeat):
			ctx.Notify("notifications/events/heartbeat", map[string]any{"cursor": cursor})
		case <-ctx.Done():
			return core.NewResponse(id, map[string]any{"_meta": map[string]any{}})
		}
	}
}

func strp(s string) *string { return &s }

func TestStream_ReconnectsWithLastCursorAfterSilence(t *testing.T) {
	c, ss := scriptedStack(t, func(n int, ctx core.MethodContext, id json.RawMessage) *core.Response {
		if n == 1 {
			return heartbeatThenSilence(ctx, id, []*string{strp("p-0"), strp("p-1"), strp("p-2"), strp("p-3")})
		}
		return holdOpen(ctx, id, strp("p-3"))
	})

	stream, err := eventsclient.Stream(t.Context(), c, eventsclient.StreamOptions{EventName: "replay.type"})
	require.NoError(t, err)
	defer stream.Stop()

	ss.awaitCall(t, 2, 2*time.Second)
	assert.Equal(t, "p-3", ss.call(1)["cursor"], "reconnect must resume from the last cursor the dead stream sent")
}

func TestStream_ReconnectSendsNullForCursorlessStream(t *testing.T) {
	c, ss := scriptedStack(t, func(n int, ctx core.MethodContext, id json.RawMessage) *core.Response {
		if n == 1 {
			return heartbeatThenSilence(ctx, id, []*string{nil, nil, nil, nil})
		}
		return holdOpen(ctx, id, nil)
	})

	stream, err := eventsclient.Stream(t.Context(), c, eventsclient.StreamOptions{EventName: "cursorless.type"})
	require.NoError(t, err)
	defer stream.Stop()

	ss.awaitCall(t, 2, 2*time.Second)
	cursor, present := ss.call(1)["cursor"]
	assert.True(t, present, "reconnect must send cursor explicitly")
	assert.Nil(t, cursor, "a stream whose frames all carried cursor:null must reconnect with cursor:null")
}

func TestStream_NoReconnectAfterTerminated(t *testing.T) {
	c, ss := scriptedStack(t, func(n int, ctx core.MethodContext, id json.RawMessage) *core.Response {
		ctx.Notify("notifications/events/active", map[string]any{"cursor": "p-0", "truncated": false})
		ctx.Notify("notifications/events/terminated", map[string]any{"error": map[string]any{"code": -32024, "message": "revoked"}})
		return core.NewResponse(id, map[string]any{"_meta": map[string]any{}})
	})

	terminated := make(chan error, 1)
	stream, err := eventsclient.Stream(t.Context(), c, eventsclient.StreamOptions{
		EventName:    "replay.type",
		OnTerminated: func(e error) { terminated <- e },
	})
	require.NoError(t, err)
	defer stream.Stop()

	select {
	case <-stream.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not end after notifications/events/terminated")
	}
	time.Sleep(10 * scriptHeartbeat)
	assert.Equal(t, 1, ss.count(), "a terminated subscription is over; the client must not reconnect")
	require.Len(t, terminated, 1)
}

func TestStream_NoReconnectAfterStop(t *testing.T) {
	c, ss := scriptedStack(t, func(n int, ctx core.MethodContext, id json.RawMessage) *core.Response {
		return holdOpen(ctx, id, strp("p-0"))
	})

	stream, err := eventsclient.Stream(t.Context(), c, eventsclient.StreamOptions{EventName: "replay.type"})
	require.NoError(t, err)
	stream.Stop()
	select {
	case <-stream.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not end the stream")
	}
	time.Sleep(10 * scriptHeartbeat)
	assert.Equal(t, 1, ss.count())
	assert.NoError(t, stream.Err())
}

func TestStream_RejectedReconnectEndsTheStream(t *testing.T) {
	c, ss := scriptedStack(t, func(n int, ctx core.MethodContext, id json.RawMessage) *core.Response {
		if n == 1 {
			return heartbeatThenSilence(ctx, id, []*string{strp("p-0"), strp("p-1"), strp("p-2")})
		}
		return core.NewErrorResponse(id, -32023, "event type removed")
	})

	stream, err := eventsclient.Stream(t.Context(), c, eventsclient.StreamOptions{EventName: "replay.type"})
	require.NoError(t, err)
	defer stream.Stop()

	select {
	case <-stream.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("a reconnect the server rejected must end the stream")
	}
	var rpcErr *client.RPCError
	require.True(t, errors.As(stream.Err(), &rpcErr), "Err() = %v, want the server's RPC error", stream.Err())
	assert.Equal(t, -32023, rpcErr.Code)
	time.Sleep(10 * scriptHeartbeat)
	assert.Equal(t, 2, ss.count(), "a rejection is an answer, not a transport failure; no retry")
}

func TestStream_DisableReconnectStaysOneShot(t *testing.T) {
	c, ss := scriptedStack(t, func(n int, ctx core.MethodContext, id json.RawMessage) *core.Response {
		return heartbeatThenSilence(ctx, id, []*string{strp("p-0"), strp("p-1"), strp("p-2")})
	})

	stream, err := eventsclient.Stream(context.Background(), c, eventsclient.StreamOptions{
		EventName:        "replay.type",
		DisableReconnect: true,
	})
	require.NoError(t, err)
	defer stream.Stop()

	time.Sleep(20 * scriptHeartbeat)
	assert.Equal(t, 1, ss.count(), "DisableReconnect keeps the one-call behavior")
}
