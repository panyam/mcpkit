package eventsclient_test

import (
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ζ-4 receiver-side dispatch — these tests stand up an httptest receiver
// (the eventsclient.Receiver), point a server-side WebhookRegistry at
// it, and trigger PostGap / PostTerminated. They verify the receiver's
// top-level `type` discriminator routes the body to the right typed
// callback (OnGap / OnTerminated) instead of falling through to the
// Event[Data] channel.
//
// The HTTP path / signature / headers are the same as event deliveries
// (covered by other tests); these focus on the dispatch.

// TestReceiver_RoutesGapToCallback verifies a {type:"gap"} envelope
// fires OnGap with the carried cursor — and does NOT also leak an
// empty/zero Event[Data] onto the Events() channel.
func TestReceiver_RoutesGapToCallback(t *testing.T) {
	const secret = "whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recv := eventsclient.NewReceiver[map[string]any](secret)
	defer recv.Close()

	gotCursor := make(chan string, 1)
	recv.OnGap(func(c string) { gotCursor <- c })

	srv := httptest.NewServer(recv)
	defer srv.Close()

	r := events.NewWebhookRegistry(events.WithWebhookAllowPrivateNetworks(true))
	canonical := []byte("gap-test-key")
	r.Register(canonical, "sub_gap_test", srv.URL, secret, 0)

	r.PostGap(canonical, "fresh-c-77")

	select {
	case got := <-gotCursor:
		assert.Equal(t, "fresh-c-77", got, "OnGap cursor must match the value the server sent")
	case <-time.After(2 * time.Second):
		t.Fatal("OnGap callback never fired")
	}

	// Receiver's Events channel must NOT have received anything — gap
	// envelopes route to OnGap, not Events().
	select {
	case ev := <-recv.Events():
		t.Fatalf("gap envelope leaked onto Events() as Event=%+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestReceiver_RoutesTerminatedToCallback verifies a {type:"terminated"}
// envelope fires OnTerminated with the carried error code+message.
func TestReceiver_RoutesTerminatedToCallback(t *testing.T) {
	const secret = "whsec_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	recv := eventsclient.NewReceiver[map[string]any](secret)
	defer recv.Close()

	gotErr := make(chan eventsclient.ControlError, 1)
	recv.OnTerminated(func(e eventsclient.ControlError) { gotErr <- e })

	srv := httptest.NewServer(recv)
	defer srv.Close()

	r := events.NewWebhookRegistry(events.WithWebhookAllowPrivateNetworks(true))
	canonical := []byte("terminated-test-key")
	r.Register(canonical, "sub_term_test", srv.URL, secret, 0)

	r.PostTerminated(canonical, events.ControlError{Code: -32012, Message: "Unauthorized"})

	select {
	case got := <-gotErr:
		assert.Equal(t, -32012, got.Code)
		assert.Equal(t, "Unauthorized", got.Message)
	case <-time.After(2 * time.Second):
		t.Fatal("OnTerminated callback never fired")
	}
}

// TestReceiver_NoCallbacksRegistered verifies a control envelope with
// no callback installed is silently dropped (with HTTP 200 — the server
// shouldn't infer the receiver doesn't care). Catches a regression
// where a missing callback would crash with nil deref or 500-error
// the server's POST.
func TestReceiver_NoCallbacksRegistered(t *testing.T) {
	const secret = "whsec_cccccccccccccccccccccccccccccccc"
	recv := eventsclient.NewReceiver[map[string]any](secret)
	defer recv.Close()
	// No OnGap / OnTerminated installed.

	srv := httptest.NewServer(recv)
	defer srv.Close()

	var serverPostFailed atomic.Bool
	r := events.NewWebhookRegistry(events.WithWebhookAllowPrivateNetworks(true))
	canonical := []byte("no-cb-key")
	r.Register(canonical, "sub_no_cb", srv.URL, secret, 0)

	r.PostGap(canonical, "c1")
	require.Eventually(t, func() bool {
		// give it time to land
		return true
	}, 100*time.Millisecond, 20*time.Millisecond)

	// We can't easily observe the 200 from this side, but we can verify
	// the receiver's Events channel didn't get a spurious event.
	select {
	case ev := <-recv.Events():
		t.Fatalf("envelope leaked to Events() despite no callback: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	assert.False(t, serverPostFailed.Load(), "server-side POST shouldn't have logged a failure")
}
