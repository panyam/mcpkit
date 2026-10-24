package rooms_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/memroom"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fanCatalog = rooms.WithPageTools([]rooms.PageToolDecl{
	{Name: "where", Description: "What each tab shows", InputSchema: sheetSchema, ReadOnly: true, FanOut: true},
	{Name: "show_sheet", Description: "Open a sheet", InputSchema: sheetSchema},
})

func whereTool(name string) rooms.PageTool {
	return rooms.PageTool{
		Name: "where", Description: "What each tab shows", InputSchema: sheetSchema, ReadOnly: true,
		Execute: func(context.Context, rooms.PageCall) (core.ToolResult, error) {
			return core.TextResult(name + " shows the power sheet"), nil
		},
	}
}

type fanOut struct {
	Results []rooms.FanOutResult `json:"results"`
}

func fanCall(t *testing.T, a *agent, args map[string]any) (*core.ToolResult, fanOut) {
	t.Helper()
	res, err := a.ToolCallFull(t.Context(), "where", args)
	require.NoError(t, err)
	var out fanOut
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)
	return res, out
}

func pagesOf(out fanOut) []rooms.PeerID {
	var ids []rooms.PeerID
	for _, r := range out.Results {
		ids = append(ids, r.Page)
	}
	return ids
}

func TestFanOut_ReachesEveryTabThatAgreedAndNoOther(t *testing.T) {
	eachWire(t, func(t *testing.T, wire string) {
		st := newStack(t, fanCatalog)
		var mine []rooms.PeerID
		for _, n := range []string{"a1", "a2", "a3"} {
			p := st.page(t, "alice", room, whereTool(n))
			p.SetAgentsAllowed(true)
			mine = append(mine, p.ID())
		}
		bobs := st.page(t, "bob", room, whereTool("b1"))
		bobs.SetAgentsAllowed(true)
		a := st.agentOn(t, wire, "alice", allScopes...)

		var out fanOut
		require.Eventually(t, func() bool {
			_, out = fanCall(t, a, map[string]any{"room": room})
			return len(out.Results) == 3
		}, 3*time.Second, 20*time.Millisecond)
		assert.ElementsMatch(t, mine, pagesOf(out))
		for _, r := range out.Results {
			assert.Equal(t, "alice", r.User)
			assert.Empty(t, r.Error)
			require.NotNil(t, r.Result)
			assert.Contains(t, r.Result.Content[0].Text, "shows the power sheet")
		}
		assert.Zero(t, st.callsTo(bobs.ID()), "bob's tab never agreed to alice")
	})
}

func TestFanOut_ASlowTabDoesNotHoldTheOthers(t *testing.T) {
	const deadline = 500 * time.Millisecond
	st := newStack(t, fanCatalog, rooms.WithCallTimeout(deadline))
	fast := st.page(t, "alice", room, whereTool("fast"))
	fast.SetAgentsAllowed(true)
	hang := rooms.PageTool{
		Name: "where", Description: "What each tab shows", InputSchema: sheetSchema, ReadOnly: true,
		Execute: func(ctx context.Context, _ rooms.PageCall) (core.ToolResult, error) {
			<-ctx.Done()
			return core.ToolResult{}, ctx.Err()
		},
	}
	slow1, slow2 := st.page(t, "alice", room, hang), st.page(t, "alice", room, hang)
	slow1.SetAgentsAllowed(true)
	slow2.SetAgentsAllowed(true)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	require.Eventually(t, func() bool { return allowedIn(t, a, room) == 3 }, 3*time.Second, 20*time.Millisecond)

	start := time.Now()
	res, out := fanCall(t, a, map[string]any{"room": room})
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 2*deadline-100*time.Millisecond, "each tab has its own deadline, run side by side, so two slow tabs cost one deadline")
	assert.False(t, res.IsError, "one tab answered, so the call as a whole did not fail")
	byPage := map[rooms.PeerID]rooms.FanOutResult{}
	for _, r := range out.Results {
		byPage[r.Page] = r
	}
	require.Len(t, byPage, 3)
	assert.NotNil(t, byPage[fast.ID()].Result)
	assert.Contains(t, byPage[slow1.ID()].Error, "did not answer")
	assert.Contains(t, byPage[slow2.ID()].Error, "did not answer")
}

// allowedIn counts the pages in resource that agreed to the agent.
func allowedIn(t *testing.T, a *agent, resource string) int {
	n := 0
	for _, p := range a.pages(t, resource) {
		if p.Allowed {
			n++
		}
	}
	return n
}

func TestFanOut_TargetsNarrowsToTheNamedUsers(t *testing.T) {
	st := newStack(t, fanCatalog)
	alices := st.page(t, "alice", room, whereTool("alice"))
	alices.SetAgentsAllowed(true)
	bobs := st.page(t, "bob", room, whereTool("bob"))
	a := st.agent(t, "alice", allScopes...)
	bobs.AcceptPairing(a.attach(t, room).PairingCode)
	a.waitPage(t, bobs.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })

	_, out := fanCall(t, a, map[string]any{"room": room})
	assert.ElementsMatch(t, []rooms.PeerID{alices.ID(), bobs.ID()}, pagesOf(out))

	_, out = fanCall(t, a, map[string]any{"room": room, "targets": map[string]any{"users": []string{"bob"}}})
	require.Equal(t, []rooms.PeerID{bobs.ID()}, pagesOf(out))
	assert.Equal(t, "bob", out.Results[0].User)
}

