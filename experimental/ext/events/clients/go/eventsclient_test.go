package eventsclient_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

type fakePayload struct {
	Msg string `json:"msg"`
}

// stack wires a server with one cursored fake.* event source, returns the
// connected MCP client + the yield closure for the source. Optional
// WebhookOptions configure the registry for mode-specific tests.
func stack(t *testing.T, whOpts ...events.WebhookOption) (*client.Client, func(fakePayload) error, *events.WebhookRegistry) {
	t.Helper()

	// ζ-1: SDK tests subscribe to httptest URLs (127.0.0.1:N). Prepend
	// the loopback escape so the dial-time SSRF guard doesn't block
	// the test deliveries. Per-test whOpts can still override.
	whOpts = append([]events.WebhookOption{events.WithWebhookAllowPrivateNetworks(true)}, whOpts...)
	webhooks := events.NewWebhookRegistry(whOpts...)
	src, yield := events.NewYieldingSource[fakePayload](events.EventDef{
		Name:        "fake.event",
		Description: "test source",
		Delivery:    []string{"push", "poll", "webhook"},
	})

	srv := server.NewServer(
		core.ServerInfo{Name: "eventsclient-test", Version: "0.1.0"},
		server.WithSubscriptions(),
	)
	events.Register(events.Config{
		Sources:                  []events.EventSource{src},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal", // SDK tests don't wire auth; γ-2 spec gate would reject otherwise
	})

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "1.0"})
	require.NoError(t, c.Connect())
	return c, yield, webhooks
}

// TestSubscribe_AutoGeneratesSecretWhenEmpty verifies the spec contract
// the SDK upholds for application convenience: when SubscribeOptions.Secret
// is empty, the SDK CSPRNG-generates a spec-conformant whsec_ secret and
// both supplies it to the server AND uses it locally so the receiver can
// verify with the same value via Subscription.Secret(). Without this, an
// SDK user who forgets to set Secret would get a -32602 InvalidParams
// from the server (since the secret is REQUIRED per spec).
func TestSubscribe_AutoGeneratesSecretWhenEmpty(t *testing.T) {
	c, _, _ := stack(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
		// Secret intentionally omitted → SDK auto-generates
	})
	require.NoError(t, err)
	defer sub.Stop()

	got := sub.Secret()
	require.True(t, len(got) > 30 && got[:6] == "whsec_",
		"SDK must auto-generate a whsec_ value when Secret is empty; got %q", got)
}

// TestSubscribe_PreservesCallerSuppliedSecret verifies that when the
// application DOES supply a secret, the SDK uses it as-is rather than
// auto-generating a different one. Subscription.Secret() returns the
// caller's value so the receiver and the signer agree.
func TestSubscribe_PreservesCallerSuppliedSecret(t *testing.T) {
	c, _, _ := stack(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	supplied := events.GenerateSecret()
	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
		Secret:      supplied,
	})
	require.NoError(t, err)
	defer sub.Stop()

	assert.Equal(t, supplied, sub.Secret(),
		"SDK must preserve caller-supplied secret, not generate a new one")
}

// TestSubscribe_AutoRefreshFiresWithinShortTTL verifies the background
// refresh loop calls OnRefresh at least twice (initial + at least one
// scheduled refresh) within a short test window. Validates the SDK actually
// runs the loop and respects RefreshFactor.
func TestSubscribe_AutoRefreshFiresWithinShortTTL(t *testing.T) {
	c, _, _ := stack(t, events.WithWebhookTTL(2*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var refreshes int32
	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:     "fake.event",
		CallbackURL:   "http://localhost:1/sink",
		RefreshFactor: 0.4,
		OnRefresh:     func() { atomic.AddInt32(&refreshes, 1) },
	})
	require.NoError(t, err)
	defer sub.Stop()

	time.Sleep(2 * time.Second)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&refreshes), int32(2),
		"OnRefresh must fire for the initial subscribe AND at least one auto-refresh")
}

// TestSubscribe_RejectsRefreshFactorAboveOne verifies the option validator
// rejects nonsensical refresh factors at construction time rather than
// causing silent boundary races at runtime.
func TestSubscribe_RejectsRefreshFactorAboveOne(t *testing.T) {
	c, _, _ := stack(t)
	_, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:     "fake.event",
		CallbackURL:   "http://localhost:1/sink",
		RefreshFactor: 1.0,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RefreshFactor")
}

// TestReceiver_DeliversTypedEvents verifies the end-to-end happy path: a
// real subscribe + yield + webhook POST round-trips through the receiver
// and yields a decoded Event[fakePayload] on the channel.
func TestReceiver_DeliversTypedEvents(t *testing.T) {
	c, yield, _ := stack(t)

	recv := eventsclient.NewReceiver[fakePayload]("")
	defer recv.Close()

	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: hookSrv.URL,
	})
	require.NoError(t, err)
	defer sub.Stop()
	recv.SetSecret(sub.Secret())

	require.NoError(t, yield(fakePayload{Msg: "hello"}))

	select {
	case ev := <-recv.Events():
		assert.Equal(t, "fake.event", ev.Name)
		assert.NotEmpty(t, ev.EventID)
		assert.True(t, ev.HasCursor(), "cursored source must deliver a non-nil cursor")
		assert.Equal(t, "hello", ev.Data.Msg)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for typed event delivery")
	}
}

