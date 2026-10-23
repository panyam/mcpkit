package eventsclient_test

import (
	"context"
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/stretchr/testify/assert"
)

func TestDelivery_QualifiedName(t *testing.T) {
	ev := events.Event{Name: "page.updated"}
	assert.Equal(t, "wikipedia/page.updated", eventsclient.Delivery{Server: "wikipedia", Event: ev}.QualifiedName())
	assert.Equal(t, "page.updated", eventsclient.Delivery{Event: ev}.QualifiedName())
}

func TestSinkFunc_Delivers(t *testing.T) {
	var got eventsclient.Delivery
	var sink eventsclient.EventSink = eventsclient.SinkFunc(func(_ context.Context, d eventsclient.Delivery) error {
		got = d
		return nil
	})
	assert.NoError(t, sink.Deliver(t.Context(), eventsclient.Delivery{Instruction: "summarize"}))
	assert.Equal(t, "summarize", got.Instruction)
}
