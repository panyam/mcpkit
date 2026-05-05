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
	"log"
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
	// Meta is opaque per-occurrence metadata (spec follow-on commit
	// d4faef9 2026-05-01). Mirrors the `_meta` field on Tool / Resource
	// / Prompt in base MCP. Library threads it through; semantics are
	// app-defined (trace ids, source-system tags, etc.).
	Meta map[string]any `json:"_meta,omitempty"`
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
	// Meta is opaque per-event-type metadata (spec follow-on commit
	// d4faef9 2026-05-01). Same `_meta` convention as Event /
	// Tool / Resource / Prompt. Sources set it once at construction
	// and the library surfaces it on events/list.
	Meta map[string]any `json:"_meta,omitempty"`
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

	// UnsafeAnonymousPrincipal stands in for claims.Subject when the
	// request has no authenticated principal — i.e. the server has no
	// auth middleware wired and ctx.AuthClaims() returns nil.
	//
	// This is a deliberate spec deviation. Per spec §"Subscription
	// Identity" → "Authentication required" L361, servers MUST reject
	// unauthenticated webhook subscribes with -32012 Unauthorized. The
	// escape hatch exists so demos and unauthenticated mcpkit servers
	// can exercise webhook delivery end-to-end without standing up an
	// OAuth provider; it is named with the "Unsafe" prefix and produces
	// a startup warning log so deployments using it know they're
	// off-spec.
	//
	// Empty (default) keeps the spec-strict behavior. Production
	// deployments wire auth via server.WithAuth(...) and leave this
	// empty; ctx.AuthClaims().Subject becomes the principal in the
	// canonical tuple. See γ PLAN.md for the design rationale.
	UnsafeAnonymousPrincipal string

	// StreamHeartbeatInterval is how often events/stream emits
	// notifications/events/heartbeat (per spec §"Push-Based Delivery"
	// → "Lifecycle" → "Heartbeat" L294). Zero or negative defaults to
	// the spec-recommended 30s. Override to a smaller value in tests
	// that need to observe heartbeats quickly.
	StreamHeartbeatInterval time.Duration
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
	registerStream(srv, sourceMap, cfg.UnsafeAnonymousPrincipal, cfg.StreamHeartbeatInterval)
	if webhooks != nil {
		registerSubscribe(srv, sourceMap, webhooks, cfg.UnsafeAnonymousPrincipal)
		registerUnsubscribe(srv, webhooks, cfg.UnsafeAnonymousPrincipal)
	}
	if cfg.UnsafeAnonymousPrincipal != "" {
		log.Printf("[events] WARNING: UnsafeAnonymousPrincipal=%q — unauthenticated webhook subscribes "+
			"will be accepted under this principal. DEVIATES from spec §\"Subscription Identity\" L361 "+
			"(MUST reject unauthenticated). Use only for demos / development.",
			cfg.UnsafeAnonymousPrincipal)
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

// listResultWire is the events/list response shape (spec follow-on
// commit d4faef9 2026-05-01). Optional `nextCursor` matches the
// tools/list / resources/list pagination convention. The library does
// not paginate today (event-type lists are small in practice); the
// field is plumbed for forward compatibility — servers that DO have a
// large advertised set can wrap or replace this handler and emit
// nextCursor without changing the wire shape.
type listResultWire struct {
	Events     []EventDef `json:"events"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

func registerList(srv *server.Server, sources []EventSource) {
	srv.HandleMethod("events/list", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		defs := make([]EventDef, 0, len(sources))
		for _, s := range sources {
			defs = append(defs, s.Def())
		}
		return core.NewResponse(id, listResultWire{Events: defs})
	})
}

func registerPoll(srv *server.Server, sourceMap map[string]EventSource) {
	srv.HandleMethod("events/poll", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		// Spec §"Poll-Based Delivery" → "Request: events/poll" L139-149:
		// flat top-level shape — no subscriptions[] wrapper. Phase 1 dropped
		// batching at the protocol level; δ-1 drops the now-vestigial
		// wrapper at the wire level. δ-2 added MaxAge.
		var req struct {
			Name      string         `json:"name"`
			Params    map[string]any `json:"params,omitempty"`
			Cursor    *string        `json:"cursor"`
			MaxEvents int            `json:"maxEvents,omitempty"`
			MaxAge    int            `json:"maxAge,omitempty"` // seconds; 0 = no floor
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		// Helpful diagnostic for clients still sending the legacy wrapper.
		// A flat-shape request with name omitted is indistinguishable from
		// a wrapper-shape request at the struct level (both leave req.Name
		// empty); probe for the wrapper specifically.
		if req.Name == "" {
			var legacyProbe struct {
				Subscriptions []json.RawMessage `json:"subscriptions"`
			}
			if err := json.Unmarshal(params, &legacyProbe); err == nil && legacyProbe.Subscriptions != nil {
				return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
					`events/poll: legacy {subscriptions: [...]} wrapper rejected — send top-level {name, cursor, maxEvents} per spec §"Poll-Based Delivery" L139-149`)
			}
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"events/poll: name is required")
		}
		if req.MaxEvents <= 0 {
			req.MaxEvents = 50
		}

		source, ok := sourceMap[req.Name]
		if !ok {
			return core.NewErrorResponse(id, ErrCodeEventNotFound, "EventNotFound")
		}

		cursorless := source.Def().Cursorless

		// Resolve `cursor: null` to the source's current head ("from now").
		// Cursored sources return Latest(); cursorless sources return "" and
		// the wire layer below translates that to JSON null.
		cursor := ""
		if req.Cursor != nil {
			cursor = *req.Cursor
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

		// δ-2: maxAge replay floor per spec §"Cursor Lifecycle" →
		// "Bounding replay with maxAge" L529. Drop events whose
		// timestamp predates now - maxAge. If filtering removes any,
		// set Truncated=true (signals the gap to the client).
		// When req.MaxAge is 0 (default), no filtering — preserves
		// pre-δ-2 behavior for callers that don't pass the field.
		if req.MaxAge > 0 && len(events) > 0 {
			floor := time.Now().Add(-time.Duration(req.MaxAge) * time.Second)
			kept := make([]Event, 0, len(events))
			for _, e := range events {
				ts, err := time.Parse(time.RFC3339, e.Timestamp)
				if err != nil || !ts.Before(floor) {
					kept = append(kept, e)
				}
			}
			if len(kept) < len(events) {
				pr.Truncated = true
				// Per spec L529: when filtering removes everything,
				// reset cursor to source head ("now") so the client
				// doesn't re-poll for the dropped events. When some
				// events survived, the resultCursor (last delivered)
				// is already past the floor.
				if len(kept) == 0 && !cursorless {
					resultCursor = source.Latest()
				}
			}
			events = kept
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

// resolvePrincipal returns the principal to use for the canonical
// subscription key, applying the spec's auth-required rule
// (§"Subscription Identity" → "Authentication required" L361) with the
// UnsafeAnonymousPrincipal escape hatch (events.Config field).
//
// Returns: (principal, ok). When ok is false, the handler MUST reject
// the request with -32012 Unauthorized — there is neither real auth
// nor a configured anonymous fallback.
//
// Path-1 (real auth): claims != nil → claims.Subject. Spec-correct.
// Path-2 (demo escape): claims == nil and unsafeAnon != "" → unsafeAnon.
//   Deliberately deviates from the spec; gated by Unsafe-prefix + startup
//   warning in Register.
// Path-3 (strict): claims == nil and unsafeAnon == "" → reject. Spec-correct.
func resolvePrincipal(ctx core.MethodContext, unsafeAnon string) (string, bool) {
	if claims := ctx.AuthClaims(); claims != nil {
		return claims.Subject, true
	}
	if unsafeAnon != "" {
		return unsafeAnon, true
	}
	return "", false
}

func registerSubscribe(srv *server.Server, sourceMap map[string]EventSource, webhooks *WebhookRegistry, unsafeAnon string) {
	srv.HandleMethod("events/subscribe", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var req struct {
			// ID is parsed only to surface a helpful error if a client
			// sends the legacy field. Per spec §"Subscription Identity"
			// → "Key composition" L363: "There is no client-generated id
			// — a subscription is fully determined by what it listens
			// for, where it delivers, and who asked." γ-3 rejects
			// client-supplied id at the wire level so old SDKs fail
			// loudly instead of silently mis-keying.
			ID       string         `json:"id"`
			Name     string         `json:"name"`
			Params   map[string]any `json:"params,omitempty"`
			Delivery struct {
				Mode   string `json:"mode"`
				URL    string `json:"url"`
				Secret string `json:"secret,omitempty"`
			} `json:"delivery"`
			Cursor *string `json:"cursor"`
			MaxAge int     `json:"maxAge,omitempty"` // δ-3: spec §"Cursor Lifecycle" L529; seconds, 0 = no floor
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if req.ID != "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"client-supplied id is not accepted; server derives id over (principal, name, params, url) per spec — drop the id field from your subscribe request")
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
		if err := webhooks.ValidateWebhookURL(req.Delivery.URL); err != nil {
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

		// Spec §"Subscription Identity" → "Authentication required" L361:
		// events/subscribe MUST be called with an authenticated principal;
		// servers MUST reject unauthenticated calls with -32012. The
		// UnsafeAnonymousPrincipal escape hatch (Config field) lets demos
		// run anonymously — see resolvePrincipal docs.
		principal, ok := resolvePrincipal(ctx, unsafeAnon)
		if !ok {
			return core.NewErrorResponse(id, ErrCodeUnauthorized, "Unauthorized")
		}

		// Spec §"Subscription Identity" → "Key composition" L363: the
		// subscription is identified by (principal, delivery.url, name,
		// params). Two subscribes producing identical canonical bytes
		// refer to the same subscription (idempotent refresh). Different
		// principals → distinct subscriptions (cross-tenant isolation
		// L378).
		canonical := canonicalKey(principal, req.Delivery.URL, req.Name, req.Params)
		derivedID := deriveSubscriptionID(canonical)

		expiresAt := webhooks.Register(canonical, derivedID, req.Delivery.URL, req.Delivery.Secret, req.MaxAge)

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
		// client supplied it, so the client already knows it.
		//
		// The id field is the SERVER-DERIVED routing handle per spec
		// §"Subscription Identity" → "Derived id" L367 — non-load-bearing
		// for security, used only as the X-MCP-Subscription-Id header
		// value on delivery POSTs (γ-4 wires the header). Knowing the
		// id grants no operations on the subscription (L378).
		respBody := map[string]any{
			"id":            derivedID,
			"cursor":        wireCursor,
			"refreshBefore": expiresAt.Format(time.RFC3339),
		}
		// ζ-5: include deliveryStatus on refresh response when the target
		// has prior delivery attempts. Spec §"Webhook Delivery Status"
		// L425-460. Omitted on first subscribe — there's nothing to
		// report and an empty object would just be wire bloat.
		if status, ok := deliveryStatusForResponse(webhooks.DeliveryStatus(canonical)); ok {
			respBody["deliveryStatus"] = status
		}
		return core.NewResponse(id, respBody)
	})
}

// deliveryStatusForResponse projects an internal DeliveryStatus into
// the spec wire shape (§"Webhook Delivery Status" L425-460). Returns
// (nil, false) when there's nothing to report (no prior attempts).
//
// Wire shape:
//
//	{
//	  "active":         bool,
//	  "lastDeliveryAt": "RFC3339" (omitted if nil),
//	  "lastError":      "categorical" (omitted if empty),
//	  "failedSince":    "RFC3339" (omitted if nil)
//	}
func deliveryStatusForResponse(s DeliveryStatus) (map[string]any, bool) {
	// "Nothing to report" = no successes AND no failures.
	if s.LastDeliveryAt == nil && s.LastError == DeliveryErrorNone && s.FailedSince == nil {
		return nil, false
	}
	out := map[string]any{
		"active": s.Active,
	}
	if s.LastDeliveryAt != nil {
		out["lastDeliveryAt"] = s.LastDeliveryAt.Format(time.RFC3339)
	}
	if s.LastError != DeliveryErrorNone {
		out["lastError"] = string(s.LastError)
	}
	if s.FailedSince != nil {
		out["failedSince"] = s.FailedSince.Format(time.RFC3339)
	}
	return out, true
}

func registerUnsubscribe(srv *server.Server, webhooks *WebhookRegistry, unsafeAnon string) {
	srv.HandleMethod("events/unsubscribe", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		// Spec §"Unsubscribing: events/unsubscribe" L509: resolves on the
		// same canonical tuple as subscribe — (principal, name, params,
		// delivery.url). The derived id is NOT accepted as input.
		var req struct {
			Name     string         `json:"name"`
			Params   map[string]any `json:"params,omitempty"`
			Delivery *struct {
				URL string `json:"url"`
			} `json:"delivery,omitempty"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if req.Name == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "name is required")
		}
		if req.Delivery == nil || req.Delivery.URL == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "delivery.url is required")
		}

		principal, ok := resolvePrincipal(ctx, unsafeAnon)
		if !ok {
			return core.NewErrorResponse(id, ErrCodeUnauthorized, "Unauthorized")
		}

		canonical := canonicalKey(principal, req.Delivery.URL, req.Name, req.Params)
		webhooks.Unregister(canonical)
		return core.NewResponse(id, map[string]any{})
	})
}
