package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

// TestCurrentRunIDLockFree is the deadlock regression for issue 1140: the
// session-scoped memory namespace func runs inside a turn while turnMu is
// already held, so currentRunID MUST read the run id without taking turnMu
// (RunID would self-deadlock there).
func TestCurrentRunIDLockFree(t *testing.T) {
	ts := startTestServer(t)
	store := agent.NewInMemoryRunStore()
	var out strings.Builder
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""),
		WithProvider(agent.NewStubProvider()), WithRunStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.AttachRun(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}

	app.turnMu.Lock()
	defer app.turnMu.Unlock()
	done := make(chan string, 1)
	go func() { done <- app.session.currentRunID() }()
	select {
	case got := <-done:
		if got != "r1" {
			t.Fatalf("currentRunID = %q, want r1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("currentRunID blocked while turnMu was held — it must not take turnMu (1140 deadlock)")
	}
}

// TestSessionScopedMemoryIsolatesByRun proves that with SessionScoped on, a
// note remembered under one run id is invisible to another run: the store
// partitions by run-id namespace and recall never crosses sessions.
func TestSessionScopedMemoryIsolatesByRun(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		// run r1: remember lang=Go
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"lang","value":"Go"}`)),
		}}},
		agent.StubTurn{Text: "saved"},
		// run r2: recall lang — should find nothing
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c2", Name: agent.RecallToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"query":"lang"}`)),
		}}},
		agent.StubTurn{Text: "nothing stored"},
	)
	memStore := agent.NewInMemoryMemoryStore()
	cfg := testConfig(ts.URL)
	cfg.Memory = &MemoryConfig{SessionScoped: true}
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""),
		WithProvider(stub), WithMemoryStore(memStore), WithRunStore(agent.NewInMemoryRunStore()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()

	if err := app.AttachRun(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(ctx, "remember my language is Go"); err != nil {
		t.Fatal(err)
	}
	if err := app.AttachRun(ctx, "r2"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(ctx, "what is my language?"); err != nil {
		t.Fatal(err)
	}

	// The store partitions by run id: r1 holds the note, r2 is empty.
	r1, _ := memStore.ListMemories(ctx, agent.ListMemoriesRequest{Namespace: "r1"})
	if len(r1.Items) != 1 || r1.Items[0].Item.Value != "Go" {
		t.Fatalf("r1 namespace = %+v, want lang=Go", r1.Items)
	}
	r2, _ := memStore.ListMemories(ctx, agent.ListMemoriesRequest{Namespace: "r2"})
	if len(r2.Items) != 0 {
		t.Fatalf("r2 namespace should be empty (isolation), got %+v", r2.Items)
	}

	// And recall on r2 fed the model nothing containing the r1 value.
	fed := toolMsgByID(t, stub.Requests()[3].Messages, "c2").Text
	if strings.Contains(fed, "Go") {
		t.Fatalf("recall on r2 leaked r1's note: %q", fed)
	}
}

// TestSessionScopedOffSharesScratchpad guards the opt-in default: with
// SessionScoped off, notes land in the shared default namespace regardless of
// the active run, so switching sessions does not isolate memory.
func TestSessionScopedOffSharesScratchpad(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"lang","value":"Go"}`)),
		}}},
		agent.StubTurn{Text: "saved"},
	)
	memStore := agent.NewInMemoryMemoryStore()
	cfg := testConfig(ts.URL)
	cfg.Memory = &MemoryConfig{} // SessionScoped defaults false
	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""),
		WithProvider(stub), WithMemoryStore(memStore), WithRunStore(agent.NewInMemoryRunStore()))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background()

	if err := app.AttachRun(ctx, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := app.RunTurn(ctx, "remember my language is Go"); err != nil {
		t.Fatal(err)
	}

	// The note is in the shared default namespace, not partitioned by run id.
	def, _ := memStore.ListMemories(ctx, agent.ListMemoriesRequest{Namespace: ""})
	if len(def.Items) != 1 || def.Items[0].Item.Value != "Go" {
		t.Fatalf("default namespace = %+v, want lang=Go (unscoped)", def.Items)
	}
	r1, _ := memStore.ListMemories(ctx, agent.ListMemoriesRequest{Namespace: "r1"})
	if len(r1.Items) != 0 {
		t.Fatalf("run-id namespace should be empty when SessionScoped is off, got %+v", r1.Items)
	}
}