func TestFanOut_OverTheLimitIsRefusedNotTruncated(t *testing.T) {
	st := newStack(t, fanCatalog, rooms.WithFanOutLimit(2))
	var tabs []*rooms.Page
	for _, n := range []string{"a1", "a2", "a3"} {
		p := st.page(t, "alice", room, whereTool(n))
		p.SetAgentsAllowed(true)
		tabs = append(tabs, p)
	}
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	require.Eventually(t, func() bool { return allowedIn(t, a, room) == 3 }, 3*time.Second, 20*time.Millisecond)

	res, _ := fanCall(t, a, map[string]any{"room": room})
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), "targets")
	for _, p := range tabs {
		assert.Zero(t, st.callsTo(p.ID()))
	}
}

func TestFanOut_ToolsShowTargetsAndNoPage(t *testing.T) {
	st := newStack(t, fanCatalog)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	tools, err := a.ListTools(t.Context())
	require.NoError(t, err)
	for _, tool := range tools {
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(raw, &schema))
		switch tool.Name {
		case "where":
			assert.Contains(t, schema.Properties, "targets")
			assert.NotContains(t, schema.Properties, "page")
		case "show_sheet":
			assert.Contains(t, schema.Properties, "page")
			assert.NotContains(t, schema.Properties, "targets")
		}
	}
}

func TestFanOut_MustBeReadOnly(t *testing.T) {
	b := rooms.New(nil, rooms.WithPageTools([]rooms.PageToolDecl{{Name: "move_everyone", FanOut: true}}))
	srv := server.NewServer(core.ServerInfo{Name: "t", Version: "0"})
	assert.PanicsWithValue(t, `rooms: WithPageTools: tool "move_everyone": a fan-out tool must be read-only; the host sends one-to-many changes with Bridge.Notify`, func() { b.Register(srv) })
}

// notified collects the page notifications a page's OnNotify handler hears.
type notified struct {
	mu  sync.Mutex
	got []string
}

func (n *notified) on(p *rooms.Page, tab, name string) {
	p.OnNotify(name, func(data json.RawMessage) {
		n.mu.Lock()
		n.got = append(n.got, tab+":"+string(data))
		n.mu.Unlock()
	})
}

func (n *notified) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.got...)
}

func TestNotify_ReachesTheSelectedUsersTabsAcrossRooms(t *testing.T) {
	st := newStack(t, fanCatalog)
	n := &notified{}
	n.on(st.page(t, "alice", roomA, whereTool("a")), "alice-A", "design.changed")
	n.on(st.page(t, "bob", roomA, whereTool("b")), "bob-A", "design.changed")
	n.on(st.page(t, "alice", roomB, whereTool("a")), "alice-B", "design.changed")

	rep, err := st.bridge.Notify(t.Context(), rooms.Selector{Rooms: []string{roomA, roomB}, Users: []string{"alice"}}, "design.changed", map[string]any{"rev": 7})
	require.NoError(t, err)
	assert.Len(t, rep.Sent, 2)
	require.Eventually(t, func() bool { return len(n.all()) == 2 }, 3*time.Second, 10*time.Millisecond)
	assert.ElementsMatch(t, []string{`alice-A:{"rev":7}`, `alice-B:{"rev":7}`}, n.all())

	rep, err = st.bridge.Notify(t.Context(), rooms.Selector{}, "design.changed", nil)
	require.NoError(t, err)
	assert.Len(t, rep.Sent, 3, "no rooms named is every room the bridge has joined, and no users is everyone")
}

func TestNotify_RefusesBadNamesAndOversizedData(t *testing.T) {
	st := newStack(t)
	_, err := st.bridge.Notify(t.Context(), rooms.Selector{}, "Not A Name", nil)
	assert.Error(t, err)
	_, err = st.bridge.Notify(t.Context(), rooms.Selector{}, "design.changed", string(make([]byte, 17<<10)))
	assert.Error(t, err)
}

func TestNotify_TrustBridgesIgnoresOtherPeers(t *testing.T) {
	st := newStack(t)
	p, err := rooms.JoinPage(t.Context(), st.relay.Transport("alice"), room, rooms.TrustBridges("bridge"))
	require.NoError(t, err)
	t.Cleanup(p.Leave)
	p.RegisterTool(whereTool("p"))
	n := &notified{}
	n.on(p, "p", "design.changed")

	mallory, err := st.relay.Transport("mallory").Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mallory.Leave() })
	spoof := `{"jsonrpc":"2.0","method":"` + rooms.MethodNotify + `","params":{"name":"design.changed","data":"spoofed"}}`
	require.NoError(t, mallory.Send(t.Context(), p.ID(), rooms.MessageKind, []byte(spoof)))

	require.Eventually(t, func() bool {
		rep, err := st.bridge.Notify(t.Context(), rooms.Selector{Rooms: []string{room}}, "design.changed", "real")
		return err == nil && len(rep.Sent) == 1
	}, 3*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return len(n.all()) > 0 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, `p:"real"`, n.all()[0])
	assert.NotContains(t, n.all(), `p:"spoofed"`)
}

