package rooms

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Reverse pairing: the page shows its user a code, and the user gives it to
// the agent, which enters it with the pair tool. It is the other direction of
// attach's code, which the agent shows and the page accepts. Either way the
// page's user is the one who agreed, and the page hears which agent paired.

const (
	// DefaultPairingCodeTTL is how long a code the page offers stays valid
	// when the page names no lifetime.
	DefaultPairingCodeTTL = 2 * time.Minute

	// MaxPairingCodeTTL caps the lifetime the bridge honours for a code.
	MaxPairingCodeTTL = 10 * time.Minute

	// MaxPairFailuresPerMinute is how many wrong codes one session may enter
	// in a minute before the bridge stops checking its guesses.
	MaxPairFailuresPerMinute = 5

	// pairingAlphabet leaves out 0, O, 1 and I, which a user reading a code
	// aloud mixes up.
	pairingAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// A code the bridge accepts from a page: 6 to 12 characters of the pairing
// alphabet. The page chooses its own code, so this is the floor that keeps a
// careless page from offering one a session could guess.
var pairingCodePattern = regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{6,12}$`)

type pairingOffer struct {
	code    string
	expires time.Time
}

// PairedByCode tells a page that a session entered the code it offered.
// The page's user sees who paired from Subject and Client, both from the
// session's validated token.
type PairedByCode struct {
	PairingRequest
	Code string `json:"code"`
}

// newPairingCode returns an 8-character code from the pairing alphabet.
func newPairingCode() string {
	var b strings.Builder
	max := big.NewInt(int64(len(pairingAlphabet)))
	for range 8 {
		n, _ := rand.Int(rand.Reader, max)
		b.WriteByte(pairingAlphabet[n.Int64()])
	}
	return b.String()
}

// offerLocked records the code a page offers, or withdraws its offer when the
// code is empty. A code that fails pairingCodePattern withdraws too, so a page
// never keeps an old code it believes it replaced.
func (b *Bridge) offerLocked(rs *roomState, pg *pageState, params json.RawMessage) {
	var o struct {
		Code  string `json:"code"`
		TTLMs int64  `json:"ttlMs"`
	}
	_ = json.Unmarshal(params, &o)
	code := strings.ToUpper(o.Code)
	if !pairingCodePattern.MatchString(code) {
		delete(rs.offers, pg.peer.ID)
		return
	}
	ttl := time.Duration(o.TTLMs) * time.Millisecond
	if ttl <= 0 {
		ttl = DefaultPairingCodeTTL
	}
	rs.offers[pg.peer.ID] = pairingOffer{code: code, expires: time.Now().Add(min(ttl, MaxPairingCodeTTL))}
}

// allowGuessLocked reports whether p may try another code, dropping failures
// older than a minute. Guesses are counted per principal, since a stateless
// caller has no session to count them on.
func (b *Bridge) allowGuessLocked(p Principal, now time.Time) bool {
	kept := b.pairFails[p.key()][:0]
	for _, t := range b.pairFails[p.key()] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	b.pairFails[p.key()] = kept
	return len(kept) < MaxPairFailuresPerMinute
}

func (b *Bridge) pair(tc core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
	var a struct {
		Resource string `json:"resource"`
		Code     string `json:"code"`
	}
	_ = json.Unmarshal(req.Arguments, &a)
	code := strings.ToUpper(strings.TrimSpace(a.Code))
	now := time.Now()
	p, _ := b.caller(tc)
	if p.Subject == "" {
		return core.ErrorResult("pair needs an authenticated subject"), nil
	}
	if a.Resource == "" {
		return core.ErrorResult("pair needs the resource whose page shows the code"), nil
	}
	rs, err := b.joinRoom(a.Resource)
	if err != nil {
		return core.ErrorResult("could not join the room for " + a.Resource + ": " + err.Error()), nil
	}
	b.settle(rs)

	b.mu.Lock()
	if !b.allowGuessLocked(p, now) {
		b.mu.Unlock()
		return core.ErrorResult("too many wrong pairing codes from you; wait a minute and ask the page's user for a fresh code"), nil
	}
	var page PeerID
	for _, id := range slices.Sorted(maps.Keys(rs.offers)) {
		o := rs.offers[id]
		if now.After(o.expires) {
			delete(rs.offers, id)
			continue
		}
		if subtle.ConstantTimeCompare([]byte(o.code), []byte(code)) == 1 {
			page = id
		}
	}
	pg := (*pageState)(nil)
	if page != "" {
		pg = rs.pages[page]
	}
	if pg == nil || !pg.listed {
		b.pairFails[p.key()] = append(b.pairFails[p.key()], now)
		b.mu.Unlock()
		return core.ErrorResult("no page in " + a.Resource + " is offering that code; codes are single-use and expire, so ask the page's user for the one it shows now"), nil
	}
	delete(rs.offers, page)
	grantLocked(pg, p)
	note := PairedByCode{
		PairingRequest: PairingRequest{Subject: p.Subject, Client: p.Client, SameSubject: p.Subject == pg.peer.Subject},
		Code:           code,
	}
	var notify []context.Context
	for _, s := range b.notifyPrincipalLocked(p) {
		notify = append(notify, s.notify)
	}
	b.mu.Unlock()

	b.sendTo(rs, page, rpcMessage{Method: MethodPairedByCode, Params: mustJSON(note)})
	for _, ctx := range notify {
		core.Notify(ctx, "notifications/tools/list_changed", nil)
	}
	return core.StructuredResult(fmt.Sprintf("Paired with page %s in %s.", page, a.Resource),
		map[string]any{"resource": a.Resource, "page": page}), nil
}

// OfferPairingCode makes up a code for the page's user to give an agent, which
// enters it with the pair tool, and offers it to every bridge the page has
// heard from, and to each one that greets the page while it is valid. It
// replaces any code offered before. ttl of zero means DefaultPairingCodeTTL;
// bridges honour at most MaxPairingCodeTTL. The code is single-use: once a
// session pairs with it, the page withdraws it everywhere and OnPaired runs.
func (p *Page) OfferPairingCode(ttl time.Duration) string {
	if ttl <= 0 {
		ttl = DefaultPairingCodeTTL
	}
	code := newPairingCode()
	p.mu.Lock()
	p.offer = pairingOffer{code: code, expires: time.Now().Add(ttl)}
	p.mu.Unlock()
	p.notifyBridges(MethodPairingOffer, map[string]any{"code": code, "ttlMs": ttl.Milliseconds()})
	return code
}

// WithdrawPairingCode withdraws the code OfferPairingCode made, if any.
func (p *Page) WithdrawPairingCode() {
	p.mu.Lock()
	p.offer = pairingOffer{}
	p.mu.Unlock()
	p.notifyBridges(MethodPairingOffer, map[string]any{"code": ""})
}

// OnPaired installs a callback for a principal pairing with the page by a
// code, whichever side showed it, which is where the page shows its user which
// agent it was.
func (p *Page) OnPaired(fn func(PairedByCode)) {
	p.mu.Lock()
	p.onPaired = fn
	p.mu.Unlock()
}

// pairedByCode handles a bridge naming the principal behind a code: one the
// page offered (spent now, so withdrawn everywhere) or an agent's code the
// page's user accepted. Any other code is ignored, so a peer that is not a
// bridge cannot grant itself, or anyone, access by claiming a pairing.
func (p *Page) pairedByCode(m rpcMessage) {
	var note PairedByCode
	_ = json.Unmarshal(m.Params, &note)
	code := strings.ToUpper(note.Code)
	now := time.Now()
	p.mu.Lock()
	offered := code != "" && code == p.offer.code && now.Before(p.offer.expires)
	exp, accepted := p.accepted[code]
	accepted = accepted && now.Before(exp)
	if !(offered || accepted) || note.Subject == "" {
		p.mu.Unlock()
		return
	}
	if offered {
		p.offer = pairingOffer{}
	}
	delete(p.accepted, code)
	g := Principal{Subject: note.Subject, Client: note.Client}
	if !slices.Contains(p.grants, g) {
		p.grants = append(p.grants, g)
	}
	fn := p.onPaired
	p.mu.Unlock()
	if offered {
		p.notifyBridges(MethodPairingOffer, map[string]any{"code": ""})
	}
	p.sendGrants()
	if fn != nil {
		fn(note)
	}
}
