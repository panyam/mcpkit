package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

// TestSubAgentCannotReachParentMemory is the enforcement guard for the issue
// 1151 decision: a sub-agent persona has no working memory and cannot touch the
// parent's scratchpad. The persona is built over serverTools only, so a
// remember call from inside it hits an unknown tool and the parent's memory
// store stays empty. If a change ever hands personas the memory-bearing
// aggregate, remember succeeds and this fails.
func TestSubAgentCannotReachParentMemory(t *testing.T) {
	ts := startTestServer(t)

	// One StubProvider serves both agents (shared turn counter), in order:
	// main delegates -> worker tries to remember -> worker replies -> main answers.
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "d1", Name: "worker",
			Args: core.NewRawJSON(json.RawMessage(`{"task":"clean up the imports"}`)),
		}}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "m1", Name: agent.RememberToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"key":"ran","value":"cleaned imports"}`)),
		}}},
		agent.StubTurn{Text: "could not save"}, // worker's reply after the failed remember
		agent.StubTurn{Text: "done"},           // main synthesizes
	)

	cfg := testConfig(ts.URL)
	cfg.Memory = &MemoryConfig{}
	cfg.SubAgents = []SubAgentConfig{{
		Name: "worker", Description: "does cleanup", Instructions: "You clean up.",
	}}
	memStore := agent.NewInMemoryMemoryStore()
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub), WithMemoryStore(memStore))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "clean up my code"); err != nil {
		t.Fatal(err)
	}

	// The worker actually ran (main delegated, worker took two turns, main
	// synthesized) — so the guard below can't pass vacuously.
	if n := len(stub.Requests()); n != 4 {
		t.Fatalf("expected 4 model calls (main + worker×2 + main), got %d", n)
	}

	// The worker's remember hit an unknown tool: its second request carries the
	// tool result for m1, and it does not confirm a save.
	fed := toolMsgByID(t, stub.Requests()[2].Messages, "m1").Text
	if !strings.Contains(fed, "unknown tool") {
		t.Fatalf("sub-agent's remember should have been an unknown tool, got %q", fed)
	}

	// The invariant: the parent's memory store is untouched by the sub-agent.
	got, _ := memStore.ListMemories(context.Background(), agent.ListMemoriesRequest{})
	if len(got.Items) != 0 {
		t.Fatalf("sub-agent reached the parent's memory store (1151 violation): %+v", got.Items)
	}
}
