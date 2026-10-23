package rooms_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Acceptance 1: a call without page goes to the paired page alone; naming the
// other page runs it there, and the paired page receives nothing.
func TestBridge_CallGoesToPairedPageOrTheNamedOne(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p1 := st.page(t, "alice", room, rec.tool("show_sheet", "p1", false))
	p2 := st.page(t, "alice", room, rec.tool("show_sheet", "p2", false))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)

	// The page that agreed last is the default target.
	p2.SetAgentsAllowed(true)
	a.waitPage(t, p2.ID(), func(p rooms.PageInfo) bool { return p.Paired })
	p1.SetAgentsAllowed(true)
	a.waitPage(t, p1.ID(), func(p rooms.PageInfo) bool { return p.Paired })

	res, err := a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s-power"})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	assert.Equal(t, []string{"p1:s-power"}, rec.all())

	res, err = a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s-io", "page": string(p2.ID())})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	assert.Equal(t, []string{"p1:s-power", "p2:s-io"}, rec.all())
	assert.Equal(t, 1, st.callsTo(p1.ID()), "naming page 2 must not reach page 1 on the relay")
	assert.Equal(t, 1, st.callsTo(p2.ID()))
}

// Acceptance 2: two sessions whose JSON-RPC ids overlap each get their own
// answers. Both clients number requests from the same start.
func TestBridge_OverlappingRequestIDsAcrossSessions(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room, rooms.PageTool{
		Name:        "echo",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"type": "string"}}},
		Execute: func(_ context.Context, call rooms.PageCall) (core.ToolResult, error) {
			var args struct{ V string }
			_ = json.Unmarshal(call.Arguments, &args)
			time.Sleep(5 * time.Millisecond)
			return core.TextResult(args.V), nil
		},
	})
	p.SetAgentsAllowed(true)
	agents := []*agent{st.agent(t, "alice", allScopes...), st.agent(t, "alice", allScopes...)}
	for _, a := range agents {
		a.attach(t, room)
		a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })
	}

	var wg sync.WaitGroup
	errs := make(chan string, 40)
	for i, a := range agents {
		for j := range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				want := fmt.Sprintf("session-%d-call-%d", i, j)
				got, err := a.ToolCall(t.Context(), "echo", map[string]any{"v": want})
				if err != nil || got != want {
					errs <- fmt.Sprintf("want %q, got %q (%v)", want, got, err)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// Acceptance 3: a page that closes mid-call fails the call at once, naming
// the page, long before the deadline.
func TestBridge_PageLeavingFailsPendingCallAtOnce(t *testing.T) {
	st := newStack(t, rooms.WithCallTimeout(time.Minute))
	started := make(chan struct{})
	p := st.page(t, "alice", room, rooms.PageTool{
		Name: "slow",
		Execute: func(ctx context.Context, _ rooms.PageCall) (core.ToolResult, error) {
			close(started)
			<-ctx.Done()
			return core.ToolResult{}, ctx.Err()
		},
	})
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })

	done := make(chan *core.ToolResult, 1)
	go func() {
		res, err := a.ToolCallFull(t.Context(), "slow", map[string]any{})
		require.NoError(t, err)
		done <- res
	}()
	<-started
	p.Leave()

	select {
	case res := <-done:
		assert.True(t, res.IsError)
		assert.Contains(t, text(res), string(p.ID()))
		assert.Contains(t, text(res), "left the room")
	case <-time.After(3 * time.Second):
		t.Fatal("the pending call waited instead of failing when its page left")
	}
}

// A page that never answers fails at the deadline, and the page is told to
// stop.
func TestBridge_DeadlineFailsCallAndCancelsPage(t *testing.T) {
	st := newStack(t, rooms.WithCallTimeout(200*time.Millisecond))
	aborted := make(chan struct{})
	p := st.page(t, "alice", room, rooms.PageTool{
		Name: "stuck",
		Execute: func(ctx context.Context, _ rooms.PageCall) (core.ToolResult, error) {
			<-ctx.Done()
			close(aborted)
			return core.ToolResult{}, ctx.Err()
		},
	})
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })

	res, err := a.ToolCallFull(t.Context(), "stuck", map[string]any{})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), "did not answer within")
	assert.Contains(t, text(res), string(p.ID()))
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("the page's execute was not cancelled at the deadline")
	}
}

