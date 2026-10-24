package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Scopes an agent's token carries to use the bridge. A page tool needs
// ScopePagesRead when it declares itself read-only and ScopePagesAct
// otherwise.
const (
	ScopeRoomsAttach = "rooms:attach"
	ScopePagesRead   = "pages:read"
	ScopePagesAct    = "pages:act"
)

// DefaultCallTimeout bounds how long the bridge waits for a page to answer a
// tool call before failing it.
const DefaultCallTimeout = 30 * time.Second

// Option configures a Bridge.
type Option func(*Bridge)

// WithCallTimeout sets how long a page has to answer a call (default
// DefaultCallTimeout). It also bounds the tools/list the bridge sends a page
// that joins.
func WithCallTimeout(d time.Duration) Option {
	return func(b *Bridge) { b.callTimeout = d }
}

// WithLocalSubject is single-user local mode: a session whose request carries
// no validated claims acts for subject. Use it only where the server has no
// identity provider, e.g. a design viewer on a laptop that mints its own relay
// tokens for the same subject. With it, the page's agents_allowed switch is
// the only step before an agent can drive the page.
func WithLocalSubject(subject string) Option {
	return func(b *Bridge) { b.localSubject = subject }
}

// WithRoomOf maps a page tool call's arguments to the room it is about, for
// hosts whose tools already name what they act on: agni maps its design
// argument to design:<mount>/<entry>. The arguments reach the page unchanged.
// Without it, the bridge adds an optional room property to every page tool.
// A call that names no room goes to the session's default rooms from attach,
// on the session wire, or fails.
func WithRoomOf(fn func(args map[string]any) (string, error)) Option {
	return func(b *Bridge) { b.roomOf = fn }
}

// PageToolDecl declares one page tool up front: what tools/list shows for it,
// whichever pages offer it at the moment.
//
// FanOut makes a call go to every tab in its room that offers the tool and
// agreed to the caller, instead of to one, and answer with a FanOutResult per
// tab. Agents narrow it with a targets argument, {users: [...]}, which the
// bridge adds to the schema in place of page. A fan-out tool must be ReadOnly;
// one-to-many changes are the host's to send, with Notify.
type PageToolDecl struct {
	Name        string
	Description string
	InputSchema map[string]any
	ReadOnly    bool
	FanOut      bool
}

// WithPageTools fixes the bridge's page-tool catalog. tools/list then answers
// the same for every caller, so it is cacheable (set server.WithListTTLMs) and
// needs no list_changed, which is what the stateless wire needs. Pages still
// register and unregister tools at runtime, and a call goes only to a tab that
// offers the tool right now; a page registering a name not declared, or a
// declared one with a different schema or read-only hint, has it refused
// (tool_refused). Without it the bridge keeps a dynamic registry, filtered per
// session, which works on the session wire only.
//
// A declaration with a reserved name (the bridge's own tools), or whose
// schema declares page, or room without WithRoomOf, makes Register panic.
func WithPageTools(decls []PageToolDecl) Option {
	return func(b *Bridge) { b.declared = slices.Clone(decls) }
}

// WithResourceMetadataURL sets the protected-resource metadata URL that the
// bridge's insufficient_scope challenges point at.
func WithResourceMetadataURL(url string) Option {
	return func(b *Bridge) { b.prmURL = url }
}

// Bridge lets agents call tools that web pages offer. It is an MCP client to
// every page in the rooms its agents attach to, over a RoomTransport, and an
// MCP server to the agents, so to an agent each page tool looks like a tool of
// the bridge.
//
// Agreement is held by the pages, and every call names its room, so calls need
// no MCP session. Without declared page tools (WithPageTools) the page-tool
// registry is dynamic and filtered per caller, which serves the stateless wire
// only with WithDynamicCatalogTTL.
type Bridge struct {
	transport    RoomTransport
	callTimeout  time.Duration
	localSubject string
	prmURL       string
	roomOf       func(args map[string]any) (string, error)
	// declared is the fixed page-tool catalog from WithPageTools; nil means
	// the dynamic registry, which needs the session wire.
	declared    []PageToolDecl
	fanOutLimit int
	// dynamicTTL, when set, serves the dynamic registry on the stateless
	// wire with this ttlMs; scopeFilter hides tools the caller lacks scopes
	// for. Both from catalog.go.
	dynamicTTL  time.Duration
	scopeFilter bool
	onEvent     func(PageEvent)
	fb          *fallbacks

	srv    *server.Server
	ctx    context.Context
	cancel context.CancelFunc
	joinMu sync.Mutex

	mu       sync.Mutex
	rooms    map[string]*roomState
	sessions map[string]*session
	pending  map[string]*pendingCall
	defs     map[string]toolSpec
	// pairFails are the times a principal entered a wrong code with pair,
	// within the last minute, keyed by Principal.key.
	pairFails map[string][]time.Time

	// pendingDefs are tools admitted under mu and not yet in the server's
	// registry, which syncTools adds outside the lock.
	pendingDefs []core.ToolDef
}

