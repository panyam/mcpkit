package events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestYieldGap_MarkerTravelsAlone pins the push-side counterpart of
// WebhookRegistry.PostGap. A source that learns of an upstream loss while it
// has nothing to deliver must be able to say so; before YieldGap the only way
// to set Truncated was to attach it to a real event, so a quiet source could
// not report a gap at all.
func TestYieldGap_MarkerTravelsAlone(t *testing.T) {
	src, _ := NewYieldingSource[fakeFilterPayload](EventDef{Name: "gap.source"})
	ch, _ := src.Subscribe(context.Background(), SubscribeOpts{SubscriptionID: "sub-1"})

	require.NoError(t, src.YieldGap())

	select {
	case se := <-ch:
		assert.True(t, se.Truncated, "the marker must carry Truncated")
		assert.Nil(t, se.Error, "a gap is not an error")
		assert.Nil(t, se.Terminated, "a gap does not end the subscription")
		assert.Empty(t, se.Event.EventID, "the marker carries no event of its own")
	case <-time.After(2 * time.Second):
		t.Fatal("YieldGap delivered nothing to a live subscriber")
	}
}

// TestYieldGap_SubscriptionSurvives covers the half that distinguishes a gap
// from the other two signals: delivery continues on the same subscription.
func TestYieldGap_SubscriptionSurvives(t *testing.T) {
	src, yield := NewYieldingSource[fakeFilterPayload](EventDef{Name: "gap.source"})
	ch, _ := src.Subscribe(context.Background(), SubscribeOpts{SubscriptionID: "sub-1"})

	require.NoError(t, src.YieldGap())
	<-ch

	require.NoError(t, yield(context.Background(), fakeFilterPayload{Msg: "after"}))
	select {
	case se := <-ch:
		assert.False(t, se.Truncated, "the truncation marker must not stick to later events")
		assert.Equal(t, "gap.source", se.Event.Name)
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not continue after a gap")
	}
}

// TestYieldGap_NoOpAfterTerminated matches YieldError's one-shot terminal
// semantic: nothing follows a terminated subscription.
func TestYieldGap_NoOpAfterTerminated(t *testing.T) {
	src, _ := NewYieldingSource[fakeFilterPayload](EventDef{Name: "gap.source"})
	ch, _ := src.Subscribe(context.Background(), SubscribeOpts{SubscriptionID: "sub-1"})

	require.NoError(t, src.YieldTerminated(EventDeliveryError{Code: -32011, Message: "gone"}))
	drainUntilClosed(t, ch)

	assert.NoError(t, src.YieldGap(), "YieldGap after termination must be a silent no-op")
}

func drainUntilClosed(t *testing.T, ch <-chan SubscriberEvent) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("subscriber channel never closed after YieldTerminated")
		}
	}
}
