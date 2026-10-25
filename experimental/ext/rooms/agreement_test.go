package rooms_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	roomA = "design:demo/a.kicad_sch"
	roomB = "design:demo/b.kicad_sch"
	roomC = "design:demo/c.kicad_sch"
)

func call(t *testing.T, a *agent, name string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := a.ToolCallFull(t.Context(), name, args)
	require.NoError(t, err)
	return !res.IsError, text(res)
}

// #1610's case: six tabs across three designs, and an agent that names the
// design on each call, with no attach.
func TestAgreement_TheRoomTravelsOnTheCall(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	tabs := map[string]*rooms.Page{}
	for _, r := range []struct{ name, room string }{
		{"A1", roomA}, {"A2", roomA}, {"B1", roomB}, {"B2", roomB}, {"C1", roomC}, {"C2", roomC},
	} {
		p := st.page(t, "alice", r.room, rec.tool("show_sheet", r.name, false))
		p.SetAgentsAllowed(true)
		tabs[r.name] = p
	}
	tabs["C1"].Focus()
	a := st.agent(t, "alice", allScopes...)

	ok, out := call(t, a, "list_pages", map[string]any{"resource": roomB})
	require.True(t, ok, out)
	require.Eventually(t, func() bool {
		ok, _ := call(t, a, "show_sheet", map[string]any{"room": roomB, "page": string(tabs["B2"].ID()), "sheet": "power"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"B2:power"}, rec.all(), "only B2 ran it")
	assert.Zero(t, st.callsTo(tabs["B1"].ID()), "B1 saw nothing on the relay")

	ok, out = call(t, a, "show_sheet", map[string]any{"room": roomC, "sheet": "io"})
	require.True(t, ok, out)
	assert.Equal(t, []string{"B2:power", "C1:io"}, rec.all(), "room C's call reaches the tab alice focused, with no attach")
}

// A call that names no room, on a session with no default room, says to name one.
func TestAgreement_NoRoomAndNoDefaultAsksForTheRoom(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", allScopes...)
	_, err := a.ToolCallFull(t.Context(), "list_pages", map[string]any{"resource": room})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		ok, _ := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	ok, out := call(t, a, "show_sheet", map[string]any{"sheet": "x"})
	assert.False(t, ok)
	assert.Contains(t, out, "name the room")
}

// Two users, one tab each, in one room. bob's calls go to his own tab, never
// alice's, until her page accepts his code, and not after she revokes it.
func TestAgreement_AnotherUsersTabNeedsHerAgreement(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	alicesTab := st.page(t, "alice", room, rec.tool("show_sheet", "alice", false))
	alicesTab.SetAgentsAllowed(true)
	bobsTab := st.page(t, "bob", room, rec.tool("show_sheet", "bob", false))
	bobsTab.SetAgentsAllowed(true)
	bob := st.agent(t, "bob", allScopes...)
	code := bob.attach(t, room).PairingCode
	bob.waitPage(t, bobsTab.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed && pi.Default })

	ok, out := call(t, bob, "show_sheet", map[string]any{"sheet": "1"})
	require.True(t, ok, out)
	ok, out = call(t, bob, "show_sheet", map[string]any{"sheet": "2", "page": string(alicesTab.ID())})
	assert.False(t, ok)
	assert.Contains(t, out, "has not agreed to you")
	assert.Equal(t, []string{"bob:1"}, rec.all())

	alicesTab.AcceptPairing(code)
	bob.waitPage(t, alicesTab.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })
	ok, out = call(t, bob, "show_sheet", map[string]any{"sheet": "3", "page": string(alicesTab.ID())})
	require.True(t, ok, out)
	ok, out = call(t, bob, "show_sheet", map[string]any{"sheet": "4"})
	assert.False(t, ok, "two tabs now agree to bob and he focused neither, so a call must name one")
	assert.Contains(t, out, "pass page")

	alicesTab.Revoke(rooms.Principal{Subject: "bob"})
	bob.waitPage(t, alicesTab.ID(), func(pi rooms.PageInfo) bool { return !pi.Allowed })
	ok, _ = call(t, bob, "show_sheet", map[string]any{"sheet": "5", "page": string(alicesTab.ID())})
	assert.False(t, ok)
	assert.Equal(t, []string{"bob:1", "alice:3"}, rec.all())
}

