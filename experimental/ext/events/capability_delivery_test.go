package events

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initialize drives the handshake and hands back the raw initialize result, so
// assertions can be made on the JSON rather than on a decoded Go struct. The
// distinction matters here: a settings object that is present-but-empty and one
// that is absent decode identically into a nil-checked field but serialize
// differently, and the wire is what a client reads.
func initializeRaw(t *testing.T, srv *server.Server) map[string]any {
	t.Helper()
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

	raw, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func capsOf(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	caps, ok := result["capabilities"].(map[string]any)
	require.True(t, ok, "initialize result carries no capabilities object: %v", result)
	return caps
}

func pollRaw(t *testing.T, srv *server.Server, params string) *core.Response {
	t.Helper()
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/poll",
		Params: core.NewRawJSON(json.RawMessage(params)),
	})
	require.NoError(t, err)
	return resp
}

// TestInitialize_DeclaresEventsCapability is the check the conformance suite
// calls sep-9999-capability-events-object. A server that answers events/list
// while declaring nothing leaves the whole surface undiscoverable to a client
// that reads capabilities first, which is what the spec tells clients to do.
//
// The declaration goes through the SEP-2133 extensions map. #1416 put it at the
// top level of capabilities, following the design sketch as it read then;
// upstream PR 7 moved the sketch to the extensions map after mcpkit and the
// reference server disagreed, so this asserts the opposite of what it did.
func TestInitialize_DeclaresEventsCapability(t *testing.T) {
	src, _ := NewYieldingSource[fakeFilterPayload](EventDef{Name: "fake.event"})
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:  []EventSource{src},
		Webhooks: NewWebhookRegistry(),
		Server:   srv,
	})

	caps := capsOf(t, initializeRaw(t, srv))
	assert.NotContains(t, caps, "events",
		"events must not appear as a top-level capability; it declares through the extensions map")

	exts, ok := caps["extensions"].(map[string]any)
	require.True(t, ok, "capabilities.extensions missing; got %v", caps)
	settings, ok := exts[ExtensionID].(map[string]any)
	require.True(t, ok, "%s missing from the extensions map; got %v", ExtensionID, exts)

	// listChanged is true because AddSource / RemoveSource broadcast
	// notifications/events/list_changed (#1381). The spec reads this as a
	// promise rather than a hint: a server declaring false must not send it.
	assert.Equal(t, true, settings["listChanged"], "listChanged should be true")
}

// TestEventsExtension_EmptySettingsWhenNoListChanged pins the false case to an
// empty object rather than an explicit `listChanged: false`. The spec defines
// false as the default and reads `{}` as event support without list-change
// notifications, so the two say the same thing and the shorter one matches a
// server that never considered the question.
func TestEventsExtension_EmptySettingsWhenNoListChanged(t *testing.T) {
	ext := EventsExtension{}.Extension()
	assert.Equal(t, ExtensionID, ext.ID)
	assert.Empty(t, ext.Settings, "a false ListChanged must not emit the key")

	raw, err := json.Marshal(ext.Settings)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(raw))
}

// TestInitialize_NoEventsCapabilityWithoutRegister guards the other direction:
// a server that never wired events must not advertise the capability.
func TestInitialize_NoEventsCapabilityWithoutRegister(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	caps := capsOf(t, initializeRaw(t, srv))
	assert.NotContains(t, caps, "events", "a server with no event sources must not declare capabilities.events")
	if exts, ok := caps["extensions"].(map[string]any); ok {
		assert.NotContains(t, exts, ExtensionID,
			"a server with no event sources must not declare the events extension either")
	}
}

// TestPollResponse_EventsAlwaysPresent is sep-9999-poll-events-array. The quiet
// poll is the common case, and omitting `events` on it breaks the obvious
// client loop over the response.
func TestPollResponse_EventsAlwaysPresent(t *testing.T) {
	wire := pollResultWire{Cursor: cursorPtr("c1")}
	raw, err := json.Marshal(wire)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"events":[]`,
		"a poll with nothing to return must carry an empty events array, not omit the key: got %s", raw)
}

// TestPoll_QuietSourceReturnsEmptyArray is the same rule over the wire rather
// than over the struct, since a nil slice and an empty one differ only after
// marshalling.
func TestPoll_QuietSourceReturnsEmptyArray(t *testing.T) {
	srv, _ := buildPollFilterStack(t)
	resp := pollRaw(t, srv, `{"name":"fake.event","cursor":null}`)
	require.Nil(t, resp.Error)

	raw, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"events":[]`, "quiet poll must return an empty array: got %s", raw)
}

