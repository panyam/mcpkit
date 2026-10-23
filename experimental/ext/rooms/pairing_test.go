package rooms_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pairCall(t *testing.T, a *agent, code string) *core.ToolResult {
	t.Helper()
	res, err := a.ToolCallFull(t.Context(), "pair", map[string]any{"resource": room, "code": code})
	require.NoError(t, err)
	return res
}

// pagedFor waits until list_pages lists page for a, so the bridge has
// greeted the page and will hear its offers.
func pagedFor(t *testing.T, a *agent, page rooms.PeerID) {
	t.Helper()
	a.waitPage(t, page, func(rooms.PageInfo) bool { return true })
}

// offered waits until the relay delivered page's offer of code to a bridge.
// Retrying pair until it works would spend the session's guesses.
func offered(t *testing.T, st *stack, page rooms.PeerID, code string) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, d := range st.relay.Deliveries() {
			var m struct {
				Method string `json:"method"`
				Params struct {
					Code string `json:"code"`
				} `json:"params"`
			}
			_ = json.Unmarshal(d.Payload, &m)
			if d.From == page && m.Method == rooms.MethodPairingOffer && m.Params.Code == code {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "the page's offer never reached a bridge")
	time.Sleep(20 * time.Millisecond) // the bridge's receive loop records it
}

// alice's page shows a code; bob, a different user, enters it and can call
// the page, and the page hears that it was bob's agent.
func TestPairing_AgentEntersTheCodeThePageShows(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p := st.page(t, "alice", room, rec.tool("show_sheet", "p", false))
	var mu sync.Mutex
	var paired []rooms.PairedByCode
	p.OnPaired(func(n rooms.PairedByCode) {
		mu.Lock()
		paired = append(paired, n)
		mu.Unlock()
	})
	bob := st.agent(t, "bob", allScopes...)
	st.tokens["bob-"+strings.Join(allScopes, ",")].Extra = map[string]any{"client_id": "claude-code"}
	bob.attach(t, room)
	pagedFor(t, bob, p.ID())

	code := p.OfferPairingCode(0)
	assert.Regexp(t, `^[A-HJ-NP-Z2-9]{8}$`, code)
	offered(t, st, p.ID(), code)
	res0 := pairCall(t, bob, code)
	require.False(t, res0.IsError, text(res0))

	res, err := bob.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "power"})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	assert.Equal(t, []string{"p:power"}, rec.all())

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(paired) == 1
	}, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, "bob", paired[0].Subject, "the page's user sees which user's agent paired")
	assert.Equal(t, "claude-code", paired[0].Client, "and which agent client, from its token")
	assert.False(t, paired[0].SameSubject)
	assert.Equal(t, code, paired[0].Code)
}

// rawOfferPage is a page that answers tools/list and offers a code by hand,
// and never withdraws it, so only the bridge stands between a spent code and
// its reuse.
func rawOfferPage(t *testing.T, st *stack, subject, code string) rooms.PeerID {
	t.Helper()
	r, err := st.relay.Transport(subject).Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Leave() })
	go func() {
		for ev := range r.Events() {
			if ev.Type != rooms.MessageReceived || ev.Kind != rooms.MessageKind {
				continue
			}
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(ev.Payload, &m)
			if m.Method != "tools/list" {
				continue
			}
			reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"tools": []any{}}})
			_ = r.Send(context.Background(), ev.Peer.ID, rooms.MessageKind, reply)
			offer, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": rooms.MethodPairingOffer, "params": map[string]any{"code": code}})
			_ = r.Send(context.Background(), ev.Peer.ID, rooms.MessageKind, offer)
		}
	}()
	return r.Self()
}

func TestPairing_CodeIsSingleUse(t *testing.T) {
	st := newStack(t)
	bob := st.agent(t, "bob", allScopes...)
	carol := st.agent(t, "carol", allScopes...)
	bob.attach(t, room)
	carol.attach(t, room)
	const code = "K7Q2M9XD"
	page := rawOfferPage(t, st, "alice", code)
	pagedFor(t, bob, page)
	offered(t, st, page, code)

	res := pairCall(t, bob, code)
	require.False(t, res.IsError, text(res))
	res = pairCall(t, carol, code)
	assert.True(t, res.IsError, "a code works once, even when the page never withdraws it")
	assert.Contains(t, text(res), "single-use")
}

// A page's code is shown to its user; the bridge refuses one short enough to
// guess, so the page's choice can't weaken the rate limit.
func TestPairing_ShortCodesAreNotAccepted(t *testing.T) {
	st := newStack(t)
	bob := st.agent(t, "bob", allScopes...)
	bob.attach(t, room)
	page := rawOfferPage(t, st, "alice", "ABC")
	pagedFor(t, bob, page)
	offered(t, st, page, "ABC")
	assert.True(t, pairCall(t, bob, "ABC").IsError)
}

func TestPairing_WrongExpiredAndWithdrawnCodesAreRefused(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room)
	bob := st.agent(t, "bob", allScopes...)
	bob.attach(t, room)
	pagedFor(t, bob, p.ID())

	assert.True(t, pairCall(t, bob, "WRONG234").IsError)

	code := p.OfferPairingCode(150 * time.Millisecond)
	offered(t, st, p.ID(), code)
	time.Sleep(300 * time.Millisecond)
	assert.True(t, pairCall(t, bob, code).IsError, "an expired code is refused")

	code = p.OfferPairingCode(0)
	offered(t, st, p.ID(), code)
	p.WithdrawPairingCode()
	time.Sleep(100 * time.Millisecond)
	assert.True(t, pairCall(t, bob, code).IsError, "a withdrawn code is refused")
}

// A session that keeps guessing is cut off before it can try the right code.
func TestPairing_GuessesAreRateLimited(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room)
	bob := st.agent(t, "bob", allScopes...)
	bob.attach(t, room)
	pagedFor(t, bob, p.ID())
	code := p.OfferPairingCode(0)
	offered(t, st, p.ID(), code)

	for range rooms.MaxPairFailuresPerMinute {
		require.True(t, pairCall(t, bob, "AAAAAAAA").IsError)
	}
	res := pairCall(t, bob, code)
	assert.True(t, res.IsError, "the right code is not checked once the session is over its limit")
	assert.Contains(t, text(res), "too many wrong pairing codes")
}

// bob is attached, but to another room, so the code for this one is refused.
func TestPairing_NeedsAttach(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room)
	bob := st.agent(t, "bob", allScopes...)
	bob.attach(t, "design:demo/other.kicad_sch")
	code := p.OfferPairingCode(0)
	res := pairCall(t, bob, code)
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), "attach first")
}

// A bridge that greets the page after the code was made still gets it.
func TestPairing_LateBridgeGetsTheStandingOffer(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	code := p.OfferPairingCode(0)
	bob := st.agent(t, "bob", allScopes...)
	bob.attach(t, room)
	offered(t, st, p.ID(), code)
	res := pairCall(t, bob, code)
	require.False(t, res.IsError, text(res))
}