type roomState struct {
	resource string
	room     Room
	pages    map[PeerID]*pageState
	// codes are the codes attach handed out, which a page accepts with
	// AcceptPairing, keyed by code; principalCodes finds a principal's.
	codes          map[string]agentCode
	principalCodes map[string]string
	offers         map[PeerID]pairingOffer
	// attachedBy is who attached to the room and when, by Principal.key, so
	// a page listed later still hears they are around.
	attachedBy map[string]Presence
	// settled closes once the pages present when the bridge joined have
	// been listed (or failed to be).
	settled chan struct{}
}

type pageState struct {
	peer        Peer
	listed      bool
	label       string
	lastFocus   time.Time
	allowAgents bool
	grants      []Principal // the page's own list, as it last sent it
	tools       map[string]bool
	// presence is the agents this bridge has seen around the page, and
	// reported what it last sent the page of each, both by Principal.key.
	presence map[string]Presence
	reported map[string]Presence
}

// session is what the bridge keeps for an MCP session on the session wire:
// whose it is, where to push to it, and its default rooms from attach. It
// holds no agreement; that is the page's.
type session struct {
	id        string
	principal Principal
	notify    context.Context
	attached  map[string]bool
}

type toolSpec struct {
	key      string // canonical inputSchema plus readOnly; a different key is a conflict
	readOnly bool
	fanOut   bool
}

type pendingCall struct {
	page     PeerID
	done     chan callOutcome
	progress func(json.RawMessage)
}

type callOutcome struct {
	msg rpcMessage
	err error
}

// New returns a bridge that joins rooms through t. Call Register to serve it.
func New(t RoomTransport, opts ...Option) *Bridge {
	ctx, cancel := context.WithCancel(context.Background())
	b := &Bridge{
		transport:   t,
		callTimeout: DefaultCallTimeout,
		fanOutLimit: DefaultFanOutLimit,
		ctx:         ctx,
		cancel:      cancel,
		rooms:       make(map[string]*roomState),
		sessions:    make(map[string]*session),
		pending:     make(map[string]*pendingCall),
		defs:        make(map[string]toolSpec),
		pairFails:   make(map[string][]time.Time),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Register adds the bridge's tools (attach, detach, list_pages, pair, and with
// WithEventFallbacks next_request and the room events resource) to srv and
// installs two middlewares: one that shows each session only the page tools of
// rooms it attached to, and a scope gate that turns a missing scope on any of
// the bridge's tools into a 403 with an insufficient_scope challenge. It also
// hooks srv's session close, so a closed session's attachments and pairings
// end with it. Page tools are added to and removed from srv's registry as
// pages come and go.
func (b *Bridge) Register(srv *server.Server) {
	b.srv = srv
	srv.RegisterTool(core.ToolDef{
		Name:        "attach",
		Description: "Join the room for a resource and get a pairing code for pages whose user is not you. On a session it also makes the resource your default room. Calls can name the resource instead, so attach is never required.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"resource": map[string]any{"type": "string", "description": "The resource the pages show, e.g. design:demo/board.kicad_sch"}},
			"required":   []string{"resource"},
		},
		ScopeChallenge: core.RequireScopes(ScopeRoomsAttach),
	}, b.attach)
	srv.RegisterTool(core.ToolDef{
		Name:        "detach",
		Description: "Drop a resource from this session's default rooms. Pages that agreed to you still do; their users revoke that on the page.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"resource": map[string]any{"type": "string"}},
			"required":   []string{"resource"},
		},
		ScopeChallenge: core.RequireScopes(ScopeRoomsAttach),
	}, b.detach)
	srv.RegisterTool(core.ToolDef{
		Name:        "list_pages",
		Description: "List the pages showing a resource: id, user, label, last focus, whether each agreed to you and which one calls go to by default. Name the resource; on a session it defaults to the rooms you attached to.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"resource": map[string]any{"type": "string", "description": "The resource whose pages to list, e.g. design:demo/board.kicad_sch"}},
		},
		Annotations:    map[string]any{"readOnlyHint": true},
		ScopeChallenge: core.RequireScopes(ScopePagesRead),
	}, b.listPages)
	srv.RegisterTool(core.ToolDef{
		Name:        "pair",
		Description: "Pair this session with a page in an attached room by entering the code the page shows its user. Codes are single-use and short-lived; the page's user sees which agent paired.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource": map[string]any{"type": "string", "description": "The attached resource whose page shows the code"},
				"code":     map[string]any{"type": "string", "description": "The code the page shows"},
			},
			"required": []string{"resource", "code"},
		},
		ScopeChallenge: core.RequireScopes(ScopeRoomsAttach),
	}, b.pair)
	b.registerDeclared(srv)
	middleware := []server.Middleware{b.filterToolsList, b.scopeGate}
	if b.fb != nil {
		b.registerFallbacks(srv)
		middleware = append(middleware, b.subscribeGate)
	}
	srv.UseMiddleware(middleware...)
	srv.OnSessionClose(func(_ context.Context, sessionID string) {
		b.EndSession(sessionID)
		if b.fb != nil {
			b.fb.forget(sessionID)
		}
	})
}

