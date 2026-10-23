package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
// It needs the session wire: attach, pairings and the tools an agent sees are
// all per MCP session, and the stateless wire has none.
type Bridge struct {
	transport    RoomTransport
	callTimeout  time.Duration
	localSubject string
	prmURL       string
	onEvent      func(PageEvent)
	fb           *fallbacks

	srv    *server.Server
	ctx    context.Context
	cancel context.CancelFunc
	joinMu sync.Mutex

	mu       sync.Mutex
	rooms    map[string]*roomState
	sessions map[string]*session
	pending  map[string]*pendingCall
	defs     map[string]toolSpec

	// pendingDefs are tools admitted under mu and not yet in the server's
	// registry, which syncTools adds outside the lock.
	pendingDefs []core.ToolDef
}

type roomState struct {
	resource string
	room     Room
	pages    map[PeerID]*pageState
	codes    map[string]string // pairing code -> session id
	offers   map[PeerID]pairingOffer
}

type pageState struct {
	peer        Peer
	listed      bool
	label       string
	lastFocus   time.Time
	allowAgents bool
	allowed     map[string]bool // session ids this page agreed to
	tools       map[string]bool
}

type session struct {
	id       string
	handle   string // what pages see instead of the MCP session id
	subject  string
	client   string
	notify   context.Context
	attached map[string]bool
	paired   map[string]PeerID // resource -> the page calls default to
	codes    map[string]string // resource -> pairing code
	// pairFails are the times this session entered a wrong code, within
	// the last minute.
	pairFails []time.Time
}