// Acceptance 5: a tool registered after the page joined reaches the agent's
// tools/list through list_changed, without reattaching.
func TestBridge_ToolRegisteredAfterJoinShowsUp(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p := st.page(t, "alice", room, rec.tool("show_sheet", "p", false))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	require.Eventually(t, func() bool { return slices.Contains(a.toolNames(t), "show_sheet") }, 3*time.Second, 10*time.Millisecond)

	for len(a.changed) > 0 {
		<-a.changed
	}
	p.RegisterTool(rec.tool("highlight_net", "p", false))
	waitChanged(t, a)
	require.Eventually(t, func() bool { return slices.Contains(a.toolNames(t), "highlight_net") }, 3*time.Second, 10*time.Millisecond)
}

// Acceptance 6, first half: a session acting for someone other than the
// page's user is refused, even with the page's switch on, until the page's
// user accepts the code attach returned.
func TestBridge_DifferentSubjectNeedsPairingCode(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	asked := make(chan rooms.PairingRequest, 4)
	p := st.page(t, "alice", room, rec.tool("show_sheet", "p", false))
	p.OnPairingRequest(func(r rooms.PairingRequest) { asked <- r })
	p.SetAgentsAllowed(true)

	bob := st.agent(t, "bob", allScopes...)
	got := bob.attach(t, room)
	require.NotEmpty(t, got.PairingCode)
	bob.waitPage(t, p.ID(), func(rooms.PageInfo) bool { return true })

	select {
	case r := <-asked:
		assert.Equal(t, "bob", r.Subject)
		assert.False(t, r.SameSubject)
	case <-time.After(3 * time.Second):
		t.Fatal("the page was never told who asked")
	}

	res, err := bob.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s1"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), string(p.ID()), "the refusal carries the roster")
	assert.Empty(t, rec.all())
	assert.Equal(t, 0, st.callsTo(p.ID()))

	p.AcceptPairing(got.PairingCode)
	bob.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })
	res, err = bob.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s1"})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	assert.Equal(t, []string{"p:s1"}, rec.all())
}

// Acceptance 6, second half: without pages:act an acting tool is refused with
// an insufficient_scope challenge; a read-only tool still runs.
func TestBridge_ActingToolNeedsPagesAct(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p := st.page(t, "alice", room, rec.tool("show_sheet", "p", false), rec.tool("get_location", "p", true))
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", rooms.ScopeRoomsAttach, rooms.ScopePagesRead)
	a.attach(t, room)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired && len(pi.Tools) == 2 })

	_, err := a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient")
	assert.Contains(t, err.Error(), rooms.ScopePagesAct)
	assert.Equal(t, 0, st.callsTo(p.ID()))

	res, err := a.ToolCallFull(t.Context(), "get_location", map[string]any{"sheet": "here"})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	assert.Equal(t, []string{"p:here"}, rec.all())
}

func TestBridge_AttachNeedsRoomsAttach(t *testing.T) {
	st := newStack(t)
	a := st.agent(t, "alice", rooms.ScopePagesRead, rooms.ScopePagesAct)
	_, err := a.ToolCallFull(t.Context(), "attach", map[string]any{"resource": room})
	require.Error(t, err)
	assert.Contains(t, err.Error(), rooms.ScopeRoomsAttach)
}

