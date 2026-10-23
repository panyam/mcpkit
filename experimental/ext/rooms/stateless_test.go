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

// The #1610 acceptance cases, each on both wires with the same answer. The
// bridge declares its page tools (WithPageTools), which is what lets it serve
// the stateless wire.

var sheetSchema = map[string]any{"type": "object", "properties": map[string]any{"sheet": map[string]any{"type": "string"}}}

var catalog = rooms.WithPageTools([]rooms.PageToolDecl{
	{Name: "show_sheet", Description: "Open a sheet", InputSchema: sheetSchema},
	{Name: "get_location", Description: "Where the viewer is", InputSchema: sheetSchema, ReadOnly: true},
})

func eachWire(t *testing.T, fn func(t *testing.T, wire string)) {
	for _, w := range wires {
		t.Run(w, func(t *testing.T) { fn(t, w) })
	}
}

func TestStateless_SixTabsThreeRoomsNoAttach(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
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
		a := st.agentOn(t, wire, "alice", allScopes...)

		ok, out := call(t, a, "show_sheet", map[string]any{"room": roomB, "page": string(tabs["B2"].ID()), "sheet": "power"})
		require.True(t, ok, out)
		assert.Equal(t, []string{"B2:power"}, rec.all())
		assert.Zero(t, st.callsTo(tabs["B1"].ID()))

		ok, out = call(t, a, "show_sheet", map[string]any{"room": roomC, "sheet": "io"})
		require.True(t, ok, out)
		assert.Equal(t, []string{"B2:power", "C1:io"}, rec.all())
	})
}

func TestStateless_DefaultIsTheTabTheUserFocusedLast(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
		rec := &recorder{}
		b1 := st.page(t, "alice", roomB, rec.tool("show_sheet", "B1", false))
		b2 := st.page(t, "alice", roomB, rec.tool("show_sheet", "B2", false))
		a := st.agentOn(t, wire, "alice", allScopes...)

		ok, out := call(t, a, "show_sheet", map[string]any{"room": roomB, "sheet": "x"})
		assert.False(t, ok, "nothing agreed yet")
		assert.Contains(t, out, `"pages":[`, "the error carries the room's roster")

		b1.SetAgentsAllowed(true)
		b2.SetAgentsAllowed(true)
		b1.Focus()
		require.Eventually(t, func() bool {
			ok, _ := call(t, a, "show_sheet", map[string]any{"room": roomB, "sheet": "y"})
			return ok
		}, 3*time.Second, 20*time.Millisecond)
		assert.Equal(t, []string{"B1:y"}, rec.all())
	})
}

func TestStateless_AnotherUsersTabAgreesOnlyByGrant(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
		rec := &recorder{}
		alicesTab := st.page(t, "alice", room, rec.tool("show_sheet", "alice", false))
		alicesTab.SetAgentsAllowed(true)
		bobsTab := st.page(t, "bob", room, rec.tool("show_sheet", "bob", false))
		bobsTab.SetAgentsAllowed(true)
		bob := st.agentOn(t, wire, "bob", allScopes...)

		ok, out := call(t, bob, "show_sheet", map[string]any{"room": room, "sheet": "1"})
		require.True(t, ok, out)
		ok, _ = call(t, bob, "show_sheet", map[string]any{"room": room, "sheet": "2", "page": string(alicesTab.ID())})
		assert.False(t, ok)

		code := bob.attach(t, room).PairingCode
		alicesTab.AcceptPairing(code)
		require.Eventually(t, func() bool {
			ok, _ := call(t, bob, "show_sheet", map[string]any{"room": room, "sheet": "3", "page": string(alicesTab.ID())})
			return ok
		}, 3*time.Second, 20*time.Millisecond)

		alicesTab.Revoke(rooms.Principal{Subject: "bob"})
		require.Eventually(t, func() bool {
			ok, _ := call(t, bob, "show_sheet", map[string]any{"room": room, "sheet": "4", "page": string(alicesTab.ID())})
			return !ok
		}, 3*time.Second, 20*time.Millisecond)
		assert.Equal(t, []string{"bob:1", "alice:3"}, rec.all())
	})
}

func TestStateless_AGrantHoldsAcrossBridges(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st1 := newStack(t, catalog)
		st2 := newStackOn(t, st1.relay, catalog)
		rec := &recorder{}
		p := st1.page(t, "alice", room, rec.tool("show_sheet", "p", false))
		bob1 := st1.agentOn(t, wire, "bob", allScopes...)
		bob2 := st2.agentOn(t, wire, "bob", allScopes...)

		code := bob1.attach(t, room).PairingCode
		ok, out := call(t, bob2, "list_pages", map[string]any{"resource": room})
		require.True(t, ok, out)
		p.AcceptPairing(code)
		require.Eventually(t, func() bool {
			ok, _ := call(t, bob2, "show_sheet", map[string]any{"room": room, "sheet": "via-2"})
			return ok
		}, 3*time.Second, 20*time.Millisecond)
		assert.Equal(t, []string{"p:via-2"}, rec.all())
	})
}

