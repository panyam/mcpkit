package rooms

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// PageTool is one tool a page offers, in the shape WebMCP's registerTool
// takes: a descriptor plus the function that runs it.
//
// ReadOnly tools need the agent to hold pages:read and every other tool needs
// pages:act. It goes on the wire as the readOnlyHint annotation.
type PageTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	ReadOnly    bool
	Execute     func(ctx context.Context, call PageCall) (core.ToolResult, error)
}

// PageCall is one invocation of a PageTool. The context passed with it is
// cancelled when the agent cancels the call.
type PageCall struct {
	// Arguments are the agent's arguments, with the bridge's page property
	// already removed.
	Arguments json.RawMessage

	// From is the bridge that forwarded the call.
	From PeerID

	// Caller is who the call acts for, from the bridge's validated token:
	// the agent's subject and client. A page applies its app's own rules
	// with it, e.g. letting a paired user view a design but not edit it.
	// It is only as trustworthy as From; with TrustBridges the page answers
	// no other peer, so set that before relying on Caller.
	Caller Principal

	// Progress reports progress back to the agent. A no-op when the agent
	// asked for none.
	Progress func(progress, total float64, message string)
}

// Page is the Go side of a page: a small MCP server whose transport is a
// room. It answers tools/list and tools/call from any bridge in the room and
// tells the bridges it has heard from about its state.
//
// Browser pages use the TypeScript client instead; this one serves Go
// programs that show something, and tests.
type Page struct {
	room Room

	mu        sync.Mutex
	tools     map[string]PageTool
	bridges   map[PeerID]bool
	label     string
	allowed   bool
	running   map[string]context.CancelFunc
	onPairing func(PairingRequest)
	offer     pairingOffer
	onPaired  func(PairedByCode)
	// grants are the principals the page agrees to beyond its agents
	// switch; accepted are agent codes its user accepted, by code, until a
	// bridge names the principal or they expire.
	grants    []Principal
	accepted  map[string]time.Time
	focusedAt time.Time
	// trusted, when set, are the relay subjects of the bridges the page
	// answers; a message carries its sender's relay subject.
	trusted map[string]bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// PageOption configures JoinPage.
type PageOption func(*Page)

// TrustBridges makes the page answer only peers whose relay subject is one of
// subjects: the identities the host's bridges join the relay as. Without it a
// page answers any peer in the room, so any holder of a relay token for the
// room can call its tools directly, past the bridge's agreement check, and
// PageCall.Caller says whatever that peer claims.
func TrustBridges(subjects ...string) PageOption {
	return func(p *Page) {
		p.trusted = make(map[string]bool, len(subjects))
		for _, s := range subjects {
			p.trusted[s] = true
		}
	}
}

// JoinPage joins room through t and starts answering. Register tools before
// or after; registering after the bridge has listed sends list_changed.
func JoinPage(ctx context.Context, t RoomTransport, room string, opts ...PageOption) (*Page, error) {
	r, err := t.Join(ctx, room)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithCancel(context.Background())
	p := &Page{
		room:     r,
		tools:    make(map[string]PageTool),
		bridges:  make(map[PeerID]bool),
		running:  make(map[string]context.CancelFunc),
		accepted: make(map[string]time.Time),
		ctx:      pctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	for _, o := range opts {
		o(p)
	}
	go p.loop()
	return p, nil
}

// ID is the page's peer id, which agents pass as the page argument.
func (p *Page) ID() PeerID { return p.room.Self() }

// RegisterTool adds or replaces a tool and tells known bridges the list
// changed.
func (p *Page) RegisterTool(t PageTool) {
	p.mu.Lock()
	p.tools[t.Name] = t
	p.mu.Unlock()
	p.notifyBridges("notifications/tools/list_changed", nil)
}

// SetLabel sets what list_pages shows for this page.
func (p *Page) SetLabel(label string) {
	p.mu.Lock()
	p.label = label
	p.mu.Unlock()
	p.notifyBridges(MethodPageState, map[string]any{"label": label})
}

// Focus reports that the page's user is looking at it now.
func (p *Page) Focus() {
	now := time.Now().UTC()
	p.mu.Lock()
	label := p.label
	p.focusedAt = now
	p.mu.Unlock()
	p.notifyBridges(MethodPageState, map[string]any{"label": label, "focused": true, "focusedAt": now.Format(time.RFC3339Nano)})
}

// SetAgentsAllowed is the "let my agents drive this tab" switch. On pairs the
// page with every session acting for the page's own user; off ends those
// pairings.
func (p *Page) SetAgentsAllowed(allowed bool) {
	p.mu.Lock()
	p.allowed = allowed
	p.mu.Unlock()
	p.notifyBridges(MethodAgentsAllowed, map[string]any{"allowed": allowed})
}

// AcceptPairing accepts a code an agent got back from attach. The bridge that
// minted it answers with the agent's principal, which the page then agrees to
// (OnPaired reports it) and sends to every bridge with its other grants.
func (p *Page) AcceptPairing(code string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	p.mu.Lock()
	p.accepted[code] = time.Now().Add(DefaultAgentCodeTTL)
	p.mu.Unlock()
	p.notifyBridges(MethodAcceptPairing, map[string]any{"code": code})
}

// Grants returns the principals the page agrees to beyond its agents switch.
func (p *Page) Grants() []Principal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.grants)
}

