package events

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotFoundData_WireShape pins the JSON wire shape for the typed
// data payload on -32011 NotFound responses. The Kind discriminator
// is what lets clients tell "unknown event" apart from "unknown
// subscription" without parsing the human-readable message.
func TestNotFoundData_WireShape(t *testing.T) {
	cases := []struct {
		name string
		kind string
		want string
	}{
		{"event", "event", `{"kind":"event"}`},
		{"subscription", "subscription", `{"kind":"subscription"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(NotFoundData{Kind: tc.kind})
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(raw))
		})
	}
}

// TestResourceExhaustedData_WireShape pins the JSON wire shape for
// the typed data payload on -32013 ResourceExhausted responses,
// including the omitempty behavior on Max: a zero Max signals "limit
// hit, ceiling not disclosed" and must not appear in the encoded JSON.
func TestResourceExhaustedData_WireShape(t *testing.T) {
	t.Run("with cap", func(t *testing.T) {
		raw, err := json.Marshal(ResourceExhaustedData{Limit: "subscriptions", Max: 5})
		require.NoError(t, err)
		assert.JSONEq(t, `{"limit":"subscriptions","max":5}`, string(raw))
	})
	t.Run("cap omitted", func(t *testing.T) {
		raw, err := json.Marshal(ResourceExhaustedData{Limit: "subscriptions"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"limit":"subscriptions"}`, string(raw),
			"Max=0 must be omitted so 'cap unknown' is distinguishable from 'cap is zero'")
	})
}

// TestUnsupportedData_WireShape pins the JSON wire shape for -32014
// Unsupported. Value is omitempty so a server can reject a feature
// outright (no specific value carried) by passing Value="".
func TestUnsupportedData_WireShape(t *testing.T) {
	t.Run("feature + value", func(t *testing.T) {
		raw, err := json.Marshal(UnsupportedData{Feature: "deliveryMode", Value: "push"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"feature":"deliveryMode","value":"push"}`, string(raw))
	})
	t.Run("feature only", func(t *testing.T) {
		raw, err := json.Marshal(UnsupportedData{Feature: "deliveryMode"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"feature":"deliveryMode"}`, string(raw))
	})
}

// TestCallbackEndpointErrorData_WireShape pins the JSON wire shape
// for -32015 CallbackEndpointError. The Reason draws from
// DeliveryErrorBucket so subscribe-time and delivery-time failures
// share one vocabulary on the wire.
func TestCallbackEndpointErrorData_WireShape(t *testing.T) {
	raw, err := json.Marshal(CallbackEndpointErrorData{Reason: "connection_refused"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"reason":"connection_refused"}`, string(raw))
}

// TestErrorConstructors_AttachTypedData verifies the helper constructors
// in errors.go actually wire the typed struct into the response's
// Error.Data field. Without this test a future contributor could
// "simplify" a helper to NewErrorResponse-without-data and lose the
// discriminator silently — wire-shape tests above wouldn't fire on
// production code paths.
func TestErrorConstructors_AttachTypedData(t *testing.T) {
	id := json.RawMessage(`1`)

	t.Run("NotFound", func(t *testing.T) {
		resp := newNotFoundError(id, "subscription", "NotFound")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrCodeNotFound, resp.Error.Code)
		assert.Equal(t, NotFoundData{Kind: "subscription"}, resp.Error.Data)
	})

	t.Run("ResourceExhausted with cap", func(t *testing.T) {
		resp := newResourceExhaustedError(id, "subscriptions", 7, "at cap")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrCodeResourceExhausted, resp.Error.Code)
		assert.Equal(t, ResourceExhaustedData{Limit: "subscriptions", Max: 7}, resp.Error.Data)
	})

	t.Run("Unsupported", func(t *testing.T) {
		resp := newUnsupportedError(id, "deliveryMode", "push", "Unsupported")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrCodeUnsupported, resp.Error.Code)
		assert.Equal(t, UnsupportedData{Feature: "deliveryMode", Value: "push"}, resp.Error.Data)
	})

	t.Run("CallbackEndpointError", func(t *testing.T) {
		resp := newCallbackEndpointError(id, "tls_error", "TLS handshake failed")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrCodeCallbackEndpointError, resp.Error.Code)
		assert.Equal(t, CallbackEndpointErrorData{Reason: "tls_error"}, resp.Error.Data)
	})

	t.Run("Forbidden has no data payload by design", func(t *testing.T) {
		resp := newForbiddenError(id, "Forbidden")
		require.NotNil(t, resp.Error)
		assert.Equal(t, ErrCodeForbidden, resp.Error.Code)
		assert.Nil(t, resp.Error.Data,
			"Forbidden carries no discriminators today; if the spec adds one, update this test and the helper together")
	})
}
