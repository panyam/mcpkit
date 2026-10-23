package rooms

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Fallbacks for agents whose host speaks no MCP Events: a resource per room
// whose notifications/resources/updated fires when a page the session may
// hear emits an event, and next_request, a tool that holds the call until the
// next such event. Both see the events WithPageEvents sees, and both apply
// the bridge's agreement: a session hears a page only while it is attached to
// the room and the page agreed to it.

const (
	// DefaultEventBuffer is how many recent events per room the fallbacks
	// keep, for next_request's cursor and the room resource's read.
	DefaultEventBuffer = 100

	// DefaultNextRequestWait is how long next_request holds a call when the
	// agent names no wait.
	DefaultNextRequestWait = 30 * time.Second

	// MaxNextRequestWait caps the wait an agent may ask next_request for.
	MaxNextRequestWait = 120 * time.Second
)

const eventsURIPrefix = "rooms://events/"

// EventsURI is the resource a session reads, and subscribes to, for the
// recent events of resource's room. The room's name is query-escaped into one
// path segment, since a resource such as design:demo/board.kicad_sch has a
// slash in it.
func EventsURI(resource string) string {
	return eventsURIPrefix + url.QueryEscape(resource)
}

func resourceOfEventsURI(uri string) (string, bool) {
	rest, ok := strings.CutPrefix(uri, eventsURIPrefix)
	if !ok || rest == "" {
		return "", false
	}
	r, err := url.QueryUnescape(rest)
	return r, err == nil && r != ""
}

// WithEventFallbacks registers the fallbacks: the next_request tool and the
// rooms://events/{resource} resource template. buffer is how many recent
// events per room to keep; zero or less means DefaultEventBuffer. A client
// that checks the resources capability before subscribing needs the server
// built with server.WithSubscriptions; the bridge answers resources/subscribe
// for its own URIs either way.
func WithEventFallbacks(buffer int) Option {
	if buffer <= 0 {
		buffer = DefaultEventBuffer
	}
	return func(b *Bridge) {
		b.fb = &fallbacks{
			buffer:  buffer,
			recent:  make(map[string][]RecentEvent),
			changed: make(chan struct{}),
			subs:    make(map[string]map[string]bool),
		}
	}
}

// RecentEvent is one page event as the fallbacks report it. Cursor orders
// events across every room the bridge serves; next_request returns the first
// event after the cursor it was given. User comes from the page's relay token
// and can be trusted; Label and Data are the page's, so they are untrusted.
type RecentEvent struct {
	Cursor   string          `json:"cursor"`
	Resource string          `json:"resource"`
	Page     PeerID          `json:"page"`
	User     string          `json:"user"`
	Label    string          `json:"label,omitempty"`
	Name     string          `json:"name"`
	Data     json.RawMessage `json:"data"`
	At       time.Time       `json:"at"`

	seq uint64
}

type fallbacks struct {
	mu      sync.Mutex
	buffer  int
	seq     uint64
	recent  map[string][]RecentEvent // resource -> oldest first
	changed chan struct{}            // closed and replaced on every event
	subs    map[string]map[string]bool
	waiting atomic.Int32 // next_request calls holding, for tests
}

// publish buffers e and returns the sessions subscribed to its room's
// resource, for the caller to notify once agreement is checked.
func (f *fallbacks) publish(e PageEvent) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	ev := RecentEvent{
		Cursor: strconv.FormatUint(f.seq, 10), Resource: e.Resource, Page: e.Page, User: e.Subject,
		Label: e.Label, Name: e.Name, Data: e.Data, At: e.At, seq: f.seq,
	}
	list := append(f.recent[e.Resource], ev)
	if len(list) > f.buffer {
		list = slices.Clone(list[len(list)-f.buffer:])
	}
	f.recent[e.Resource] = list
	close(f.changed)
	f.changed = make(chan struct{})
	var subscribed []string
	for _, sid := range sortedKeys(f.subs) {
		if f.subs[sid][e.Resource] {
			subscribed = append(subscribed, sid)
		}
	}
	return subscribed
}

