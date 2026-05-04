package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamTestStack wires a server with a YieldingSource backing a fake event
// + an InProcessTransport that captures every notification onto a channel.
// All ε-2 tests share this fixture — events/stream is a long-lived call so
// every test follows the same pattern: start the call in a goroutine, read
// notifications, cancel ctx, verify the final response.
//
// unsafeAnon "" → spec-strict auth (anonymous calls fail -32012);
// non-empty   → demo escape hatch.
type streamTestStack struct {
	srv       *server.Server
	transport *server.InProcessTransport
	source    *YieldingSource[fakePayload]
	yield     func(fakePayload) error
	notifs    chan capturedNotif
}

type capturedNotif struct {
	method string
	params json.RawMessage
}

func newStreamTestStack(t *testing.T, unsafeAnon string, opts ...streamStackOption) *streamTestStack {
	t.Helper()
	cfg := streamStackConfig{
		def:       EventDef{Name: "fake.event", Description: "test", Delivery: []string{"push", "poll"}},
		heartbeat: time.Hour, // disable by default; tests that exercise heartbeat override
	}
	for _, o := range opts {
		o(&cfg)
	}

	src, yield := NewYieldingSource[fakePayload](cfg.def, cfg.yieldOpts...)

	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: unsafeAnon,
		StreamHeartbeatInterval:  cfg.heartbeat,
	})

	notifs := make(chan capturedNotif, 64)
	transport := server.NewInProcessTransport(srv, server.WithNotificationHandler(func(method string, params []byte) {
		// Copy params; the transport may reuse the buffer.
		raw := make([]byte, len(params))
		copy(raw, params)
		select {
		case notifs <- capturedNotif{method: method, params: raw}:
		default:
			t.Logf("notification capture chan full; dropping %s", method)
		}
	}))
	require.NoError(t, transport.Connect(context.Background()))

	// Initialize the session so handlers will dispatch.
	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := transport.Call(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`0`), Method: "initialize", Params: initParams,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	require.NoError(t, transport.Notify(context.Background(), &core.Request{
		JSONRPC: "2.0", Method: "notifications/initialized",
	}))

	t.Cleanup(func() { _ = transport.Close() })

	return &streamTestStack{
		srv: srv, transport: transport,
		source: src, yield: yield, notifs: notifs,
	}
}

type streamStackConfig struct {
	def       EventDef
	yieldOpts []YieldingOption
	heartbeat time.Duration
}

type streamStackOption func(*streamStackConfig)

func withStreamSourceDef(def EventDef) streamStackOption {
	return func(c *streamStackConfig) { c.def = def }
}

func withStreamSourceYieldOpts(opts ...YieldingOption) streamStackOption {
	return func(c *streamStackConfig) { c.yieldOpts = append(c.yieldOpts, opts...) }
}

func withStreamHeartbeat(d time.Duration) streamStackOption {
	return func(c *streamStackConfig) { c.heartbeat = d }
}

// startStream calls events/stream in a goroutine and returns a struct that
// lets the test cancel the call (ending the stream) and read the final
// response. The goroutine sends to a one-shot chan when Call returns.
type runningStream struct {
	cancel context.CancelFunc
	done   chan streamResult
}

type streamResult struct {
	resp *core.Response
	err  error
}

func (s *streamTestStack) startStream(t *testing.T, params map[string]any) *runningStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	rawParams, err := json.Marshal(params)
	require.NoError(t, err)

	done := make(chan streamResult, 1)
	go func() {
		resp, err := s.transport.Call(ctx, &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`42`), Method: "events/stream", Params: rawParams,
		})
		done <- streamResult{resp: resp, err: err}
	}()
	return &runningStream{cancel: cancel, done: done}
}

func (rs *runningStream) endAndAwait(t *testing.T, d time.Duration) streamResult {
	t.Helper()
	rs.cancel()
	select {
	case r := <-rs.done:
		return r
	case <-time.After(d):
		t.Fatalf("stream did not return within %s after cancel", d)
		return streamResult{}
	}
}

// expectNotif drains until a matching notification arrives or the timeout
// fires. Returns the captured notification.
func expectNotif(t *testing.T, ch <-chan capturedNotif, method string, d time.Duration) capturedNotif {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case n := <-ch:
			if n.method == method {
				return n
			}
		case <-deadline:
			t.Fatalf("did not see notification %q within %s", method, d)
			return capturedNotif{}
		}
	}
}

// validStreamParams returns a well-formed events/stream request body.
func validStreamParams() map[string]any {
	return map[string]any{
		"name": "fake.event",
	}
}

// TestStream_RejectsUnknownEvent verifies the spec contract that an
// events/stream against an unknown event name fails with -32011 EventNotFound
// before any stream is opened (§"Push-Based Delivery" → "Request: events/stream"
// L269: "If the subscription is invalid, the server responds immediately with
// a JSON-RPC error and no stream is opened").
func TestStream_RejectsUnknownEvent(t *testing.T) {
	st := newStreamTestStack(t, "test-principal")
	rs := st.startStream(t, map[string]any{"name": "no.such.event"})
	r := rs.endAndAwait(t, time.Second)
	require.NoError(t, r.err)
	require.NotNil(t, r.resp.Error, "expected -32011; got result")
	assert.Equal(t, ErrCodeEventNotFound, r.resp.Error.Code)
}

// TestStream_RejectsAnonymousUnderStrictSpec verifies events/stream enforces
// the same authentication gate as events/subscribe per §"Subscription Identity"
// → "Authentication required" L361 — the spec L267 lists Unauthorized among
// the immediate-error responses.
//
// Without the gate, anonymous push-mode subscribes would silently succeed
// even with no UnsafeAnonymousPrincipal escape configured.
func TestStream_RejectsAnonymousUnderStrictSpec(t *testing.T) {
	st := newStreamTestStack(t, "")
	rs := st.startStream(t, validStreamParams())
	r := rs.endAndAwait(t, time.Second)
	require.NoError(t, r.err)
	require.NotNil(t, r.resp.Error, "expected -32012 Unauthorized; got result")
	assert.Equal(t, ErrCodeUnauthorized, r.resp.Error.Code)
}

