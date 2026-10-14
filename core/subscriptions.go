package core

import "encoding/json"

// subscriptions/listen wire vocabulary (2026-07-28, SEP-2575).
//
// A client opens one long-lived stream with subscriptions/listen and names
// the notifications it wants in params.notifications. The server answers
// with notifications/subscriptions/acknowledged, whose params.notifications
// carries only the keys it agreed to honor, and then delivers matching
// notifications on the same stream.

const (
	// MethodSubscriptionsListen opens a subscription stream.
	MethodSubscriptionsListen = "subscriptions/listen"

	// NotificationSubscriptionsAcknowledged is the first message on every
	// subscription stream. Its params.notifications is the honored filter.
	NotificationSubscriptionsAcknowledged = "notifications/subscriptions/acknowledged"
)

// Filter keys the base protocol defines. Extensions define their own
// (eventsListChanged, agentsListChanged, taskIds) and register them with
// the server rather than adding them here.
const (
	SubscriptionFilterToolsListChanged     = "toolsListChanged"
	SubscriptionFilterPromptsListChanged   = "promptsListChanged"
	SubscriptionFilterResourcesListChanged = "resourcesListChanged"
)

// SubscriptionFilter is the notifications object on a subscriptions/listen
// request and on its acknowledgement. Each key names one opt-in and its
// value is whatever that key defines: JSON true for a list-changed switch,
// an array of IDs for a key like taskIds.
//
// It is a map rather than a struct so a key defined by an extension
// survives a decode and re-encode intact. encoding/json writes map keys in
// sorted order, so the wire form is stable.
type SubscriptionFilter map[string]json.RawMessage

// Enabled reports whether key is present with the JSON value true. A key
// carrying anything else (false, a string, an array) is not enabled.
func (f SubscriptionFilter) Enabled(key string) bool {
	raw, ok := f[key]
	if !ok {
		return false
	}
	var b bool
	return json.Unmarshal(raw, &b) == nil && b
}

// Set stores v under key, marshalled to JSON. It returns f so calls chain
// from a literal: core.SubscriptionFilter{}.Set("taskIds", ids).
// A value that cannot be marshalled is dropped.
func (f SubscriptionFilter) Set(key string, v any) SubscriptionFilter {
	raw, err := json.Marshal(v)
	if err == nil {
		f[key] = raw
	}
	return f
}

// ListChangedFilter returns a filter with each key set to true, the shape
// of every list-changed opt-in.
func ListChangedFilter(keys ...string) SubscriptionFilter {
	f := make(SubscriptionFilter, len(keys))
	for _, k := range keys {
		f[k] = json.RawMessage("true")
	}
	return f
}
