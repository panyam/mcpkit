// Package main implements the kitchen-sink events demo — a single-
// process showcase of the per-subscription delivery surface added by
// the η work: Match / Transform / OnSubscribe / OnUnsubscribe hooks
// on EventDef, plus EmitToSubscription for targeted delivery.
//
// Discord and telegram each demonstrate one source against a handful
// of subscribers; this demo has three sources and several distinct
// subscriber shapes per source, so the broadcast-vs-targeted
// differentiator the per-subscription model enables actually earns
// its keep on the wire.
//
// Companion: examples/whole-enchilada/events/ — production-shape
// multi-tier reference for the deploy axis (same protocol features,
// different scaling story).
package main

import (
	"encoding/json"
	"regexp"
	"sync"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// ChatMessageData is the cursored chat payload. The Channel field
// drives the Match hook so subscribers can scope to a single channel
// via params: {channel: "general"}.
type ChatMessageData struct {
	Channel   string `json:"channel" jsonschema:"description=Logical chat channel name."`
	Sender    string `json:"sender" jsonschema:"description=Username of the message author."`
	Text      string `json:"text" jsonschema:"description=Message body."`
	Timestamp string `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}

// AlertData is the cursored alert payload. Severity drives Match
// (subscribers can scope to P1 alerts only). Reporter + Message are
// the PII-bearing fields the Transform hook redacts when the
// subscriber opts out via params: {redact_pii: true}.
type AlertData struct {
	Severity  string `json:"severity" jsonschema:"description=Alert severity,enum=P1,enum=P2,enum=P3"`
	Service   string `json:"service" jsonschema:"description=Service the alert fires on."`
	Reporter  string `json:"reporter" jsonschema:"description=Username of the reporter (PII)."`
	Message   string `json:"message" jsonschema:"description=Human-readable description; may contain emails."`
	Timestamp string `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}

// PresenceChangedData is the cursorless presence payload. The User
// field is what the per-sub watch-list filtering keys on inside the
// presence feeder's OnSubscribe-provisioned upstream loop.
type PresenceChangedData struct {
	User      string `json:"user" jsonschema:"description=Username whose presence changed."`
	State     string `json:"state" jsonschema:"description=Presence state,enum=online,enum=away,enum=offline"`
	Timestamp string `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}

// watchListRegistry holds per-subscription user watch lists provisioned
// at events/subscribe time by the presence source's OnSubscribe hook,
// and consumed by the presence feeder at emit time to drive
// EmitToSubscription (one frame per matched (sub, transition) pair)
// instead of fanning out to every subscriber and Match-filtering.
//
// Spec §"Server SDK Guidance" L630 describes this pattern: the author
// has already shaped the event for the specific subscription, so
// Match / Transform are NOT applied on the targeted emit path.
type watchListRegistry struct {
	mu      sync.Mutex
	byEntry map[string][]string // subscriptionID → watched usernames
}

func newWatchListRegistry() *watchListRegistry {
	return &watchListRegistry{byEntry: map[string][]string{}}
}

func (r *watchListRegistry) set(subID string, users []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(users) == 0 {
		delete(r.byEntry, subID)
		return
	}
	r.byEntry[subID] = users
}

func (r *watchListRegistry) clear(subID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byEntry, subID)
}

// matchingSubs returns the subscription IDs whose watch list includes
// user. Returned in no particular order — caller must not rely on
// stability.
func (r *watchListRegistry) matchingSubs(user string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []string{}
	for subID, users := range r.byEntry {
		for _, u := range users {
			if u == user {
				out = append(out, subID)
				break
			}
		}
	}
	return out
}

// emailRedaction strips RFC 5322-ish email addresses from message
// text. Intentionally simple — the demo is about wiring, not about
// being a real PII scrubber.
var emailRedaction = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// chatEventDef returns the chat.message EventDef wired with a Match
// hook that scopes deliveries to params.channel. An empty channel
// param matches every event (the spec's default Match behavior).
func chatEventDef() events.EventDef {
	return events.EventDef{
		Name:        "chat.message",
		Description: "Chat messages from synthetic feeders. Match by params.channel.",
		Delivery:    []string{"push", "poll", "webhook"},
		Meta:        map[string]any{"category": "messaging"},
		Match: func(_ events.HookContext, e events.Event, params map[string]any) bool {
			want, _ := params["channel"].(string)
			if want == "" {
				return true
			}
			var data ChatMessageData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				return false
			}
			return data.Channel == want
		},
	}
}

// alertEventDef returns the alert.fired EventDef wired with Match (by
// params.severity) and Transform (redacts the reporter + emails in
// the message body when params.redact_pii is true). Subscribers that
// don't opt in see the original bytes.
func alertEventDef() events.EventDef {
	return events.EventDef{
		Name:        "alert.fired",
		Description: "Alerts from synthetic feeders. Match by params.severity; Transform redacts PII when params.redact_pii.",
		Delivery:    []string{"push", "poll", "webhook"},
		Meta:        map[string]any{"category": "ops"},
		Match: func(_ events.HookContext, e events.Event, params map[string]any) bool {
			want, _ := params["severity"].(string)
			if want == "" {
				return true
			}
			var data AlertData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				return false
			}
			return data.Severity == want
		},
		Transform: func(_ events.HookContext, e events.Event, params map[string]any) (events.Event, bool) {
			redact, _ := params["redact_pii"].(bool)
			if !redact {
				return e, false
			}
			var data AlertData
			if err := json.Unmarshal(e.Data, &data); err != nil {
				return e, false
			}
			data.Reporter = ""
			data.Message = emailRedaction.ReplaceAllString(data.Message, "<redacted-email>")
			raw, err := json.Marshal(data)
			if err != nil {
				return e, false
			}
			e.Data = raw
			return e, true
		},
	}
}

// presenceEventDef returns the presence.changed EventDef wired with
// OnSubscribe (captures the watch list onto the registry keyed by
// subID) and OnUnsubscribe (clears it). The presence feeder consumes
// the registry to drive EmitToSubscription per matched (sub, user)
// pair — Match / Transform are NOT used on this source, the routing
// is fully resolved at emit time by the per-sub upstream pattern.
func presenceEventDef(registry *watchListRegistry) events.EventDef {
	return events.EventDef{
		Name:        "presence.changed",
		Description: "Cursorless presence transitions. OnSubscribe records params.watch_users; the feeder uses EmitToSubscription to deliver only matched users to each subscription.",
		Delivery:    []string{"push", "webhook"},
		Meta:        map[string]any{"category": "presence"},
		OnSubscribe: func(ctx events.HookContext, params map[string]any) error {
			users := stringSlice(params["watch_users"])
			registry.set(ctx.SubscriptionID(), users)
			return nil
		},
		OnUnsubscribe: func(ctx events.HookContext, _ map[string]any) {
			registry.clear(ctx.SubscriptionID())
		},
	}
}

func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