// isBridgeTool reports whether name is one of the bridge's own tools or a page
// tool it registered.
func (b *Bridge) isBridgeTool(name string) bool {
	switch name {
	case "attach", "detach", "list_pages", "pair", "next_request":
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.defs[name]
	return ok
}

// scopeGate enforces the ScopeChallenge on the bridge's own tools before
// dispatch, so a refusal is a real 403 with a WWW-Authenticate header. It
// answers exactly as ext/auth's NewScopeMiddleware would; the bridge carries
// its own because constraint C4 keeps extension modules from requiring each
// other, and a bridge whose acting tools are ungated unless the host remembers
// a middleware is the wrong default. The host's other tools are left to the
// host.
func (b *Bridge) scopeGate(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
	if req.Method != "tools/call" || b.srv == nil {
		return next(ctx, req)
	}
	var p struct {
		Name string `json:"name"`
	}
	if req.Params.Bind(&p) != nil || !b.isBridgeTool(p.Name) {
		return next(ctx, req)
	}
	def, ok := b.srv.Registry().ToolDef(p.Name)
	if !ok || def.ScopeChallenge == nil {
		return next(ctx, req)
	}
	challenge, err := def.ScopeChallenge(ctx, req)
	if err == nil && challenge == nil {
		return next(ctx, req)
	}
	return nil, challenge.AuthError(b.prmURL)
}

// EndSession drops what the bridge keeps for an MCP session: its default
// rooms and where to push to it. Agreement is the page's, so pages that agreed
// to the session's principal still do. Register wires it to the server's
// session-close hook; it stays exported for hosts that end sessions some other
// way. Calling it twice, or for a session the bridge never saw, does nothing.
func (b *Bridge) EndSession(sessionID string) {
	b.mu.Lock()
	delete(b.sessions, sessionID)
	b.mu.Unlock()
}

// Close leaves every room. Calls in flight fail as if their page had left.
func (b *Bridge) Close() {
	b.mu.Lock()
	rooms := make([]Room, 0, len(b.rooms))
	for _, rs := range b.rooms {
		rooms = append(rooms, rs.room)
	}
	b.mu.Unlock()
	for _, r := range rooms {
		_ = r.Leave()
	}
	b.cancel()
}

// PageInfo is one row of list_pages and of the roster a targeting error
// carries.
type PageInfo struct {
	Page      PeerID     `json:"page"`
	Resource  string     `json:"resource"`
	User      string     `json:"user"`
	Label     string     `json:"label,omitempty"`
	LastFocus *time.Time `json:"lastFocus,omitempty"`
	Allowed   bool       `json:"allowed"`
	Default   bool       `json:"default"`
	Tools     []string   `json:"tools"`
}

func (b *Bridge) subjectOf(tc core.ToolContext) (subject, client string) {
	return b.subjectOfClaims(tc.AuthClaims())
}

// subjectOfClaims is the subject and client behind validated claims, or the
// local subject when there are none.
func (b *Bridge) subjectOfClaims(c *core.Claims) (subject, client string) {
	if c != nil && c.Subject != "" {
		for _, k := range []string{"client_id", "azp"} {
			if v, ok := c.Extra[k].(string); ok {
				client = v
				break
			}
		}
		return c.Subject, client
	}
	return b.localSubject, ""
}

func resourceArg(req core.ToolRequest) string {
	var a struct {
		Resource string `json:"resource"`
	}
	_ = json.Unmarshal(req.Arguments, &a)
	return a.Resource
}

// sortedPages returns the ids of rs's listed pages in order.
func sortedPages(rs *roomState) []PeerID {
	ids := make([]PeerID, 0, len(rs.pages))
	for id, pg := range rs.pages {
		if pg.listed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// joinRoom joins resource's room the first time any session attaches to it.
// The bridge stays in the room until Close, so a page's tools keep their
// registration while agents come and go.
func (b *Bridge) joinRoom(resource string) (*roomState, error) {
	b.joinMu.Lock()
	defer b.joinMu.Unlock()
	b.mu.Lock()
	rs := b.rooms[resource]
	b.mu.Unlock()
	if rs != nil {
		return rs, nil
	}
	room, err := b.transport.Join(b.ctx, resource)
	if err != nil {
		return nil, err
	}
	rs = &roomState{
		resource: resource, room: room, pages: make(map[PeerID]*pageState),
		codes: make(map[string]agentCode), principalCodes: make(map[string]string),
		offers: make(map[PeerID]pairingOffer), attachedBy: make(map[string]Presence), settled: make(chan struct{}),
	}
	roster := room.Roster()
	b.mu.Lock()
	b.rooms[resource] = rs
	for _, p := range roster {
		rs.pages[p.ID] = newPageState(p)
	}
	b.mu.Unlock()
	go b.roomLoop(rs)
	var listing sync.WaitGroup
	for _, p := range roster {
		listing.Go(func() { b.listTools(rs, p.ID) })
	}
	go func() {
		listing.Wait()
		close(rs.settled)
	}()
	return rs, nil
}

func newPageState(p Peer) *pageState {
	return &pageState{peer: p, tools: make(map[string]bool), presence: make(map[string]Presence), reported: make(map[string]Presence)}
}

func (b *Bridge) roomLoop(rs *roomState) {
	for ev := range rs.room.Events() {
		switch ev.Type {
		case PeerJoined:
			b.mu.Lock()
			rs.pages[ev.Peer.ID] = newPageState(ev.Peer)
			b.mu.Unlock()
			go b.listTools(rs, ev.Peer.ID)
		case PeerLeft:
			b.pageLeft(rs, ev.Peer.ID)
		case MessageReceived:
			if ev.Kind != MessageKind {
				continue
			}
			var m rpcMessage
			if json.Unmarshal(ev.Payload, &m) == nil {
				b.fromPage(rs, ev.Peer.ID, m)
			}
		}
	}
}

func (b *Bridge) fromPage(rs *roomState, from PeerID, m rpcMessage) {
	if m.isResponse() {
		var id string
		if json.Unmarshal(m.ID, &id) != nil {
			return
		}
		b.mu.Lock()
		pc := b.pending[id]
		if pc == nil || pc.page != from {
			b.mu.Unlock()
			return // unknown call id, or a reply from a peer the call did not go to
		}
		delete(b.pending, id)
		b.mu.Unlock()
		pc.done <- callOutcome{msg: m}
		return
	}

	var notify []context.Context
	var event *PageEvent
	var paired *PairedByCode
	b.mu.Lock()
	pg := rs.pages[from]
	if pg == nil {
		b.mu.Unlock()
		return
	}
	switch m.Method {
	case MethodPageEvent:
		if pg.listed {
			if e, ok := b.pageEventLocked(rs, pg, m.Params); ok {
				event = &e
			}
		}
	case "notifications/tools/list_changed":
		go b.listTools(rs, from)
	case "notifications/progress":
		var p struct {
			Token json.RawMessage `json:"progressToken"`
		}
		_ = json.Unmarshal(m.Params, &p)
		var id string
		_ = json.Unmarshal(p.Token, &id)
		if pc := b.pending[id]; pc != nil && pc.page == from && pc.progress != nil {
			pc.progress(m.Params)
		}
	case MethodPageState:
		var st struct {
			Label     string `json:"label"`
			Focused   bool   `json:"focused"`
			FocusedAt string `json:"focusedAt"`
		}
		_ = json.Unmarshal(m.Params, &st)
		pg.label = st.Label
		if st.Focused {
			// The page's own clock, when it says, so a focus from before
			// this bridge joined keeps its place; never in the future.
			at := time.Now()
			if t, err := time.Parse(time.RFC3339Nano, st.FocusedAt); err == nil && t.Before(at) {
				at = t
			}
			pg.lastFocus = at
		}
	case MethodAgentsAllowed:
		var a struct {
			Allowed bool `json:"allowed"`
		}
		_ = json.Unmarshal(m.Params, &a)
		pg.allowAgents = a.Allowed
		for _, sid := range sortedKeys(b.sessions) {
			if s := b.sessions[sid]; s.principal.Subject == pg.peer.Subject {
				notify = append(notify, s.notify)
			}
		}
	case MethodGrants:
		var g struct {
			Grants []Principal `json:"grants"`
		}
		_ = json.Unmarshal(m.Params, &g)
		pg.grants = slices.DeleteFunc(g.Grants, func(p Principal) bool { return p.Subject == "" })
		for _, sid := range sortedKeys(b.sessions) {
			notify = append(notify, b.sessions[sid].notify)
		}
	case MethodAcceptPairing:
		var a struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(m.Params, &a)
		code := strings.ToUpper(a.Code)
		if c, ok := rs.codes[code]; ok && time.Now().Before(c.expires) {
			grantLocked(pg, c.principal)
			paired = &PairedByCode{
				PairingRequest: PairingRequest{Subject: c.principal.Subject, Client: c.principal.Client, SameSubject: c.principal.Subject == pg.peer.Subject},
				Code:           code,
			}
			for _, s := range b.notifyPrincipalLocked(c.principal) {
				notify = append(notify, s.notify)
			}
		}
	case MethodPairingOffer:
		// Recorded before the page is listed: a page sends its standing
		// offer when a bridge greets it, which can arrive before the
		// tools/list answer is processed. pair checks the page is listed.
		b.offerLocked(rs, pg, m.Params)
	}
	b.mu.Unlock()
	if paired != nil {
		// The page decides: it adds the grant and sends its grants to every
		// bridge, which is how the other bridges in the room hear of it.
		b.sendTo(rs, from, rpcMessage{Method: MethodPairedByCode, Params: mustJSON(paired)})
	}
	for _, ctx := range notify {
		core.Notify(ctx, "notifications/tools/list_changed", nil)
	}
	if event != nil {
		if b.onEvent != nil {
			b.onEvent(*event)
		}
		if b.fb != nil {
			b.fallbackEvent(*event)
		}
	}
}

// pageLeft forgets a page, fails every call pending on it at once rather than
// at its deadline, and drops tools no page offers any more.
func (b *Bridge) pageLeft(rs *roomState, id PeerID) {
	b.mu.Lock()
	pg := rs.pages[id]
	delete(rs.pages, id)
	delete(rs.offers, id)
	var failed []*pendingCall
	for cid, pc := range b.pending {
		if pc.page == id {
			failed = append(failed, pc)
			delete(b.pending, cid)
		}
	}
	label := ""
	if pg != nil {
		label = pg.label
	}
	b.mu.Unlock()

	err := fmt.Errorf("page %s left the room before answering", pageName(id, label))
	for _, pc := range failed {
		pc.done <- callOutcome{err: err}
	}
	b.syncTools(rs)
}

func pageName(id PeerID, label string) string {
	if label == "" {
		return string(id)
	}
	return fmt.Sprintf("%s (%s)", id, label)
}

// request sends one JSON-RPC request to a page and waits for its answer, the
// page leaving, ctx ending, or the call timeout, whichever is first. The id on
// the relay is a fresh call id, since an agent's own request id is unique only
// within its session. With progress set, the call id doubles as the progress
// token, so the page's progress can be matched back to this call.
func (b *Bridge) request(ctx context.Context, rs *roomState, page PeerID, method string, params map[string]any, progress func(json.RawMessage)) (rpcMessage, error) {
	callID := randomID(16)
	if progress != nil {
		meta, _ := params["_meta"].(map[string]any)
		if meta == nil {
			meta = map[string]any{}
		}
		meta["progressToken"] = callID
		params["_meta"] = meta
	}
	pc := &pendingCall{page: page, done: make(chan callOutcome, 1), progress: progress}
	b.mu.Lock()
	b.pending[callID] = pc
	label := ""
	if pg := rs.pages[page]; pg != nil {
		label = pg.label
	}
	b.mu.Unlock()
	forget := func() {
		b.mu.Lock()
		delete(b.pending, callID)
		b.mu.Unlock()
	}

	msg := rpcMessage{ID: mustJSON(callID), Method: method}
	if params != nil {
		msg.Params = mustJSON(params)
	}
	if err := rs.room.Send(b.ctx, page, MessageKind, encode(msg)); err != nil {
		forget()
		return rpcMessage{}, fmt.Errorf("page %s could not be reached: %w", pageName(page, label), err)
	}

	cancelPage := func(reason string) {
		forget()
		b.sendTo(rs, page, rpcMessage{Method: "notifications/cancelled", Params: mustJSON(map[string]any{"requestId": callID, "reason": reason})})
	}
	timer := time.NewTimer(b.callTimeout)
	defer timer.Stop()
	select {
	case out := <-pc.done:
		if out.err != nil {
			return rpcMessage{}, out.err
		}
		if out.msg.Error != nil {
			return rpcMessage{}, fmt.Errorf("page %s: %s", pageName(page, label), out.msg.Error.Message)
		}
		return out.msg, nil
	case <-ctx.Done():
		cancelPage("cancelled by the agent")
		return rpcMessage{}, ctx.Err()
	case <-timer.C:
		cancelPage("timed out")
		return rpcMessage{}, fmt.Errorf("page %s did not answer within %s", pageName(page, label), b.callTimeout)
	}
}

func (b *Bridge) sendTo(rs *roomState, page PeerID, m rpcMessage) {
	_ = rs.room.Send(b.ctx, page, MessageKind, encode(m))
}

// listTools asks a page for its tools and reconciles them with what the
// bridge offers. A page is in the roster only once this has succeeded, so a
// peer that is not a page (another bridge, say) never shows up.
func (b *Bridge) listTools(rs *roomState, page PeerID) {
	resp, err := b.request(b.ctx, rs, page, "tools/list", nil, nil)
	if err != nil {
		return
	}
	var list struct {
		Tools []core.ToolDef `json:"tools"`
	}
	if json.Unmarshal(resp.Result, &list) != nil {
		return
	}
	type refusal struct{ name, reason string }
	var refused []refusal

	b.mu.Lock()
	pg := rs.pages[page]
	if pg == nil {
		b.mu.Unlock()
		return
	}
	accepted := make(map[string]bool)
	for _, t := range list.Tools {
		if reason := b.admitLocked(t); reason != "" {
			refused = append(refused, refusal{t.Name, reason})
			continue
		}
		accepted[t.Name] = true
	}
	pg.tools = accepted
	firstListing := !pg.listed
	pg.listed = true
	// A page listed after a session attached never got that session's
	// pairing request, so it gets it now.
	asks := []PairingRequest{}
	if firstListing {
		for _, sid := range sortedKeys(b.sessions) {
			s := b.sessions[sid]
			if s.attached[rs.resource] && !agreesLocked(pg, s.principal) {
				asks = append(asks, PairingRequest{Subject: s.principal.Subject, Client: s.principal.Client, SameSubject: s.principal.Subject == pg.peer.Subject})
			}
		}
	}
	var presence []Presence
	if firstListing {
		presence = attachedLocked(rs, pg, time.Now())
	}
	b.mu.Unlock()

	if presence != nil {
		b.sendPresence(rs, page, presence)
	}
	for _, a := range asks {
		b.sendTo(rs, page, rpcMessage{Method: MethodPairingRequested, Params: mustJSON(a)})
	}

	for _, r := range refused {
		b.sendTo(rs, page, rpcMessage{Method: MethodToolRefused, Params: mustJSON(map[string]any{"name": r.name, "reason": r.reason})})
	}
	b.syncTools(rs)
}

// admitLocked decides whether the bridge can offer a page's tool. The first
// page to register a name fixes its schema across the bridge; a later page
// whose schema differs has that tool refused.
func (b *Bridge) admitLocked(t core.ToolDef) string {
	if reason := b.reservedLocked(t); reason != "" {
		return reason
	}
	readOnly, _ := t.Annotations["readOnlyHint"].(bool)
	key := string(mustJSON(t.InputSchema)) + fmt.Sprint(readOnly)
	if b.declared != nil {
		spec, ok := b.defs[t.Name]
		switch {
		case !ok:
			return "the bridge's catalog (WithPageTools) does not declare this tool"
		case spec.key != key:
			return "the schema or read-only hint differs from the bridge's declaration"
		}
		return ""
	}
	if spec, ok := b.defs[t.Name]; ok && spec.key != key {
		return "another page already offers a tool with this name and a different schema"
	}
	if _, ok := b.defs[t.Name]; !ok {
		b.defs[t.Name] = toolSpec{key: key, readOnly: readOnly}
		b.pendingDefs = append(b.pendingDefs, t)
	}
	return ""
}

// syncTools registers page tools that have no registration yet, removes the
// ones no page offers any more, and tells the sessions attached to rs that
// their list may have changed.
func (b *Bridge) syncTools(rs *roomState) {
	b.mu.Lock()
	add := b.pendingDefs
	b.pendingDefs = nil
	offered := make(map[string]bool)
	for _, r := range b.rooms {
		for _, pg := range r.pages {
			for name := range pg.tools {
				offered[name] = true
			}
		}
	}
	var remove []string
	for _, name := range sortedKeys(b.defs) {
		if !offered[name] && b.declared == nil {
			remove = append(remove, name)
			delete(b.defs, name)
		}
	}
	var notify []context.Context
	for _, sid := range sortedKeys(b.sessions) {
		if s := b.sessions[sid]; s.attached[rs.resource] {
			notify = append(notify, s.notify)
		}
	}
	b.mu.Unlock()

	if b.srv != nil && b.declared == nil {
		reg := b.srv.Registry()
		for _, t := range add {
			if offered[t.Name] {
				_ = reg.AddTool(b.bridgeDef(t, false), b.pageToolHandler(t.Name))
			}
		}
		for _, name := range remove {
			reg.RemoveTool(name)
		}
	}
	for _, ctx := range notify {
		core.Notify(ctx, "notifications/tools/list_changed", nil)
	}
}

// bridgeDef is the page's descriptor as agents see it: the same tool with an
// optional page property (targets for a fan-out tool) and the scope its kind
// needs.
func (b *Bridge) bridgeDef(t core.ToolDef, fanOut bool) core.ToolDef {
	schema := map[string]any{"type": "object"}
	if s, ok := t.InputSchema.(map[string]any); ok {
		for k, v := range s {
			schema[k] = v
		}
	}
	props := map[string]any{}
	if p, ok := schema["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	if fanOut {
		props["targets"] = targetsSchema
	} else {
		props["page"] = map[string]any{"type": "string", "description": "Page id from list_pages. Omit for the page that agreed to you which your user focused last, or the only one that agreed."}
	}
	if b.roomOf == nil {
		props["room"] = map[string]any{"type": "string", "description": "The resource the pages show, e.g. design:demo/board.kicad_sch. Omit to use your default room from attach."}
	}
	schema["properties"] = props
	readOnly, _ := t.Annotations["readOnlyHint"].(bool)
	scope := ScopePagesAct
	if readOnly {
		scope = ScopePagesRead
	}
	return core.ToolDef{
		Name:           t.Name,
		Description:    t.Description,
		InputSchema:    schema,
		Annotations:    t.Annotations,
		ScopeChallenge: core.RequireScopes(scope),
	}
}

// errNoTarget carries the roster so the model can choose a page.
type errNoTarget struct {
	reason string
	roster []PageInfo
}

func (b *Bridge) pageToolHandler(name string) core.ToolHandler {
	return func(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
		var args map[string]json.RawMessage
		_ = json.Unmarshal(req.Arguments, &args)
		var page PeerID
		if raw, ok := args["page"]; ok {
			var s string
			_ = json.Unmarshal(raw, &s)
			page = PeerID(s)
			delete(args, "page")
		}
		if args == nil {
			args = map[string]json.RawMessage{}
		}
		if b.declared == nil && b.dynamicTTL <= 0 && tc.SessionID() == "" {
			return core.ErrorResult("this bridge serves page tools on the session wire only; its host declares them with WithPageTools, or serves them with WithDynamicCatalogTTL, to serve callers with no session"), nil
		}
		room, err := b.roomOfCall(args)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		p, s := b.caller(tc)
		rooms, err := b.roomArgs(room, s)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		b.mu.Lock()
		fanOut := b.defs[name].fanOut
		b.mu.Unlock()
		if fanOut {
			return b.fanOutCall(tc, name, p, rooms, args)
		}

		rs, target, nt := b.target(p, rooms, name, page)
		if nt != nil {
			res := core.ErrorResult(nt.reason)
			res.StructuredContent = map[string]any{"error": nt.reason, "pages": nt.roster}
			return res, nil
		}

		b.seen(rs, []PeerID{target}, p, true)

		// The caller's principal, from the bridge's validated token, so the
		// page can refuse per caller (see PageCall.Caller).
		params := map[string]any{"name": name, "arguments": args, "_meta": map[string]any{MetaCaller: p}}
		var progress func(json.RawMessage)
		if token := tc.ProgressToken(); token != nil {
			progress = func(raw json.RawMessage) {
				var p struct {
					Progress float64 `json:"progress"`
					Total    float64 `json:"total"`
					Message  string  `json:"message"`
				}
				_ = json.Unmarshal(raw, &p)
				tc.EmitProgress(token, p.Progress, p.Total, p.Message)
			}
		}
		resp, err := b.request(tc, rs, target, "tools/call", params, progress)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, err
			}
			return core.ErrorResult(err.Error()), nil
		}
		var res core.ToolResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			return core.ErrorResult("page sent a result that is not a tool result: " + err.Error()), nil
		}
		return res, nil
	}
}

