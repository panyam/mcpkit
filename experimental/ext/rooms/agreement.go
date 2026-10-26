package rooms

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Agreement is held by the page, not the bridge (#1610). A page agrees to a
// principal, the subject from a caller's validated token plus, optionally,
// the client it used, and sends its grants to every bridge that lists it. So
// agreement needs no MCP session, survives the agent reconnecting, and is the
// same on every bridge in the room. It ends when the page revokes it or
// leaves.

// Principal is who a call acts for: the subject of the caller's validated
// token, and the client that made it.
type Principal struct {
	Subject string `json:"subject"`
	Client  string `json:"client,omitempty"`
}

// covers reports whether a grant to g agrees to p. A grant with no client
// covers every client of its subject.
func (g Principal) covers(p Principal) bool {
	return g.Subject == p.Subject && (g.Client == "" || g.Client == p.Client)
}

func (p Principal) key() string { return p.Subject + "\x00" + p.Client }

// DefaultAgentCodeTTL is how long the code attach returns stays acceptable.
// It used to live as long as the MCP session; with no session to end it, it
// expires.
const DefaultAgentCodeTTL = 10 * time.Minute

type agentCode struct {
	principal Principal
	expires   time.Time
}

// agreesLocked reports whether pg agreed to p: by its agents switch for the
// page's own user, or by a grant.
func agreesLocked(pg *pageState, p Principal) bool {
	if p.Subject == "" {
		return false
	}
	if pg.allowAgents && pg.peer.Subject == p.Subject {
		return true
	}
	return slices.ContainsFunc(pg.grants, func(g Principal) bool { return g.covers(p) })
}

// grantLocked adds g to the bridge's copy of pg's grants, ahead of the page's
// own grants arriving, so a call made right after pairing works. The page's
// next grants message replaces the copy.
func grantLocked(pg *pageState, g Principal) {
	if !slices.Contains(pg.grants, g) {
		pg.grants = append(pg.grants, g)
	}
}

// caller resolves the principal behind a tool call, and on the session wire
// the bridge's record of the session, made on first use.
func (b *Bridge) caller(tc core.ToolContext) (Principal, *session) {
	subject, client := b.subjectOf(tc)
	p := Principal{Subject: subject, Client: client}
	sid := tc.SessionID()
	if sid == "" || subject == "" {
		return p, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[sid]
	if s == nil {
		s = &session{id: sid, attached: make(map[string]bool)}
		b.sessions[sid] = s
	}
	s.principal = p
	s.notify = core.DetachForBackground(tc)
	return p, s
}

// agentCodeLocked returns p's code for rs, minting one when it has none or
// the old one expired.
func (b *Bridge) agentCodeLocked(rs *roomState, p Principal) string {
	if code := rs.principalCodes[p.key()]; code != "" {
		if c, ok := rs.codes[code]; ok && time.Now().Before(c.expires) {
			return code
		}
		delete(rs.codes, code)
	}
	code := strings.ToUpper(randomID(3))
	rs.codes[code] = agentCode{principal: p, expires: time.Now().Add(DefaultAgentCodeTTL)}
	rs.principalCodes[p.key()] = code
	return code
}

// settle waits briefly for the pages already in rs when the bridge joined to
// be listed, so a call naming a room the bridge has only just joined finds
// them. A peer that never answers tools/list (another bridge) is not waited
// for past the cap.
func (b *Bridge) settle(rs *roomState) {
	select {
	case <-rs.settled:
	case <-time.After(2 * time.Second):
	}
}

// roomArgs resolves the rooms a call is about: the one it names, or, on the
// session wire, the rooms the session attached to as defaults.
func (b *Bridge) roomArgs(named string, s *session) ([]string, error) {
	if named != "" {
		rs, err := b.joinRoom(named)
		if err != nil {
			return nil, fmt.Errorf("could not join the room for %s: %w", named, err)
		}
		b.settle(rs)
		return []string{named}, nil
	}
	if s != nil {
		b.mu.Lock()
		rooms := sortedKeys(s.attached)
		b.mu.Unlock()
		if len(rooms) > 0 {
			return rooms, nil
		}
	}
	return nil, fmt.Errorf("name the room this call is about with %s", b.roomWords())
}

func (b *Bridge) attach(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resource, err := b.namedRoom(req)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	if resource == "" {
		return core.ErrorResult("attach needs " + b.roomWords()), nil
	}
	p, s := b.caller(tc)
	if p.Subject == "" {
		return core.ErrorResult("attach needs an authenticated subject"), nil
	}
	rs, err := b.joinRoom(resource)
	if err != nil {
		return core.ErrorResult("could not join the room for " + resource + ": " + err.Error()), nil
	}
	b.settle(rs)

	b.mu.Lock()
	if s != nil {
		s.attached[resource] = true
	}
	code := b.agentCodeLocked(rs, p)
	asks := map[PeerID]PairingRequest{}
	for _, id := range sortedPages(rs) {
		if pg := rs.pages[id]; !agreesLocked(pg, p) {
			asks[id] = PairingRequest{Subject: p.Subject, Client: p.Client, SameSubject: pg.peer.Subject == p.Subject}
		}
	}
	roster := b.rosterLocked(p, []string{resource})
	pages := sortedPages(rs)
	b.mu.Unlock()
	b.seen(rs, pages, p, false)

	for _, id := range slices.Sorted(maps.Keys(asks)) {
		b.sendTo(rs, id, rpcMessage{Method: MethodPairingRequested, Params: mustJSON(asks[id])})
	}
	if s != nil {
		core.Notify(s.notify, "notifications/tools/list_changed", nil)
	}

	res := core.TextResult(fmt.Sprintf("Attached to %s. Pages whose user is not you can pair by accepting code %s, or by showing a code you enter with pair.", resource, code))
	out := map[string]any{"resource": resource, "pairingCode": code, "pages": roster}
	if b.fb != nil {
		out["eventsResource"] = EventsURI(resource)
	}
	res.StructuredContent = out
	return res, nil
}

func (b *Bridge) detach(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	resource, err := b.namedRoom(req)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	_, s := b.caller(tc)
	b.mu.Lock()
	if s == nil || !s.attached[resource] {
		b.mu.Unlock()
		return core.ErrorResult("this session has no default room " + resource), nil
	}
	delete(s.attached, resource)
	notify := s.notify
	b.mu.Unlock()
	core.Notify(notify, "notifications/tools/list_changed", nil)
	res := core.TextResult("Detached from " + resource + ". Pages that agreed to you still do; their users revoke that on the page.")
	res.StructuredContent = map[string]any{"resource": resource}
	return res, nil
}

func (b *Bridge) listPages(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	p, s := b.caller(tc)
	named, err := b.namedRoom(req)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	rooms, err := b.roomArgs(named, s)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	b.mu.Lock()
	roster := b.rosterLocked(p, rooms)
	b.mu.Unlock()
	text, _ := json.Marshal(roster)
	res := core.TextResult(string(text))
	out := map[string]any{"pages": roster}
	if b.scopeFilter {
		out["stepUp"] = b.stepUpsFor(tc, p, s)
	}
	res.StructuredContent = out
	return res, nil
}

// defaultPageLocked is the page a call in rs goes to when it names none: the
// tab that agreed to p which p's own user focused last; else the only tab that
// agreed to p. Only pages for which ok holds count.
func defaultPageLocked(rs *roomState, p Principal, ok func(*pageState) bool) (PeerID, int) {
	var agreed []*pageState
	for _, id := range sortedPages(rs) {
		if pg := rs.pages[id]; ok(pg) && agreesLocked(pg, p) {
			agreed = append(agreed, pg)
		}
	}
	var best *pageState
	for _, pg := range agreed {
		if pg.peer.Subject != p.Subject || pg.lastFocus.IsZero() {
			continue
		}
		if best == nil || pg.lastFocus.After(best.lastFocus) {
			best = pg
		}
	}
	if best != nil {
		return best.peer.ID, len(agreed)
	}
	if len(agreed) == 1 {
		return agreed[0].peer.ID, 1
	}
	return "", len(agreed)
}

// rosterLocked lists the listed pages of rooms, sorted by room and then page,
// marked with whether each agreed to p and which one p's calls go to by
// default.
func (b *Bridge) rosterLocked(p Principal, rooms []string) []PageInfo {
	out := []PageInfo{}
	for _, r := range slices.Sorted(slices.Values(rooms)) {
		rs := b.rooms[r]
		if rs == nil {
			continue
		}
		def, _ := defaultPageLocked(rs, p, func(*pageState) bool { return true })
		for _, id := range sortedPages(rs) {
			pg := rs.pages[id]
			info := PageInfo{
				Page: id, Resource: r, User: pg.peer.Subject, Label: pg.label,
				Allowed: agreesLocked(pg, p), Default: id == def, Tools: sortedKeys(pg.tools),
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

// target picks the one page a call to the page tool name goes to, among the
// pages of rooms that offer it:
//  1. page, when the agent named one, if it agreed to p;
//  2. else the tab that agreed to p which p's own user focused last;
//  3. else the only tab that agreed to p;
//  4. else nothing, and the error carries the roster.
func (b *Bridge) target(p Principal, rooms []string, name string, page PeerID) (*roomState, PeerID, *errNoTarget) {
	b.mu.Lock()
	defer b.mu.Unlock()
	roster := b.rosterLocked(p, rooms)
	offers := func(pg *pageState) bool { return pg.tools[name] }

	var withTool []string
	for _, r := range rooms {
		rs := b.rooms[r]
		if rs == nil {
			continue
		}
		for _, id := range sortedPages(rs) {
			if offers(rs.pages[id]) {
				withTool = append(withTool, r)
				break
			}
		}
	}
	if page != "" {
		for _, r := range withTool {
			rs := b.rooms[r]
			if pg := rs.pages[page]; pg != nil && pg.listed && offers(pg) {
				if !agreesLocked(pg, p) {
					return nil, "", &errNoTarget{reason: fmt.Sprintf("page %s has not agreed to you; its user can turn on agent access, accept your pairing code from attach, or show a code you enter with pair", page), roster: roster}
				}
				return rs, page, nil
			}
		}
		return nil, "", &errNoTarget{reason: fmt.Sprintf("no page %s offering %s in %s", page, name, strings.Join(rooms, ", ")), roster: roster}
	}
	switch len(withTool) {
	case 0:
		return nil, "", &errNoTarget{reason: "no page in " + strings.Join(rooms, ", ") + " offers " + name, roster: roster}
	case 1:
	default:
		return nil, "", &errNoTarget{reason: "pages in several rooms offer " + name + "; name the room", roster: roster}
	}
	rs := b.rooms[withTool[0]]
	def, agreed := defaultPageLocked(rs, p, offers)
	switch {
	case def != "":
		return rs, def, nil
	case agreed == 0:
		return nil, "", &errNoTarget{reason: "no page offering " + name + " has agreed to you yet; its user can turn on agent access, accept your pairing code from attach, or show a code you enter with pair", roster: roster}
	default:
		return nil, "", &errNoTarget{reason: "several pages that agreed to you offer " + name + "; pass page to choose one", roster: roster}
	}
}

// visibleToolsLocked is the page tools p may see in a dynamic registry: those
// offered by pages in its default rooms (attached, on the session wire), and by
// any page that agreed to it.
func (b *Bridge) visibleToolsLocked(p Principal, attached map[string]bool) map[string]bool {
	visible := map[string]bool{}
	if p.Subject == "" {
		return visible
	}
	for _, rs := range b.rooms {
		for _, pg := range rs.pages {
			if attached[rs.resource] || agreesLocked(pg, p) {
				for name := range pg.tools {
					visible[name] = true
				}
			}
		}
	}
	return visible
}

// notifyPrincipalLocked collects the notify contexts of the sessions acting
// for p, to tell them their tool list may have changed.
func (b *Bridge) notifyPrincipalLocked(p Principal) []*session {
	var out []*session
	for _, sid := range sortedKeys(b.sessions) {
		if s := b.sessions[sid]; p.covers(s.principal) || s.principal == p {
			out = append(out, s)
		}
	}
	return out
}
