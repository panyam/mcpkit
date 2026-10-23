package rooms_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noted is an agent that records every notifications/resources/updated URI.
type noted struct {
	*client.Client
	mu      sync.Mutex
	updated []string
}

func (st *stack) notedAgent(t *testing.T, subject string) *noted {
	t.Helper()
	token := subject + "-noted"
	st.tokens[token] = &core.Claims{Subject: subject, Scopes: allScopes}
	n := &noted{}
	n.Client = client.NewClient(st.url, core.ClientInfo{Name: "agent-" + subject, Version: "0"},
		client.WithTokenSource(stepUpRefuser{token}),
		client.WithGetSSEStream(),
		client.WithNotificationCallback(func(method string, params any) {
			if method != "notifications/resources/updated" {
				return
			}
			b, _ := json.Marshal(params)
			var p struct {
				URI string `json:"uri"`
			}
			_ = json.Unmarshal(b, &p)
			n.mu.Lock()
			n.updated = append(n.updated, p.URI)
			n.mu.Unlock()
		}),
	)
	require.NoError(t, n.Connect(t.Context()))
	t.Cleanup(func() { n.Close() })
	return n
}

func (n *noted) updates() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.updated...)
}

type nextResult struct {
	Event  *rooms.RecentEvent `json:"event"`
	Cursor string             `json:"cursor"`
}

func nextRequest(t *testing.T, c *client.Client, args map[string]any) (nextResult, *core.ToolResult) {
	t.Helper()
	res, err := c.ToolCallFull(t.Context(), "next_request", args)
	require.NoError(t, err)
	var out nextResult
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)
	return out, res
}

func TestFallbacks_NextRequestReturnsTheNextEventFromAnAgreedPage(t *testing.T) {
	st := newStack(t, rooms.WithEventFallbacks(0))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := st.page(t, "alice", room)
	p.SetAgentsAllowed(true)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = p.Ask("why is SDA flagged?")
	}()
	got, res := nextRequest(t, a.Client, map[string]any{"wait_seconds": 3})
	require.False(t, res.IsError, text(res))
	require.NotNil(t, got.Event, "the ask emitted while the call held should come back")
	assert.Equal(t, rooms.EventAsk, got.Event.Name)
	assert.Equal(t, room, got.Event.Resource)
	assert.Equal(t, p.ID(), got.Event.Page)
	assert.Equal(t, "alice", got.Event.User)
	assert.JSONEq(t, `{"text":"why is SDA flagged?"}`, string(got.Event.Data))
	assert.Equal(t, got.Event.Cursor, got.Cursor)
}

// An event that arrives between two calls is not lost: the second call,
// passing the first call's cursor, gets it at once.
func TestFallbacks_CursorCarriesEventsBetweenCalls(t *testing.T) {
	st := newStack(t, rooms.WithEventFallbacks(0))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	p := st.page(t, "alice", room)
	p.SetAgentsAllowed(true)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Allowed })

	first, _ := nextRequest(t, a.Client, map[string]any{"wait_seconds": 0.1})
	require.Nil(t, first.Event)

	require.NoError(t, p.SelectionChanged(map[string]any{"net": "SDA"}))
	require.NoError(t, p.Emit("viewer.sheet_opened", map[string]any{"sheet": "power"}))
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	second, _ := nextRequest(t, a.Client, map[string]any{"after": first.Cursor, "wait_seconds": 3})
	require.NotNil(t, second.Event)
	assert.Equal(t, rooms.EventSelectionChanged, second.Event.Name)
	assert.Less(t, time.Since(start), time.Second, "a buffered event comes back without waiting")

	third, _ := nextRequest(t, a.Client, map[string]any{"after": second.Cursor, "wait_seconds": 3})
	require.NotNil(t, third.Event)
	assert.Equal(t, "viewer.sheet_opened", third.Event.Name)
}