// filterToolsList shows a caller the bridge's own tools plus the page tools
// it may see (pageToolsFilterLocked), and with WithScopeFilteredList only the
// tools its scopes let it call. Page tools live in the server's one registry,
// so without this every session would see every room's tools.
func (b *Bridge) filterToolsList(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
	resp, err := next(ctx, req)
	if req.Method != "tools/list" || err != nil || resp == nil || resp.Error != nil || resp.Result == nil {
		return resp, err
	}
	if b.declared != nil && !b.scopeFilter {
		return resp, nil
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return resp, nil
	}
	var result map[string]json.RawMessage
	var tools []json.RawMessage
	if json.Unmarshal(raw, &result) != nil || json.Unmarshal(result["tools"], &tools) != nil {
		return resp, nil
	}

	sid := core.GetSessionID(ctx)
	p := b.principalOfCtx(ctx)
	b.mu.Lock()
	visible := b.pageToolsFilterLocked(p, b.sessions[sid], sid == "")
	b.mu.Unlock()
	kept := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		var head struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(t, &head)
		if visible != nil && b.isPageTool(head.Name) && !visible[head.Name] {
			continue
		}
		if b.scopeFilter && b.missingScopes(ctx, head.Name) != nil {
			continue
		}
		kept = append(kept, t)
	}

	result["tools"] = mustJSON(kept)
	if b.scopeFilter || b.servesDynamicStateless(sid) {
		result["cacheScope"] = mustJSON(core.CacheScopePrivate)
	}
	if b.servesDynamicStateless(sid) {
		result["ttlMs"] = mustJSON(b.dynamicTTL.Milliseconds())
	}
	resp.Result = result
	return resp, nil
}

