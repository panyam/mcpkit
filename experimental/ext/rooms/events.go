package rooms

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// MethodPageEvent: page to bridge, {name, data}. Something happened on the
// page that an agent may want to hear about: the user asked a question, or
// changed what is selected. The bridge hands it to WithPageEvents; it does
// nothing with it otherwise.
const MethodPageEvent = "notifications/rooms/event"

// Event names a page emits through Ask and SelectionChanged. Any other name a
// page passes to Emit is its own, and reaches the bridge's hook under that
// name.
const (
	EventAsk              = "room.ask"
	EventSelectionChanged = "page.selection_changed"
)

// MaxEventDataBytes caps the encoded data of one page event. A page is
// untrusted, and what it emits ends up in front of a model, so the bridge
// refuses anything larger rather than truncating it.
const MaxEventDataBytes = 16 << 10

var eventNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$`)

// ValidEventName reports whether name is one a page may emit: one to four
// dot-separated lowercase segments, each starting with a letter, at most 64
// bytes, e.g. "room.ask" or "viewer.sheet_opened".
func ValidEventName(name string) bool {
	return len(name) <= 64 && eventNamePattern.MatchString(name)
}

// PageEvent is one event a page emitted, as the bridge's hook receives it.
// Subject and Label are what the bridge knows about the page, from its relay
// token and its last page_state; Data is the page's, unvalidated beyond its
// size, so whatever consumes it treats it as untrusted.
type PageEvent struct {
	Resource string
	Page     PeerID
	Subject  string
	Label    string
	Name     string
	Data     json.RawMessage
	At       time.Time
}

// WithPageEvents installs fn to receive every valid event a page in one of the
// bridge's rooms emits. It runs on the room's receive goroutine, so it must
// not block; hand slow work to a goroutine of its own. Events with an invalid
// name or oversized data are dropped before fn sees them.
//
// The bridge applies no agreement here: a page's events reach fn whether or
// not any agent may call the page. Whoever forwards them to agents decides
// who hears what, typically with PageAgreed.
func WithPageEvents(fn func(PageEvent)) Option {
	return func(b *Bridge) { b.onEvent = fn }
}

// Attached reports whether the MCP session made resource one of its default
// rooms with attach and has not detached since.
func (b *Bridge) Attached(sessionID, resource string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sessionID]
	return s != nil && s.attached[resource]
}

// PageAgreed reports whether page, in resource's room, agreed to the
// principal acting in the MCP session: by its agents switch for the page's own
// user, or by a grant. It is the same agreement that lets that principal call
// the page's tools. A session the bridge has not seen a call from agrees to
// nothing.
func (b *Bridge) PageAgreed(sessionID, resource string, page PeerID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sessionID]
	if s == nil {
		return false
	}
	return b.agreedLocked(s.principal, resource, page)
}

// PageAgreedTo reports whether page, in resource's room, agreed to p, for a
// caller with no MCP session.
func (b *Bridge) PageAgreedTo(p Principal, resource string, page PeerID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.agreedLocked(p, resource, page)
}

func (b *Bridge) agreedLocked(p Principal, resource string, page PeerID) bool {
	rs := b.rooms[resource]
	if rs == nil {
		return false
	}
	pg := rs.pages[page]
	return pg != nil && agreesLocked(pg, p)
}

// pageEventLocked validates an event a page sent and returns it for the hook,
// or false when the bridge drops it.
func (b *Bridge) pageEventLocked(rs *roomState, pg *pageState, params json.RawMessage) (PageEvent, bool) {
	if b.onEvent == nil && b.fb == nil {
		return PageEvent{}, false
	}
	var e struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(params, &e) != nil || !ValidEventName(e.Name) || len(e.Data) > MaxEventDataBytes {
		return PageEvent{}, false
	}
	if len(e.Data) == 0 {
		e.Data = json.RawMessage("null")
	}
	return PageEvent{
		Resource: rs.resource,
		Page:     pg.peer.ID,
		Subject:  pg.peer.Subject,
		Label:    pg.label,
		Name:     e.Name,
		Data:     e.Data,
		At:       time.Now().UTC(),
	}, true
}

// ErrInvalidEvent is what Emit returns for a name ValidEventName refuses or
// data larger than MaxEventDataBytes, before anything reaches the room.
var ErrInvalidEvent = errors.New("rooms: invalid page event")

// Emit tells every bridge the page has heard from that something happened.
// data is encoded as JSON; nil sends null. Bridges receive it only once they
// have listed the page's tools, so an event emitted right after JoinPage,
// before any bridge greeted it, goes nowhere.
func (p *Page) Emit(name string, data any) error {
	if !ValidEventName(name) {
		return fmt.Errorf("%w: name %q", ErrInvalidEvent, name)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvent, err)
	}
	if len(raw) > MaxEventDataBytes {
		return fmt.Errorf("%w: data is %d bytes, over %d", ErrInvalidEvent, len(raw), MaxEventDataBytes)
	}
	p.notifyBridges(MethodPageEvent, map[string]any{"name": name, "data": json.RawMessage(raw)})
	return nil
}

// Ask emits EventAsk: the page's user asked the agents something, typed into
// the page.
func (p *Page) Ask(text string) error {
	return p.Emit(EventAsk, map[string]any{"text": text})
}

// SelectionChanged emits EventSelectionChanged with whatever the page counts
// as its selection.
func (p *Page) SelectionChanged(selection any) error {
	return p.Emit(EventSelectionChanged, map[string]any{"selection": selection})
}
