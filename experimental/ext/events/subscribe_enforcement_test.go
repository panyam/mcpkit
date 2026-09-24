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

func subscribeStack(t *testing.T, opts ...WebhookOption) *server.Server {
	t.Helper()
	src, _ := NewYieldingSource[fakeFilterPayload](EventDef{
		Name:     "chat.message",
		Delivery: []string{"poll", "push", "webhook"},
	})
	pushOnly, _ := NewYieldingSource[fakeFilterPayload](EventDef{
		Name:     "push.only",
		Delivery: []string{"push"},
	})
	srv := server.NewServer(core.ServerInfo{Name: "t", Version: "1"})
	Register(Config{
		Sources:                  []EventSource{src, pushOnly},
		Webhooks:                 NewWebhookRegistry(append([]WebhookOption{WithUnsafeSkipEndpointVerification()}, opts...)...),
		Server:                   srv,
		UnsafeAnonymousPrincipal: "test-principal",
	})
	initializeRaw(t, srv)
	return srv
}

func subscribeCall(t *testing.T, srv *server.Server, params string) *core.Response {
	t.Helper()
	resp, err := srv.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "events/subscribe",
		Params: core.NewRawJSON(json.RawMessage(params)),
	})
	require.NoError(t, err)
	return resp
}

const testSecret = "whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"

// TestSubscribe_RequiresHTTPS covers the rule and the error code together,
// which were two separate defects: http was accepted at all, and the rejection
// it did produce was -32015 where the spec names -32602.
func TestSubscribe_RequiresHTTPS(t *testing.T) {
	srv := subscribeStack(t)
	resp := subscribeCall(t, srv, `{"name":"chat.message","delivery":{"mode":"webhook","url":"http://example.test/hook","secret":"`+testSecret+`"}}`)
	require.NotNil(t, resp.Error, "an http callback must be refused")
	assert.Equal(t, core.ErrCodeInvalidParams, resp.Error.Code,
		"the spec names -32602 InvalidParams for a rejected delivery.url")

	ok := subscribeCall(t, srv, `{"name":"chat.message","delivery":{"mode":"webhook","url":"https://example.test/hook","secret":"`+testSecret+`"}}`)
	assert.Nil(t, ok.Error, "https must still be accepted")
}

// TestSubscribe_PlaintextEscapeHatch pins that the demos keep working, and that
// it takes its own opt-in rather than riding on the private-networks flag.
func TestSubscribe_PlaintextEscapeHatch(t *testing.T) {
	srv := subscribeStack(t, WithUnsafeWebhookAllowPlaintextCallbacks())
	resp := subscribeCall(t, srv, `{"name":"chat.message","delivery":{"mode":"webhook","url":"http://example.test/hook","secret":"`+testSecret+`"}}`)
	assert.Nil(t, resp.Error, "the escape hatch must permit http")

	strict := subscribeStack(t, WithWebhookAllowPrivateNetworks(true))
	refused := subscribeCall(t, strict, `{"name":"chat.message","delivery":{"mode":"webhook","url":"http://example.test/hook","secret":"`+testSecret+`"}}`)
	require.NotNil(t, refused.Error,
		"allowing private networks must not also allow plaintext; they are different decisions")
}

// TestSubscribe_RejectsUnsupportedDeliveryMode is the webhook twin of the poll
// gate. It was inferred in #1417 and only demonstrated once a fixture offered
// an event type that declines webhook.
func TestSubscribe_RejectsUnsupportedDeliveryMode(t *testing.T) {
	srv := subscribeStack(t)
	resp := subscribeCall(t, srv, `{"name":"push.only","delivery":{"mode":"webhook","url":"https://example.test/hook","secret":"`+testSecret+`"}}`)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32014, resp.Error.Code)

	data, ok := resp.Error.Data.(UnsupportedData)
	require.True(t, ok, "the error must name the mode, got %T", resp.Error.Data)
	assert.Equal(t, "deliveryMode", data.Feature)
	assert.Equal(t, "webhook", data.Value)
}

// TestUnsubscribe_UnknownTupleIsNotFound: a client cannot tell a teardown from
// a no-op otherwise, and a typo in the tuple reads back as success.
func TestUnsubscribe_UnknownTupleIsNotFound(t *testing.T) {
	srv := subscribeStack(t)
	sub := subscribeCall(t, srv, `{"name":"chat.message","delivery":{"mode":"webhook","url":"https://example.test/hook","secret":"`+testSecret+`"}}`)
	require.Nil(t, sub.Error)

	unsub := func(url string) *core.Response {
		resp, err := srv.Dispatch(context.Background(), &core.Request{
			JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "events/unsubscribe",
			Params: core.NewRawJSON(json.RawMessage(
				`{"name":"chat.message","delivery":{"url":"` + url + `"}}`)),
		})
		require.NoError(t, err)
		return resp
	}

	assert.Nil(t, unsub("https://example.test/hook").Error, "the real tuple must succeed")

	typo := unsub("https://example.test/hookk")
	require.NotNil(t, typo.Error, "a tuple that names no subscription must not report success")
	assert.Equal(t, -32011, typo.Error.Code)

	again := unsub("https://example.test/hook")
	require.NotNil(t, again.Error, "unsubscribing twice must not report success the second time")
}
