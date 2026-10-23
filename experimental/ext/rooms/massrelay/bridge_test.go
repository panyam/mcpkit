package massrelay_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

// The bridge's acceptance cases from issue 1581, over a real relay: the
// bridge joins in-process, pages dial in with relay tokens, and agents reach
// the bridge over MCP.

// Acceptance 1: a call without page goes to the agreed tab its user focused
// last, alone; naming page 2 runs it there, and the relay delivers nothing to
// page 1.
func TestBridgeOverRelay_CallGoesToTheLastFocusedPageOrTheNamedOne(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p1, calls1 := st.page(t, "alice", rec.tool("show_sheet", "p1"))
	p2, calls2 := st.page(t, "alice", rec.tool("show_sheet", "p2"))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)

	p1.SetAgentsAllowed(true)
	p2.SetAgentsAllowed(true)
	p2.Focus()
	a.waitPaired(t, p2.ID())
	time.Sleep(5 * time.Millisecond)
	p1.Focus()
	a.waitPaired(t, p1.ID())

	res, err := a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s-power"})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))
	res, err = a.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s-io", "page": string(p2.ID())})
	require.NoError(t, err)
	require.False(t, res.IsError, text(res))

	assert.Equal(t, []string{"p1:s-power", "p2:s-io"}, rec.all())
	assert.Equal(t, int32(1), calls1.Load(), "naming page 2 must not reach page 1 on the relay")
	assert.Equal(t, int32(1), calls2.Load())
}

// Acceptance 3: a page that closes mid-call fails the call at once, naming it.
func TestBridgeOverRelay_PageLeavingFailsPendingCallAtOnce(t *testing.T) {
	st := newStack(t, rooms.WithCallTimeout(time.Minute))
	started := make(chan struct{})
	p, _ := st.page(t, "alice", rooms.PageTool{
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
	a.waitPaired(t, p.ID())

	done := make(chan *core.ToolResult, 1)
	go func() {
		res, err := a.ToolCallFull(t.Context(), "slow", map[string]any{})
		if err == nil {
			done <- res
		}
	}()
	<-started
	p.Leave()

	select {
	case res := <-done:
		assert.True(t, res.IsError)
		assert.Contains(t, text(res), string(p.ID()))
		assert.Contains(t, text(res), "left the room")
	case <-time.After(5 * time.Second):
		t.Fatal("the pending call waited instead of failing when its page left")
	}
}

// Acceptance 4: an agent's cancel reaches the page's Execute as a cancelled
// context, across the relay.
func TestBridgeOverRelay_AgentCancelReachesExecute(t *testing.T) {
	st := newStack(t, rooms.WithCallTimeout(time.Minute))
	started, cancelled := make(chan struct{}), make(chan struct{})
	p, _ := st.page(t, "alice", rooms.PageTool{
		Name: "slow",
		Execute: func(ctx context.Context, _ rooms.PageCall) (core.ToolResult, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return core.ToolResult{}, ctx.Err()
		},
	})
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	a.waitPaired(t, p.ID())

	ctx, cancel := context.WithCancel(t.Context())
	go func() { _, _ = a.ToolCallFull(ctx, "slow", map[string]any{}) }()
	<-started
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the agent's cancel never reached the page")
	}
}

// Acceptance 5: a tool registered after the page joined shows up for the agent
// without reattaching.
func TestBridgeOverRelay_ToolRegisteredAfterJoinShowsUp(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p, _ := st.page(t, "alice", rec.tool("show_sheet", "p"))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	names := func() []string {
		tools, err := a.ListTools(t.Context())
		require.NoError(t, err)
		var out []string
		for _, tool := range tools {
			out = append(out, tool.Name)
		}
		return out
	}
	require.Eventually(t, func() bool { return slices.Contains(names(), "show_sheet") }, 5*time.Second, 20*time.Millisecond)

	p.RegisterTool(rec.tool("highlight_net", "p"))
	require.Eventually(t, func() bool { return slices.Contains(names(), "highlight_net") }, 5*time.Second, 20*time.Millisecond)
}

// Acceptance 6, first half: the page's switch pairs only its own user's
// sessions, compared on the subject from the page's relay token.
func TestBridgeOverRelay_SwitchPairsOnlyTheTokensSubject(t *testing.T) {
	st := newStack(t)
	rec := &recorder{}
	p, calls := st.page(t, "alice", rec.tool("show_sheet", "p"))
	p.SetAgentsAllowed(true)
	bob := st.agent(t, "bob", allScopes...)
	bob.attach(t, room)

	require.Eventually(t, func() bool {
		tools, err := bob.ListTools(t.Context())
		return err == nil && slices.ContainsFunc(tools, func(tl core.ToolDef) bool { return tl.Name == "show_sheet" })
	}, 5*time.Second, 20*time.Millisecond)
	res, err := bob.ToolCallFull(t.Context(), "show_sheet", map[string]any{"sheet": "s"})
	require.NoError(t, err)
	assert.True(t, res.IsError, "bob's session is not alice's, so alice's switch must not let it in")
	assert.Empty(t, rec.all())
	assert.Equal(t, int32(0), calls.Load())
}
