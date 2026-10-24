package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// DefaultFanOutLimit is the most tabs one fan-out call reaches. It sits under
// massrelay's 30 messages a second per connection, so one call never needs
// more than a second of the bridge's sending.
const DefaultFanOutLimit = 25

// WithFanOutLimit sets the most tabs one fan-out call may reach. A call that
// would reach more is refused, not truncated, and told to narrow with targets.
// n <= 0 keeps DefaultFanOutLimit.
func WithFanOutLimit(n int) Option {
	return func(b *Bridge) {
		if n > 0 {
			b.fanOutLimit = n
		}
	}
}

// FanOutResult is one tab's answer to a fan-out call. The call's
// structuredContent is {results: [FanOutResult]}, sorted by page id. Exactly
// one of Result and Error is set; Error is a tab that failed, timed out or
// left, which does not fail the others.
type FanOutResult struct {
	Page     PeerID           `json:"page"`
	Resource string           `json:"resource"`
	User     string           `json:"user"`
	Label    string           `json:"label,omitempty"`
	Result   *core.ToolResult `json:"result,omitempty"`
	Error    string           `json:"error,omitempty"`
}

var targetsSchema = map[string]any{
	"type":        "object",
	"description": "Narrow which tabs the call reaches. Omit to reach every tab in the room that agreed to you.",
	"properties": map[string]any{
		"users": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only tabs whose user is one of these."},
	},
}

// fanOutReason refuses a fan-out declaration the bridge could not serve.
func fanOutReason(d PageToolDecl) string {
	if !d.ReadOnly {
		return "a fan-out tool must be read-only; the host sends one-to-many changes with Bridge.Notify"
	}
	if props, ok := d.InputSchema["properties"].(map[string]any); ok {
		if _, ok := props["targets"]; ok {
			return "the bridge adds a property named targets to every fan-out tool, so a fan-out tool may not declare one"
		}
	}
	return ""
}

// fanOutCall sends one call to every tab in the room that offers name and
// agreed to p, narrowed by the targets argument, each with its own deadline.
func (b *Bridge) fanOutCall(tc core.ToolContext, name string, p Principal, rooms []string, args map[string]json.RawMessage) (core.ToolResponse, error) {
	if len(rooms) != 1 {
		return core.ErrorResult(name + " reaches every tab in one room; name the room"), nil
	}
	var targets struct {
		Users []string `json:"users"`
	}
	if raw, ok := args["targets"]; ok {
		_ = json.Unmarshal(raw, &targets)
		delete(args, "targets")
	}

	b.mu.Lock()
	rs := b.rooms[rooms[0]]
	var picked []FanOutResult
	if rs != nil {
		for _, id := range sortedPages(rs) {
			pg := rs.pages[id]
			if !pg.listed || !pg.tools[name] || !agreesLocked(pg, p) {
				continue
			}
			if len(targets.Users) > 0 && !slices.Contains(targets.Users, pg.peer.Subject) {
				continue
			}
			picked = append(picked, FanOutResult{Page: id, Resource: rs.resource, User: pg.peer.Subject, Label: pg.label})
		}
	}
	var roster []PageInfo
	if len(picked) == 0 {
		roster = b.rosterLocked(p, rooms)
	}
	b.mu.Unlock()

	switch {
	case len(picked) == 0:
		reason := "no tab in " + rooms[0] + " that agreed to you offers " + name
		res := core.ErrorResult(reason)
		res.StructuredContent = map[string]any{"error": reason, "pages": roster}
		return res, nil
	case len(picked) > b.fanOutLimit:
		return core.ErrorResult(fmt.Sprintf("%s would reach %d tabs, over this bridge's limit of %d; narrow it with targets", name, len(picked), b.fanOutLimit)), nil
	}

	ids := make([]PeerID, len(picked))
	for i := range picked {
		ids[i] = picked[i].Page
	}
	b.seen(rs, ids, p, true)

	params := map[string]any{"name": name, "arguments": args, "_meta": map[string]any{MetaCaller: p}}
	var wg sync.WaitGroup
	for i := range picked {
		wg.Go(func() {
			r := &picked[i]
			resp, err := b.request(tc, rs, r.Page, "tools/call", params, nil)
			if err != nil {
				r.Error = err.Error()
				return
			}
			var res core.ToolResult
			if err := json.Unmarshal(resp.Result, &res); err != nil {
				r.Error = "page sent a result that is not a tool result: " + err.Error()
				return
			}
			r.Result = &res
		})
	}
	wg.Wait()
	if err := tc.Err(); errors.Is(err, context.Canceled) {
		return nil, err
	}

	failed := 0
	for _, r := range picked {
		if r.Error != "" {
			failed++
		}
	}
	res := core.TextResult(fmt.Sprintf("%s ran on %d tabs: %d answered, %d failed.", name, len(picked), len(picked)-failed, failed))
	res.IsError = failed == len(picked)
	res.StructuredContent = map[string]any{"results": picked}
	return res, nil
}