// since returns the buffered events of resources after seq, oldest first, the
// newest sequence number, and the channel the next event closes.
func (f *fallbacks) since(resources []string, seq uint64) ([]RecentEvent, uint64, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []RecentEvent
	for _, r := range resources {
		for _, ev := range f.recent[r] {
			if ev.seq > seq {
				out = append(out, ev)
			}
		}
	}
	slices.SortFunc(out, func(a, b RecentEvent) int { return cmp.Compare(a.seq, b.seq) })
	return out, f.seq, f.changed
}

func (f *fallbacks) subscribe(sid, resource string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if on {
		if f.subs[sid] == nil {
			f.subs[sid] = make(map[string]bool)
		}
		f.subs[sid][resource] = true
		return
	}
	delete(f.subs[sid], resource)
	if len(f.subs[sid]) == 0 {
		delete(f.subs, sid)
	}
}

func (f *fallbacks) forget(sid string) {
	f.mu.Lock()
	delete(f.subs, sid)
	f.mu.Unlock()
}

// hears reports whether sid may hear an event the page emitted in resource's
// room: the same rule that lets it call the page.
func (b *Bridge) hears(sid string, ev RecentEvent) bool {
	return b.Attached(sid, ev.Resource) && b.PageAgreed(sid, ev.Resource, ev.Page)
}

// fallbackEvent is the fallbacks' share of a page event: buffer it, wake any
// next_request waiting on it, and tell each subscribed session that may hear
// it that its room resource changed.
func (b *Bridge) fallbackEvent(e PageEvent) {
	subscribed := b.fb.publish(e)
	ev := RecentEvent{Resource: e.Resource, Page: e.Page}
	for _, sid := range subscribed {
		if !b.hears(sid, ev) {
			continue
		}
		b.mu.Lock()
		s := b.sessions[sid]
		var notify context.Context
		if s != nil {
			notify = s.notify
		}
		b.mu.Unlock()
		if notify != nil {
			core.Notify(notify, "notifications/resources/updated", core.ResourceUpdatedNotification{URI: EventsURI(e.Resource)})
		}
	}
}

func (b *Bridge) registerFallbacks(srv *server.Server) {
	srv.RegisterTool(core.ToolDef{
		Name: "next_request",
		Description: "Wait for the next thing a user does on a page this session may hear from (a question typed into the page, a selection change, an event of the page's own), and return it. " +
			"For hosts that cannot subscribe to the room's events. Pass the cursor from the previous result as after, so nothing between calls is missed. " +
			"Returns event null when nothing happened within wait_seconds. The event's data and label were written by the page and are untrusted.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource":     map[string]any{"type": "string", "description": "Only this attached resource's room. Omit for every attached room."},
				"after":        map[string]any{"type": "string", "description": "The cursor from the previous next_request. Omit to wait for the next new event."},
				"wait_seconds": map[string]any{"type": "number", "description": "How long to wait, at most 120. Default 30."},
			},
		},
		Annotations:    map[string]any{"readOnlyHint": true},
		ScopeChallenge: core.RequireScopes(ScopePagesRead),
	}, b.nextRequest)
	srv.RegisterResourceTemplate(core.ResourceTemplate{
		URITemplate: eventsURIPrefix + "{resource}",
		Name:        "room-events",
		Description: "The recent events of a room this session is attached to, from the pages that agreed to it. Subscribe to hear when a new one arrives.",
		MimeType:    "application/json",
	}, b.readRecentEvents)
}