// sendCounter wraps the bridge's transport and records each notification Send
// and every SendEach its rooms make. The bridge's own traffic to new tabs
// (listing, grants, presence) goes by Send and isn't counted. drop, when set, is a peer SendEach reports as not in the
// room without delivering to it.
type sendCounter struct {
	rooms.RoomTransport
	mu       sync.Mutex
	sends    int
	sendEach [][]rooms.PeerID
	drop     rooms.PeerID
}

func (c *sendCounter) Join(ctx context.Context, room string) (rooms.Room, error) {
	r, err := c.RoomTransport.Join(ctx, room)
	if err != nil {
		return nil, err
	}
	return &countedRoom{Room: r, c: c}, nil
}

func (c *sendCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends, c.sendEach = 0, nil
}

type countedRoom struct {
	rooms.Room
	c *sendCounter
}

func (r *countedRoom) Send(ctx context.Context, to rooms.PeerID, kind string, payload json.RawMessage) error {
	if strings.Contains(string(payload), rooms.MethodNotify) {
		r.c.mu.Lock()
		r.c.sends++
		r.c.mu.Unlock()
	}
	return r.Room.Send(ctx, to, kind, payload)
}

func (r *countedRoom) SendEach(ctx context.Context, to []rooms.PeerID, kind string, payload json.RawMessage) error {
	r.c.mu.Lock()
	r.c.sendEach = append(r.c.sendEach, append([]rooms.PeerID(nil), to...))
	drop := r.c.drop
	r.c.mu.Unlock()
	var keep []rooms.PeerID
	for _, id := range to {
		if id != drop {
			keep = append(keep, id)
		}
	}
	if err := r.Room.SendEach(ctx, keep, kind, payload); err != nil || len(keep) == len(to) {
		return err
	}
	return &rooms.NotMembersError{Peers: []rooms.PeerID{drop}}
}

// massrelay v0.3 addresses one AppMessage to several peers, so a notification
// to many tabs costs the relay, and the dialled transport's pacing, one
// message per room rather than one per tab.
func TestNotify_SendsOneMessagePerRoom(t *testing.T) {
	relay := memroom.New()
	c := &sendCounter{RoomTransport: relay.Transport("bridge")}
	st := newStackVia(t, relay, c, fanCatalog)
	n := &notified{}
	n.on(st.page(t, "alice", roomA, whereTool("a")), "alice-A", "design.changed")
	n.on(st.page(t, "bob", roomA, whereTool("b")), "bob-A", "design.changed")
	n.on(st.page(t, "carol", roomA, whereTool("c")), "carol-A", "design.changed")
	n.on(st.page(t, "alice", roomB, whereTool("a")), "alice-B", "design.changed")
	c.reset()

	rep, err := st.bridge.Notify(t.Context(), rooms.Selector{Rooms: []string{roomA, roomB}}, "design.changed", map[string]any{"rev": 8})
	require.NoError(t, err)
	assert.Len(t, rep.Sent, 4)
	require.Eventually(t, func() bool { return len(n.all()) == 4 }, 3*time.Second, 10*time.Millisecond)

	c.mu.Lock()
	defer c.mu.Unlock()
	assert.Zero(t, c.sends, "no tab is sent to on its own")
	require.Len(t, c.sendEach, 2, "one message per room")
	assert.Len(t, c.sendEach[0], 3)
	assert.Len(t, c.sendEach[1], 1)
}

// A tab the relay says has gone fails on its own, and the rest of its room
// still counts as sent.
func TestNotify_AGoneTabFailsAloneInItsRoom(t *testing.T) {
	relay := memroom.New()
	c := &sendCounter{RoomTransport: relay.Transport("bridge")}
	st := newStackVia(t, relay, c, fanCatalog)
	gone := st.page(t, "alice", roomA, whereTool("a"))
	st.page(t, "bob", roomA, whereTool("b"))
	c.mu.Lock()
	c.drop = gone.ID()
	c.mu.Unlock()

	rep, err := st.bridge.Notify(t.Context(), rooms.Selector{Rooms: []string{roomA}}, "design.changed", nil)
	require.NoError(t, err)
	require.Len(t, rep.Failed, 1)
	assert.Equal(t, gone.ID(), rep.Failed[0].Page)
	var nm *rooms.NotMembersError
	require.True(t, errors.As(rep.Failed[0].Err, &nm))
	assert.Equal(t, []rooms.PeerID{gone.ID()}, nm.Peers)
	require.Len(t, rep.Sent, 1)
	assert.Equal(t, "bob", rep.Sent[0].User)
}
