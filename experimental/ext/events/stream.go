package events

// events/stream — push delivery for the MCP Events extension. Spec
// §"Push-Based Delivery" → "Request: events/stream" L240-294.
//
// One open events/stream call holds a single subscription's per-stream
// goroutine. The handler:
//
//  1. Validates the subscription (event name exists, principal authorized,
//     source supports push).
//  2. Sends notifications/events/active with the resolved cursor (§"Push-
//     Based Delivery" → "Request: events/stream" L240).
//  3. Subscribes to the source's live channel.
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
	"crypto/rand"
	"encoding/base64"
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
// rejects events/stream for sources lacking this capability with -32014
// Unsupported (data.feature="deliveryMode", data.value="push") per spec.
//
// SubscribeOpts: the SDK passes the resolved subscriber identity
// (Principal / SubscriptionID / Params) at Subscribe time so the
// source can stash it on its subscriberSlot and apply the EventDef's
// Match / Transform (spec §"Server SDK Guidance" L623-629) on fanout.
// Implementations that don't care can ignore the opts.
//
// Returns (chan, sender). The sender delivers a single event to THIS
// specific subscription, bypassing Match / Transform — used by
// EmitToSubscription (spec §"Server SDK Guidance" L630) to route by
// sub id. Sources that don't support targeted delivery can return a
// no-op sender.
type streamSubscribable interface {
	Subscribe(ctx context.Context, opts SubscribeOpts) (<-chan SubscriberEvent, func(Event))
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
// per the spec's JSON-RPC-error-shaped error payload.
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

func registerStream(srv *server.Server, reg *Registry, unsafeAnon string, heartbeat time.Duration, idx SubscriptionIndexStore, quota *Quota) {
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
		source, ok := reg.Source(req.Name)
		if !ok {
			return newNotFoundError(id, "event", "NotFound")
		}

		// Spec §"Subscription Identity" → "Authentication required" L361:
		// events/stream MUST be called with an authenticated principal —
		// the spec lists -32012 Forbidden among events/stream's immediate
		// errors at L267. Same auth gate as events/subscribe.
		principal, ok := resolvePrincipal(ctx, unsafeAnon)
		if !ok {
			return newForbiddenError(id, "Forbidden")
		}

		// SEP-2575 stateless wire admission: events/stream emits active /
		// event / heartbeat frames via ctx.Notify, so the request needs
		// a live push channel for its lifetime. PR 754 enabled
		// response-as-SSE for the stateless wire — a POST that carries
		// Accept: text/event-stream gets a NotifyFunc threaded onto ctx
		// via WithStatelessNotifyFunc and CanNotify returns true; a POST
		// with plain JSON Accept does not, so ctx.Notify would silently
		// no-op.
		//
		// Fail fast with the same -32014 shape the source-lacks-push
		// branch uses below so clients pick events/poll via one
		// decision rule.
		//
		// Scoped to the stateless wire specifically — legacy callers
		// without an attached notify (e.g., a session whose GET SSE has
		// not opened yet, or test fixtures that call srv.Dispatch
		// directly) keep the historical silent-drop behavior.
		if core.IsStatelessWire(ctx.Context) && !ctx.CanNotify() {
			return newUnsupportedError(id, "deliveryMode", "push",
				"Unsupported: stateless wire does not support push delivery without text/event-stream Accept")
		}

		// Sources that don't expose a Subscribe channel (TypedSource today)
		// can't be served via push. Spec lists -32014 Unsupported among
		// events/stream's immediate errors at L267.
		sub, ok := source.(streamSubscribable)
		if !ok {
			return newUnsupportedError(id, "deliveryMode", "push",
				"Unsupported: source does not support push delivery")
		}

		// Enforce quota BEFORE Subscribe + on_subscribe per spec
		// §"Server SDK Guidance" → "Subscription lifecycle hooks"
		// L705. Done early so a rejected stream never registers a
		// slot or fires any author hooks. Defer the Release on every
		// return path below — pairs 1:1 with this Reserve regardless
		// of how the stream ends (ctx cancel, evCh close, terminated
		// frame, panic).
		if err := quota.Reserve(principal, req.Name); err != nil {
			return newResourceExhaustedError(id, "subscriptions", int64(quota.Cap(req.Name)), err.Error())
		}
		defer quota.Release(principal, req.Name)

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

		// Derive the per-stream sub id BEFORE subscribing so it can
		// ride on the subscriberSlot for fanout-time HookContext
		// construction. Each stream open gets a fresh random sub id —
		// push doesn't share canonical-tuple identity across
		// concurrent streams from the same principal/name/params, so
		// unlike webhook (where deriveSubscriptionID collapses
		// duplicate subscribes), every open IS a new subscription.
		var subIDBuf [16]byte
		_, _ = rand.Read(subIDBuf[:])
		streamSubID := "sub_" + base64.RawURLEncoding.EncodeToString(subIDBuf[:])

		// Subscribe BEFORE sending the active notification. Per spec
		// L240 active MUST arrive before any event delivery, but
		// "delivery" here means the handler's select loop reading
		// from evCh — that loop doesn't start until below. Doing
		// Subscribe first eliminates a race where a client that
		// observes active and immediately triggers a yield could see
		// the yield miss this stream because the slot wasn't yet
		// registered.
		evCh, sender := sub.Subscribe(ctx, SubscribeOpts{
			Principal:      principal,
			SubscriptionID: streamSubID,
			Params:         req.Params,
		})

		// Register this stream's sender in the SubscriptionIndex so
		// EmitToSubscription(idx, event, streamSubID) routes here
		// (spec §"Server SDK Guidance" L630). Removed on every
		// return path so a closed stream's id can't match a stale
		// entry.
		_, _ = idx.AddSubscription(ctx, AddSubscriptionRequest{
			SubscriptionID: streamSubID,
			Mode:           DeliveryModePush,
			Deliver:        sender,
		})
		defer func() {
			_, _ = idx.RemoveSubscription(context.Background(), RemoveSubscriptionRequest{SubscriptionID: streamSubID})
		}()

		// Send the confirmation notification. The loop below reads
		// from evCh; active goes out via ctx.Notify before the loop
		// starts, so any events queued on evCh between Subscribe and
		// Notify still arrive AFTER the active frame on the wire.
		ctx.Notify("notifications/events/active", activeNotifParams{
			RequestID: id,
			Cursor:    initialCursor,
		})

		// Lifecycle hooks for push (spec §"Server SDK Guidance" →
		// "Subscription lifecycle hooks" L691). on_subscribe fires
		// after the channel is acquired (the subscription is now
		// live and would receive events); on_unsubscribe fires on
		// every return path via defer.
		hc := newHookContext(ctx, principal, streamSubID, DeliveryModePush)
		if err := safeOnSubscribe(def.OnSubscribe, hc, req.Params); err != nil {
			// Author rejected provisioning; close out the stream
			// before any delivery happens. Returning an error here
			// produces a JSON-RPC error response per the spec's
			// "events/stream's immediate errors" at L267.
			// Cap unknown at this site — author-defined refusal is
			// not a server-side quota, so Max is omitted.
			return newResourceExhaustedError(id, "subscriptions", 0,
				"on_subscribe rejected: "+err.Error())
		}
		defer safeOnUnsubscribe(def.OnUnsubscribe, hc, req.Params)

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

				// Terminal source signal. Emit notifications/events/
				// terminated per spec L783-795 and return —
				// subscription has ended; SDK callback fires + stream
				// closes.
				if se.Terminated != nil {
					ctx.Notify("notifications/events/terminated", errorNotifParams{
						RequestID: id,
						Error:     errPayload{Code: se.Terminated.Code, Message: se.Terminated.Message},
					})
					return core.NewResponse(id, StreamEventsResult{Meta: map[string]any{}})
				}

				// Transient source signal. Emit notifications/events/
				// error per spec L255+L261 and stay open —
				// subscription remains active; the next event
				// continues normally.
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
