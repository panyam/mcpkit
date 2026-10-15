package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for Registry.ChangeSchema, SEP-3415 § Event type removal and breaking
// changes: an additive change keeps every subscription, a breaking one ends
// them with -32026 {feature, reason: "schema_changed"}, and both announce
// notifications/events/list_changed.

var (
	oldInput = map[string]any{
		"type":       "object",
		"properties": map[string]any{"severity": map[string]any{"type": "string"}},
	}
	extendedInput = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"severity": map[string]any{"type": "string"},
			"team":     map[string]any{"type": "string"},
		},
	}
	retypedInput = map[string]any{
		"type":       "object",
		"properties": map[string]any{"severity": map[string]any{"type": "integer"}},
	}
)

// schemaChangeStack registers one streamable source with a live push
// subscriber and one webhook subscription, and returns what each receives.
func schemaChangeStack(t *testing.T) (*Registry, *WebhookRegistry, *captureBroadcast, <-chan SubscriberEvent, <-chan []byte) {
	t.Helper()
	reg, webhooks, cap := buildRemovalStack(t)
	src, _ := NewYieldingSource[map[string]any](EventDef{
		Name:        "shape.event",
		Delivery:    []string{"poll", "push", "webhook"},
		InputSchema: oldInput,
	})
	require.NoError(t, reg.AddSource(src))
	ch, _ := src.Subscribe(context.Background(), SubscribeOpts{})

	got := make(chan []byte, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		got <- buf
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)
	webhooks.Register(RegisterParams{
		CanonicalKey: []byte("shape-key"),
		DerivedID:    "sub_shape",
		URL:          receiver.URL,
		Secret:       generateSecret(),
		EventName:    "shape.event",
		Principal:    "test-principal",
	})
	return reg, webhooks, cap, ch, got
}

func TestChangeSchema_AdditiveKeepsSubscriptions(t *testing.T) {
	reg, webhooks, cap, ch, got := schemaChangeStack(t)
	before := cap.count("notifications/events/list_changed")

	require.NoError(t, reg.ChangeSchema("shape.event", SchemaChange{InputSchema: extendedInput}))

	select {
	case se := <-ch:
		t.Fatalf("an additive change reached the push subscriber: %#v", se)
	case body := <-got:
		t.Fatalf("an additive change reached the webhook: %s", body)
	case <-time.After(300 * time.Millisecond):
	}
	assert.Len(t, webhooks.Targets(), 1, "an additive change MUST NOT end webhook subscriptions")
	assert.Equal(t, before+1, cap.count("notifications/events/list_changed"))

	def, ok := reg.Def("shape.event")
	require.True(t, ok)
	assert.Equal(t, extendedInput, def.InputSchema, "events/list advertises the new schema")
	assert.Equal(t, []string{"poll", "push", "webhook"}, def.Delivery, "delivery is untouched")
}

func TestChangeSchema_BreakingTerminatesWithSchemaChanged(t *testing.T) {
	reg, webhooks, cap, ch, got := schemaChangeStack(t)
	before := cap.count("notifications/events/list_changed")

	require.NoError(t, reg.ChangeSchema("shape.event", SchemaChange{InputSchema: retypedInput, Breaking: true}))

	select {
	case se, ok := <-ch:
		require.True(t, ok, "subscriber channel closed without a terminal signal")
		require.NotNil(t, se.Terminated)
		assert.Equal(t, ErrCodeUnsupported, se.Terminated.Code,
			"a breaking change is -32026, not -32024: the principal's access did not change")
		data, ok := se.Terminated.Data.(UnsupportedData)
		require.True(t, ok, "terminal signal must carry UnsupportedData; got %#v", se.Terminated.Data)
		assert.Equal(t, UnsupportedData{Feature: "inputSchema", Reason: ReasonSchemaChanged}, data)
	case <-time.After(3 * time.Second):
		t.Fatal("no terminal signal delivered to the push subscriber")
	}

	select {
	case body := <-got:
		var env struct {
			Type  string `json:"type"`
			Error struct {
				Code int            `json:"code"`
				Data map[string]any `json:"data"`
			} `json:"error"`
		}
		require.NoError(t, json.Unmarshal(body, &env))
		assert.Equal(t, "terminated", env.Type)
		assert.Equal(t, ErrCodeUnsupported, env.Error.Code)
		assert.Equal(t, "inputSchema", env.Error.Data["feature"])
		assert.Equal(t, ReasonSchemaChanged, env.Error.Data["reason"])
	case <-time.After(3 * time.Second):
		t.Fatal("no terminated envelope delivered to the webhook")
	}
	assert.Empty(t, webhooks.Targets(), "terminated webhook subscriptions are dropped")
	assert.Equal(t, before+1, cap.count("notifications/events/list_changed"))
}

func TestChangeSchema_BreakingPayloadNamesPayloadSchema(t *testing.T) {
	reg, _, _, ch, _ := schemaChangeStack(t)
	require.NoError(t, reg.ChangeSchema("shape.event", SchemaChange{
		PayloadSchema: map[string]any{"type": "object"},
		Breaking:      true,
	}))
	select {
	case se := <-ch:
		require.NotNil(t, se.Terminated)
		assert.Equal(t, "payloadSchema", se.Terminated.Data.(UnsupportedData).Feature)
	case <-time.After(3 * time.Second):
		t.Fatal("no terminal signal")
	}
}

// The new inputSchema is the one requests validate against.
func TestChangeSchema_ValidatesAgainstTheNewInputSchema(t *testing.T) {
	reg, _, _, _, _ := schemaChangeStack(t)
	old := map[string]any{"severity": "P1"}
	require.Nil(t, validateArguments(json.RawMessage(`1`), reg, "shape.event", old))

	require.NoError(t, reg.ChangeSchema("shape.event", SchemaChange{InputSchema: extendedInput}))
	assert.Nil(t, validateArguments(json.RawMessage(`1`), reg, "shape.event", old),
		"after an additive change, previously accepted arguments are still accepted")

	require.NoError(t, reg.ChangeSchema("shape.event", SchemaChange{InputSchema: retypedInput, Breaking: true}))
	resp := validateArguments(json.RawMessage(`1`), reg, "shape.event", old)
	require.NotNil(t, resp, "after the retype, a string severity no longer validates")
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestChangeSchema_RejectsWithoutAnnouncing(t *testing.T) {
	reg, _, cap, _, _ := schemaChangeStack(t)
	before := cap.count("notifications/events/list_changed")

	assert.Error(t, reg.ChangeSchema("no.such.event", SchemaChange{InputSchema: extendedInput}))
	assert.Error(t, reg.ChangeSchema("shape.event", SchemaChange{}), "a change with no schema")
	assert.Error(t, reg.ChangeSchema("shape.event", SchemaChange{InputSchema: map[string]any{"type": 7}}),
		"an inputSchema that does not compile")
	assert.Equal(t, before, cap.count("notifications/events/list_changed"))

	def, _ := reg.Def("shape.event")
	assert.Equal(t, oldInput, def.InputSchema, "a rejected change leaves the descriptor alone")
}
