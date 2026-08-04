package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// recordingBacking is a stand-in for the advertising server: it records every
// tool name dispatched to it and returns a fixed text result. Its Tools list is
// intentionally broad (more than the scoped set) so a test can prove the scoped
// source refuses names the backing would otherwise serve.
type recordingBacking struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingBacking) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{{Name: "search"}, {Name: "delete_everything"}}, nil
}

func (r *recordingBacking) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name)
	r.mu.Unlock()
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "backing:" + name}}}, nil
}

func (r *recordingBacking) received(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == name {
			return true
		}
	}
	return false
}

func scopedDefs() []core.ToolDef {
	return []core.ToolDef{{Name: "search", Description: "search the corpus", InputSchema: map[string]any{"type": "object"}}}
}

// TestServerAgentSource_DispatchesScopedCall proves the happy path: the child
// calls a scoped tool, the call reaches the backing (the server), and the
// child's final text bubbles up through the delegate.
func TestServerAgentSource_DispatchesScopedCall(t *testing.T) {
	backing := &recordingBacking{}
	prov := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "1", Name: "search", Args: core.NewRawJSON([]byte(`{"q":"x"}`))}}},
		StubTurn{Text: "found it"},
	)
	src, err := NewServerAgentSource(ServerAgentConfig{
		Name:         "research",
		Description:  "a research specialist",
		Instructions: "You research.",
		Tools:        scopedDefs(),
		Backing:      backing,
		Provider:     prov,
	})
	if err != nil {
		t.Fatalf("NewServerAgentSource: %v", err)
	}
	res, err := src.Call(context.Background(), "research", map[string]any{"task": "look it up"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !backing.received("search") {
		t.Fatalf("scoped tool call did not reach the backing; calls=%v", backing.calls)
	}
	if got := res.Content[0].Text; got != "found it" {
		t.Fatalf("delegate returned %q, want child final text %q", got, "found it")
	}
}

// TestServerAgentSource_RejectsUnscopedTool is the capability-boundary guard:
// the child model calls a tool the backing server HAS but the agent definition
// did NOT scope. The call must never reach the backing; the child is told the
// tool is unknown and its turn continues. Proven to fail if scopedSource were
// to delegate every name straight through.
func TestServerAgentSource_RejectsUnscopedTool(t *testing.T) {
	backing := &recordingBacking{}
	prov := NewStubProvider(
		StubTurn{ToolCalls: []ToolCall{{ID: "1", Name: "delete_everything", Args: core.NewRawJSON([]byte(`{}`))}}},
		StubTurn{Text: "could not"},
	)
	src, err := NewServerAgentSource(ServerAgentConfig{
		Name:     "research",
		Tools:    scopedDefs(), // scopes "search" only
		Backing:  backing,
		Provider: prov,
	})
	if err != nil {
		t.Fatalf("NewServerAgentSource: %v", err)
	}
	if _, err := src.Call(context.Background(), "research", map[string]any{"task": "wipe it"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if backing.received("delete_everything") {
		t.Fatalf("un-scoped tool reached the backing: scoping boundary breached")
	}
}

// TestServerAgentSource_ThreadsInstructionsAndScopedTools asserts the child
// Runner is seeded with the agent's instructions and advertises exactly the
// scoped tools to its own model (progressive disclosure: the scoped schemas
// live in the child, and only the scoped names are offered).
func TestServerAgentSource_ThreadsInstructionsAndScopedTools(t *testing.T) {
	backing := &recordingBacking{}
	prov := NewStubProvider(StubTurn{Text: "ok"})
	src, err := NewServerAgentSource(ServerAgentConfig{
		Name:         "research",
		Instructions: "You are a careful researcher.",
		Tools:        scopedDefs(),
		Backing:      backing,
		Provider:     prov,
	})
	if err != nil {
		t.Fatalf("NewServerAgentSource: %v", err)
	}
	if _, err := src.Call(context.Background(), "research", map[string]any{"task": "hi"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	reqs := prov.Requests()
	if len(reqs) == 0 {
		t.Fatal("child provider received no request")
	}
	if reqs[0].Instructions != "You are a careful researcher." {
		t.Fatalf("child instructions = %q, want the agent's", reqs[0].Instructions)
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "search" {
		t.Fatalf("child was offered %v, want only the scoped [search]", reqs[0].Tools)
	}
}

func TestServerAgentSource_Validates(t *testing.T) {
	backing := &recordingBacking{}
	prov := NewStubProvider()
	cases := map[string]ServerAgentConfig{
		"missing name":     {Provider: prov, Backing: backing},
		"missing provider": {Name: "x", Backing: backing},
		"missing backing":  {Name: "x", Provider: prov},
	}
	for name, cfg := range cases {
		if _, err := NewServerAgentSource(cfg); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
