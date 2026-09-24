package events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for the 2026-09-14 alignment against the MERGED design sketch
// (modelcontextprotocol/experimental-ext-triggers-events PR1, merged
// 2026-09-08). Three deltas, two of them four months stale:
//
//   - nextPollSeconds → nextPollMs, in milliseconds (spec 197c32b4)
//   - maxAge (seconds) → maxAgeMs (milliseconds)      (spec 197c32b4)
//   - inputSchema on events/list, and -32602 when arguments violate it
//
// The unit half of the rename is the part a name-only sed would miss, so
// every duration assertion here pins the VALUE and not just the key. A
// server that renamed the field and kept emitting seconds fails these.

// schemaSource advertises an inputSchema so the argument-validation path
// has something to reject against. Deliberately strict: additionalProperties
// false plus a required enum field, so all three failure shapes (wrong type,
// bad enum, missing required) are reachable from one source.
type schemaSource struct{}

func (schemaSource) Def() EventDef {
	return EventDef{
		Name:     "schema.event",
		Delivery: []string{"poll", "push", "webhook"},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"severity": map[string]any{
					"type": "string",
					"enum": []any{"low", "high"},
				},
				"room": map[string]any{"type": "string"},
			},
			"required":             []any{"severity"},
			"additionalProperties": false,
		},
	}
}

func (schemaSource) Poll(cursor string, limit int) PollResult { return PollResult{} }
func (schemaSource) Latest() string                           { return "" }

func buildSchemaStack(t *testing.T) (*server.Server, *WebhookRegistry) {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	webhooks := NewWebhookRegistry(WithWebhookAllowPrivateNetworks(true), WithUnsafeWebhookAllowPlaintextCallbacks())
	Register(Config{
		Sources:                  []EventSource{schemaSource{}},
		Webhooks:                 webhooks,
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	initParams := json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`0`), Method: "initialize", Params: core.NewRawJSON(initParams),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	_, err = srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", Method: "notifications/initialized",
	})
	require.NoError(t, err)
	return srv, webhooks
}

func dispatchMethod(t *testing.T, srv *server.Server, method string, params map[string]any) *core.Response {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: core.NewRawJSON(raw),
	})
	require.NoError(t, err)
	return resp
}