// A grant made through one bridge holds on another bridge in the same room:
// the page sends its grants to every bridge that lists it, so the bridges
// share nothing.
func TestAgreement_AGrantHoldsOnEveryBridgeInTheRoom(t *testing.T) {
	st1 := newStack(t)
	st2 := newStackOn(t, st1.relay)
	rec := &recorder{}
	p := st1.page(t, "alice", room, rec.tool("show_sheet", "p", false))
	bob1 := st1.agent(t, "bob", allScopes...)
	bob2 := st2.agent(t, "bob", allScopes...)

	code := bob1.attach(t, room).PairingCode
	_, err := bob2.ToolCallFull(t.Context(), "list_pages", map[string]any{"resource": room})
	require.NoError(t, err)
	p.AcceptPairing(code)

	require.Eventually(t, func() bool {
		ok, _ := call(t, bob2, "show_sheet", map[string]any{"room": room, "sheet": "via-2"})
		return ok
	}, 3*time.Second, 20*time.Millisecond, "bridge 2 honours the grant bridge 1's code made")
	assert.Equal(t, []string{"p:via-2"}, rec.all())

	// A bridge that joins after the grant hears it when it greets the page.
	// It joins the room through list_pages, since with the dynamic registry
	// a bridge has no page tools until it does (declared tools change that).
	st3 := newStackOn(t, st1.relay)
	bob3 := st3.agent(t, "bob", allScopes...)
	_, err = bob3.ToolCallFull(t.Context(), "list_pages", map[string]any{"resource": room})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		ok, _ := call(t, bob3, "show_sheet", map[string]any{"room": room, "sheet": "via-3"})
		return ok
	}, 5*time.Second, 50*time.Millisecond, "a late bridge gets the page's grants on greet")

	p.Revoke(rooms.Principal{Subject: "bob"})
	require.Eventually(t, func() bool {
		ok, _ := call(t, bob2, "show_sheet", map[string]any{"room": room, "sheet": "after"})
		return !ok
	}, 3*time.Second, 20*time.Millisecond, "and the revoke reaches it too")
}

// Only the page decides who it agrees to. A peer that claims a pairing for a
// code the page never offered or accepted grants nothing.
func TestAgreement_APageIgnoresAPairingItDidNotMake(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	eve := st.agent(t, "eve", allScopes...)
	eve.attach(t, room)
	eve.waitPage(t, p.ID(), func(rooms.PageInfo) bool { return true })

	forged, err := st.relay.Transport("mallory").Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = forged.Leave() })
	note := `{"jsonrpc":"2.0","method":"` + rooms.MethodPairedByCode + `","params":{"subject":"eve","code":"NEVERSEEN"}}`
	require.NoError(t, forged.Send(t.Context(), p.ID(), rooms.MessageKind, []byte(note)))
	time.Sleep(200 * time.Millisecond)

	assert.Empty(t, p.Grants())
	ok, _ := call(t, eve, "show_sheet", map[string]any{"sheet": "x", "page": string(p.ID())})
	assert.False(t, ok)
}

// A host whose tools already name their design maps that to the room, and the
// argument reaches the page unchanged.
func TestAgreement_WithRoomOfMapsTheHostsOwnArgument(t *testing.T) {
	st := newStack(t, rooms.WithRoomOf(func(args map[string]any) (string, error) {
		d, _ := args["design"].(string)
		if d == "" {
			return "", nil
		}
		return "design:demo/" + d, nil
	}, nil))
	var got []string
	tool := rooms.PageTool{
		Name:        "show_sheet",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"design": map[string]any{"type": "string"}, "sheet": map[string]any{"type": "string"}}},
		Execute: func(_ context.Context, c rooms.PageCall) (core.ToolResult, error) {
			got = append(got, string(c.Arguments))
			return core.TextResult("ok"), nil
		},
	}
	p := st.page(t, "alice", roomB, tool)
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", allScopes...)
	_, err := a.ToolCallFull(t.Context(), "list_pages", map[string]any{"resource": roomB})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		ok, _ := call(t, a, "show_sheet", map[string]any{"design": "b.kicad_sch", "sheet": "power"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	require.NotEmpty(t, got)
	assert.JSONEq(t, `{"design":"b.kicad_sch","sheet":"power"}`, got[0], "the host's argument reaches the page as it was sent")
}

// A call made right after pair succeeds works before the page's own grants
// arrive: the bridge records the grant it just made. The page here offers a
// tool and a code by hand and never sends grants, so only that record lets the
// call through.
func TestAgreement_PairWorksBeforeThePagesGrantsArrive(t *testing.T) {
	st := newStack(t)
	const code = "Q4RT7ZWN"
	r, err := st.relay.Transport("alice").Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Leave() })
	ran := make(chan string, 4)
	go func() {
		send := func(to rooms.PeerID, m map[string]any) {
			m["jsonrpc"] = "2.0"
			b, _ := json.Marshal(m)
			_ = r.Send(context.Background(), to, rooms.MessageKind, b)
		}
		for ev := range r.Events() {
			if ev.Type != rooms.MessageReceived || ev.Kind != rooms.MessageKind {
				continue
			}
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(ev.Payload, &m)
			switch m.Method {
			case "tools/list":
				send(ev.Peer.ID, map[string]any{"method": rooms.MethodPairingOffer, "params": map[string]any{"code": code}})
				send(ev.Peer.ID, map[string]any{"id": m.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "show_sheet", "inputSchema": map[string]any{"type": "object"}}}}})
			case "tools/call":
				ran <- "ran"
				send(ev.Peer.ID, map[string]any{"id": m.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}})
			}
		}
	}()
	bob := st.agent(t, "bob", allScopes...)
	ok, out := call(t, bob, "list_pages", map[string]any{"resource": room})
	require.True(t, ok, out)
	offered(t, st, r.Self(), code)

	ok, out = call(t, bob, "pair", map[string]any{"resource": room, "code": code})
	require.True(t, ok, out)
	ok, out = call(t, bob, "show_sheet", map[string]any{"room": room})
	require.True(t, ok, out)
	assert.Len(t, ran, 1)
}
