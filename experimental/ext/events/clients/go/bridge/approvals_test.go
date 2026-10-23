package bridge_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/experimental/ext/events/clients/go/bridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func approvalDelivery(payload string) eventsclient.Delivery {
	return eventsclient.Delivery{
		Server:         "ops",
		SubscriptionID: "sub-1",
		Instruction:    "email the on-call",
		Event:          events.Event{EventID: "evt-1", Name: "alert.fired", Data: []byte(payload)},
	}
}

// approveAsync runs Approve on its own goroutine, since it blocks until
// decided, and returns its answer on a channel.
func approveAsync(ctx context.Context, p *bridge.PendingApprover, d eventsclient.Delivery, tools ...string) <-chan bool {
	out := make(chan bool, 1)
	go func() {
		ok, _ := p.Approve(ctx, d, tools)
		out <- ok
	}()
	return out
}

func waitPending(t *testing.T, p *bridge.PendingApprover, n int) []bridge.Approval {
	t.Helper()
	require.Eventually(t, func() bool { return len(p.Pending()) == n }, 2*time.Second, 5*time.Millisecond)
	return p.Pending()
}

func answer(t *testing.T, got <-chan bool) bool {
	t.Helper()
	select {
	case ok := <-got:
		return ok
	case <-time.After(3 * time.Second):
		t.Fatal("Approve never returned")
		return false
	}
}

func TestPendingApprover_WaitsForApprove(t *testing.T) {
	p := &bridge.PendingApprover{Timeout: time.Minute}
	got := approveAsync(t.Context(), p, approvalDelivery(`{"msg":"db down"}`), "mcp__gmail__send")

	pending := waitPending(t, p, 1)
	a := pending[0]
	assert.Equal(t, "ops/alert.fired", a.Event)
	assert.Equal(t, "evt-1", a.EventID)
	assert.Equal(t, "email the on-call", a.Instruction)
	assert.Equal(t, []string{"mcp__gmail__send"}, a.Tools)
	assert.Equal(t, `{"msg":"db down"}`, a.PayloadUntrusted)
	assert.WithinDuration(t, a.RequestedAt.Add(time.Minute), a.ExpiresAt, time.Second)

	require.NoError(t, p.Decide(a.ID, true))
	assert.True(t, answer(t, got))
	assert.Empty(t, p.Pending(), "a decided request is no longer pending")
	assert.ErrorIs(t, p.Decide(a.ID, true), bridge.ErrNoApproval, "a request is decided once")
}

func TestPendingApprover_DenyDenies(t *testing.T) {
	p := &bridge.PendingApprover{Timeout: time.Minute}
	got := approveAsync(t.Context(), p, approvalDelivery("{}"), "x")
	require.NoError(t, p.Decide(waitPending(t, p, 1)[0].ID, false))
	assert.False(t, answer(t, got))
}

func TestPendingApprover_TimesOutAsDenial(t *testing.T) {
	p := &bridge.PendingApprover{Timeout: 50 * time.Millisecond}
	got := approveAsync(t.Context(), p, approvalDelivery("{}"), "x")
	assert.False(t, answer(t, got))
	assert.Empty(t, p.Pending(), "an expired request is no longer pending")
}

func TestPendingApprover_CancelledRunIsDenied(t *testing.T) {
	p := &bridge.PendingApprover{Timeout: time.Minute}
	ctx, cancel := context.WithCancel(t.Context())
	got := approveAsync(ctx, p, approvalDelivery("{}"), "x")
	waitPending(t, p, 1)
	cancel()
	assert.False(t, answer(t, got))
	assert.Empty(t, p.Pending())
}

func TestPendingApprover_TellsOnPendingAndTruncatesThePayload(t *testing.T) {
	seen := make(chan bridge.Approval, 1)
	p := &bridge.PendingApprover{Timeout: time.Minute, OnPending: func(a bridge.Approval) { seen <- a }}
	big := strings.Repeat("x", 1000)
	got := approveAsync(t.Context(), p, approvalDelivery(big), "x")

	var a bridge.Approval
	select {
	case a = <-seen:
	case <-time.After(2 * time.Second):
		t.Fatal("OnPending was not called")
	}
	assert.Less(t, len(a.PayloadUntrusted), 300)
	assert.True(t, strings.HasSuffix(a.PayloadUntrusted, "..."))
	require.NoError(t, p.Decide(a.ID, false))
	answer(t, got)
}

func TestPendingApprover_DecideUnknownID(t *testing.T) {
	p := &bridge.PendingApprover{}
	assert.ErrorIs(t, p.Decide("ap_nope", true), bridge.ErrNoApproval)
}

// End to end through the MCP tools: a run parked by the sink's approver is
// listed by approvals and released by approve.
func TestApprovalTools_ListAndApprove(t *testing.T) {
	p := &bridge.PendingApprover{Timeout: time.Minute}
	b := newBridgeWith(t, newHookServer(t), bridge.Config{Sink: make(recordingSink, 1), Approvals: p}, map[string]upstream{"ops": newUpstream(t)})
	c := agent(t, b)

	got := approveAsync(t.Context(), p, approvalDelivery(`{"msg":"db down"}`), "mcp__gmail__send")
	waitPending(t, p, 1)

	res := callTool(t, c, "approvals", nil)
	list := structured[struct {
		Approvals []bridge.Approval `json:"approvals"`
	}](t, res)
	require.Len(t, list.Approvals, 1)
	id := list.Approvals[0].ID
	assert.Contains(t, res.Content[0].Text, id+" ops/alert.fired wants mcp__gmail__send")
	assert.Equal(t, `{"msg":"db down"}`, list.Approvals[0].PayloadUntrusted)

	res = callTool(t, c, "approve", map[string]any{"id": id})
	require.False(t, res.IsError, res.Content[0].Text)
	assert.Equal(t, "approved "+id, res.Content[0].Text)
	assert.True(t, answer(t, got))

	res = callTool(t, c, "deny", map[string]any{"id": id})
	assert.True(t, res.IsError, "a decided request can't be answered again")
	assert.Contains(t, res.Content[0].Text, "call approvals")
}

func TestApprovalTools_AbsentWithoutAPendingApprover(t *testing.T) {
	c := agent(t, newBridge(t, newHookServer(t), make(recordingSink, 1), nil, map[string]upstream{"ops": newUpstream(t)}))
	tools, err := c.ListTools(t.Context())
	require.NoError(t, err)
	for _, tool := range tools {
		assert.NotContains(t, []string{"approvals", "approve", "deny"}, tool.Name)
	}
}