// TestPoll_RejectsUnsupportedDeliveryMode is sep-9999-poll-mode-unsupported.
// Before this, `delivery` was descriptive: a source could advertise push and
// webhook only and still answer polls, so the array told a client nothing it
// could rely on.
func TestPoll_RejectsUnsupportedDeliveryMode(t *testing.T) {
	pushOnly, _ := NewYieldingSource[fakeFilterPayload](EventDef{
		Name:     "push.only",
		Delivery: []string{"push", "webhook"},
	})
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	Register(Config{
		Sources:                  []EventSource{pushOnly},
		Webhooks:                 NewWebhookRegistry(),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	initializeRaw(t, srv)

	resp := pollRaw(t, srv, `{"name":"push.only","cursor":null}`)
	require.NotNil(t, resp.Error, "polling a source that does not offer poll must fail")
	assert.Equal(t, -32014, resp.Error.Code)

	data, ok := resp.Error.Data.(UnsupportedData)
	require.True(t, ok, "error data should name the unsupported mode, got %T", resp.Error.Data)
	assert.Equal(t, "deliveryMode", data.Feature)
	assert.Equal(t, "poll", data.Value)
}

// TestDelivery_NormalizedAtRegistration covers the descriptor contract the
// suite calls sep-9999-descriptor-delivery-subset: every advertised event type
// carries a non-empty subset of poll/push/webhook.
//
// A source that declares nothing gets what it can actually serve rather than a
// fixed list, so the array stays a promise the server keeps. A source that
// declares explicitly is left exactly as written, including the narrowing that
// the poll gate above then enforces.
func TestDelivery_NormalizedAtRegistration(t *testing.T) {
	undeclared, _ := NewYieldingSource[fakeFilterPayload](EventDef{Name: "undeclared.source"})
	narrowed, _ := NewYieldingSource[fakeFilterPayload](EventDef{
		Name:     "narrowed.source",
		Delivery: []string{"webhook"},
	})
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	reg := Register(Config{
		Sources:  []EventSource{undeclared, narrowed},
		Webhooks: NewWebhookRegistry(),
		Server:   srv,
	})

	for _, name := range []string{"undeclared.source", "narrowed.source", TopologySourceName} {
		def, ok := reg.Def(name)
		require.True(t, ok, "source %q not registered", name)
		require.NotEmpty(t, def.Delivery, "source %q advertises an empty delivery array", name)
		for _, mode := range def.Delivery {
			assert.Contains(t, []string{"poll", "push", "webhook"}, mode,
				"source %q advertises unknown delivery mode %q", name, mode)
		}
	}

	narrowedDef, _ := reg.Def("narrowed.source")
	assert.Equal(t, []string{"webhook"}, narrowedDef.Delivery,
		"an explicit delivery array must survive registration unchanged")

	undeclaredDef, _ := reg.Def("undeclared.source")
	assert.Contains(t, undeclaredDef.Delivery, "poll",
		"a YieldingSource can be polled, so the derived set must say so")
	assert.Contains(t, undeclaredDef.Delivery, "push",
		"a YieldingSource can be streamed, so the derived set must say so")
}

// TestTopologySource_KeepsDescriptorContract pins the meta-source to the same
// contract every other source has to meet. It was the one descriptor the
// library itself constructs, and it was the one that broke both rules.
func TestTopologySource_KeepsDescriptorContract(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "test", Version: "1.0"})
	reg := Register(Config{Webhooks: NewWebhookRegistry(), Server: srv})
	initializeRaw(t, srv)

	def, ok := reg.Def(TopologySourceName)
	require.True(t, ok)
	assert.NotEmpty(t, def.Delivery, "the topology meta-source must advertise a delivery mode")
	assert.Contains(t, def.Delivery, "poll", "the topology source answers polls, so it must advertise poll")
	assert.NotNil(t, def.InputSchema, "every descriptor carries an inputSchema, including this one")

	resp := pollRaw(t, srv, `{"name":"`+TopologySourceName+`","cursor":null}`)
	assert.Nil(t, resp.Error, "the topology source must still be pollable after the delivery gate")
}