// Selector picks the tabs Notify reaches. Rooms empty means every room the
// bridge has joined; a room named that it has not joined is joined first.
// Users empty means every tab's user; otherwise only tabs whose relay subject
// is one of them.
type Selector struct {
	Rooms []string
	Users []string
}

// NotifyTarget is one tab Notify tried to reach.
type NotifyTarget struct {
	Resource string
	Page     PeerID
	User     string
	// Err is why the relay would not take the message, on a Failed target.
	Err error
}

// NotifyReport is where Notify sent, sorted by room then page. Sent means the
// relay took the message, not that the page has handled it: a notification
// has no answer.
type NotifyReport struct {
	Sent   []NotifyTarget
	Failed []NotifyTarget
}

// Notify sends a page notification, name with data, to the tabs sel picks.
// It is the host talking to its own pages, with no agent and no agreement
// involved, which is why one-to-many changes go here and not through a
// fan-out tool. A page handles it with OnNotify (onNotify in TypeScript).
//
// name follows ValidEventName and data encodes to at most MaxEventDataBytes;
// either failing returns ErrInvalidEvent before anything is sent. Notify
// reaches only tabs the bridge has listed, and rooms the bridge has joined
// (or sel names), since finding every tab of a user in rooms it is not in
// would need the relay to answer that.
func (b *Bridge) Notify(ctx context.Context, sel Selector, name string, data any) (NotifyReport, error) {
	if !ValidEventName(name) {
		return NotifyReport{}, fmt.Errorf("%w: name %q", ErrInvalidEvent, name)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return NotifyReport{}, fmt.Errorf("%w: data: %v", ErrInvalidEvent, err)
	}
	if len(raw) > MaxEventDataBytes {
		return NotifyReport{}, fmt.Errorf("%w: data is %d bytes, over %d", ErrInvalidEvent, len(raw), MaxEventDataBytes)
	}
	resources := slices.Clone(sel.Rooms)
	if len(resources) == 0 {
		b.mu.Lock()
		resources = sortedKeys(b.rooms)
		b.mu.Unlock()
	} else {
		slices.Sort(resources)
		resources = slices.Compact(resources)
		for _, r := range resources {
			rs, err := b.joinRoom(r)
			if err != nil {
				return NotifyReport{}, fmt.Errorf("rooms: notify: join %s: %w", r, err)
			}
			b.settle(rs)
		}
	}

	type send struct {
		rs *roomState
		NotifyTarget
	}
	var sends []send
	b.mu.Lock()
	for _, r := range resources {
		rs := b.rooms[r]
		if rs == nil {
			continue
		}
		for _, id := range sortedPages(rs) {
			pg := rs.pages[id]
			if pg.listed && (len(sel.Users) == 0 || slices.Contains(sel.Users, pg.peer.Subject)) {
				sends = append(sends, send{rs, NotifyTarget{Resource: r, Page: id, User: pg.peer.Subject}})
			}
		}
	}
	b.mu.Unlock()

	msg := encode(rpcMessage{Method: MethodNotify, Params: mustJSON(map[string]any{"name": name, "data": json.RawMessage(raw)})})
	var rep NotifyReport
	for _, s := range sends {
		if err := s.rs.room.Send(ctx, s.Page, MessageKind, msg); err != nil {
			s.Err = err
			rep.Failed = append(rep.Failed, s.NotifyTarget)
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			continue
		}
		rep.Sent = append(rep.Sent, s.NotifyTarget)
	}
	return rep, nil
}

// OnNotify installs the handler for the host's notifications named name (see
// Bridge.Notify), replacing any before it; nil removes it. data is the JSON
// the host sent, null when it sent none. A notification with no handler is
// dropped. With TrustBridges only the host's bridges are heard.
func (p *Page) OnNotify(name string, fn func(data json.RawMessage)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if fn == nil {
		delete(p.onNotify, name)
		return
	}
	p.onNotify[name] = fn
}

func (p *Page) notified(params json.RawMessage) {
	var n struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	p.mu.Lock()
	fn := p.onNotify[strings.TrimSpace(n.Name)]
	p.mu.Unlock()
	if fn != nil {
		if len(n.Data) == 0 {
			n.Data = json.RawMessage("null")
		}
		fn(n.Data)
	}
}