// TestPollResponse_NextPollMs_IsMillisecondsNotSeconds pins both halves of
// spec 197c32b4: the key is nextPollMs, and the value is on a millisecond
// scale. mcpkit emitted nextPollSeconds:5 for four months after the rename.
// A client applying the spec's 1000ms floor to a value of 5 would poll in a
// tight loop, which is exactly the failure the floor exists to prevent.
func TestPollResponse_NextPollMs_IsMillisecondsNotSeconds(t *testing.T) {
	srv, _ := buildSchemaStack(t)
	resp := dispatchMethod(t, srv, "events/poll", map[string]any{
		"name":      "schema.event",
		"arguments": map[string]any{"severity": "high"},
	})
	require.Nil(t, resp.Error)

	body, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"nextPollMs":`, "spec field nextPollMs missing at top level")
	assert.NotContains(t, string(body), `"nextPollSeconds"`, "pre-rename nextPollSeconds must be gone (spec 197c32b4)")

	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))
	next, ok := m["nextPollMs"].(float64)
	require.True(t, ok, "nextPollMs must be a number; got %#v", m["nextPollMs"])
	assert.GreaterOrEqual(t, next, 1000.0,
		"nextPollMs must be milliseconds and respect the spec's 1000ms client floor; got %v (looks like seconds)", next)
}

// TestSubscribe_AcceptsMaxAgeMsInMilliseconds pins the maxAgeMs rename on
// events/subscribe and, via Targets(), that the value is stored unscaled.
// Reading it back is what distinguishes a real unit change from a rename
// that quietly divides by 1000 on the way in.
func TestSubscribe_AcceptsMaxAgeMsInMilliseconds(t *testing.T) {
	srv, webhooks := buildSchemaStack(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	resp := dispatchMethod(t, srv, "events/subscribe", map[string]any{
		"name":      "schema.event",
		"arguments": map[string]any{"severity": "high"},
		"maxAgeMs":  300000,
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	})
	require.Nil(t, resp.Error, "subscribe with maxAgeMs MUST be accepted; got %+v", resp.Error)

	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.Equal(t, 300000, targets[0].MaxAgeMs,
		"maxAgeMs must be stored as the millisecond value the client sent, not rescaled")
}

// TestSubscribe_IgnoresLegacyMaxAgeKey is the counter-test to the rename.
// A server that still honors the pre-197c32b4 key would read 300 as the
// replay floor, and a client that has migrated to maxAgeMs would silently
// get a floor 1000x smaller than it asked for.
func TestSubscribe_IgnoresLegacyMaxAgeKey(t *testing.T) {
	srv, webhooks := buildSchemaStack(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	resp := dispatchMethod(t, srv, "events/subscribe", map[string]any{
		"name":      "schema.event",
		"arguments": map[string]any{"severity": "high"},
		"maxAge":    300,
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	})
	require.Nil(t, resp.Error)

	targets := webhooks.Targets()
	require.Len(t, targets, 1)
	assert.Zero(t, targets[0].MaxAgeMs,
		"legacy maxAge key must be ignored after the rename; got %d", targets[0].MaxAgeMs)
}

// TestEventsList_AdvertisesInputSchema covers spec L91: events/list declares
// an inputSchema for subscription arguments alongside payloadSchema, mirroring
// the inputSchema/arguments pairing on tools. Without it a client cannot tell
// what a subscription accepts, and -32602 becomes the only discovery mechanism.
func TestEventsList_AdvertisesInputSchema(t *testing.T) {
	srv, _ := buildSchemaStack(t)
	resp := dispatchMethod(t, srv, "events/list", map[string]any{})
	require.Nil(t, resp.Error)

	body, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"inputSchema"`, "events/list must advertise inputSchema (spec L91)")

	var m struct {
		Events []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(body, &m))

	// events/list also carries the SDK-reserved events.topology meta-source,
	// so select by name rather than by position.
	var found *map[string]any
	for i := range m.Events {
		if m.Events[i].Name == "schema.event" {
			found = &m.Events[i].InputSchema
		}
	}
	require.NotNil(t, found, "schema.event missing from events/list")
	require.NotNil(t, *found, "inputSchema must be present on the event descriptor")
	assert.Equal(t, "object", (*found)["type"])
	assert.Contains(t, *found, "properties")
}

// TestEventsList_OmitsInputSchemaWhenUndeclared keeps the field optional.
// Sources that take no subscription arguments should not be forced to
// advertise an empty schema, and omitempty is what keeps their wire
// unchanged by this PR.
func TestEventsList_OmitsInputSchemaWhenUndeclared(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal") // fakeSecretValidationSource declares none
	resp := dispatchMethod(t, srv, "events/list", map[string]any{})
	require.Nil(t, resp.Error)

	body, err := json.Marshal(resp.Result)
	require.NoError(t, err)

	// Scoped to the source under test rather than the whole list body. The
	// assertion used to lean on events.topology declaring no InputSchema
	// either, which stopped being true when the meta-source was brought up to
	// the descriptor contract the conformance suite checks (#1380). The rule
	// being pinned here is unchanged and still exactly enforced: a source that
	// declares nothing emits nothing.
	var list struct {
		Events []map[string]any `json:"events"`
	}
	require.NoError(t, json.Unmarshal(body, &list))

	var found bool
	for _, e := range list.Events {
		if e["name"] != "fake.event" {
			continue
		}
		found = true
		assert.NotContains(t, e, "inputSchema",
			"a source with no InputSchema must not emit the key at all")
	}
	require.True(t, found, "fake.event missing from events/list: %s", body)
}

