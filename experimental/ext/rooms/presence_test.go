package rooms_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// presenceOf finds who's entry in p's presence list.
func presenceOf(p *rooms.Page, who rooms.Principal) (rooms.Presence, bool) {
	for _, e := range p.Presence() {
		if e.Subject == who.Subject && e.Client == who.Client {
			return e, true
		}
	}
	return rooms.Presence{}, false
}

// presenceSent is every presence message the relay delivered to page.
func presenceSent(st *stack, page rooms.PeerID) []json.RawMessage {
	var out []json.RawMessage
	for _, d := range st.relay.Deliveries() {
		var m struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(d.Payload, &m)
		if d.To == page && m.Method == rooms.MethodPresence {
			out = append(out, d.Payload)
		}
	}
	return out
}

func TestPresence_ACallShowsTheAgentActing(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
		rec := &recorder{}
		p := st.page(t, "alice", room, rec.tool("show_sheet", "p", false))
		p.SetAgentsAllowed(true)
		heard := make(chan []rooms.Presence, 8)
		p.OnPresence(func(list []rooms.Presence) { heard <- list })
		a := st.agentOn(t, wire, "alice", allScopes...)

		require.Eventually(t, func() bool {
			ok, _ := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
			return ok
		}, 3*time.Second, 20*time.Millisecond)

		select {
		case list := <-heard:
			require.Len(t, list, 1)
			assert.Equal(t, "alice", list[0].Subject)
			assert.True(t, list[0].Acting)
			assert.WithinDuration(t, time.Now(), list[0].LastSeen, 5*time.Second)
		case <-time.After(3 * time.Second):
			t.Fatal("the page never heard who called it")
		}
		e, ok := presenceOf(p, rooms.Principal{Subject: "alice"})
		require.True(t, ok)
		assert.True(t, e.Acting)
	})
}

func TestPresence_AttachWithoutACallIsAttachedNotActing(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
		p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
		a := st.agentOn(t, wire, "bob", allScopes...)
		a.attach(t, room)

		require.Eventually(t, func() bool {
			_, ok := presenceOf(p, rooms.Principal{Subject: "bob"})
			return ok
		}, 3*time.Second, 10*time.Millisecond, "an agent that attached is present before it calls")
		e, _ := presenceOf(p, rooms.Principal{Subject: "bob"})
		assert.False(t, e.Acting, "attached, not acting: bob was never agreed to and never called")
	})
}

func TestPresence_APageListedAfterTheAttachHearsIt(t *testing.T) {
	st := newStack(t, catalog)
	a := st.agent(t, "bob", allScopes...)
	a.attach(t, room)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))

	require.Eventually(t, func() bool {
		_, ok := presenceOf(p, rooms.Principal{Subject: "bob"})
		return ok
	}, 3*time.Second, 10*time.Millisecond)
}

