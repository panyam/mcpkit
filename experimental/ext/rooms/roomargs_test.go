package rooms_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/memroom"
)

// designRoom maps agni-style design arguments to rooms, and refuses one it
// can't place, the way a host's mapper might.
func designRoom(args map[string]any) (string, error) {
	d, _ := args["design"].(string)
	switch d {
	case "":
		return "", nil
	case "nowhere":
		return "", errors.New("no design named nowhere")
	}
	return "design:demo/" + d, nil
}

var designArgs = rooms.RoomArgs{"design": {"type": "string", "description": "The design, as the host's tools name it"}}

// The bridge's own tools take the host's room arguments too (#1633), so an
// agent names a room the same way for list_pages as for a page tool.
func TestRoomArgs_ListPagesTakesTheHostsArgument(t *testing.T) {
	for _, wire := range wires {
		t.Run(wire, func(t *testing.T) {
			st := newStack(t, catalog, rooms.WithRoomOf(designRoom, designArgs))
			p := st.page(t, "alice", roomB)
			p.SetAgentsAllowed(true)
			a := st.agentOn(t, wire, "alice", allScopes...)

			byResource := a.pages(t, roomB)
			require.Len(t, byResource, 1)
			var got struct {
				Pages []rooms.PageInfo `json:"pages"`
			}
			res, err := a.ToolCallFull(t.Context(), "list_pages", map[string]any{"design": "b.kicad_sch"})
			require.NoError(t, err)
			require.False(t, res.IsError, text(res))
			b, _ := json.Marshal(res.StructuredContent)
			require.NoError(t, json.Unmarshal(b, &got))
			assert.Equal(t, byResource, got.Pages)
		})
	}
}

// agni's case: a reader's question reaches an agent with no session that
// names the design, not the room's resolved entry.
func TestRoomArgs_NextRequestOnTheStatelessWire(t *testing.T) {
	st := newStack(t, catalog, rooms.WithEventFallbacks(0), rooms.WithRoomOf(designRoom, designArgs))
	p := st.page(t, "alice", roomB)
	p.SetAgentsAllowed(true)
	a := st.agentOn(t, "stateless", "alice", allScopes...)
	ok, out := call(t, a, "list_pages", map[string]any{"resource": roomB})
	require.True(t, ok, out)

	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = p.Ask("from the page")
	}()
	next, res := nextRequest(t, a.Client, map[string]any{"design": "b.kicad_sch", "wait_seconds": 3})
	require.False(t, res.IsError, text(res))
	require.NotNil(t, next.Event)
	assert.Equal(t, roomB, next.Event.Resource)
}

func TestRoomArgs_AttachMakesTheMappedRoomTheDefault(t *testing.T) {
	st := newStack(t, rooms.WithRoomOf(designRoom, designArgs))
	p := st.page(t, "alice", roomB)
	a := st.agent(t, "alice", allScopes...)
	ok, out := call(t, a, "attach", map[string]any{"design": "b.kicad_sch"})
	require.True(t, ok, out)

	pages := a.pages(t)
	require.Len(t, pages, 1)
	assert.Equal(t, p.ID(), pages[0].Page)
	assert.Equal(t, roomB, pages[0].Resource)
}

func TestRoomArgs_AMapperErrorIsTheToolsError(t *testing.T) {
	st := newStack(t, rooms.WithEventFallbacks(0), rooms.WithRoomOf(designRoom, designArgs))
	a := st.agent(t, "alice", allScopes...)
	for _, tool := range []string{"attach", "detach", "list_pages", "pair", "next_request"} {
		ok, out := call(t, a, tool, map[string]any{"design": "nowhere", "code": "Q4RT7ZWN"})
		assert.False(t, ok, tool)
		assert.Contains(t, out, "no design named nowhere", tool)
	}
}

func TestRoomArgs_SchemasCarryThemOnlyWhenDeclared(t *testing.T) {
	schemas := func(opts ...rooms.Option) map[string]map[string]any {
		st := newStack(t, opts...)
		a := st.agent(t, "alice", allScopes...)
		tools, err := a.ListTools(t.Context())
		require.NoError(t, err)
		out := map[string]map[string]any{}
		for _, tool := range tools {
			b, _ := json.Marshal(tool.InputSchema)
			var s map[string]any
			require.NoError(t, json.Unmarshal(b, &s))
			out[tool.Name] = s
		}
		return out
	}
	props := func(s map[string]any) map[string]any { p, _ := s["properties"].(map[string]any); return p }

	with := schemas(rooms.WithEventFallbacks(0), rooms.WithRoomOf(designRoom, designArgs))
	for _, name := range []string{"attach", "detach", "list_pages", "pair", "next_request"} {
		assert.Contains(t, props(with[name]), "design", name)
		assert.Contains(t, props(with[name]), "resource", name)
		required, _ := with[name]["required"].([]any)
		assert.NotContains(t, required, "resource", name)
	}
	assert.Equal(t, []any{"code"}, with["pair"]["required"])

	without := schemas(rooms.WithEventFallbacks(0), rooms.WithRoomOf(designRoom, nil))
	for _, name := range []string{"attach", "list_pages", "next_request"} {
		assert.NotContains(t, props(without[name]), "design", name)
	}
	assert.Equal(t, []any{"resource"}, without["attach"]["required"])
}

func TestRoomArgs_AttachStillNeedsARoom(t *testing.T) {
	st := newStack(t, rooms.WithRoomOf(designRoom, designArgs))
	a := st.agent(t, "alice", allScopes...)
	ok, out := call(t, a, "attach", map[string]any{})
	assert.False(t, ok)
	assert.Contains(t, out, "design")
}

func TestRoomArgs_RegisterRefusesOneNamedLikeTheBridgesOwn(t *testing.T) {
	for _, name := range []string{"resource", "code", "after", "wait_seconds"} {
		b := rooms.New(memroom.New().Transport("bridge"), rooms.WithRoomOf(designRoom, rooms.RoomArgs{name: {"type": "string"}}))
		t.Cleanup(b.Close)
		srv := server.NewServer(core.ServerInfo{Name: "rooms-test", Version: "0"})
		assert.Panics(t, func() { b.Register(srv) }, name)
	}
}

// detach names the room it left, as attach names the one it joined, so a
// caller that attached by the host's argument knows which to stop watching
// (#1640).
func TestRoomArgs_DetachNamesTheRoomItLeft(t *testing.T) {
	st := newStack(t, rooms.WithRoomOf(designRoom, designArgs))
	a := st.agent(t, "alice", allScopes...)
	ok, out := call(t, a, "attach", map[string]any{"design": "b.kicad_sch"})
	require.True(t, ok, out)
	res, err := a.ToolCallFull(t.Context(), "detach", map[string]any{"design": "b.kicad_sch"})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	b, _ := json.Marshal(res.StructuredContent)
	assert.JSONEq(t, `{"resource":"`+roomB+`"}`, string(b))
}