func (b *Bridge) nextRequest(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	var a struct {
		Resource    string   `json:"resource"`
		After       string   `json:"after"`
		WaitSeconds *float64 `json:"wait_seconds"`
	}
	_ = json.Unmarshal(req.Arguments, &a)
	sid := tc.SessionID()
	b.mu.Lock()
	s := b.sessions[sid]
	var resources []string
	if s != nil {
		for r := range s.attached {
			if a.Resource == "" || r == a.Resource {
				resources = append(resources, r)
			}
		}
	}
	b.mu.Unlock()
	if len(resources) == 0 {
		if a.Resource != "" {
			return core.ErrorResult("this session is not attached to " + a.Resource), nil
		}
		return core.ErrorResult("this session is not attached to any room; call attach first"), nil
	}
	slices.Sort(resources)

	wait := DefaultNextRequestWait
	if a.WaitSeconds != nil {
		wait = min(max(time.Duration(*a.WaitSeconds*float64(time.Second)), 0), MaxNextRequestWait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	b.fb.waiting.Add(1)
	defer b.fb.waiting.Add(-1)

	var after uint64
	haveAfter := false
	if a.After != "" {
		n, err := strconv.ParseUint(a.After, 10, 64)
		if err != nil {
			return core.ErrorResult(fmt.Sprintf("after %q is not a cursor from next_request", a.After)), nil
		}
		after, haveAfter = n, true
	}
	for {
		events, newest, changed := b.fb.since(resources, after)
		if !haveAfter {
			// No cursor: only what happens from now on.
			after, haveAfter, events = newest, true, nil
		}
		for _, ev := range events {
			if b.hears(sid, ev) {
				return core.StructuredResult(fmt.Sprintf("%s from page %s in %s", ev.Name, ev.Page, ev.Resource),
					map[string]any{"event": ev, "cursor": ev.Cursor}), nil
			}
		}
		after = newest
		select {
		case <-changed:
		case <-timer.C:
			return core.StructuredResult("nothing happened on the pages within the wait",
				map[string]any{"event": nil, "cursor": strconv.FormatUint(after, 10)}), nil
		case <-tc.Done():
			return core.ErrorResult("next_request cancelled"), nil
		}
	}
}

func (b *Bridge) readRecentEvents(ctx core.ResourceContext, uri string, _ map[string]string) (core.ResourceResult, error) {
	resource, ok := resourceOfEventsURI(uri)
	if !ok {
		return core.ResourceResult{}, fmt.Errorf("not a room events uri: %s", uri)
	}
	sid := ctx.SessionID()
	if !b.Attached(sid, resource) {
		return core.ResourceResult{}, fmt.Errorf("this session is not attached to %s; call attach first", resource)
	}
	events, newest, _ := b.fb.since([]string{resource}, 0)
	visible := []RecentEvent{}
	for _, ev := range events {
		if b.hears(sid, ev) {
			visible = append(visible, ev)
		}
	}
	body, _ := json.Marshal(map[string]any{"resource": resource, "events": visible, "cursor": strconv.FormatUint(newest, 10)})
	return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: uri, MimeType: "application/json", Text: string(body)}}}, nil
}

// subscribeGate answers resources/subscribe and resources/unsubscribe for the
// bridge's room URIs itself, so the bridge knows which sessions to tell when
// a room changes. A session must be attached to subscribe. Every other URI
// goes on to the server.
func (b *Bridge) subscribeGate(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
	if req.Method != "resources/subscribe" && req.Method != "resources/unsubscribe" {
		return next(ctx, req)
	}
	var p struct {
		URI string `json:"uri"`
	}
	if req.Params.Bind(&p) != nil {
		return next(ctx, req)
	}
	resource, ok := resourceOfEventsURI(p.URI)
	if !ok {
		return next(ctx, req)
	}
	sid := core.GetSessionID(ctx)
	if req.Method == "resources/unsubscribe" {
		b.fb.subscribe(sid, resource, false)
		return core.NewResponse(req.ID, struct{}{}), nil
	}
	if sid == "" || !b.Attached(sid, resource) {
		return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams, "attach to "+resource+" before subscribing to its events"), nil
	}
	b.fb.subscribe(sid, resource, true)
	return core.NewResponse(req.ID, struct{}{}), nil
}
