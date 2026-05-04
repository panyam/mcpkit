package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPollResponse_FlatShape pins the events/poll success-response wire shape
// against the spec: top-level fields {events, cursor, hasMore, truncated,
// nextPollSeconds}, NO results[] wrapper, NO per-result `id`. Failing this
// test means a client decoding the spec shape would not find its data — or
// would find it under the legacy nested key path.
//
// The legacy results[] wrapper was a partial-success container from the
// batching era (one entry per subscription, each with its own error field).
// Phase 1 dropped batching at the protocol level; α drops the now-vestigial
// wrapper at the wire level too.
func TestPollResponse_FlatShape(t *testing.T) {
	cursor := "cursor_xyz"
	wire := pollResultWire{
		Events:          []Event{{EventID: "evt_1", Name: "demo", Timestamp: "t", Data: json.RawMessage(`{}`), Cursor: &cursor}},
		Cursor:          &cursor,
		HasMore:         false,
		Truncated:       false,
		NextPollSeconds: 5,
	}
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	body := string(raw)

	// Required top-level keys per spec.
	assert.Contains(t, body, `"events":`, "spec field events missing at top level")
	assert.Contains(t, body, `"cursor":`, "spec field cursor missing at top level")
	assert.Contains(t, body, `"hasMore":`, "spec field hasMore missing at top level")
	assert.Contains(t, body, `"nextPollSeconds":`, "spec field nextPollSeconds missing at top level")

	// No legacy wrapper or wrapper-only keys.
	assert.False(t, strings.Contains(body, `"results"`),
		"results[] wrapper must be gone — spec has flat top-level shape: got %s", body)
	assert.False(t, strings.Contains(body, `"id":`),
		"per-result id must be gone — there is no longer a per-sub id in the response: got %s", body)
}

// TestPollResponse_FlatShape_Truncated verifies the truncated field appears
// at top level (not nested in a wrapper) when the server signals a gap.
// The spec uses one signal across modes; without this test passing, a
// spec-conformant client checking response.truncated would never see true.
func TestPollResponse_FlatShape_Truncated(t *testing.T) {
	cursor := "cursor_after_gap"
	wire := pollResultWire{
		Events:          nil,
		Cursor:          &cursor,
		HasMore:         false,
		Truncated:       true,
		NextPollSeconds: 5,
	}
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	body := string(raw)

	assert.Contains(t, body, `"truncated":true`, "truncated:true must appear at top level when set")
	assert.False(t, strings.Contains(body, `"cursorGap"`),
		"legacy cursorGap field must be gone: got %s", body)
}

// TestEventNotFound_UsesSpecCode verifies the EventNotFound error response
// uses the spec-mandated code -32011 (ErrCodeEventNotFound), not the legacy
// -32001 which collides with base MCP ResourceNotFound. The spec reserves
// -32011..-32017 for events errors specifically.
//
// The spec's flat-response shape means EventNotFound surfaces as a
// top-level JSON-RPC error, not an embedded per-result error from the
// legacy partial-success model.
func TestEventNotFound_UsesSpecCode(t *testing.T) {
	id := json.RawMessage(`1`)
	resp := core.NewErrorResponse(id, ErrCodeEventNotFound, "EventNotFound")
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	body := string(raw)

	assert.Contains(t, body, `"code":-32011`, "EventNotFound must use spec code -32011")
	assert.Contains(t, body, `"message":"EventNotFound"`)
	assert.False(t, strings.Contains(body, `"code":-32001`),
		"legacy -32001 code must not appear (collides with base MCP ResourceNotFound)")
}

// TestPoll_FlatRequestShape pins the spec contract for events/poll
// REQUEST shape per §"Poll-Based Delivery" → "Request: events/poll"
// L139-149: top-level {name, cursor?, maxEvents?, params?} — no
// subscriptions[] wrapper. δ-1 dropped the wrapper after phase 1
// removed batching at the protocol level. Failing this test means
// a spec-conformant client's request would not parse.
func TestPoll_FlatRequestShape(t *testing.T) {
	srv := buildSecretValidationStack(t)
	rawReq, err := json.Marshal(map[string]any{
		"name":   "fake.event",
		"cursor": "0",
	})
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/poll", Params: rawReq,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "flat top-level shape must be accepted; got %+v", resp.Error)
}

// TestPoll_RejectsLegacyWrapper verifies that a request still using the
// pre-δ {subscriptions: [...]} wrapper gets a -32602 with a message
// pointing at the spec change. Without this helpful diagnostic, an old
// SDK sending the legacy shape would just see "name is required" and
// have no clue why.
func TestPoll_RejectsLegacyWrapper(t *testing.T) {
	srv := buildSecretValidationStack(t)
	rawReq, err := json.Marshal(map[string]any{
		"subscriptions": []map[string]any{
			{"name": "fake.event", "cursor": "0"},
		},
	})
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/poll", Params: rawReq,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Error, "legacy wrapper must be rejected, not silently parsed as empty")
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "legacy",
		"error message should explain the wrapper is rejected; got %q", resp.Error.Message)
	assert.Contains(t, resp.Error.Message, "L139", "error should cite the spec section")
}

// TestInvalidCallbackUrl_UsesSpecCode verifies the InvalidCallbackUrl error
// uses spec code -32015, not the legacy -32005.
func TestInvalidCallbackUrl_UsesSpecCode(t *testing.T) {
	id := json.RawMessage(`1`)
	resp := core.NewErrorResponse(id, ErrCodeInvalidCallbackUrl, "url not allowed")
	raw, err := json.Marshal(resp)
	require.NoError(t, err)
	body := string(raw)

	assert.Contains(t, body, `"code":-32015`)
	assert.False(t, strings.Contains(body, `"code":-32005`),
		"legacy -32005 code must not appear")
}