// TestReceiver_RejectsBadSignature verifies that a delivery signed with the
// wrong secret is rejected at the HTTP layer (401) and does NOT show up on
// the typed channel.
func TestReceiver_RejectsBadSignature(t *testing.T) {
	c, yield, _ := stack(t)

	recv := eventsclient.NewReceiver[fakePayload]("the-wrong-secret")
	defer recv.Close()

	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: hookSrv.URL,
	})
	require.NoError(t, err)
	defer sub.Stop()
	// deliberately do NOT call recv.SetSecret(sub.Secret())

	require.NoError(t, yield(fakePayload{Msg: "should-be-rejected"}))

	select {
	case ev := <-recv.Events():
		t.Fatalf("receiver delivered an event despite signature mismatch: %+v", ev)
	case <-time.After(500 * time.Millisecond):
		// expected — silent rejection at the HTTP layer
	}
}

// TestReceiver_AcceptsStandardWebhooksHeaders verifies that switching the
// server's header mode to StandardWebhooks doesn't require any client-side
// configuration — the receiver auto-detects the header set.
func TestReceiver_AcceptsStandardWebhooksHeaders(t *testing.T) {
	c, yield, _ := stack(t, events.WithWebhookHeaderMode(events.StandardWebhooks))

	recv := eventsclient.NewReceiver[fakePayload]("")
	defer recv.Close()

	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: hookSrv.URL,
	})
	require.NoError(t, err)
	defer sub.Stop()
	recv.SetSecret(sub.Secret())

	require.NoError(t, yield(fakePayload{Msg: "via-standard-webhooks"}))

	select {
	case ev := <-recv.Events():
		assert.Equal(t, "via-standard-webhooks", ev.Data.Msg)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for delivery via Standard Webhooks headers")
	}
}

// TestReceiver_CloseUnblocksRangeOverEvents verifies that Close cleanly
// closes the channel so consumers ranging over Events() exit. Without this
// a long-lived subscriber would leak goroutines on shutdown.
func TestReceiver_CloseUnblocksRangeOverEvents(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload]("")
	done := make(chan struct{})
	go func() {
		for range recv.Events() {
		}
		close(done)
	}()
	recv.Close()
	select {
	case <-done:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ranging over Events() did not exit after Close")
	}
}

// TestReceiver_DropsOnFullChannel verifies that a slow consumer doesn't
// stall the inbound HTTP handler — the receiver drops new deliveries when
// the buffered channel is full and bumps the Rejected counter.
func TestReceiver_DropsOnFullChannel(t *testing.T) {
	c, yield, _ := stack(t)

	recv := eventsclient.NewReceiver[fakePayload]("")

	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: hookSrv.URL,
	})
	require.NoError(t, err)
	defer sub.Stop()
	recv.SetSecret(sub.Secret())

	for i := 0; i < 80; i++ {
		_ = yield(fakePayload{Msg: "x"})
	}
	time.Sleep(500 * time.Millisecond)

	assert.Greater(t, recv.Rejected(), uint64(0),
		"slow consumer must trigger drops, not block the registry")
	recv.Close()
}

// TestSubscribe_StopsOnContextCancel verifies the refresh loop honours
// parent context cancellation. Without this an SDK consumer that cancels
// its context would still see refresh traffic until Stop was called.
func TestSubscribe_StopsOnContextCancel(t *testing.T) {
	c, _, _ := stack(t, events.WithWebhookTTL(2*time.Second))

	ctx, cancel := context.WithCancel(context.Background())

	var refreshes int32
	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
		EventName:     "fake.event",
		CallbackURL:   "http://localhost:1/sink",
		RefreshFactor: 0.4,
		OnRefresh:     func() { atomic.AddInt32(&refreshes, 1) },
	})
	require.NoError(t, err)

	cancel()

	time.Sleep(2 * time.Second)
	atBefore := atomic.LoadInt32(&refreshes)
	time.Sleep(1 * time.Second)
	atAfter := atomic.LoadInt32(&refreshes)
	assert.Equal(t, atBefore, atAfter,
		"refresh loop must stop firing once the parent context is cancelled")

	sub.Stop()
}

// TestSubscribe_StopIsIdempotent verifies multiple Stop calls don't panic.
func TestSubscribe_StopIsIdempotent(t *testing.T) {
	c, _, _ := stack(t)
	sub, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
		EventName:   "fake.event",
		CallbackURL: "http://localhost:1/sink",
	})
	require.NoError(t, err)
	sub.Stop()
	assert.NotPanics(t, func() { sub.Stop() })
}

// TestReceiver_GoneAfterClose verifies the receiver returns 410 on
// post-Close deliveries rather than silently accepting them.
func TestReceiver_GoneAfterClose(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload]("")
	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	recv.Close()

	body, _ := json.Marshal(events.Event{EventID: "evt_1", Name: "fake.event"})
	resp, err := http.Post(hookSrv.URL, "application/json", strings.NewReader(string(body)))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode)
	_, _ = io.Copy(io.Discard, resp.Body)
}