// TestStream_ActiveNotificationFiresFirst verifies the first frame on a
// successfully-opened stream is notifications/events/active per spec L240
// + L273: confirmation MUST be sent before any event delivery, carrying
// the resolved cursor and the requestId of the parent events/stream call.
func TestStream_ActiveNotificationFiresFirst(t *testing.T) {
	st := newStreamTestStack(t, "test-principal")
	rs := st.startStream(t, validStreamParams())
	defer rs.endAndAwait(t, time.Second)

	n := expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	var p map[string]any
	require.NoError(t, json.Unmarshal(n.params, &p))
	assert.EqualValues(t, 42, p["requestId"], "requestId must echo the events/stream call id")
	_, hasCursor := p["cursor"]
	assert.True(t, hasCursor, "active notification must include the resolved cursor field (may be null)")
}

// TestStream_EventNotificationCarriesRequestId verifies a yielded event
// surfaces as notifications/events/event with the matching requestId per
// spec L243-271 + example L276. The requestId echoing is what makes
// per-stream isolation work — multiple concurrent streams (especially on
// stdio) demux notifications by this field.
func TestStream_EventNotificationCarriesRequestId(t *testing.T) {
	st := newStreamTestStack(t, "test-principal")
	rs := st.startStream(t, validStreamParams())
	defer rs.endAndAwait(t, time.Second)

	// Drain the initial active notification.
	expectNotif(t, st.notifs, "notifications/events/active", time.Second)

	require.NoError(t, st.yield(fakePayload{Msg: "hello"}))
	n := expectNotif(t, st.notifs, "notifications/events/event", time.Second)

	var p map[string]any
	require.NoError(t, json.Unmarshal(n.params, &p))
	assert.EqualValues(t, 42, p["requestId"], "every events/event must echo requestId")
	assert.Equal(t, "fake.event", p["name"])
	assert.NotEmpty(t, p["eventId"])
}

// TestStream_FinalFrameIsStreamEventsResult verifies that after the client
// cancels the stream, the handler returns an empty StreamEventsResult per
// spec L293: "{\"_meta\": {}}" — a typed final frame so the JSON-RPC contract
// (every request has a response) is satisfied.
func TestStream_FinalFrameIsStreamEventsResult(t *testing.T) {
	st := newStreamTestStack(t, "test-principal")
	rs := st.startStream(t, validStreamParams())
	expectNotif(t, st.notifs, "notifications/events/active", time.Second)

	r := rs.endAndAwait(t, time.Second)
	require.NoError(t, r.err)
	require.Nil(t, r.resp.Error, "stream cancellation must produce a successful StreamEventsResult, not an error")

	// Must be a JSON object with exactly the _meta key (may be empty).
	body, err := json.Marshal(r.resp.Result)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	_, ok := m["_meta"]
	assert.True(t, ok, "StreamEventsResult must contain _meta key per spec L293; got %s", string(body))
}

// TestStream_HeartbeatFiresOnInterval verifies the spec heartbeat contract
// (§"Lifecycle" → "Heartbeat" L294): periodic notifications/events/heartbeat
// with the source's current cursor. We use a 50ms interval to keep the
// test fast; production default is ≥30s.
//
// The heartbeat carrying cursor is what advances the client's persisted
// cursor during quiet periods — without it, a long-quiet upstream would
// leave the cursor stale and at risk of falling outside retention.
func TestStream_HeartbeatFiresOnInterval(t *testing.T) {
	st := newStreamTestStack(t, "test-principal", withStreamHeartbeat(50*time.Millisecond))
	rs := st.startStream(t, validStreamParams())
	defer rs.endAndAwait(t, time.Second)

	expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	n := expectNotif(t, st.notifs, "notifications/events/heartbeat", time.Second)

	var p map[string]any
	require.NoError(t, json.Unmarshal(n.params, &p))
	assert.EqualValues(t, 42, p["requestId"])
	// cursor key MUST be present even when null (cursored sources with no
	// events yet emit the source's current head; cursorless emit JSON null).
	_, hasCursor := p["cursor"]
	assert.True(t, hasCursor, "heartbeat MUST carry cursor field per spec L294")
}

// TestStream_HeartbeatCursorIsNullForCursorless verifies the cursorless
// branch of the heartbeat: §"Heartbeat" L294 says cursor "is null for event
// types that do not support replay." A cursorless source's heartbeat must
// JSON-encode cursor as null, not as an empty string or absent key.
func TestStream_HeartbeatCursorIsNullForCursorless(t *testing.T) {
	st := newStreamTestStack(t, "test-principal",
		withStreamSourceDef(EventDef{Name: "fake.event", Description: "ephemeral", Delivery: []string{"push"}}),
		withStreamSourceYieldOpts(WithoutCursors()),
		withStreamHeartbeat(50*time.Millisecond),
	)
	rs := st.startStream(t, validStreamParams())
	defer rs.endAndAwait(t, time.Second)

	expectNotif(t, st.notifs, "notifications/events/active", time.Second)
	n := expectNotif(t, st.notifs, "notifications/events/heartbeat", time.Second)

	// Inspect raw JSON to distinguish "cursor: null" from "cursor missing".
	body := string(n.params)
	assert.Contains(t, body, `"cursor":null`,
		"cursorless heartbeat MUST emit cursor as JSON null per spec L294; got %s", body)
}

