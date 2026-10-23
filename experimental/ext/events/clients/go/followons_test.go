package eventsclient_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceiver_EventCarriesSubscriptionID(t *testing.T) {
	recv := eventsclient.NewReceiver[fakePayload](dedupSecret)
	defer recv.Close()

	postSigned(t, recv, "evt-1", "sub-a", time.Now())
	postSigned(t, recv, "evt-2", "", time.Now())
	assert.Equal(t, "sub-a", (<-recv.Events()).SubscriptionID)
	assert.Empty(t, (<-recv.Events()).SubscriptionID)
}

// Against a real server: the header the registry stamps is the id Subscribe
// read back, so a receiver can match a delivery to its Subscription.
func TestReceiver_SubscriptionIDMatchesSubscribe(t *testing.T) {
	c, yield, _ := stack(t)
	recv := eventsclient.NewReceiver[fakePayload]("")
	defer recv.Close()
	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	sub, err := eventsclient.Subscribe(t.Context(), c, eventsclient.SubscribeOptions{EventName: "fake.event", CallbackURL: hookSrv.URL})
	require.NoError(t, err)
	defer sub.Stop()
	recv.SetSecret(sub.Secret())

	require.NoError(t, yield(context.Background(), fakePayload{Msg: "hi"}))
	select {
	case ev := <-recv.Events():
		assert.Equal(t, sub.ID(), ev.SubscriptionID)
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
	}
}

func TestSubscription_StopAndUnsubscribeEndsDeliveryAtOnce(t *testing.T) {
	c, yield, _ := stack(t)
	recv := eventsclient.NewReceiver[fakePayload]("")
	defer recv.Close()
	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	sub, err := eventsclient.Subscribe(t.Context(), c, eventsclient.SubscribeOptions{EventName: "fake.event", CallbackURL: hookSrv.URL})
	require.NoError(t, err)
	recv.SetSecret(sub.Secret())

	require.NoError(t, sub.StopAndUnsubscribe(t.Context()))
	require.NoError(t, yield(context.Background(), fakePayload{Msg: "after"}))
	select {
	case ev := <-recv.Events():
		t.Fatalf("delivered after StopAndUnsubscribe: %+v", ev)
	case <-time.After(500 * time.Millisecond):
	}

	assert.Error(t, sub.StopAndUnsubscribe(t.Context()), "a second call finds nothing to remove upstream")
}

// Stop alone leaves the subscription upstream, which is why
// StopAndUnsubscribe exists.
func TestSubscription_StopAloneKeepsDelivering(t *testing.T) {
	c, yield, _ := stack(t)
	recv := eventsclient.NewReceiver[fakePayload]("")
	defer recv.Close()
	hookSrv := httptest.NewServer(recv)
	defer hookSrv.Close()

	sub, err := eventsclient.Subscribe(t.Context(), c, eventsclient.SubscribeOptions{EventName: "fake.event", CallbackURL: hookSrv.URL})
	require.NoError(t, err)
	recv.SetSecret(sub.Secret())
	sub.Stop()

	require.NoError(t, yield(context.Background(), fakePayload{Msg: "after stop"}))
	select {
	case ev := <-recv.Events():
		assert.Equal(t, "after stop", ev.Data.Msg)
	case <-time.After(2 * time.Second):
		t.Fatal("Stop is documented to leave the subscription in place")
	}
}
