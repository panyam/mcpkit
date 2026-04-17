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
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Event is the wire-format event envelope delivered via all three modes.
// The Data field is typed per-source via generics at construction time
// (see MakeEvent), but serialized as json.RawMessage on the wire.
type Event struct {
	EventID   string          `json:"eventId"`
	Name      string          `json:"name"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
	Cursor    string          `json:"cursor"`
}

// EventDef describes an event type advertised via events/list.
type EventDef struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Delivery      []string `json:"delivery"`
	PayloadSchema any      `json:"payloadSchema,omitempty"`
}

// PollResult holds the result of a cursor-based poll from an event source.
type PollResult struct {
	Events []Event
	Cursor string

	// CursorGap is true when the client's cursor points to events that have
	// been evicted (e.g., ring buffer wrapped). This is NOT in Peter's spec —
	// it's an mcpkit extension to signal that events were silently lost.
	// The client decides the policy: re-sync, warn, or ignore.
	//
	// Rationale: an error is too strong (the subscription is still valid),
	// but silent loss is too weak for clients that need reliable delivery.
	// A boolean signal is the minimal-cost indicator.
	CursorGap bool
}

// EventSource is the interface that event producers implement. The library
// calls these methods to serve events/list and events/poll requests.
//
// Cursors are opaque strings per the spec — the source defines their format.
// The spec says null cursor means "start from now" — the source should return
// no events and a fresh cursor for subsequent polls.
type EventSource interface {
	// Def returns the event definition for events/list.
	Def() EventDef

	// Poll returns events since the given cursor, up to limit.
	// An empty cursor means "start from now" (return empty + fresh cursor).
	Poll(cursor string, limit int) PollResult
}

// TypedSource creates an EventSource with auto-derived payloadSchema from the
// Data type parameter, matching the TypedTool ergonomic pattern.
//
// Example:
//
//	source := events.TypedSource[TelegramEventData](events.EventDef{
//	    Name:        "telegram.message",
//	    Description: "Fires when a message is received",
//	    Delivery:    []string{"push", "poll", "webhook"},
//	}, func(cursor string, limit int) events.PollResult {
//	    // ... cursor-based retrieval from your store
//	})
func TypedSource[Data any](def EventDef, poll func(cursor string, limit int) PollResult) EventSource {
	if def.PayloadSchema == nil {
		def.PayloadSchema = core.GenerateSchema[Data]()
	}
	return &typedSource{def: def, poll: poll}
}

type typedSource struct {
	def  EventDef
	poll func(cursor string, limit int) PollResult
}

func (s *typedSource) Def() EventDef                            { return s.def }
func (s *typedSource) Poll(cursor string, limit int) PollResult { return s.poll(cursor, limit) }

// MakeEvent creates an Event envelope with typed data. The data is serialized
// to JSON for the wire format.
func MakeEvent[Data any](name string, eventID string, cursor string, ts time.Time, data Data) Event {
	raw, _ := json.Marshal(data)
	return Event{
		EventID:   eventID,
		Name:      name,
		Timestamp: ts.Format(time.RFC3339),
		Data:      raw,
		Cursor:    cursor,
	}
}

// Config holds the options for registering event sources on an MCP server.
type Config struct {
	Sources  []EventSource
	Webhooks *WebhookRegistry // nil disables webhook delivery
	Server   *server.Server
}

// Register hooks up events/list, events/poll, events/subscribe, and
// events/unsubscribe as custom JSON-RPC methods on the server.
func Register(cfg Config) {
	srv := cfg.Server
	sources := cfg.Sources
	webhooks := cfg.Webhooks

	sourceMap := make(map[string]EventSource, len(sources))
	for _, s := range sources {
		sourceMap[s.Def().Name] = s
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

// pollResultWire is the per-subscription result in an events/poll response.
// nextPollSeconds is per-subscription per Peter's spec — the server can
// recommend different poll intervals for different event types. Client SDKs
// should coalesce by taking the minimum across subscriptions.
type pollResultWire struct {
	ID              string  `json:"id"`
	Events          []Event `json:"events,omitempty"`
	Cursor          string  `json:"cursor"`
	HasMore         bool    `json:"hasMore"`
	CursorGap       bool    `json:"cursorGap,omitempty"`
	NextPollSeconds int     `json:"nextPollSeconds,omitempty"`
	Error           *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
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
		if req.MaxEvents <= 0 {
			req.MaxEvents = 50
		}

		results := make([]pollResultWire, 0, len(req.Subscriptions))
		for _, sub := range req.Subscriptions {
			source, ok := sourceMap[sub.Name]
			if !ok {
				results = append(results, pollResultWire{
					ID: sub.ID,
					Error: &struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					}{Code: -32001, Message: "EventNotFound"},
				})
				continue
			}

			cursor := ""
			if sub.Cursor != nil {
				cursor = *sub.Cursor
			}

			pr := source.Poll(cursor, req.MaxEvents+1)
			hasMore := len(pr.Events) > req.MaxEvents
			events := pr.Events
			resultCursor := pr.Cursor
			if hasMore {
				events = events[:req.MaxEvents]
				resultCursor = events[len(events)-1].Cursor
			}

			results = append(results, pollResultWire{
				ID:              sub.ID,
				Events:          events,
				Cursor:          resultCursor,
				HasMore:         hasMore,
				CursorGap:       pr.CursorGap,
				NextPollSeconds: 5,
			})
		}

		return core.NewResponse(id, map[string]any{"results": results})
	})
}

func registerSubscribe(srv *server.Server, sourceMap map[string]EventSource, webhooks *WebhookRegistry) {
	srv.HandleMethod("events/subscribe", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
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
			return core.NewErrorResponse(id, -32001, "EventNotFound")
		}
		if req.Delivery.Mode != "webhook" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "only webhook delivery mode is supported")
		}
		if req.Delivery.URL == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "delivery.url is required")
		}
		if err := ValidateWebhookURL(req.Delivery.URL); err != nil {
			return core.NewErrorResponse(id, -32005, err.Error())
		}

		secret := req.Delivery.Secret
		if secret == "" {
			secret = fmt.Sprintf("whsec_%d", time.Now().UnixNano())
		}

		expiresAt := webhooks.Register(req.ID, req.Delivery.URL, secret)

		cursorStr := "0"
		if req.Cursor != nil {
			cursorStr = *req.Cursor
		}

		return core.NewResponse(id, map[string]any{
			"id":            req.ID,
			"secret":        secret,
			"cursor":        cursorStr,
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
		webhooks.Unregister(req.Delivery.URL, req.ID)
		return core.NewResponse(id, map[string]any{})
	})
}
