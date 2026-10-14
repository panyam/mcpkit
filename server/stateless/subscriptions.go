package stateless

import (
	"encoding/json"

	core "github.com/panyam/mcpkit/core"
)

// SEP-2575 subscriptions/listen wire types.
//
// The stream's first frame is `notifications/subscriptions/acknowledged`
// carrying the freshly-minted subscriptionId; every subsequent frame on
// the same stream carries the same id under _meta so clients can route
// multi-stream concurrent listens correctly.
//
// The subscriptionId is the JSON-RPC id of the subscriptions/listen
// request that opened the stream (2026-07-28 schema), not a value the
// server mints.
//
// Filter semantics: the server MUST NOT deliver notification types
// outside the filter the client requested at subscribe time. Which keys
// exist, and what they match, is decided by the server package's filter
// registry, so an extension can add a key without touching this file.

// SubscribeParams is the params shape clients post to subscriptions/listen.
//
// _meta carries the standard SEP-2575 envelope (validated upstream by
// the dispatcher) plus optional client identifiers. Notifications carries
// the subscription filter and is required. A key the server does not
// recognise, or declines, is simply not honored: it is left out of the
// acknowledgement and nothing it names is delivered.
type SubscribeParams struct {
	Notifications core.SubscriptionFilter `json:"notifications"`
}

// AcknowledgedFrame is the very first frame the server emits on every
// subscriptions/listen stream. The frame is a notification (no id),
// method = `notifications/subscriptions/acknowledged`, carrying the
// subscription's ID on params._meta and the honored filter on
// params.notifications.
//
// Used by the streaming handler to construct the ack body; not part of
// the dispatcher's synchronous path.
type AcknowledgedFrame struct {
	JSONRPC string                  `json:"jsonrpc"`
	Method  string                  `json:"method"`
	Params  AcknowledgedFrameParams `json:"params"`
}

// AcknowledgedFrameParams carries the subscriptionId under _meta where
// the SEP-2575 conformance suite (and any other listener) expects it, and
// the subset of the requested filter the server agreed to honor.
// Notifications is always present, as {} when nothing was honored.
type AcknowledgedFrameParams struct {
	Meta          map[string]any          `json:"_meta"`
	Notifications core.SubscriptionFilter `json:"notifications"`
}

// NewAcknowledgedFrame builds the ack frame for a subscription.
// subscriptionID is the JSON-RPC id of the subscriptions/listen request,
// carried verbatim so a numeric id stays numeric. A nil honored filter is
// sent as {}.
func NewAcknowledgedFrame(subscriptionID json.RawMessage, honored core.SubscriptionFilter) AcknowledgedFrame {
	if honored == nil {
		honored = core.SubscriptionFilter{}
	}
	return AcknowledgedFrame{
		JSONRPC: "2.0",
		Method:  core.NotificationSubscriptionsAcknowledged,
		Params: AcknowledgedFrameParams{
			Meta: map[string]any{
				core.MetaKeySubscriptionID: subscriptionID,
			},
			Notifications: honored,
		},
	}
}

// TagFrameWithSubscriptionID wraps any outbound notification in the
// SEP-2575-required envelope: jsonrpc, method, and a params._meta block
// carrying the subscriptionId. The original params are preserved verbatim
// under the same key. The transport calls this for every notification it
// fans out to an open stream.
//
// origParams may be nil (notification with no params); the result still
// carries _meta.subscriptionId so clients can route the frame.
type TaggedFrame struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

// NewTaggedFrame builds a notification frame stamped with the given
// subscriptionId under params._meta[io.modelcontextprotocol/subscriptionId].
// subscriptionID is the listen request's JSON-RPC id, carried verbatim.
//
// origParams may be nil, a map, or any value that marshals to a JSON
// object (an extension's typed params struct). Its fields keep their
// keys, and any _meta it already carries (a traceparent, say) is kept
// alongside the subscriptionId. A value that does not marshal to an
// object is stashed under "data" so it is not lost.
func NewTaggedFrame(method string, origParams any, subscriptionID json.RawMessage) TaggedFrame {
	params := ParamsAsObject(origParams)
	meta := map[string]any{}
	if m, ok := params["_meta"].(map[string]any); ok {
		for k, v := range m {
			meta[k] = v
		}
	}
	meta[core.MetaKeySubscriptionID] = subscriptionID
	params["_meta"] = meta
	return TaggedFrame{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
}

// ParamsAsObject returns notification params as a fresh JSON object map,
// the form a subscription frame and a filter matcher both work on. nil
// gives an empty map. A map[string]any is copied shallowly. Anything else
// round-trips through encoding/json, and a value that is not an object
// lands under "data".
func ParamsAsObject(origParams any) map[string]any {
	out := map[string]any{}
	switch op := origParams.(type) {
	case nil:
		return out
	case map[string]any:
		for k, v := range op {
			out[k] = v
		}
		return out
	}
	raw, err := json.Marshal(origParams)
	if err == nil {
		var obj map[string]any
		if json.Unmarshal(raw, &obj) == nil && obj != nil {
			return obj
		}
	}
	out["data"] = origParams
	return out
}
