// Package roomsevents makes the room bridge an MCP Events server for its rooms
// (SEP-3415, experimental/ext/events), so an agent subscribes to what happens
// on the pages showing a resource instead of polling for it.
//
// It lives in its own module because constraint C4 keeps extension modules from
// requiring each other: experimental/ext/rooms knows nothing of events, and
// experimental/ext/events nothing of rooms. The bridge hands every page event
// to a hook (rooms.WithPageEvents); this package turns those into SEP-3415
// event types and decides which agent session hears which page.
//
//	ev := roomsevents.New()
//	bridge := rooms.New(transport, rooms.WithPageEvents(ev.Publish))
//	bridge.Register(srv)
//	ev.Register(srv, bridge, events.Config{})
//
// Who hears what follows the bridge's own rules. A session may subscribe to a
// room's events only while it is attached to that room, and it receives a
// page's events only if that page agreed to it, the same agreement that lets
// it call the page's tools. One user's page never talks to another user's
// agent unless that user accepted its pairing code.
//
// Delivery is push (events/stream) and poll only. Attachment lasts as long as
// the MCP session, and a webhook subscription outlives every session, so this
// package offers none.
package roomsevents

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/server"
)

// Event type names the bridge offers through events/list. EventAsk and
// EventSelectionChanged carry the page events of the same name; EventPage
// carries every other name a page emits.
const (
	EventAsk              = rooms.EventAsk
	EventSelectionChanged = rooms.EventSelectionChanged
	EventPage             = "page.event"
)

// EventNames lists the three event types, sorted, as a client subscribes to
// them.
var EventNames = []string{EventPage, EventSelectionChanged, EventAsk}

// Origin is what every payload says about where it came from. User is the
// page's subject from its relay token, so a consumer can trust it; Label is
// what the page set, so a consumer cannot.
type Origin struct {
	Resource string    `json:"resource"`
	Page     string    `json:"page"`
	User     string    `json:"user"`
	Label    string    `json:"label,omitempty"`
	At       time.Time `json:"at"`
}

// AskPayload is EventAsk's data: what the page's user typed for the agents.
type AskPayload struct {
	Origin
	Text string `json:"text"`
}

// SelectionPayload is EventSelectionChanged's data.
type SelectionPayload struct {
	Origin
	Selection json.RawMessage `json:"selection"`
}

// PagePayload is EventPage's data: an event name of the page's own and
// whatever it sent with it.
type PagePayload struct {
	Origin
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

// Events adapts one bridge's page events to SEP-3415 event types. Make it with
// New, give Publish to rooms.WithPageEvents, then call Register.
type Events struct {
	mu     sync.Mutex
	bridge *rooms.Bridge
	// streams maps a push subscription id to the MCP session that opened it.
	// A stream's Match hook runs on a background context with no session in
	// it, so the session is recorded when the stream subscribes.
	streams map[string]string

	sources   []events.EventSource
	ask       func(context.Context, AskPayload) error
	selection func(context.Context, SelectionPayload) error
	page      func(context.Context, PagePayload) error
}

var resourceArgs = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"resource": map[string]any{"type": "string", "description": "The resource whose room to hear from, as passed to attach."}},
	"required":             []any{"resource"},
	"additionalProperties": false,
}

// New builds the three event sources. They deliver nothing until Register
// binds them to a bridge.
func New() *Events {
	e := &Events{streams: map[string]string{}}
	def := func(name, desc string) events.EventDef {
		return events.EventDef{
			Name:          name,
			Description:   desc,
			Delivery:      []string{"push", "poll"},
			InputSchema:   resourceArgs,
			OnSubscribe:   e.onSubscribe,
			OnUnsubscribe: e.onUnsubscribe,
			Match:         e.match,
		}
	}
	askSrc, ask := events.NewYieldingSource[AskPayload](def(EventAsk,
		"The user of a page showing the resource asked the agents something. The text is the user's, typed into the page."))
	selSrc, sel := events.NewYieldingSource[SelectionPayload](def(EventSelectionChanged,
		"What is selected on a page showing the resource changed. The selection's shape is the app's."))
	pageSrc, page := events.NewYieldingSource[PagePayload](def(EventPage,
		"A page showing the resource emitted an event of its own; name says which."))
	e.sources = []events.EventSource{askSrc, selSrc, pageSrc}
	e.ask, e.selection, e.page = ask, sel, page
	return e
}

