// Package events is EXPERIMENTAL and subject to breaking changes.
//
// It implements the MCP Events protocol extension (design sketch by Peter
// Alexander, triggers-events-wg) as a reusable library on top of mcpkit.
// Servers register typed event sources; the library handles protocol methods
// (events/list, events/poll, events/subscribe, events/unsubscribe), webhook
// delivery with HMAC signing, and push via Server.Broadcast.
//
// Stability: experimental. The wire format and Go API will change as the
// triggers-events-wg iterates on the spec.
//
// See: https://github.com/modelcontextprotocol/experimental-ext-triggers-events
package events

import (
	"encoding/json"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Event is the wire-format event envelope delivered via all three modes.
// The Data field is typed per-source via generics at construction time
// (see MakeEvent), but serialized as json.RawMessage on the wire.
//
// Cursor is a pointer so cursorless sources can emit `cursor: null` per
// upstream WG PR#1 line 392. Use HasCursor / CursorStr to access it without
// dealing with the pointer directly.
type Event struct {
	EventID   string          `json:"eventId"`
	Name      string          `json:"name"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
	Cursor    *string         `json:"cursor"`
}

// HasCursor reports whether the event carries a cursor (cursored source) or
// is cursor-less (best-effort source). Wire shape is `cursor: <string>` vs
// `cursor: null`.
func (e Event) HasCursor() bool { return e.Cursor != nil }

// CursorStr returns the cursor string for cursored events, or "" when the
// event is cursorless. Convenience wrapper to avoid `*event.Cursor` at call
// sites that don't care about the cursored / cursorless distinction.
func (e Event) CursorStr() string {
	if e.Cursor == nil {
		return ""
	}
	return *e.Cursor
}

// EventDef describes an event type advertised via events/list.
//
// Cursorless declares a source that does not support cursor-based replay.
// The library still serves events/poll for it (always returning empty +
// `cursor: null`), and push/webhook delivery still works — events arrive
// with `cursor: null`. Use this for ephemeral-state sources (typing
// indicators, presence, current-readings) where replay carries no value.
type EventDef struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Delivery      []string `json:"delivery"`
	PayloadSchema any      `json:"payloadSchema,omitempty"`
	Cursorless    bool     `json:"cursorless,omitempty"`
}

// PollResult holds the result of a cursor-based poll from an event source.
//
// Cursor is the string a client should pass on its next poll. For cursored
// sources this is typically the cursor of the last delivered event (or
// Latest() when nothing was delivered). For cursorless sources Cursor is
// always "" and the wire layer translates it to `cursor: null`.
type PollResult struct {
	Events []Event
	Cursor string

	// Truncated is true when the server started delivery from a position later
	// than the cursor the client supplied — i.e., events were skipped. Causes
	// are not distinguished on the wire: the supplied cursor may have fallen
	// outside the upstream's retention window, the maxAge floor may have
	// advanced past it, or the server may have applied its own replay ceiling.
	// In all cases the server resets to a position it can serve from and
	// continues delivering.
	//
	// Clients SHOULD treat truncated as a possible gap (e.g., re-fetch
	// authoritative state via tools if it matters) and persist the fresh
	// cursor returned alongside it. The subscription stays valid.
	Truncated bool
}

// EventSource is the interface that event producers implement. The library
// calls these methods to serve events/list and events/poll requests.
//
// Cursors are opaque strings per the spec — the source defines their format.
// Latest() supports the `cursor: null` subscribe / poll semantic ("from now"):
// the library asks the source for its current head and returns it as the
// resume cursor. Cursorless sources should return "".
type EventSource interface {
	// Def returns the event definition for events/list.
	Def() EventDef

	// Poll returns events since the given cursor, up to limit.
	// An empty cursor means "start from now" (return empty + fresh cursor).
	Poll(cursor string, limit int) PollResult

	// Latest returns the cursor of the most recent event the source knows
	// about. Used to resolve `cursor: null` subscribe requests to a concrete
	// resume point. Cursorless sources should return "".
	Latest() string
}

// TypedSource creates an EventSource with auto-derived payloadSchema from the
// Data type parameter, matching the TypedTool ergonomic pattern. Pass `latest`
// returning the head cursor of your store; return "" if your source does not
// support cursor-based replay.
//
// Example:
//
//	source := events.TypedSource[TelegramEventData](events.EventDef{
//	    Name:        "telegram.message",
//	    Description: "Fires when a message is received",
//	    Delivery:    []string{"push", "poll", "webhook"},
//	},
//	    func(cursor string, limit int) events.PollResult { /* read store */ },
//	    func() string { return store.HeadCursor() },
//	)
func TypedSource[Data any](def EventDef, poll func(cursor string, limit int) PollResult, latest func() string) EventSource {
	if def.PayloadSchema == nil {
		def.PayloadSchema = core.GenerateSchema[Data]()
	}
	if latest == nil {
		latest = func() string { return "" }
	}
	return &typedSource{def: def, poll: poll, latest: latest}
}

type typedSource struct {
	def    EventDef
	poll   func(cursor string, limit int) PollResult
	latest func() string
}

func (s *typedSource) Def() EventDef                            { return s.def }
func (s *typedSource) Poll(cursor string, limit int) PollResult { return s.poll(cursor, limit) }
func (s *typedSource) Latest() string                           { return s.latest() }

// MakeEvent creates an Event envelope with typed data. The data is serialized
// to JSON for the wire format. An empty cursor maps to nil (wire `cursor: null`)
// — convenient for cursorless sources without forcing every caller to deal
// with `*string`.
func MakeEvent[Data any](name string, eventID string, cursor string, ts time.Time, data Data) Event {
	raw, _ := json.Marshal(data)
	e := Event{
		EventID:   eventID,
		Name:      name,
		Timestamp: ts.Format(time.RFC3339),
		Data:      raw,
	}
	if cursor != "" {
		e.Cursor = &cursor
	}
	return e
}

// Config holds the options for registering event sources on an MCP server.
type Config struct {
	Sources  []EventSource
	Webhooks *WebhookRegistry // nil disables webhook delivery
	Server   *server.Server
}

// Register hooks up events/list, events/poll, events/subscribe, and
// events/unsubscribe as custom JSON-RPC methods on the server.
//
// For sources that implement emitterAware (notably YieldingSource), Register
// installs a fanout hook so each yielded event is automatically broadcast
// via push and POSTed to webhook subscribers — the source author writes
// no fanout code. Sources that don't implement emitterAware (TypedSource)
// remain responsible for calling Emit / EmitToWebhooks themselves.
func Register(cfg Config) {
	srv := cfg.Server
	sources := cfg.Sources
	webhooks := cfg.Webhooks

	sourceMap := make(map[string]EventSource, len(sources))
	for _, s := range sources {
		sourceMap[s.Def().Name] = s
		if ea, ok := s.(emitterAware); ok {
			ea.SetEmitHook(func(event Event) {
				Emit(srv, event)
				if webhooks != nil {
					EmitToWebhooks(webhooks, event)
				}
			})
		}
	}

	registerList(srv, sources)
	registerPoll(srv, sourceMap)
	if webhooks != nil {
		registerSubscribe(srv, sourceMap, webhooks)
		registerUnsubscribe(srv, webhooks)
	}
}

// Emit broadcasts an event to all connected SSE clients via Server.Broadcast.
// This is the push delivery path.
func Emit(srv *server.Server, event Event) {
	srv.Broadcast("notifications/events/event", event)
}

// EmitToWebhooks delivers an event to all registered webhooks.
func EmitToWebhooks(webhooks *WebhookRegistry, event Event) {
	webhooks.Deliver(event)
}

// --- Protocol method implementations ---

// pollResultWire is the events/poll response shape per the spec — flat
// top-level fields, no `results[]` wrapper. The wrapper was leftover
// from the batching era; with single-subscription enforcement the
// wrapper carried exactly one entry, so we hoist its contents.
//
// Cursor is a pointer so cursorless sources serialize as `cursor: null`.
// Note: there is intentionally no `omitempty` — cursored sources with empty
// cursor still emit `cursor: ""`, only nil maps to JSON null.
//
// Per-result errors used to live inside this struct (legacy partial-
// success model). They now surface as top-level JSON-RPC errors per
// the spec — single-sub call, single-sub response, single-sub error
// path. See the EventNotFound branch in registerPoll.
type pollResultWire struct {
	Events          []Event `json:"events,omitempty"`
	Cursor          *string `json:"cursor"`
	HasMore         bool    `json:"hasMore"`
	Truncated       bool    `json:"truncated,omitempty"`
	NextPollSeconds int     `json:"nextPollSeconds,omitempty"`
}

func registerList(srv *server.Server, sources []EventSource) {
	srv.HandleMethod("events/list", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		defs := make([]EventDef, 0, len(sources))
		for _, s := range sources {
			defs = append(defs, s.Def())
		}
		return core.NewResponse(id, map[string]any{"events": defs})
	})
}

func registerPoll(srv *server.Server, sourceMap map[string]EventSource) {
	srv.HandleMethod("events/poll", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			MaxEvents     int `json:"maxEvents,omitempty"`
			Subscriptions []struct {
				ID     string  `json:"id"`
				Name   string  `json:"name"`
				Cursor *string `json:"cursor"`
			} `json:"subscriptions"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		// events/poll is single-subscription per upstream WG PR#1 line 185
		// (comment r3140480214). Reject batched requests with a clear
		// pointer to the spec change.
		if len(req.Subscriptions) > 1 {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"events/poll: pass exactly one subscription per call (multi-sub support has been removed)")
		}
		if len(req.Subscriptions) == 0 {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"events/poll: subscriptions[] must contain exactly one entry")
		}
		if req.MaxEvents <= 0 {
			req.MaxEvents = 50
		}

		sub := req.Subscriptions[0]
		source, ok := sourceMap[sub.Name]
		if !ok {
			return core.NewErrorResponse(id, ErrCodeEventNotFound, "EventNotFound")
		}

		cursorless := source.Def().Cursorless

		// Resolve `cursor: null` to the source's current head ("from now").
		// Cursored sources return Latest(); cursorless sources return "" and
		// the wire layer below translates that to JSON null.
		cursor := ""
		if sub.Cursor != nil {
			cursor = *sub.Cursor
		} else if !cursorless {
			cursor = source.Latest()
		}

		pr := source.Poll(cursor, req.MaxEvents+1)
		hasMore := len(pr.Events) > req.MaxEvents
		events := pr.Events
		resultCursor := pr.Cursor
		if hasMore {
			events = events[:req.MaxEvents]
			resultCursor = events[len(events)-1].CursorStr()
		}

		// For cursorless sources, the wire `cursor` is null regardless of
		// what the source returned. For cursored sources, marshal the
		// resolved string into a *string so it serializes as a JSON string.
		var wireCursor *string
		if !cursorless {
			c := resultCursor
			wireCursor = &c
		}

		return core.NewResponse(id, pollResultWire{
			Events:          events,
			Cursor:          wireCursor,
			HasMore:         hasMore,
			Truncated:       pr.Truncated,
			NextPollSeconds: 5,
		})
	})
}

