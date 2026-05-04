package events

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
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

// TestPoll_MaxAgeFiltersOldEvents verifies the spec's maxAge replay
// floor per §"Cursor Lifecycle" → "Bounding replay with maxAge" L529.
// Server discards events whose timestamp predates `now - maxAge` and
// signals the gap via Truncated=true. Without filtering, a long-offline
// client reconnects with a stale cursor and triggers an unbounded
// backfill — maxAge bounds the worst case.
//
// This test uses a real YieldingSource registered on a stack so the
// timestamps come from the actual yield path (Now-based per yield call).
func TestPoll_MaxAgeFiltersOldEvents(t *testing.T) {
	srv, src := buildPollFilterStack(t)

	// Yield 2 events that are "old" (manipulate their timestamp via
	// the underlying source) and 2 fresh ones. Easier to backdate
	// the events post-yield by walking the source's ring directly —
	// but for δ-2's purposes we just yield them at different real
	// times and use a small maxAge to filter.

	// Yield "old" events with a backdated timestamp 10s in the past.
	src.entries = append(src.entries,
		yieldedEntry[fakeFilterPayload]{
			data: fakeFilterPayload{Msg: "old1"},
			event: Event{
				EventID:   "evt_old_1",
				Name:      "fake.event",
				Timestamp: time.Now().Add(-10 * time.Second).Format(time.RFC3339),
				Data:      json.RawMessage(`{"msg":"old1"}`),
				Cursor:    cursorPtr("1"),
			},
		},
		yieldedEntry[fakeFilterPayload]{
			data: fakeFilterPayload{Msg: "old2"},
			event: Event{
				EventID:   "evt_old_2",
				Name:      "fake.event",
				Timestamp: time.Now().Add(-8 * time.Second).Format(time.RFC3339),
				Data:      json.RawMessage(`{"msg":"old2"}`),
				Cursor:    cursorPtr("2"),
			},
		},
	)
	// Fresh events.
	src.entries = append(src.entries,
		yieldedEntry[fakeFilterPayload]{
			data: fakeFilterPayload{Msg: "fresh1"},
			event: Event{
				EventID:   "evt_fresh_1",
				Name:      "fake.event",
				Timestamp: time.Now().Format(time.RFC3339),
				Data:      json.RawMessage(`{"msg":"fresh1"}`),
				Cursor:    cursorPtr("3"),
			},
		},
	)

	// Poll with maxAge=5s — should drop both old events, keep the fresh one.
	rawReq, _ := json.Marshal(map[string]any{
		"name":   "fake.event",
		"cursor": "0",
		"maxAge": 5,
	})
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/poll", Params: rawReq,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	body, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var bodyMap map[string]any
	require.NoError(t, json.Unmarshal(body, &bodyMap))

	events, _ := bodyMap["events"].([]any)
	assert.Len(t, events, 1, "maxAge=5s must drop the 2 old events; got %d kept", len(events))
	assert.Equal(t, true, bodyMap["truncated"], "filtering must set truncated=true to signal the gap")
}

// TestPoll_MaxAgeZeroMeansNoFilter is the counter-test: maxAge omitted
// or set to 0 must NOT filter anything. Catches over-eager filtering
// that would silently drop events when the client expects everything.
func TestPoll_MaxAgeZeroMeansNoFilter(t *testing.T) {
	srv, src := buildPollFilterStack(t)
	// Yield one ancient event.
	src.entries = append(src.entries,
		yieldedEntry[fakeFilterPayload]{
			data: fakeFilterPayload{Msg: "ancient"},
			event: Event{
				EventID:   "evt_ancient",
				Name:      "fake.event",
				Timestamp: time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
				Data:      json.RawMessage(`{"msg":"ancient"}`),
				Cursor:    cursorPtr("1"),
			},
		},
	)

	rawReq, _ := json.Marshal(map[string]any{
		"name":   "fake.event",
		"cursor": "0",
		// maxAge intentionally omitted
	})
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/poll", Params: rawReq,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)

	body, _ := json.Marshal(resp.Result)
	var bodyMap map[string]any
	_ = json.Unmarshal(body, &bodyMap)
	events, _ := bodyMap["events"].([]any)
	assert.Len(t, events, 1, "no maxAge means no filter — ancient events must still be returned")
	// Truncated may or may not be present depending on omitempty
	if v, ok := bodyMap["truncated"]; ok {
		assert.Equal(t, false, v, "no filtering happened — truncated must be false (or omitted)")
	}
}

// --- shared helpers for δ-2 tests ---

type fakeFilterPayload struct {
	Msg string `json:"msg"`
}

func cursorPtr(s string) *string { return &s }

// buildPollFilterStack builds a stack with a writable YieldingSource so
// tests can inject events with crafted timestamps. Returns the source
// directly (not a yield closure) so tests can manipulate the buffer.
func buildPollFilterStack(t *testing.T) (*server.Server, *YieldingSource[fakeFilterPayload]) {
	t.Helper()
	src, _ := NewYieldingSource[fakeFilterPayload](EventDef{Name: "fake.event"})
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{src},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`0`), Method: "initialize", Params: initParams,
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	_, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", Method: "notifications/initialized",
	})
	require.NoError(t, err)
	return srv, src
}

// TestSubscribe_AcceptsMaxAge verifies the spec's maxAge floor on
// events/subscribe per §"Cursor Lifecycle" → "Bounding replay with
// maxAge" L529. Client supplies a per-subscription replay floor that
// the server records on the WebhookTarget for use on (future) reconnect
// — δ-3 plumbs the field, ζ wires the actual replay-with-floor logic.
//
// Without storing maxAge on the target, a long-offline subscriber's
// reconnect would replay everything the cursor still covers; with it,
// the server can bound replay to the requested floor.
func TestSubscribe_AcceptsMaxAge(t *testing.T) {
	srv, webhooks := buildAuthGateStack(t, "test-principal")
	params := validSubscribeParams()
	params["maxAge"] = 300
	resp := dispatchSubscribe(t, srv, params)
	require.Nil(t, resp.Error, "subscribe with maxAge must succeed; got %+v", resp.Error)

	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.Equal(t, 300, targets[0].MaxAgeSeconds,
		"maxAge from subscribe request must be stored on WebhookTarget for reconnect-replay bounding")
}

// TestSubscribe_DefaultsMaxAgeToZero is the counter-test: when maxAge is
// omitted, the target stores 0 — no floor, replay is unbounded by maxAge
// (still bounded by cursor + source retention). Catches over-eager
// defaulting that would silently shrink replay for callers who didn't
// opt in.
func TestSubscribe_DefaultsMaxAgeToZero(t *testing.T) {
	srv, webhooks := buildAuthGateStack(t, "test-principal")
	params := validSubscribeParams()
	// maxAge intentionally omitted
	resp := dispatchSubscribe(t, srv, params)
	require.Nil(t, resp.Error)

	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.Equal(t, 0, targets[0].MaxAgeSeconds,
		"omitted maxAge must default to 0 (no floor)")
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