// TestSubscribe_RejectsArgumentsViolatingInputSchema covers spec L115: -32602
// when arguments do not match the event's inputSchema. Three shapes, one per
// subtest, because each exercises a different jsonschema keyword and a server
// can plausibly get one right and the others wrong.
func TestSubscribe_RejectsArgumentsViolatingInputSchema(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"value outside enum", map[string]any{"severity": "catastrophic"}},
		{"wrong type", map[string]any{"severity": 42}},
		{"missing required", map[string]any{"room": "general"}},
		{"additional property", map[string]any{"severity": "low", "nope": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := buildSchemaStack(t)
			resp := dispatchMethod(t, srv, "events/subscribe", map[string]any{
				"name":      "schema.event",
				"arguments": tc.args,
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    receiver.URL,
					"secret": generateSecret(),
				},
			})
			require.NotNil(t, resp.Error, "arguments violating inputSchema MUST be rejected (spec L115)")
			assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code,
				"schema mismatch is -32602 InvalidParams, not %d", resp.Error.Code)
		})
	}
}

// TestSubscribe_RejectionCarriesValidationErrors pins the error.data payload.
// A bare -32602 tells the client it was wrong; the errors[] tells it which
// field, which is the difference between a fixable failure and a guess.
func TestSubscribe_RejectionCarriesValidationErrors(t *testing.T) {
	srv, _ := buildSchemaStack(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	resp := dispatchMethod(t, srv, "events/subscribe", map[string]any{
		"name":      "schema.event",
		"arguments": map[string]any{"severity": "catastrophic"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	})
	require.NotNil(t, resp.Error)
	require.NotNil(t, resp.Error.Data, "-32602 from schema validation must carry typed data")

	raw, err := json.Marshal(resp.Error.Data)
	require.NoError(t, err)
	var payload core.ValidationErrors
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.NotEmpty(t, payload.Errors, "validation failure must name at least one violation")
	assert.True(t, strings.Contains(payload.Errors[0].Path, "severity"),
		"violation should point at the offending field; got path %q", payload.Errors[0].Path)
}

// TestSubscribe_AcceptsArgumentsMatchingInputSchema is the green half. Without
// it, a validator that rejects everything would pass every test above.
func TestSubscribe_AcceptsArgumentsMatchingInputSchema(t *testing.T) {
	srv, _ := buildSchemaStack(t)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	resp := dispatchMethod(t, srv, "events/subscribe", map[string]any{
		"name":      "schema.event",
		"arguments": map[string]any{"severity": "high", "room": "ops"},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	})
	assert.Nil(t, resp.Error, "conforming arguments MUST be accepted; got %+v", resp.Error)
}

// TestPoll_RejectsArgumentsViolatingInputSchema extends validation to poll.
// The spec scopes -32602 to the event's inputSchema rather than to any one
// method, and poll carries arguments too, so a poll-only client would
// otherwise never learn its arguments are wrong.
func TestPoll_RejectsArgumentsViolatingInputSchema(t *testing.T) {
	srv, _ := buildSchemaStack(t)
	resp := dispatchMethod(t, srv, "events/poll", map[string]any{
		"name":      "schema.event",
		"arguments": map[string]any{"severity": "catastrophic"},
	})
	require.NotNil(t, resp.Error, "events/poll must validate arguments against inputSchema")
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)
}

// TestStream_RejectsArgumentsViolatingInputSchema extends validation to push.
// Spec L249-267 requires validation before the stream opens, so this must be
// an immediate JSON-RPC error and not a terminated stream.
func TestStream_RejectsArgumentsViolatingInputSchema(t *testing.T) {
	srv, _ := buildSchemaStack(t)
	resp := dispatchMethod(t, srv, "events/stream", map[string]any{
		"name":      "schema.event",
		"arguments": map[string]any{"severity": "catastrophic"},
	})
	require.NotNil(t, resp.Error, "events/stream must validate arguments before opening the stream")
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code)
}

// TestValidation_SkippedWhenNoInputSchema keeps the feature opt-in. Sources
// that declare no schema must accept whatever arguments they are given, or
// this PR silently breaks every existing source in the tree.
func TestValidation_SkippedWhenNoInputSchema(t *testing.T) {
	srv, _ := buildAuthGateStack(t, "test-principal") // fakeSecretValidationSource declares none
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	resp := dispatchMethod(t, srv, "events/subscribe", map[string]any{
		"name":      "fake.event",
		"arguments": map[string]any{"anything": "goes", "n": 1},
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    receiver.URL,
			"secret": generateSecret(),
		},
	})
	assert.Nil(t, resp.Error, "a source with no inputSchema must not validate; got %+v", resp.Error)
}
