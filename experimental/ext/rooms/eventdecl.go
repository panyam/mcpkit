package rooms

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/panyam/mcpkit/core"
)

// MethodEventRefused: bridge to page, {name, reason}. The bridge did not
// accept the page's declaration of event type name, or dropped an event the
// page emitted under it, and says why.
const MethodEventRefused = "notifications/rooms/event_refused"

// MetaContextHint is the _meta key a declared event type's context hint rides
// under in events/list. Hosts that inject events into a model's context read
// it there; to the bridge it is opaque.
const MetaContextHint = "io.github.panyam.mcpkit/context-hint"

// originFields are the payload fields roomsevents adds to every event, so a
// declared payload may not use them.
var originFields = []string{"resource", "page", "user", "label", "at"}

// PageEventDecl declares one event type a page emits: what events/list shows
// for it and what its data must look like.
//
// A declared event's data is a JSON object. Its fields reach subscribers at
// the top level of the event payload, beside where it came from (resource,
// page, user, label, at), so a context hint's template can name them.
type PageEventDecl struct {
	// Name is the event type, as ValidEventName accepts. room.ask,
	// page.selection_changed and page.event are the bridge's own.
	Name        string
	Description string
	// PayloadSchema is a JSON Schema for the data object. Nil accepts any
	// object.
	PayloadSchema map[string]any
	// ContextHint says how a host should put occurrences in front of a model:
	// priority, aggregation, a template, retention. The bridge passes it on
	// unread under MetaContextHint.
	ContextHint map[string]any
}

// WithPageEventCatalog fixes the bridge's page event types, the way
// WithPageTools fixes its tools. events/list then shows the same types for
// every caller. A page declaring a type outside it, or one with a different
// payload schema, has the declaration refused, and an event it emits under an
// undeclared name is refused too, except room.ask and page.selection_changed.
// Without it the first page to declare a name fixes its schema, and an
// undeclared name passes through as page.event.
//
// A declaration that ValidEventName refuses, that reuses a built-in name, or
// whose schema does not compile or claims an origin field, makes New panic.
func WithPageEventCatalog(decls []PageEventDecl) Option {
	return func(b *Bridge) {
		b.eventCatalog = slices.Clone(decls)
		if b.eventCatalog == nil {
			b.eventCatalog = []PageEventDecl{}
		}
	}
}

// EventCatalog lists the declared page event types the bridge offers now,
// sorted by name: the fixed catalog, or every type some page in a joined room
// declared.
func (b *Bridge) EventCatalog() []PageEventDecl {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]PageEventDecl, 0, len(b.evDefs))
	for _, name := range sortedKeys(b.evDefs) {
		out = append(out, b.evDefs[name].decl)
	}
	return out
}

// OnEventCatalogChange calls fn, outside any lock, each time EventCatalog's
// answer changes. It replaces an earlier fn. A fixed catalog never changes.
func (b *Bridge) OnEventCatalogChange(fn func()) {
	b.mu.Lock()
	b.onCatalog = fn
	b.mu.Unlock()
}

// eventSpec is one admitted event type: its declaration, the canonical form
// that tells a matching redeclaration from a conflict, and its compiled
// schema.
type eventSpec struct {
	decl   PageEventDecl
	key    string
	schema *core.CompiledSchema
}

// wireEventDecl is a declaration as a page sends it in its events/list
// answer, in the shape of a SEP-3415 event type.
type wireEventDecl struct {
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	PayloadSchema map[string]any `json:"payloadSchema,omitempty"`
	Meta          map[string]any `json:"_meta,omitempty"`
}

func (d PageEventDecl) wire() wireEventDecl {
	w := wireEventDecl{Name: d.Name, Description: d.Description, PayloadSchema: d.PayloadSchema}
	if d.ContextHint != nil {
		w.Meta = map[string]any{MetaContextHint: d.ContextHint}
	}
	return w
}

func (w wireEventDecl) decl() PageEventDecl {
	d := PageEventDecl{Name: w.Name, Description: w.Description, PayloadSchema: w.PayloadSchema}
	if h, ok := w.Meta[MetaContextHint].(map[string]any); ok {
		d.ContextHint = h
	}
	return d
}

func isBuiltinEvent(name string) bool {
	return name == EventAsk || name == EventSelectionChanged || name == "page.event"
}

