package events

// events/stream — push delivery for the MCP Events extension (ε-2).
//
// One open events/stream call holds a single subscription's per-stream
// goroutine. The handler:
//
//  1. Validates the subscription (event name exists, principal authorized,
//     source supports push).
//  2. Sends notifications/events/active with the resolved cursor (§"Push-
//     Based Delivery" → "Request: events/stream" L240).
//  3. Subscribes to the source's live channel (ε-1's Subscribe API).
//  4. Loops on (event arrival, heartbeat tick, ctx.Done):
//     - Event arrival → notifications/events/event (L243-271). If the source
//       signals Truncated=true, prepends a fresh notifications/events/active
//       per spec L285.
//     - Heartbeat tick → notifications/events/heartbeat with the source's
//       current cursor (L294). Cursor is JSON null for cursorless sources.
//     - Ctx done (HTTP abort or notifications/cancelled on stdio) → return
//       the empty StreamEventsResult per spec L293.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

const defaultStreamHeartbeatInterval = 30 * time.Second

// StreamEventsResult is the typed final frame of an events/stream call per
// spec §"Push-Based Delivery" → "Lifecycle" → "Stream termination" L293:
// "an empty typed result ({"_meta": {}})." Carries no information — exists
// to satisfy JSON-RPC's request/response contract; all meaningful content
// is in the preceding notifications.
//
// Meta is intentionally always-emitted (no omitempty) so the wire shape
// matches the spec example exactly. An empty map serializes to `{}`.
type StreamEventsResult struct {
	Meta map[string]any `json:"_meta"`
}

// streamSubscribable is the optional interface a source implements to support
// events/stream push delivery. YieldingSource satisfies this; TypedSource
// does not (it has no internal buffer to fan out from). registerStream
// rejects events/stream for sources lacking this capability with -32017
// DeliveryModeUnsupported per spec.
type streamSubscribable interface {
	Subscribe(ctx context.Context) <-chan SubscriberEvent
}

// activeNotifParams is the wire shape of notifications/events/active per
// spec L240. Cursor is *string so cursorless sources serialize as null.
// Truncated is omitted when false to match the spec example payload.
type activeNotifParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Cursor    *string         `json:"cursor"`
	Truncated bool            `json:"truncated,omitempty"`
}