// Revoke ends the page's agreement with who: every grant to who's subject
// when who.Client is empty, or the one for that client. It does not turn off
// the agents switch, which SetAgentsAllowed(false) does.
func (p *Page) Revoke(who Principal) {
	p.mu.Lock()
	p.grants = slices.DeleteFunc(p.grants, func(g Principal) bool {
		return g.Subject == who.Subject && (who.Client == "" || g.Client == who.Client)
	})
	p.mu.Unlock()
	p.sendGrants()
}

// RevokeAll ends every grant. The agents switch is unchanged.
func (p *Page) RevokeAll() {
	p.mu.Lock()
	p.grants = nil
	p.mu.Unlock()
	p.sendGrants()
}

func (p *Page) sendGrants() {
	p.mu.Lock()
	grants := slices.Clone(p.grants)
	p.mu.Unlock()
	p.notifyBridges(MethodGrants, map[string]any{"grants": nonNil(grants)})
}

func nonNil(g []Principal) []Principal {
	if g == nil {
		return []Principal{}
	}
	return g
}

// OnPairingRequest installs a callback for sessions asking to drive the page,
// which is where a page shows its user which agent asked.
func (p *Page) OnPairingRequest(fn func(PairingRequest)) {
	p.mu.Lock()
	p.onPairing = fn
	p.mu.Unlock()
}

// Leave leaves the room. Calls in flight are cancelled.
func (p *Page) Leave() {
	_ = p.room.Leave()
	p.cancel()
	<-p.done
}

func (p *Page) notifyBridges(method string, params any) {
	p.mu.Lock()
	targets := make([]PeerID, 0, len(p.bridges))
	for id := range p.bridges {
		targets = append(targets, id)
	}
	p.mu.Unlock()
	slices.Sort(targets)
	msg := rpcMessage{Method: method}
	if params != nil {
		msg.Params = mustJSON(params)
	}
	for _, id := range targets {
		_ = p.room.Send(p.ctx, id, MessageKind, encode(msg))
	}
}

func (p *Page) loop() {
	defer close(p.done)
	for ev := range p.room.Events() {
		switch ev.Type {
		case PeerLeft:
			p.mu.Lock()
			delete(p.bridges, ev.Peer.ID)
			p.mu.Unlock()
		case MessageReceived:
			if ev.Kind != MessageKind {
				continue
			}
			var m rpcMessage
			if json.Unmarshal(ev.Payload, &m) != nil {
				continue
			}
			if p.trusted != nil && !p.trusted[ev.Peer.Subject] {
				continue // not one of the host's bridges
			}
			p.handle(ev.Peer.ID, m)
		}
	}
	p.cancel()
}

