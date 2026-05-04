package eventsclient_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamStack is a stream-test variant of stack() that lets the test set
// the server-side StreamHeartbeatInterval (so tests can observe heartbeats
// quickly without waiting 30s).
func streamStack(t *testing.T, heartbeat time.Duration) (*client.Client, func(fakePayload) error) {
	t.Helper()
	src, yield := events.NewYieldingSource[fakePayload](events.EventDef{
		Name:        "fake.event",
		Description: "test source",
		Delivery:    []string{"push", "poll", "webhook"},
	})
	srv := server.NewServer(
		core.ServerInfo{Name: "stream-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Webhooks:                 events.NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
		StreamHeartbeatInterval:  heartbeat,
	})

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	return c, yield
}

// TestStream_DeliversEventsViaCallback verifies the typed callback path:
// open a Stream, yield events server-side, OnEvent fires per event with
// the spec-shape EventOccurrence in the wire payload. This is the
// happy-path that the discord/telegram demos will rely on after ε-6.
//
// Failing this means the SDK's per-call notify hook plumbing is broken
// or the Stream goroutine isn't dispatching to OnEvent.
func TestStream_DeliversEventsViaCallback(t *testing.T) {
	c, yield := streamStack(t, time.Hour) // disable heartbeats

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan events.Event, 4)
	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "fake.event",
		OnEvent: func(ev events.Event) {
			got <- ev
		},
	})
	require.NoError(t, err)
	defer stream.Stop()

	require.NoError(t, yield(fakePayload{Msg: "alpha"}))

	select {
	case ev := <-got:
		assert.Equal(t, "fake.event", ev.Name)
		assert.NotEmpty(t, ev.EventID)
	case <-time.After(2 * time.Second):
		t.Fatal("OnEvent never fired; stream failed to deliver event")
	}
}

// TestStream_HeartbeatCallback verifies OnHeartbeat fires for
// notifications/events/heartbeat at the configured interval per spec
// L294. Without this, clients can't advance their persisted cursor
// during quiet periods — the spec's whole reason for cursor-bearing
// heartbeats.
func TestStream_HeartbeatCallback(t *testing.T) {
	c, _ := streamStack(t, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	beats := make(chan *string, 4)
	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "fake.event",
		OnHeartbeat: func(cursor *string) {
			beats <- cursor
		},
	})
	require.NoError(t, err)
	defer stream.Stop()

	select {
	case <-beats:
		// Heartbeat arrived — content not asserted here (cursor may be
		// the source's current head, including empty for fresh sources).
	case <-time.After(2 * time.Second):
		t.Fatal("OnHeartbeat never fired within 2s of opening stream")
	}
}

// TestStream_StopEndsTheCall verifies Stop() cancels the underlying call
// promptly, the goroutine exits, and Done() closes.
func TestStream_StopEndsTheCall(t *testing.T) {
	c, _ := streamStack(t, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "fake.event",
	})
	require.NoError(t, err)

	stream.Stop()

	select {
	case <-stream.Done():
		// expected — stream goroutine exited after server returned StreamEventsResult.
	case <-time.After(2 * time.Second):
		t.Fatal("stream goroutine did not exit within 2s of Stop()")
	}
}

// TestStream_RejectsInitialError verifies that a server-side validation
// failure (e.g., unknown event name → -32011) surfaces as an error from
// Stream() rather than hanging waiting for an active notification that
// will never arrive.
func TestStream_RejectsInitialError(t *testing.T) {
	c, _ := streamStack(t, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
		EventName: "no.such.event",
	})
	require.Error(t, err, "Stream() must surface the immediate JSON-RPC error from the server")
}