type toolSpec struct {
	key      string // canonical inputSchema plus readOnly; a different key is a conflict
	readOnly bool
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
		ctx:         ctx,
		cancel:      cancel,
		rooms:       make(map[string]*roomState),
		sessions:    make(map[string]*session),
		pending:     make(map[string]*pendingCall),
		defs:        make(map[string]toolSpec),
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
		Description: "Attach this session to the room for a resource, so the tools of the pages showing it become callable. Returns a pairing code for pages whose user is not you.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"resource": map[string]any{"type": "string", "description": "The resource the pages show, e.g. design:demo/board.kicad_sch"}},
			"required":   []string{"resource"},
		},
		ScopeChallenge: core.RequireScopes(ScopeRoomsAttach),
	}, b.attach)
	srv.RegisterTool(core.ToolDef{
		Name:        "detach",
		Description: "Detach this session from a resource's room and end its pairings there.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"resource": map[string]any{"type": "string"}},
			"required":   []string{"resource"},
		},
		ScopeChallenge: core.RequireScopes(ScopeRoomsAttach),
	}, b.detach)
	srv.RegisterTool(core.ToolDef{
		Name:        "list_pages",
		Description: "List the pages in the rooms this session is attached to: id, user, label, last focus, whether each agreed to this session and which one calls go to by default.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"resource": map[string]any{"type": "string", "description": "Limit to one attached resource"}},
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

// EndSession drops everything the bridge holds for an MCP session: its
// attachments, its pairings and its pairing codes. Register wires it to the
// server's session-close hook, so a host need not call it; it stays exported
// for hosts that end a session some other way. Calling it twice, or for a
// session the bridge never saw, does nothing.
func (b *Bridge) EndSession(sessionID string) {
	b.mu.Lock()
	s := b.sessions[sessionID]
	if s == nil {
		b.mu.Unlock()
		return
	}
	for resource := range s.attached {
		b.detachLocked(s, resource)
	}
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
	Paired    bool       `json:"paired"`
	Tools     []string   `json:"tools"`
}

func (b *Bridge) subjectOf(tc core.ToolContext) (subject, client string) {
	if c := tc.AuthClaims(); c != nil && c.Subject != "" {
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

func (b *Bridge) attach(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resource := resourceArg(req)
	if resource == "" {
		return core.ErrorResult("attach needs a resource"), nil
	}
	sid := tc.SessionID()
	if sid == "" {
		return core.ErrorResult("attach needs an MCP session; the stateless wire has none"), nil
	}
	subject, client := b.subjectOf(tc)
	if subject == "" {
		return core.ErrorResult("attach needs an authenticated subject"), nil
	}
	rs, err := b.joinRoom(resource)
	if err != nil {
		return core.ErrorResult("could not join the room for " + resource + ": " + err.Error()), nil
	}

	b.mu.Lock()
	s := b.sessions[sid]
	if s == nil {
		s = &session{
			id: sid, handle: randomID(8), subject: subject, client: client,
			attached: make(map[string]bool), paired: make(map[string]PeerID), codes: make(map[string]string),
		}
		b.sessions[sid] = s
	}
	s.notify = core.DetachForBackground(tc)
	s.attached[resource] = true
	code := s.codes[resource]
	if code == "" {
		code = strings.ToUpper(randomID(3))
		s.codes[resource] = code
		rs.codes[code] = sid
	}
	asks := map[PeerID]PairingRequest{}
	for _, id := range sortedPages(rs) {
		pg := rs.pages[id]
		switch {
		case pg.allowed[sid]:
		case pg.allowAgents && pg.peer.Subject == subject:
			b.grantLocked(rs, pg, s)
		default:
			asks[id] = PairingRequest{Session: s.handle, Subject: subject, Client: client, SameSubject: pg.peer.Subject == subject}
		}
	}
	roster := b.rosterLocked(s, resource)
	b.mu.Unlock()

	for _, id := range slices.Sorted(maps.Keys(asks)) {
		b.sendTo(rs, id, rpcMessage{Method: MethodPairingRequested, Params: mustJSON(asks[id])})
	}
	core.Notify(s.notify, "notifications/tools/list_changed", nil)

	res := core.TextResult(fmt.Sprintf("Attached to %s. Pages whose user is not you can pair by accepting code %s, or by showing a code you enter with pair.", resource, code))
	out := map[string]any{"resource": resource, "pairingCode": code, "pages": roster}
	if b.fb != nil {
		out["eventsResource"] = EventsURI(resource)
	}
	res.StructuredContent = out
	return res, nil
}

func (b *Bridge) detach(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resource := resourceArg(req)
	b.mu.Lock()
	s := b.sessions[tc.SessionID()]
	if s == nil || !s.attached[resource] {
		b.mu.Unlock()
		return core.ErrorResult("this session is not attached to " + resource), nil
	}
	b.detachLocked(s, resource)
	notify := s.notify
	b.mu.Unlock()
	core.Notify(notify, "notifications/tools/list_changed", nil)
	return core.TextResult("Detached from " + resource), nil
}

func (b *Bridge) detachLocked(s *session, resource string) {
	delete(s.attached, resource)
	delete(s.paired, resource)
	if rs := b.rooms[resource]; rs != nil {
		delete(rs.codes, s.codes[resource])
		for _, pg := range rs.pages {
			delete(pg.allowed, s.id)
		}
	}
	delete(s.codes, resource)
}

func (b *Bridge) listPages(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resource := resourceArg(req)
	b.mu.Lock()
	s := b.sessions[tc.SessionID()]
	if s == nil || len(s.attached) == 0 {
		b.mu.Unlock()
		return core.ErrorResult("this session is not attached to any room; call attach first"), nil
	}
	if resource != "" && !s.attached[resource] {
		b.mu.Unlock()
		return core.ErrorResult("this session is not attached to " + resource), nil
	}
	roster := b.rosterLocked(s, resource)
	b.mu.Unlock()
	text, _ := json.Marshal(roster)
	res := core.TextResult(string(text))
	res.StructuredContent = map[string]any{"pages": roster}
	return res, nil
}

// grantLocked records that pg agreed to s. The page that agreed most recently
// is the one s's calls in that room go to by default.
func (b *Bridge) grantLocked(rs *roomState, pg *pageState, s *session) {
	pg.allowed[s.id] = true
	s.paired[rs.resource] = pg.peer.ID
}

func (b *Bridge) revokeLocked(rs *roomState, pg *pageState, sid string) {
	delete(pg.allowed, sid)
	if s := b.sessions[sid]; s != nil && s.paired[rs.resource] == pg.peer.ID {
		delete(s.paired, rs.resource)
	}
}

// rosterLocked lists the listed pages of s's attached rooms, or of one of
// them, sorted by resource and then page id.
func (b *Bridge) rosterLocked(s *session, only string) []PageInfo {
	resources := make([]string, 0, len(s.attached))
	for r := range s.attached {
		if only == "" || r == only {
			resources = append(resources, r)
		}
	}
	slices.Sort(resources)
	out := []PageInfo{}
	for _, r := range resources {
		rs := b.rooms[r]
		if rs == nil {
			continue
		}
		for _, id := range sortedPages(rs) {
			pg := rs.pages[id]
			info := PageInfo{
				Page: id, Resource: r, User: pg.peer.Subject, Label: pg.label,
				Allowed: pg.allowed[s.id], Paired: s.paired[r] == id, Tools: sortedKeys(pg.tools),
			}
			if !pg.lastFocus.IsZero() {
				t := pg.lastFocus
				info.LastFocus = &t
			}
			out = append(out, info)
		}
	}
	return out
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
	rs = &roomState{resource: resource, room: room, pages: make(map[PeerID]*pageState), codes: make(map[string]string), offers: make(map[PeerID]pairingOffer)}
	b.mu.Lock()
	b.rooms[resource] = rs
	for _, p := range room.Roster() {
		rs.pages[p.ID] = newPageState(p)
	}
	b.mu.Unlock()
	go b.roomLoop(rs)
	for _, p := range room.Roster() {
		go b.listTools(rs, p.ID)
	}
	return rs, nil
}

func newPageState(p Peer) *pageState {
	return &pageState{peer: p, allowed: make(map[string]bool), tools: make(map[string]bool)}
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
			Label   string `json:"label"`
			Focused bool   `json:"focused"`
		}
		_ = json.Unmarshal(m.Params, &st)
		pg.label = st.Label
		if st.Focused {
			pg.lastFocus = time.Now()
		}
	case MethodAgentsAllowed:
		var a struct {
			Allowed bool `json:"allowed"`
		}
		_ = json.Unmarshal(m.Params, &a)
		pg.allowAgents = a.Allowed
		for _, sid := range sortedKeys(b.sessions) {
			s := b.sessions[sid]
			if !s.attached[rs.resource] || s.subject != pg.peer.Subject {
				continue
			}
			if a.Allowed {
				b.grantLocked(rs, pg, s)
			} else {
				b.revokeLocked(rs, pg, sid)
			}
			notify = append(notify, s.notify)
		}
	case MethodAcceptPairing:
		var a struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(m.Params, &a)
		if s := b.sessions[rs.codes[strings.ToUpper(a.Code)]]; s != nil && s.attached[rs.resource] {
			b.grantLocked(rs, pg, s)
			notify = append(notify, s.notify)
		}
	case MethodPairingOffer:
		// Recorded before the page is listed: a page sends its standing
		// offer when a bridge greets it, which can arrive before the
		// tools/list answer is processed. pair checks the page is listed.
		b.offerLocked(rs, pg, m.Params)
	case MethodRevoke:
		var r struct {
			Session string `json:"session"`
		}
		_ = json.Unmarshal(m.Params, &r)
		for _, sid := range sortedKeys(pg.allowed) {
			if s := b.sessions[sid]; r.Session == "" || (s != nil && s.handle == r.Session) {
				b.revokeLocked(rs, pg, sid)
			}
		}
	}
	b.mu.Unlock()
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
	if pg != nil {
		for _, sid := range sortedKeys(pg.allowed) {
			b.revokeLocked(rs, pg, sid)
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
		params["_meta"] = map[string]any{"progressToken": callID}
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
			if s.attached[rs.resource] && !pg.allowed[sid] {
				asks = append(asks, PairingRequest{Session: s.handle, Subject: s.subject, Client: s.client, SameSubject: s.subject == pg.peer.Subject})
			}
		}
	}
	b.mu.Unlock()

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
	if t.Name == "" || slices.Contains([]string{"attach", "detach", "list_pages"}, t.Name) {
		return "the name is reserved by the bridge"
	}
	schema, _ := t.InputSchema.(map[string]any)
	if props, ok := schema["properties"].(map[string]any); ok {
		if _, ok := props["page"]; ok {
			return "the bridge adds a property named page to every tool, so a tool may not declare one"
		}
	}
	readOnly, _ := t.Annotations["readOnlyHint"].(bool)
	key := string(mustJSON(t.InputSchema)) + fmt.Sprint(readOnly)
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
		if !offered[name] {
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

	if b.srv != nil {
		reg := b.srv.Registry()
		for _, t := range add {
			if offered[t.Name] {
				_ = reg.AddTool(b.bridgeDef(t), b.pageToolHandler(t.Name))
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
// optional page property and the scope its kind needs.
func (b *Bridge) bridgeDef(t core.ToolDef) core.ToolDef {
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
	props["page"] = map[string]any{"type": "string", "description": "Page id from list_pages. Omit to use the page paired with this session."}
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

		rs, target, nt := b.target(tc.SessionID(), name, page)
		if nt != nil {
			res := core.ErrorResult(nt.reason)
			res.StructuredContent = map[string]any{"error": nt.reason, "pages": nt.roster}
			return res, nil
		}

		params := map[string]any{"name": name, "arguments": args}
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

// target picks the one page a call goes to:
//  1. page, when the agent named one;
//  2. else the page paired with this session in that room;
//  3. else, when one page in the room has agreed to this session, that page;
//  4. else nothing, and the error carries the roster.
//
// Only rooms this session attached to are searched, and only pages that offer
// the tool.
func (b *Bridge) target(sid, name string, page PeerID) (*roomState, PeerID, *errNoTarget) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sid]
	if s == nil || len(s.attached) == 0 {
		return nil, "", &errNoTarget{reason: "this session is not attached to any room; call attach first", roster: []PageInfo{}}
	}
	roster := b.rosterLocked(s, "")
	type cand struct {
		rs *roomState
		pg *pageState
	}
	var cands []cand
	for _, r := range sortedKeys(s.attached) {
		rs := b.rooms[r]
		if rs == nil {
			continue
		}
		for _, id := range sortedPages(rs) {
			if pg := rs.pages[id]; pg.tools[name] {
				cands = append(cands, cand{rs, pg})
			}
		}
	}
	if page != "" {
		for _, c := range cands {
			if c.pg.peer.ID != page {
				continue
			}
			if !c.pg.allowed[sid] {
				return nil, "", &errNoTarget{reason: fmt.Sprintf("page %s has not agreed to this session; its user can turn on agent access or accept the pairing code from attach", page), roster: roster}
			}
			return c.rs, page, nil
		}
		return nil, "", &errNoTarget{reason: fmt.Sprintf("no page %s offering %s in the rooms this session is attached to", page, name), roster: roster}
	}

	rooms := map[string]bool{}
	for _, c := range cands {
		rooms[c.rs.resource] = true
	}
	switch len(rooms) {
	case 0:
		return nil, "", &errNoTarget{reason: "no page in the attached rooms offers " + name, roster: roster}
	case 1:
	default:
		return nil, "", &errNoTarget{reason: "pages in several attached rooms offer " + name + "; pass page to choose one", roster: roster}
	}
	rs := cands[0].rs
	if paired, ok := s.paired[rs.resource]; ok {
		for _, c := range cands {
			if c.pg.peer.ID == paired {
				return rs, paired, nil
			}
		}
	}
	var allowed []PeerID
	for _, c := range cands {
		if c.pg.allowed[sid] {
			allowed = append(allowed, c.pg.peer.ID)
		}
	}
	switch len(allowed) {
	case 1:
		return rs, allowed[0], nil
	case 0:
		return nil, "", &errNoTarget{reason: "no page offering " + name + " has agreed to this session yet; its user can turn on agent access or accept the pairing code from attach", roster: roster}
	default:
		return nil, "", &errNoTarget{reason: "several pages offer " + name + "; pass page to choose one", roster: roster}
	}
}

// filterToolsList shows a session the bridge's own tools plus the page tools
// of the rooms it attached to. Page tools live in the server's one registry,
// so without this every session would see every room's tools.
func (b *Bridge) filterToolsList(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
	resp, err := next(ctx, req)
	if req.Method != "tools/list" || err != nil || resp == nil || resp.Error != nil || resp.Result == nil {
		return resp, err
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
	b.mu.Lock()
	visible := map[string]bool{}
	if s := b.sessions[sid]; s != nil {
		for r := range s.attached {
			if rs := b.rooms[r]; rs != nil {
				for _, pg := range rs.pages {
					for name := range pg.tools {
						visible[name] = true
					}
				}
			}
		}
	}
	kept := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		var head struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(t, &head)
		if _, isPage := b.defs[head.Name]; !isPage || visible[head.Name] {
			kept = append(kept, t)
		}
	}
	b.mu.Unlock()

	result["tools"] = mustJSON(kept)
	resp.Result = result
	return resp, nil
}