// tools/list is the declared catalog, the same for every caller, and a page
// that registers anything else, or a declared name with another schema, has it
// refused.
func TestStateless_ToolsListIsTheDeclaredCatalog(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
		p := st.page(t, "alice", room,
			(&recorder{}).tool("show_sheet", "p", false),
			rooms.PageTool{Name: "delete_design", InputSchema: map[string]any{"type": "object"}},
			rooms.PageTool{Name: "get_location", InputSchema: map[string]any{"type": "object"}, ReadOnly: true},
		)
		p.SetAgentsAllowed(true)
		a := st.agentOn(t, wire, "alice", allScopes...)
		ok, out := call(t, a, "list_pages", map[string]any{"resource": room})
		require.True(t, ok, out)

		names := a.toolNames(t)
		assert.Contains(t, names, "show_sheet")
		assert.Contains(t, names, "get_location")
		assert.NotContains(t, names, "delete_design", "an undeclared page tool never reaches the list")

		var pages []rooms.PageInfo
		require.Eventually(t, func() bool {
			pages = a.pages(t, room)
			return len(pages) == 1 && len(pages[0].Tools) > 0
		}, 3*time.Second, 20*time.Millisecond)
		assert.Equal(t, []string{"show_sheet"}, pages[0].Tools, "the undeclared tool and the mismatched get_location are refused")
	})
}

// A caller missing pages:act is refused an acting tool with the same
// insufficient_scope challenge on both wires.
func TestStateless_InsufficientScopeOnBothWires(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
		p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
		p.SetAgentsAllowed(true)
		a := st.agentOn(t, wire, "alice", rooms.ScopeRoomsAttach, rooms.ScopePagesRead)
		_, err := a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"room": room, "sheet": "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "challenge asked for: "+rooms.ScopePagesAct)
	})
}

// The page gets the caller's principal on every call, to apply its own
// rules.
func TestStateless_ThePageSeesTheCaller(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, catalog)
		var mu sync.Mutex
		var callers []rooms.Principal
		p := st.page(t, "alice", room, rooms.PageTool{
			Name: "show_sheet", InputSchema: sheetSchema,
			Execute: func(_ context.Context, c rooms.PageCall) (core.ToolResult, error) {
				mu.Lock()
				callers = append(callers, c.Caller)
				mu.Unlock()
				return core.TextResult("ok"), nil
			},
		})
		p.SetAgentsAllowed(true)
		a := st.agentOn(t, wire, "alice", allScopes...)
		st.tokens["alice-"+strings.Join(allScopes, ",")].Extra = map[string]any{"client_id": "cursor"}
		ok, out := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
		require.True(t, ok, out)
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []rooms.Principal{{Subject: "alice", Client: "cursor"}}, callers)
	})
}

// With no declared catalog, a bridge serves page tools to sessions only, and a
// stateless call says how to fix that.
func TestStateless_DynamicRegistryIsSessionWireOnly(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room, (&recorder{}).tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)
	sess := st.agentOn(t, "session", "alice", allScopes...)
	sess.attach(t, room)
	sess.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })

	a := st.agentOn(t, "stateless", "alice", allScopes...)
	ok, out := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "x"})
	assert.False(t, ok)
	assert.Contains(t, out, "WithPageTools")
}

// next_request works with no session, naming its room, and hears only pages
// that agreed to the caller.
func TestStateless_NextRequestWithNoSession(t *testing.T) {
	st := newStack(t, catalog, rooms.WithEventFallbacks(0))
	p := st.page(t, "alice", room)
	p.SetAgentsAllowed(true)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	ok, out := call(t, a, "list_pages", map[string]any{"resource": room})
	require.True(t, ok, out)

	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = p.Ask("from the page")
	}()
	res, err := a.ToolCallFull(t.Context(), "next_request", map[string]any{"resource": room, "wait_seconds": 3})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	b, _ := json.Marshal(res.StructuredContent)
	assert.Contains(t, string(b), "from the page")
}

// A page that trusts only the host's bridges answers no other peer, so a peer
// holding a relay token cannot call its tools directly, past agreement.
func TestStateless_TrustBridgesIgnoresOtherPeers(t *testing.T) {
	st := newStack(t, catalog)
	rec := &recorder{}
	p, err := rooms.JoinPage(t.Context(), st.relay.Transport("alice"), room, rooms.TrustBridges("bridge"))
	require.NoError(t, err)
	t.Cleanup(p.Leave)
	p.RegisterTool(rec.tool("show_sheet", "p", false))
	p.SetAgentsAllowed(true)

	mallory, err := st.relay.Transport("mallory").Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mallory.Leave() })
	direct := `{"jsonrpc":"2.0","id":"x","method":"tools/call","params":{"name":"show_sheet","arguments":{"sheet":"direct"}}}`
	require.NoError(t, mallory.Send(t.Context(), p.ID(), rooms.MessageKind, []byte(direct)))

	a := st.agentOn(t, "stateless", "alice", allScopes...)
	require.Eventually(t, func() bool {
		ok, _ := call(t, a, "show_sheet", map[string]any{"room": room, "sheet": "via-bridge"})
		return ok
	}, 3*time.Second, 20*time.Millisecond)
	assert.Equal(t, []string{"p:via-bridge"}, rec.all(), "the direct call from a non-bridge peer was ignored")
}