// roomOfCall finds the room a page tool call names: through WithRoomOf from
// the arguments, which go to the page unchanged, or from the room property the
// bridge added, which it removes. Empty means the call named none.
func (b *Bridge) roomOfCall(args map[string]json.RawMessage) (string, error) {
	if b.roomOf != nil {
		decoded := make(map[string]any, len(args))
		for k, v := range args {
			var x any
			_ = json.Unmarshal(v, &x)
			decoded[k] = x
		}
		room, err := b.roomOf(decoded)
		if err != nil {
			return "", fmt.Errorf("which room: %w", err)
		}
		return room, nil
	}
	var room string
	if raw, ok := args["room"]; ok {
		_ = json.Unmarshal(raw, &room)
		delete(args, "room")
	}
	return room, nil
}

// registerDeclared adds the WithPageTools catalog to srv's registry, once,
// and records each declaration so admitLocked holds pages to it.
func (b *Bridge) registerDeclared(srv *server.Server) {
	if b.declared == nil {
		return
	}
	decls := slices.SortedFunc(slices.Values(b.declared), func(a, c PageToolDecl) int { return strings.Compare(a.Name, c.Name) })
	reg := srv.Registry()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, d := range decls {
		schema := d.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		t := core.ToolDef{Name: d.Name, Description: d.Description, InputSchema: schema, Annotations: map[string]any{"readOnlyHint": d.ReadOnly}}
		reason := b.reservedLocked(t)
		if reason == "" && d.FanOut {
			reason = fanOutReason(d)
		}
		if reason != "" {
			panic(fmt.Sprintf("rooms: WithPageTools: tool %q: %s", d.Name, reason))
		}
		b.defs[d.Name] = toolSpec{key: string(mustJSON(t.InputSchema)) + fmt.Sprint(d.ReadOnly), readOnly: d.ReadOnly, fanOut: d.FanOut}
		_ = reg.AddTool(b.bridgeDef(t, d.FanOut), b.pageToolHandler(d.Name))
	}
}

// reservedLocked refuses a tool whose name is one of the bridge's own, or
// whose schema declares a property the bridge adds.
func (b *Bridge) reservedLocked(t core.ToolDef) string {
	if t.Name == "" || slices.Contains([]string{"attach", "detach", "list_pages", "pair", "next_request"}, t.Name) {
		return "the name is reserved by the bridge"
	}
	schema, _ := t.InputSchema.(map[string]any)
	if props, ok := schema["properties"].(map[string]any); ok {
		if _, ok := props["page"]; ok {
			return "the bridge adds a property named page to every tool, so a tool may not declare one"
		}
		if _, ok := props["room"]; ok && b.roomOf == nil {
			return "the bridge adds a property named room to every tool, so a tool may not declare one unless the host maps rooms with WithRoomOf"
		}
	}
	return ""
}
