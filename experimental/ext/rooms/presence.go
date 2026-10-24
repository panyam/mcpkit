package rooms

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"
)

// Presence is one agent a bridge has seen around a page: one that called the
// page (Acting), or attached to the page's room without calling it yet.
// It names a principal and never a session, since sessions come and go and
// the stateless wire has none.
type Presence struct {
	Subject  string    `json:"subject"`
	Client   string    `json:"client,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
	Acting   bool      `json:"acting"`
}

// DefaultPresenceTTL is how long an agent stays present without being seen
// again, on the bridge (which stops reporting it) and on the page (which drops
// it, see WithPresenceTTL).
const DefaultPresenceTTL = time.Hour

// presenceRefresh is how far an agent's last-seen time has to move before a
// bridge reports it again. Within it a repeat call is not news.
const presenceRefresh = 30 * time.Second

func (p Presence) principal() Principal { return Principal{Subject: p.Subject, Client: p.Client} }

// sortPresence orders by subject, then client.
func sortPresence(list []Presence) {
	slices.SortFunc(list, func(a, c Presence) int {
		if n := strings.Compare(a.Subject, c.Subject); n != 0 {
			return n
		}
		return strings.Compare(a.Client, c.Client)
	})
}

// seenLocked records that p was seen at pg, acting or only attached, and
// reports whether the page should hear about it now: p is new to it, has just
// started acting, or was last reported more than presenceRefresh ago.
func seenLocked(pg *pageState, p Principal, acting bool, at time.Time) bool {
	if p.Subject == "" {
		return false
	}
	k := p.key()
	cur, had := pg.presence[k]
	cur.Subject, cur.Client = p.Subject, p.Client
	cur.LastSeen = at
	cur.Acting = cur.Acting || acting
	pg.presence[k] = cur
	last, reported := pg.reported[k]
	return !had || !reported || (cur.Acting && !last.Acting) || at.Sub(last.LastSeen) >= presenceRefresh
}

// presenceListLocked is what the bridge reports to pg: every principal seen
// within DefaultPresenceTTL, which it records as reported.
func presenceListLocked(pg *pageState, now time.Time) []Presence {
	list := []Presence{}
	for k, e := range pg.presence {
		if now.Sub(e.LastSeen) > DefaultPresenceTTL {
			delete(pg.presence, k)
			delete(pg.reported, k)
			continue
		}
		pg.reported[k] = e
		list = append(list, e)
	}
	sortPresence(list)
	return list
}

func (b *Bridge) sendPresence(rs *roomState, page PeerID, list []Presence) {
	b.sendTo(rs, page, rpcMessage{Method: MethodPresence, Params: mustJSON(map[string]any{"agents": list})})
}

// WithPresenceTTL sets how long the page keeps an agent in Presence after a
// bridge last reported seeing it. Default DefaultPresenceTTL.
func WithPresenceTTL(d time.Duration) PageOption {
	return func(p *Page) { p.presenceTTL = d }
}

// OnPresence installs a callback that runs whenever a bridge reports the
// agents it has seen around the page, with the page's merged list (see
// Presence). It is where a page shows its user which agents have used the tab
// and offers to revoke them.
func (p *Page) OnPresence(fn func([]Presence)) {
	p.mu.Lock()
	p.onPresence = fn
	p.mu.Unlock()
}

// Presence returns the agents seen around the page within its presence TTL,
// merged across every bridge in the room: one entry per principal, with the
// latest last-seen time, and Acting if any bridge saw it call. Sorted by
// subject, then client.
func (p *Page) Presence() []Presence {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.presenceLocked(time.Now())
}

func (p *Page) presenceLocked(now time.Time) []Presence {
	list := []Presence{}
	for k, e := range p.presence {
		if now.Sub(e.LastSeen) > p.presenceTTL {
			delete(p.presence, k)
			continue
		}
		list = append(list, e)
	}
	sortPresence(list)
	return list
}

// mergePresence folds one bridge's report into the page's list. A bridge's
// clock is not trusted to be ahead of the page's, so a last-seen in the future
// counts as now.
func (p *Page) mergePresence(params json.RawMessage) {
	var r struct {
		Agents []Presence `json:"agents"`
	}
	if json.Unmarshal(params, &r) != nil {
		return
	}
	now := time.Now()
	p.mu.Lock()
	for _, e := range r.Agents {
		if e.Subject == "" {
			continue
		}
		if e.LastSeen.After(now) {
			e.LastSeen = now
		}
		k := e.principal().key()
		cur, ok := p.presence[k]
		if ok {
			e.Acting = e.Acting || cur.Acting
			if cur.LastSeen.After(e.LastSeen) {
				e.LastSeen = cur.LastSeen
			}
		}
		p.presence[k] = e
	}
	list := p.presenceLocked(now)
	fn := p.onPresence
	p.mu.Unlock()
	if fn != nil {
		fn(list)
	}
}

// seen records p at each of pages in rs and sends each page that should hear
// about it its list. An attach also goes on the room's record, so a page
// listed afterwards hears of it (attachedLocked).
func (b *Bridge) seen(rs *roomState, pages []PeerID, p Principal, acting bool) {
	if p.Subject == "" {
		return
	}
	now := time.Now()
	reports := map[PeerID][]Presence{}
	b.mu.Lock()
	if !acting {
		rs.attachedBy[p.key()] = Presence{Subject: p.Subject, Client: p.Client, LastSeen: now}
	}
	for _, id := range pages {
		if pg := rs.pages[id]; pg != nil && pg.listed && seenLocked(pg, p, acting, now) {
			reports[id] = presenceListLocked(pg, now)
		}
	}
	b.mu.Unlock()
	for _, id := range slices.Sorted(maps.Keys(reports)) {
		b.sendPresence(rs, id, reports[id])
	}
}

// attachedLocked seeds a newly listed page with the agents that attached to
// its room before it was listed, and returns the list to send it, or nil.
func attachedLocked(rs *roomState, pg *pageState, now time.Time) []Presence {
	seeded := false
	for k, e := range rs.attachedBy {
		if now.Sub(e.LastSeen) > DefaultPresenceTTL {
			delete(rs.attachedBy, k)
			continue
		}
		if seenLocked(pg, e.principal(), false, e.LastSeen) {
			seeded = true
		}
	}
	if !seeded {
		return nil
	}
	return presenceListLocked(pg, now)
}
