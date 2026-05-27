package stateless

import (
	core "github.com/panyam/mcpkit/core"
)

// SEP-2575 subscriptions/listen wire types.
//
// The stream's first frame is `notifications/subscriptions/acknowledged`
// carrying the freshly-minted subscriptionId; every subsequent frame on
// the same stream carries the same id under _meta so clients can route
// multi-stream concurrent listens correctly.
//
// Filter semantics: the server MUST NOT deliver notification types
// outside the filter the client requested at subscribe time. Adding
// new filter knobs later is an opt-in additive change.

// SubscribeParams is the params shape clients post to subscriptions/listen.
//
// _meta carries the standard SEP-2575 envelope (validated upstream by
// the dispatcher) plus optional client identifiers. Notifications carries
// the subscription filter. Fields default to false → the server MUST NOT
// emit that notification type for this subscription.
type SubscribeParams struct {
	Notifications SubscribeFilter `json:"notifications"`
}

// SubscribeFilter is the structured per-notification opt-in. Each boolean
// gates a class of notification methods. Resource-update subscriptions
// (per-URI granularity) are deferred — defaultable to a future
// ResourceUpdates []string field without breaking wire compat.
type SubscribeFilter struct {
	ToolsListChanged     bool `json:"toolsListChanged,omitempty"`
	PromptsListChanged   bool `json:"promptsListChanged,omitempty"`
	ResourcesListChanged bool `json:"resourcesListChanged,omitempty"`
}

// Matches returns true if the given notification method falls within
// this filter. The transport-level fanout checks Matches before pushing
// a frame onto any open subscription stream.
func (f SubscribeFilter) Matches(method string) bool {
	switch method {
	case "notifications/tools/list_changed":
		return f.ToolsListChanged
	case "notifications/prompts/list_changed":
		return f.PromptsListChanged
	case "notifications/resources/list_changed":
		return f.ResourcesListChanged
	default:
		// Unknown methods are not delivered. New filter knobs must
		// land alongside their wire flags.
		return false
	}
}

// AcknowledgedFrame is the very first frame the server emits on every
// subscriptions/listen stream. The frame is a notification (no id),
// method = `notifications/subscriptions/acknowledged`, with the freshly-
// minted subscriptionId on params._meta.
//
// Used by the streaming handler to construct the ack body; not part of
// the dispatcher's synchronous path.
type AcknowledgedFrame struct {
	JSONRPC string                 `json:"jsonrpc"`
	Method  string                 `json:"method"`
	Params  AcknowledgedFrameParams `json:"params"`
}

// AcknowledgedFrameParams carries the subscriptionId under _meta where
// the SEP-2575 conformance suite (and any other listener) expects it.
type AcknowledgedFrameParams struct {
	Meta map[string]string `json:"_meta"`
}

// NewAcknowledgedFrame builds the ack frame for the given subscriptionId.
// Single allocation per stream; the streaming handler emits it before
// looping over filter-matching notifications.
func NewAcknowledgedFrame(subscriptionID string) AcknowledgedFrame {
	return AcknowledgedFrame{
		JSONRPC: "2.0",
		Method:  "notifications/subscriptions/acknowledged",
		Params: AcknowledgedFrameParams{
			Meta: map[string]string{
				core.MetaKeySubscriptionID: subscriptionID,
			},
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
func NewTaggedFrame(method string, origParams any, subscriptionID string) TaggedFrame {
	params := map[string]any{
		"_meta": map[string]string{
			core.MetaKeySubscriptionID: subscriptionID,
		},
	}
	// Carry through any non-_meta original params under their existing keys.
	switch op := origParams.(type) {
	case nil:
		// no extra params
	case map[string]any:
		for k, v := range op {
			if k == "_meta" {
				continue
			}
			params[k] = v
		}
	default:
		// Unknown shape — stash under "data" so it isn't lost, though
		// the spec-defined list-changed notifications carry no params.
		params["data"] = op
	}
	return TaggedFrame{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
}