// newEventSpec checks a declaration and compiles its schema, or says why it
// cannot be offered.
func newEventSpec(d PageEventDecl) (eventSpec, string) {
	switch {
	case !ValidEventName(d.Name):
		return eventSpec{}, "the name is not a valid event name"
	case isBuiltinEvent(d.Name):
		return eventSpec{}, "the name is one of the bridge's own event types"
	}
	schema := d.PayloadSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	if t, ok := schema["type"]; ok && t != "object" {
		return eventSpec{}, `the payload schema must have type "object"`
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, f := range originFields {
			if _, clash := props[f]; clash {
				return eventSpec{}, fmt.Sprintf("the payload schema declares %q, which every event already carries", f)
			}
		}
	}
	compiled, err := core.CompileSchema(schema)
	if err != nil {
		return eventSpec{}, "the payload schema does not compile: " + err.Error()
	}
	return eventSpec{decl: d, key: string(mustJSON(d.PayloadSchema)), schema: compiled}, ""
}

// initEventCatalogLocked admits the fixed catalog when New runs, panicking on
// a declaration no page could ever match.
func (b *Bridge) initEventCatalogLocked() {
	for _, d := range b.eventCatalog {
		spec, reason := newEventSpec(d)
		if reason != "" {
			panic(fmt.Sprintf("rooms: WithPageEventCatalog: event %q: %s", d.Name, reason))
		}
		b.evDefs[d.Name] = spec
	}
}

// admitEventLocked decides whether the bridge can offer a page's declared
// event type, in the same way admitLocked does for tools.
func (b *Bridge) admitEventLocked(d PageEventDecl) string {
	spec, reason := newEventSpec(d)
	if reason != "" {
		return reason
	}
	have, ok := b.evDefs[d.Name]
	if b.eventCatalog != nil {
		switch {
		case !ok:
			return "the bridge's catalog (WithPageEventCatalog) does not declare this event"
		case have.key != spec.key:
			return "the payload schema differs from the bridge's declaration"
		}
		return ""
	}
	if ok && have.key != spec.key {
		return "another page already declares this event with a different payload schema"
	}
	if !ok {
		b.evDefs[d.Name] = spec
	}
	return ""
}

// checkEventLocked is why the bridge drops an event a page emitted, or "".
// A declared name's data must match its schema. With a fixed catalog an
// undeclared name is dropped too, apart from the built-in events.
func (b *Bridge) checkEventLocked(name string, data json.RawMessage) string {
	spec, ok := b.evDefs[name]
	if !ok {
		if b.eventCatalog != nil && name != EventAsk && name != EventSelectionChanged {
			return "the bridge's catalog (WithPageEventCatalog) does not declare this event"
		}
		return ""
	}
	if errs := spec.schema.Validate(data); errs != nil && len(errs.Errors) > 0 {
		e := errs.Errors[0]
		where := e.Path
		if where == "" {
			where = "the data"
		}
		return fmt.Sprintf("the data does not match the declared payload schema: %s: %s", where, e.Message)
	}
	return ""
}

// syncEventsLocked drops dynamic event types no page declares any more and
// reports whether the catalog changed since before.
func (b *Bridge) syncEventsLocked(before []string) bool {
	if b.eventCatalog == nil {
		declared := map[string]bool{}
		for _, r := range b.rooms {
			for _, pg := range r.pages {
				for name := range pg.events {
					declared[name] = true
				}
			}
		}
		for name := range b.evDefs {
			if !declared[name] {
				delete(b.evDefs, name)
			}
		}
	}
	return !slices.Equal(before, sortedKeys(b.evDefs))
}

// listEvents asks a page for the event types it declares and reconciles them
// with the bridge's catalog. A page that does not answer events/list, such
// as one from before declarations, declares none.
func (b *Bridge) listEvents(rs *roomState, page PeerID) {
	var list struct {
		Events []wireEventDecl `json:"events"`
	}
	resp, err := b.request(b.ctx, rs, page, "events/list", nil, nil)
	if err == nil && resp.Error == nil {
		_ = json.Unmarshal(resp.Result, &list)
	}
	type refusal struct{ name, reason string }
	var refused []refusal
	b.mu.Lock()
	pg := rs.pages[page]
	if pg == nil {
		b.mu.Unlock()
		return
	}
	before := sortedKeys(b.evDefs)
	accepted := map[string]bool{}
	for _, w := range list.Events {
		if reason := b.admitEventLocked(w.decl()); reason != "" {
			refused = append(refused, refusal{w.Name, reason})
			continue
		}
		accepted[w.Name] = true
	}
	pg.events = accepted
	changed := b.syncEventsLocked(before)
	fn := b.onCatalog
	b.mu.Unlock()
	for _, r := range refused {
		b.sendTo(rs, page, rpcMessage{Method: MethodEventRefused, Params: mustJSON(map[string]any{"name": r.name, "reason": r.reason})})
	}
	if changed && fn != nil {
		fn()
	}
}

// eventsAfterPageLeft drops the event types only the departed page declared.
func (b *Bridge) eventsAfterPageLeft(before []string) {
	b.mu.Lock()
	changed := b.syncEventsLocked(before)
	fn := b.onCatalog
	b.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}
