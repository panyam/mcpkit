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
// Who hears what follows the bridge's own rules. Any MCP session may subscribe
// to a room's events, before or after it attaches. It receives an event only
// while it is attached to that room, only if the event happened after it
// attached, and only if the page agreed to it, the same agreement that lets it
// call the page's tools. One user's page never talks to another user's agent
// unless that user accepted its pairing code.
//
// Delivery is push (events/stream) and poll only. Attachment lasts as long as
// the MCP session, and a webhook subscription outlives every session, so this
// package offers none.
package roomsevents

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

	// declared are the sources for the bridge's declared page event types,
	// by name, and reg is where Register put them, so the catalog's changes
	// can add and remove them.
	declared map[string]declaredSource
	reg      *events.Registry
	syncMu   sync.Mutex
}

// declaredSource is one declared page event type's source, the declaration
// it was built from, and its yield.
type declaredSource struct {
	decl  rooms.PageEventDecl
	src   events.EventSource
	yield func(context.Context, json.RawMessage) error
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
	e := &Events{streams: map[string]string{}, declared: map[string]declaredSource{}}
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
//
// Each event type a page declared (rooms.PageEventDecl) becomes an event type
// of its own, with the page's payload schema and context hint. A fixed
// catalog (rooms.WithPageEventCatalog) is registered here and never changes.
// Otherwise types come and go with the pages that declare them, and each
// change announces notifications/events/list_changed.
func (e *Events) Register(srv *server.Server, b *rooms.Bridge, cfg events.Config) *events.Registry {
	e.Bind(b)
	cfg.Server = srv
	cfg.Sources = append(append([]events.EventSource(nil), cfg.Sources...), e.sources...)
	e.mu.Lock()
	for _, d := range b.EventCatalog() {
		ds := e.newDeclaredSource(d)
		e.declared[d.Name] = ds
		cfg.Sources = append(cfg.Sources, ds.src)
	}
	e.mu.Unlock()
	reg := events.Register(cfg)
	e.mu.Lock()
	e.reg = reg
	e.mu.Unlock()
	b.OnEventCatalogChange(e.syncCatalog)
	e.syncCatalog()
	return reg
}

// newDeclaredSource builds the event type for a page's declaration. Its
// payload is the page's data object with the origin fields beside it, so the
// schema it advertises is the page's with those fields added.
func (e *Events) newDeclaredSource(d rooms.PageEventDecl) declaredSource {
	def := events.EventDef{
		Name:          d.Name,
		Description:   d.Description,
		Delivery:      []string{"push", "poll"},
		InputSchema:   resourceArgs,
		PayloadSchema: withOrigin(d.PayloadSchema),
		OnSubscribe:   e.onSubscribe,
		OnUnsubscribe: e.onUnsubscribe,
		Match:         e.match,
	}
	if d.ContextHint != nil {
		def.Meta = map[string]any{rooms.MetaContextHint: d.ContextHint}
	}
	src, yield := events.NewYieldingSource[json.RawMessage](def)
	return declaredSource{decl: d, src: src, yield: yield}
}

// syncCatalog adds a source for each type the bridge's catalog gained and
// removes those it lost. A type whose declaration changed is replaced. The
// registry calls run outside e.mu, since removing a source can end its
// subscriptions and run onUnsubscribe; syncMu keeps two syncs from
// interleaving.
func (e *Events) syncCatalog() {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	e.mu.Lock()
	reg, b := e.reg, e.bridge
	have := make(map[string]declaredSource, len(e.declared))
	for k, v := range e.declared {
		have[k] = v
	}
	e.mu.Unlock()
	if reg == nil || b == nil {
		return
	}
	want := map[string]rooms.PageEventDecl{}
	for _, d := range b.EventCatalog() {
		want[d.Name] = d
	}
	for _, name := range sortedNames(have) {
		if d, ok := want[name]; !ok || !sameDecl(d, have[name].decl) {
			_ = reg.RemoveSource(name)
			delete(have, name)
		}
	}
	for _, name := range sortedNames(want) {
		if _, ok := have[name]; ok {
			continue
		}
		ds := e.newDeclaredSource(want[name])
		if reg.AddSource(ds.src) == nil {
			have[name] = ds
		}
	}
	e.mu.Lock()
	e.declared = have
	e.mu.Unlock()
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func sameDecl(a, b rooms.PageEventDecl) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// withOrigin is a declared payload schema with the origin fields every event
// carries added to its properties.
func withOrigin(schema map[string]any) map[string]any {
	out := map[string]any{"type": "object"}
	for k, v := range schema {
		out[k] = v
	}
	props := map[string]any{}
	if p, ok := out["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	for k, v := range originProps {
		props[k] = v
	}
	out["properties"] = props
	return out
}

var originProps = map[string]any{
	"resource": map[string]any{"type": "string", "description": "The resource the page shows."},
	"page":     map[string]any{"type": "string", "description": "The page's peer id."},
	"user":     map[string]any{"type": "string", "description": "The page's user, from its relay token."},
	"label":    map[string]any{"type": "string", "description": "What the page calls itself. The page sets it, so do not trust it."},
	"at":       map[string]any{"type": "string", "format": "date-time"},
}

// Publish turns one page event into the matching event type. Give it to
// rooms.WithPageEvents. An ask with no text string is dropped, and so is a
// declared event whose data is not a JSON object.
func (e *Events) Publish(pe rooms.PageEvent) {
	origin := Origin{Resource: pe.Resource, Page: string(pe.Page), User: pe.Subject, Label: pe.Label, At: pe.At}
	ctx := context.Background()
	e.mu.Lock()
	ds, declared := e.declared[pe.Name]
	e.mu.Unlock()
	if declared {
		if payload, ok := mergeOrigin(origin, pe.Data); ok {
			_ = ds.yield(ctx, payload)
		}
		return
	}
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

// onSubscribe accepts a subscription from any MCP session, attached to the
// room or not: match decides what it hears. A host that opens its streams as
// soon as it connects, before it calls attach, gets a quiet stream that starts
// delivering once the session attaches (#1676). A call with no session is
// refused, since attachment belongs to a session.
func (e *Events) onSubscribe(hc events.HookContext, params map[string]any) error {
	resource := resourceOf(params)
	sid := core.GetSessionID(hc.Context())
	e.mu.Lock()
	b := e.bridge
	e.mu.Unlock()
	if b == nil || sid == "" {
		return fmt.Errorf("the events of %q need an MCP session, which attaches to the room: %w", resource, events.ErrSubscribeForbidden)
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

// match delivers an event to a subscription only for its own resource, only
// while the session is attached and the emitting page agrees to it, and only
// if the event happened after the session attached. A poll runs match when it
// arrives, not when the event happened, so "attached now" alone would hand a
// session that subscribed early the events from before its attach.
func (e *Events) match(hc events.HookContext, ev events.Event, params map[string]any) bool {
	var o Origin
	if json.Unmarshal(ev.Data, &o) != nil || o.Resource != resourceOf(params) {
		return false
	}
	e.mu.Lock()
	b := e.bridge
	e.mu.Unlock()
	sid := e.sessionOf(hc)
	if b == nil || sid == "" {
		return false
	}
	since, attached := b.AttachedSince(sid, o.Resource)
	return attached && !o.At.Before(since) && b.HearsPage(sid, o.Resource, rooms.PeerID(o.Page))
}

// mergeOrigin puts the origin fields beside a declared event's own. The
// bridge has already refused data that claims an origin field.
func mergeOrigin(o Origin, data json.RawMessage) (json.RawMessage, bool) {
	fields := map[string]json.RawMessage{}
	if string(data) != "null" && json.Unmarshal(data, &fields) != nil {
		return nil, false
	}
	var origin map[string]json.RawMessage
	_ = json.Unmarshal(mustMarshal(o), &origin)
	for k, v := range origin {
		fields[k] = v
	}
	return mustMarshal(fields), true
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