func (p *Page) handle(from PeerID, m rpcMessage) {
	switch m.Method {
	case "tools/list":
		// Greet before answering: a bridge handles one peer's messages in
		// order, so by the time it has the list it also has the page's state.
		p.greet(from)
		p.reply(from, m.ID, map[string]any{"tools": p.descriptors()})
	case "tools/call":
		p.greet(from)
		p.call(from, m)
	case "notifications/cancelled":
		var c struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		_ = json.Unmarshal(m.Params, &c)
		p.mu.Lock()
		cancel := p.running[string(from)+"/"+string(c.RequestID)]
		p.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case MethodPairedByCode:
		p.pairedByCode(m)
	case MethodPairingRequested:
		var req PairingRequest
		_ = json.Unmarshal(m.Params, &req)
		p.mu.Lock()
		fn := p.onPairing
		p.mu.Unlock()
		if fn != nil {
			fn(req)
		}
	default:
		if len(m.ID) > 0 && m.Method != "" {
			p.send(from, rpcMessage{ID: m.ID, Error: &rpcError{Code: core.ErrCodeMethodNotFound, Message: "method not found: " + m.Method}})
		}
	}
}

// greet records a bridge the first time it speaks and sends it the state it
// missed, since the page cannot address a bridge before hearing from it.
func (p *Page) greet(from PeerID) {
	p.mu.Lock()
	known := p.bridges[from]
	p.bridges[from] = true
	label, allowed, offer, grants, focusedAt := p.label, p.allowed, p.offer, slices.Clone(p.grants), p.focusedAt
	p.mu.Unlock()
	if known {
		return
	}
	if !focusedAt.IsZero() {
		p.send(from, rpcMessage{Method: MethodPageState, Params: mustJSON(map[string]any{"label": label, "focused": true, "focusedAt": focusedAt.Format(time.RFC3339Nano)})})
	}
	if len(grants) > 0 {
		p.send(from, rpcMessage{Method: MethodGrants, Params: mustJSON(map[string]any{"grants": grants})})
	}
	if left := time.Until(offer.expires); offer.code != "" && left > 0 {
		p.send(from, rpcMessage{Method: MethodPairingOffer, Params: mustJSON(map[string]any{"code": offer.code, "ttlMs": left.Milliseconds()})})
	}
	if label != "" {
		p.send(from, rpcMessage{Method: MethodPageState, Params: mustJSON(map[string]any{"label": label})})
	}
	if allowed {
		p.send(from, rpcMessage{Method: MethodAgentsAllowed, Params: mustJSON(map[string]any{"allowed": true})})
	}
}

func (p *Page) descriptors() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, 0, len(p.tools))
	for name := range p.tools {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		t := p.tools[name]
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
			"annotations": map[string]any{"readOnlyHint": t.ReadOnly},
		})
	}
	return out
}

func (p *Page) call(from PeerID, m rpcMessage) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
			Caller        Principal       `json:"io.mcpkit/rooms/caller"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(m.Params, &params)
	p.mu.Lock()
	tool, ok := p.tools[params.Name]
	p.mu.Unlock()
	if !ok || tool.Execute == nil {
		p.send(from, rpcMessage{ID: m.ID, Error: &rpcError{Code: core.ErrCodeInvalidParams, Message: "unknown tool: " + params.Name}})
		return
	}
	key := string(from) + "/" + string(m.ID)
	ctx, cancel := context.WithCancel(p.ctx)
	p.mu.Lock()
	p.running[key] = cancel
	p.mu.Unlock()

	progress := func(float64, float64, string) {}
	if len(params.Meta.ProgressToken) > 0 {
		token := params.Meta.ProgressToken
		progress = func(done, total float64, message string) {
			p.send(from, rpcMessage{Method: "notifications/progress", Params: mustJSON(map[string]any{
				"progressToken": token, "progress": done, "total": total, "message": message,
			})})
		}
	}

	go func() {
		defer func() {
			cancel()
			p.mu.Lock()
			delete(p.running, key)
			p.mu.Unlock()
		}()
		res, err := tool.Execute(ctx, PageCall{Arguments: params.Arguments, From: from, Progress: progress, Caller: params.Meta.Caller})
		if ctx.Err() != nil {
			return // cancelled: MCP sends no response for a cancelled request
		}
		if err != nil {
			res = core.ErrorResult(err.Error())
		}
		p.reply(from, m.ID, res)
	}()
}

func (p *Page) reply(to PeerID, id json.RawMessage, result any) {
	p.send(to, rpcMessage{ID: id, Result: mustJSON(result)})
}

func (p *Page) send(to PeerID, m rpcMessage) {
	_ = p.room.Send(p.ctx, to, MessageKind, encode(m))
}