// bob attached to alice's room, but alice's page never agreed to him, so he
// hears nothing from it, by next_request or by reading the room's resource.
func TestFallbacks_ASessionThePageDidNotAgreeToHearsNothing(t *testing.T) {
	st := newStack(t, rooms.WithEventFallbacks(0))
	bob := st.notedAgent(t, "bob")
	_, err := bob.ToolCallFull(t.Context(), "attach", map[string]any{"resource": room})
	require.NoError(t, err)
	require.NoError(t, bob.SubscribeResource(t.Context(), rooms.EventsURI(room)))
	p := st.page(t, "alice", room)
	p.SetAgentsAllowed(true)
	time.Sleep(100 * time.Millisecond)

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = p.Ask("only for alice's agents")
	}()
	got, res := nextRequest(t, bob.Client, map[string]any{"wait_seconds": 0.6})
	require.False(t, res.IsError, text(res))
	assert.Nil(t, got.Event)

	body, err := bob.ReadResource(t.Context(), rooms.EventsURI(room))
	require.NoError(t, err)
	assert.NotContains(t, body, "only for alice's agents")
	assert.Empty(t, bob.updates(), "no resources/updated for an event the session may not hear")
}

func TestFallbacks_RoomResourceNotifiesSubscribersAndListsEvents(t *testing.T) {
	st := newStack(t, rooms.WithEventFallbacks(0))
	alice := st.notedAgent(t, "alice")
	uri := rooms.EventsURI(room)

	err := alice.SubscribeResource(t.Context(), uri)
	require.Error(t, err, "subscribing before attach is refused")
	assert.Contains(t, err.Error(), "attach")

	res, err := alice.ToolCallFull(t.Context(), "attach", map[string]any{"resource": room})
	require.NoError(t, err)
	b, _ := json.Marshal(res.StructuredContent)
	assert.Contains(t, string(b), `"eventsResource":"`+uri+`"`)
	require.NoError(t, alice.SubscribeResource(t.Context(), uri))

	p := st.page(t, "alice", room)
	p.SetAgentsAllowed(true)
	require.Eventually(t, func() bool {
		r, _ := alice.ToolCallFull(t.Context(), "list_pages", map[string]any{})
		return strings.Contains(text(r), `"allowed":true`)
	}, 3*time.Second, 10*time.Millisecond)

	require.NoError(t, p.Ask("hello"))
	require.Eventually(t, func() bool { return len(alice.updates()) == 1 }, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{uri}, alice.updates())

	body, err := alice.ReadResource(t.Context(), uri)
	require.NoError(t, err)
	var read struct {
		Resource string              `json:"resource"`
		Events   []rooms.RecentEvent `json:"events"`
		Cursor   string              `json:"cursor"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &read))
	assert.Equal(t, room, read.Resource)
	require.Len(t, read.Events, 1)
	assert.Equal(t, rooms.EventAsk, read.Events[0].Name)
	assert.Equal(t, read.Events[0].Cursor, read.Cursor)

	require.NoError(t, alice.UnsubscribeResource(t.Context(), uri))
	require.NoError(t, p.Ask("after unsubscribing"))
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, alice.updates(), 1, "no update after unsubscribe")
}

func TestFallbacks_NextRequestHonoursCancellation(t *testing.T) {
	st := newStack(t, rooms.WithEventFallbacks(0))
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		_, _ = a.ToolCallFull(ctx, "next_request", map[string]any{"wait_seconds": 30})
		close(done)
	}()
	require.Eventually(t, func() bool { return st.bridge.NextRequestWaiting() == 1 }, 3*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.Eventually(t, func() bool { return st.bridge.NextRequestWaiting() == 0 }, 3*time.Second, 10*time.Millisecond,
		"the bridge stops holding a call the agent cancelled")
}

func TestFallbacks_NextRequestNeedsAnAttachedRoom(t *testing.T) {
	st := newStack(t, rooms.WithEventFallbacks(0))
	a := st.agent(t, "alice", allScopes...)
	_, res := nextRequest(t, a.Client, map[string]any{"wait_seconds": 0.1})
	assert.True(t, res.IsError)
	assert.Contains(t, text(res), "attach first")
}

func TestFallbacks_AreOffUnlessAsked(t *testing.T) {
	st := newStack(t)
	a := st.agent(t, "alice", allScopes...)
	assert.NotContains(t, a.toolNames(t), "next_request")
	assert.Contains(t, a.toolNames(t), "pair")
}