// Sources returns the event sources, for a caller assembling its own
// events.Config. Register is the shorter path.
func (e *Events) Sources() []events.EventSource { return e.sources }

// Bind ties the sources to the bridge whose attachments and agreements decide
// who hears what. Register calls it.
func (e *Events) Bind(b *rooms.Bridge) {
	e.mu.Lock()
	e.bridge = b
	e.mu.Unlock()
}

// Register binds e to b and registers the events extension on srv with e's
// sources added to cfg's. cfg.Server is set to srv and cfg.Webhooks is left
// as given; this package's types offer no webhook delivery either way. With
// no auth on srv (the bridge's local mode), set cfg.UnsafeAnonymousPrincipal,
// since events/stream refuses an unauthenticated caller.
func (e *Events) Register(srv *server.Server, b *rooms.Bridge, cfg events.Config) *events.Registry {
	e.Bind(b)
	cfg.Server = srv
	cfg.Sources = append(append([]events.EventSource(nil), cfg.Sources...), e.sources...)
	return events.Register(cfg)
}

// Publish turns one page event into the matching event type. Give it to
// rooms.WithPageEvents. An ask with no text string is dropped.
func (e *Events) Publish(pe rooms.PageEvent) {
	origin := Origin{Resource: pe.Resource, Page: string(pe.Page), User: pe.Subject, Label: pe.Label, At: pe.At}
	ctx := context.Background()
	switch pe.Name {
	case rooms.EventAsk:
		var d struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(pe.Data, &d) != nil || d.Text == nil {
			return
		}
		_ = e.ask(ctx, AskPayload{Origin: origin, Text: *d.Text})
	case rooms.EventSelectionChanged:
		var d struct {
			Selection json.RawMessage `json:"selection"`
		}
		_ = json.Unmarshal(pe.Data, &d)
		if len(d.Selection) == 0 {
			d.Selection = json.RawMessage("null")
		}
		_ = e.selection(ctx, SelectionPayload{Origin: origin, Selection: d.Selection})
	default:
		_ = e.page(ctx, PagePayload{Origin: origin, Name: pe.Name, Data: pe.Data})
	}
}

func resourceOf(params map[string]any) string {
	r, _ := params["resource"].(string)
	return r
}

// sessionOf is the MCP session behind a hook call: the request's, for a poll
// or a subscribe, or the one recorded when a stream subscribed.
func (e *Events) sessionOf(hc events.HookContext) string {
	if sid := core.GetSessionID(hc.Context()); sid != "" {
		return sid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streams[hc.SubscriptionID()]
}

func (e *Events) onSubscribe(hc events.HookContext, params map[string]any) error {
	resource := resourceOf(params)
	sid := core.GetSessionID(hc.Context())
	e.mu.Lock()
	b := e.bridge
	e.mu.Unlock()
	if b == nil || sid == "" || !b.Attached(sid, resource) {
		return fmt.Errorf("attach to %q before subscribing to its events", resource)
	}
	if id := hc.SubscriptionID(); id != "" {
		e.mu.Lock()
		e.streams[id] = sid
		e.mu.Unlock()
	}
	return nil
}

func (e *Events) onUnsubscribe(hc events.HookContext, _ map[string]any) {
	if id := hc.SubscriptionID(); id != "" {
		e.mu.Lock()
		delete(e.streams, id)
		e.mu.Unlock()
	}
}

// match delivers an event to a subscription only for its own resource, and
// only while the session is attached and the emitting page agrees to it.
func (e *Events) match(hc events.HookContext, ev events.Event, params map[string]any) bool {
	var o Origin
	if json.Unmarshal(ev.Data, &o) != nil || o.Resource != resourceOf(params) {
		return false
	}
	e.mu.Lock()
	b := e.bridge
	e.mu.Unlock()
	sid := e.sessionOf(hc)
	return b != nil && sid != "" && b.Attached(sid, o.Resource) && b.PageAgreed(sid, o.Resource, rooms.PeerID(o.Page))
}