func registerSubscribe(srv *server.Server, sourceMap map[string]EventSource, webhooks *WebhookRegistry) {
	srv.HandleMethod("events/subscribe", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Delivery struct {
				Mode   string `json:"mode"`
				URL    string `json:"url"`
				Secret string `json:"secret,omitempty"`
			} `json:"delivery"`
			Cursor *string `json:"cursor"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if _, ok := sourceMap[req.Name]; !ok {
			return core.NewErrorResponse(id, ErrCodeEventNotFound, "EventNotFound")
		}
		if req.Delivery.Mode != "webhook" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "only webhook delivery mode is supported")
		}
		if req.Delivery.URL == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "delivery.url is required")
		}
		if err := ValidateWebhookURL(req.Delivery.URL); err != nil {
			return core.NewErrorResponse(id, ErrCodeInvalidCallbackUrl, err.Error())
		}

		// Spec: delivery.secret is REQUIRED, client-supplied, and MUST
		// match whsec_ + base64 of 24-64 random bytes. Reject malformed
		// values at subscribe time rather than creating a subscription
		// that produces unverifiable deliveries.
		if req.Delivery.Secret == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"delivery.secret is required (must be whsec_<base64 of 24-64 random bytes>)")
		}
		if err := validateClientSecret(req.Delivery.Secret); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"delivery.secret invalid: "+err.Error())
		}

		expiresAt := webhooks.Register(req.ID, req.Delivery.URL, req.Delivery.Secret)

		// Resolve `cursor: null` to the source's current head ("from now")
		// for cursored sources. Cursorless sources always serialize as null.
		// An explicit non-null client cursor passes through unchanged.
		source := sourceMap[req.Name] // already validated above
		cursorless := source.Def().Cursorless
		var wireCursor *string
		if cursorless {
			wireCursor = nil
		} else if req.Cursor != nil {
			c := *req.Cursor
			wireCursor = &c
		} else {
			c := source.Latest()
			wireCursor = &c
		}

		// Per spec, the response does NOT echo back the secret. The
		// client supplied it, so the client already knows it. Echoing
		// would also risk leaking the secret to anyone who can observe
		// the response (proxies, logs, IDE network panes during
		// development).
		return core.NewResponse(id, map[string]any{
			"id":            req.ID,
			"cursor":        wireCursor,
			"refreshBefore": expiresAt.Format(time.RFC3339),
		})
	})
}

func registerUnsubscribe(srv *server.Server, webhooks *WebhookRegistry) {
	srv.HandleMethod("events/unsubscribe", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			ID       string `json:"id"`
			Delivery *struct {
				URL string `json:"url"`
			} `json:"delivery,omitempty"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if req.Delivery == nil || req.Delivery.URL == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "delivery.url required")
		}
		if req.ID == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "id is required")
		}
		// γ will replace id with the (principal, name, params, url) tuple per spec.
		webhooks.Unregister(req.Delivery.URL, req.ID)
		return core.NewResponse(id, map[string]any{})
	})
}