// eventNotifParams is the wire shape of notifications/events/event per
// spec L243-271 + L276 example. The Event fields are inlined alongside
// requestId — clients deserialize the params as a flat object.
type eventNotifParams struct {
	RequestID json.RawMessage `json:"requestId"`
	EventID   string          `json:"eventId"`
	Name      string          `json:"name"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
	Cursor    *string         `json:"cursor"`
	Meta      map[string]any  `json:"_meta,omitempty"`
}

// heartbeatNotifParams is the wire shape of notifications/events/heartbeat
// per spec L294. Cursor is *string with NO omitempty — the spec mandates
// the field be present even when null.
type heartbeatNotifParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Cursor    *string         `json:"cursor"`
}

// errorNotifParams is the shared wire shape of notifications/events/error
// (spec L255+L261, transient — stream stays open) and
// notifications/events/terminated (spec L783-795, terminal — stream
// closes). Both carry the same {requestId, error{code,message}} envelope
// per the spec's JSON-RPC-error-shaped error payload. ζ-7.
type errorNotifParams struct {
	RequestID json.RawMessage `json:"requestId"`
	Error     errPayload      `json:"error"`
}

// errPayload mirrors the JSON-RPC error object shape used in the
// error / terminated notification params. Distinct from ControlError
// (control envelopes) only in serialization site — same fields.
type errPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func registerStream(srv *server.Server, sourceMap map[string]EventSource, unsafeAnon string, heartbeat time.Duration) {
	if heartbeat <= 0 {
		heartbeat = defaultStreamHeartbeatInterval
	}
	srv.HandleMethod("events/stream", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		// Spec L249-267: validate before opening the stream. Errors here
		// produce an immediate JSON-RPC error response and no stream.
		var req struct {
			Name   string         `json:"name"`
			Params map[string]any `json:"params,omitempty"`
			Cursor *string        `json:"cursor"`
			MaxAge int            `json:"maxAge,omitempty"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		source, ok := sourceMap[req.Name]
		if !ok {
			return core.NewErrorResponse(id, ErrCodeEventNotFound, "EventNotFound")
		}

		// Spec §"Subscription Identity" → "Authentication required" L361:
		// events/stream MUST be called with an authenticated principal —
		// the spec lists Unauthorized among events/stream's immediate
		// errors at L267. Same auth gate as events/subscribe (γ-2).
		if _, ok := resolvePrincipal(ctx, unsafeAnon); !ok {
			return core.NewErrorResponse(id, ErrCodeUnauthorized, "Unauthorized")
		}

		// Sources that don't expose a Subscribe channel (TypedSource today)
		// can't be served via push. Spec lists DeliveryModeUnsupported
		// among events/stream's immediate errors at L267.
		sub, ok := source.(streamSubscribable)
		if !ok {
			return core.NewErrorResponse(id, ErrCodeDeliveryModeUnsupported,
				"DeliveryModeUnsupported: source does not support push delivery")
		}

		def := source.Def()
		cursorless := def.Cursorless

		// Resolve the initial cursor. Mirrors registerSubscribe:
		//   - cursorless sources always emit `cursor: null`
		//   - non-null client cursor passes through
		//   - null client cursor resolves to source.Latest() ("from now")
		var initialCursor *string
		if !cursorless {
			if req.Cursor != nil {
				c := *req.Cursor
				initialCursor = &c
			} else {
				c := source.Latest()
				initialCursor = &c
			}
		}

		// Send the confirmation notification. Per spec L240, this MUST
		// arrive before any event delivery.
		ctx.Notify("notifications/events/active", activeNotifParams{
			RequestID: id,
			Cursor:    initialCursor,
		})

		evCh := sub.Subscribe(ctx)

		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Cancellation: HTTP abort (Streamable HTTP) or
				// notifications/cancelled (stdio, dispatched via the
				// inflight cancel map at server/dispatch.go).
				return core.NewResponse(id, StreamEventsResult{Meta: map[string]any{}})

			case se, ok := <-evCh:
				if !ok {
					// Subscriber chan closed — source removed our slot.
					// Treat as ctx done.
					return core.NewResponse(id, StreamEventsResult{Meta: map[string]any{}})
				}

				// ζ-7: terminal source signal. Emit notifications/events/
				// terminated per spec L783-795 and return — subscription
				// has ended; SDK callback fires + stream closes.
				if se.Terminated != nil {
					ctx.Notify("notifications/events/terminated", errorNotifParams{
						RequestID: id,
						Error:     errPayload{Code: se.Terminated.Code, Message: se.Terminated.Message},
					})
					return core.NewResponse(id, StreamEventsResult{Meta: map[string]any{}})
				}

				// ζ-7: transient source signal. Emit notifications/events/
				// error per spec L255+L261 and stay open — subscription
				// remains active; the next event continues normally.
				if se.Error != nil {
					ctx.Notify("notifications/events/error", errorNotifParams{
						RequestID: id,
						Error:     errPayload{Code: se.Error.Code, Message: se.Error.Message},
					})
					continue
				}

				if se.Truncated {
					// Spec L285: "the server sends a fresh
					// notifications/events/active {requestId, cursor:<fresh>,
					// truncated:true} and continues delivering."
					var c *string
					if !cursorless {
						latest := source.Latest()
						c = &latest
					}
					ctx.Notify("notifications/events/active", activeNotifParams{
						RequestID: id,
						Cursor:    c,
						Truncated: true,
					})
				}
				ctx.Notify("notifications/events/event", eventNotifParams{
					RequestID: id,
					EventID:   se.Event.EventID,
					Name:      se.Event.Name,
					Timestamp: se.Event.Timestamp,
					Data:      se.Event.Data,
					Cursor:    se.Event.Cursor,
					Meta:      se.Event.Meta,
				})

			case <-ticker.C:
				var c *string
				if !cursorless {
					latest := source.Latest()
					c = &latest
				}
				ctx.Notify("notifications/events/heartbeat", heartbeatNotifParams{
					RequestID: id,
					Cursor:    c,
				})
			}
		}
	})
}