func TestBridge_RefusesPageToolDeclaringPage(t *testing.T) {
	st := newStack(t)
	p := st.page(t, "alice", room,
		rooms.PageTool{Name: "bad", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"page": map[string]any{"type": "string"}}}},
		rooms.PageTool{Name: "good"},
	)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return true })

	names := a.toolNames(t)
	assert.Contains(t, names, "good")
	assert.NotContains(t, names, "bad")
	refused := false
	for _, d := range st.relay.Deliveries() {
		var m struct {
			Method string `json:"method"`
			Params struct{ Name string }
		}
		_ = json.Unmarshal(d.Payload, &m)
		if d.To == p.ID() && m.Method == rooms.MethodToolRefused && m.Params.Name == "bad" {
			refused = true
		}
	}
	assert.True(t, refused, "the page is told its tool was refused")
}

// The first page to register a name fixes its schema; a page offering the same
// name with a different schema has that tool refused.
func TestBridge_RefusesConflictingSchema(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p1 := st.page(t, "alice", room, rec.tool("show_sheet", "p1", false))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	a.waitPage(t, p1.ID(), func(pi rooms.PageInfo) bool { return len(pi.Tools) == 1 })

	p2 := st.page(t, "alice", room, rooms.PageTool{Name: "show_sheet", InputSchema: map[string]any{"type": "object"}})
	a.waitPage(t, p2.ID(), func(rooms.PageInfo) bool { return true })
	for _, pi := range a.pages(t) {
		if pi.Page == p2.ID() {
			assert.Empty(t, pi.Tools)
		}
	}
}

// Each session sees the bridge's tools plus the tools of rooms it attached
// to, and nothing from rooms it did not.
func TestBridge_SessionSeesOnlyAttachedRooms(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	st.page(t, "alice", "review:1", rec.tool("approve", "r", false))
	st.page(t, "alice", room, rec.tool("show_sheet", "d", false))
	a := st.agent(t, "alice", allScopes...)
	b := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	b.attach(t, "review:1")

	require.Eventually(t, func() bool { return slices.Contains(a.toolNames(t), "show_sheet") }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return slices.Contains(b.toolNames(t), "approve") }, 3*time.Second, 10*time.Millisecond)
	assert.NotContains(t, a.toolNames(t), "approve")
	assert.NotContains(t, b.toolNames(t), "show_sheet")
}

// With two pages agreed and neither the default (the paired page left), a
// call without page is refused with the roster rather than sent to a guess.
func TestBridge_AmbiguousTargetCarriesRoster(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p1 := st.page(t, "alice", room, rec.tool("show_sheet", "p1", false))
	p2 := st.page(t, "alice", room, rec.tool("show_sheet", "p2", false))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p1.SetAgentsAllowed(true)
	p2.SetAgentsAllowed(true)
	a.waitPage(t, p1.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })
	a.waitPage(t, p2.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })
	p3 := st.page(t, "alice", room, rec.tool("show_sheet", "p3", false))
	p3.SetAgentsAllowed(true)
	a.waitPage(t, p3.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })
	p3.Leave()
	require.Eventually(t, func() bool { return len(a.pages(t)) == 2 }, 3*time.Second, 10*time.Millisecond)

	res, err := a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), "pass page")
	assert.Contains(t, text(res), string(p1.ID()))
	assert.Contains(t, text(res), string(p2.ID()))
	assert.Empty(t, rec.all())
}

// A pairing ends when the page revokes it or the session ends.
func TestBridge_RevokeAndEndSessionEndPairings(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p := st.page(t, "alice", room, rec.tool("show_sheet", "p", false))
	bob := st.agent(t, "bob", allScopes...)
	code := bob.attach(t, room).PairingCode
	bob.waitPage(t, p.ID(), func(rooms.PageInfo) bool { return true })
	p.AcceptPairing(code)
	bob.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })

	p.Revoke("")
	bob.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return !pi.Allowed && !pi.Paired })
	res, err := bob.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s"})
	require.NoError(t, err)
	assert.True(t, res.IsError)

	p.AcceptPairing(code)
	bob.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })
	st.bridge.EndSession(bob.SessionID())
	res, err = bob.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), "attach first")
	assert.Empty(t, rec.all())
}
