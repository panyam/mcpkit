package events

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamMeta_CarriesTheParentRequestID pins the routing key a client with
// two concurrent streams needs. mcpkit carried the id only as a top-level
// `requestId`, which is this extension's own spelling; nothing else in MCP
// reads it, so a multi-stream client had no supported way to tell which
// subscription a notification belonged to.
func TestStreamMeta_CarriesTheParentRequestID(t *testing.T) {
	meta := streamMeta(json.RawMessage(`7`))
	require.NotNil(t, meta)
	assert.Equal(t, float64(7), meta[core.MetaKeySubscriptionID],
		"a numeric JSON-RPC id must survive as a number")

	meta = streamMeta(json.RawMessage(`"req-1"`))
	assert.Equal(t, "req-1", meta[core.MetaKeySubscriptionID],
		"a string JSON-RPC id must survive as a string")

	assert.Nil(t, streamMeta(nil), "no id means no meta rather than a null entry")
}

// TestMergeStreamMeta_DoesNotMutateTheEvent guards the sharing hazard. A
// source's Event.Meta map reaches every subscriber, so writing one stream's
// request id into it would hand that id to all the others.
func TestMergeStreamMeta_DoesNotMutateTheEvent(t *testing.T) {
	eventMeta := map[string]any{"category": "ops"}
	merged := mergeStreamMeta(eventMeta, json.RawMessage(`1`))

	assert.Equal(t, "ops", merged["category"], "the event's own keys survive")
	assert.Equal(t, float64(1), merged[core.MetaKeySubscriptionID])
	assert.NotContains(t, eventMeta, core.MetaKeySubscriptionID,
		"the source's map must not gain the per-subscriber id")

	other := mergeStreamMeta(eventMeta, json.RawMessage(`2`))
	assert.Equal(t, float64(2), other[core.MetaKeySubscriptionID])
	assert.Equal(t, float64(1), merged[core.MetaKeySubscriptionID],
		"two subscribers must not see each other's id")
}

// TestStreamNotifications_AllCarrySubscriptionID pins the rule's scope: the
// spec says every notifications/events/* frame, not just the event ones.
func TestStreamNotifications_AllCarrySubscriptionID(t *testing.T) {
	id := json.RawMessage(`3`)
	cursor := "c1"

	for name, params := range map[string]any{
		"active":     activeNotifParams{RequestID: id, Cursor: &cursor, Meta: streamMeta(id)},
		"event":      eventNotifParams{RequestID: id, EventID: "e1", Meta: mergeStreamMeta(nil, id)},
		"heartbeat":  heartbeatNotifParams{RequestID: id, Cursor: &cursor, Meta: streamMeta(id)},
		"error":      errorNotifParams{RequestID: id, Meta: streamMeta(id)},
		"terminated": errorNotifParams{RequestID: id, Meta: streamMeta(id)},
	} {
		raw, err := json.Marshal(params)
		require.NoError(t, err, name)
		assert.Contains(t, string(raw), core.MetaKeySubscriptionID,
			"notifications/events/%s must carry the subscription id", name)
	}
}