func TestPresence_CarriesNoSessionID(t *testing.T) {
	st := newStack(t, catalog)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	require.Eventually(t, func() bool {
		ok, _ := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	sid := a.SessionID()
	require.NotEmpty(t, sid)
	sent := presenceSent(st, p.ID())
	require.NotEmpty(t, sent)
	for _, m := range sent {
		assert.NotContains(t, string(m), sid)
		assert.NotContains(t, strings.ToLower(string(m)), "session")
	}
}

func TestPresence_RepeatedCallsSendOneReport(t *testing.T) {
	st := newStack(t, catalog)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	require.Eventually(t, func() bool {
		ok, _ := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	for range 4 {
		ok, out := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "y"})
		require.True(t, ok, out)
	}
	assert.Len(t, presenceSent(st, p.ID()), 1, "an agent calling again within the refresh interval is not news")
}

func TestPresence_TwoBridgesMergeByPrincipal(t *testing.T) {
	st1 := newStack(t, catalog)
	st2 := newStackOn(t, st1.relay, catalog)
	p := st1.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a1 := st1.agentOn(t, "stateless", "alice", allScopes...)
	a2 := st2.agentOn(t, "stateless", "alice", allScopes...)

	for _, a := range []*agent{a1, a2} {
		require.Eventually(t, func() bool {
			ok, _ := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
			return ok
		}, 3*time.Second, 20*time.Millisecond)
	}
	require.Eventually(t, func() bool { return len(presenceSent(st1, p.ID())) >= 2 }, 3*time.Second, 10*time.Millisecond, "each bridge reports")
	require.Eventually(t, func() bool { return len(p.Presence()) == 1 }, time.Second, 10*time.Millisecond)
	assert.Len(t, p.Presence(), 1, "one entry per principal, however many bridges report it")
}

func TestPresence_EntriesExpireAfterTheTTL(t *testing.T) {
	st := newStack(t, catalog)
	p, err := rooms.JoinPage(t.Context(), st.relay.Transport("alice"), room, rooms.WithPresenceTTL(200*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(p.Leave)
	p.RegisterTool((&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	require.Eventually(t, func() bool {
		ok, _ := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)

	require.Eventually(t, func() bool { return len(p.Presence()) == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return len(p.Presence()) == 0 }, 3*time.Second, 20*time.Millisecond, "an agent not seen within the TTL is no longer present")
}

func TestPresence_ActingOnOneBridgeOutlivesALaterAttachOnAnother(t *testing.T) {
	st1 := newStack(t, catalog)
	st2 := newStackOn(t, st1.relay, catalog)
	p := st1.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a1 := st1.agentOn(t, "stateless", "alice", allScopes...)
	require.Eventually(t, func() bool {
		ok, _ := call(t, a1, "show_sheet", map[string]any{"room": room, "sheet": "x"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool {
		e, ok := presenceOf(p, rooms.Principal{Subject: "alice"})
		return ok && e.Acting
	}, 3*time.Second, 10*time.Millisecond)

	before := len(presenceSent(st1, p.ID()))
	st2.agentOn(t, "stateless", "alice", allScopes...).attach(t, room)
	require.Eventually(t, func() bool { return len(presenceSent(st1, p.ID())) > before }, 3*time.Second, 10*time.Millisecond, "the second bridge reports the attach")
	e, _ := presenceOf(p, rooms.Principal{Subject: "alice"})
	assert.True(t, e.Acting, "a later attached-only report does not undo acting")
}

func TestPresence_PageKeepsActingTheLatestLastSeenAndItsOwnClock(t *testing.T) {
	st := newStack(t, catalog)
	p := st.page(t, "alice", room)
	bridge2, err := st.relay.Transport("bridge").Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bridge2.Leave() })
	report := func(agents ...rooms.Presence) {
		msg := map[string]any{"jsonrpc": "2.0", "method": rooms.MethodPresence, "params": map[string]any{"agents": agents}}
		b, _ := json.Marshal(msg)
		require.NoError(t, bridge2.Send(t.Context(), p.ID(), rooms.MessageKind, b))
	}
	recent, earlier := time.Now().Add(-time.Second), time.Now().Add(-10*time.Second)
	report(rooms.Presence{Subject: "carol", Client: "cursor", LastSeen: recent, Acting: true})
	require.Eventually(t, func() bool { return len(p.Presence()) == 1 }, 3*time.Second, 10*time.Millisecond)
	report(rooms.Presence{Subject: "carol", Client: "cursor", LastSeen: earlier}, rooms.Presence{Subject: "dave", LastSeen: time.Now().Add(time.Hour)})
	require.Eventually(t, func() bool { return len(p.Presence()) == 2 }, 3*time.Second, 10*time.Millisecond)

	got := p.Presence()
	assert.Equal(t, "carol", got[0].Subject)
	assert.True(t, got[0].Acting)
	assert.WithinDuration(t, recent, got[0].LastSeen, time.Millisecond)
	assert.False(t, got[1].LastSeen.After(time.Now()), "a bridge's future clock counts as now")
}
