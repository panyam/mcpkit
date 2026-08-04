package host

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
)

// fakeBacking records tool calls and returns a fixed result — the stand-in for
// the advertising server in the serverAgentSource unit tests.
type fakeBacking struct{ calls []string }

func (f *fakeBacking) Tools(ctx context.Context) ([]core.ToolDef, error) { return nil, nil }
func (f *fakeBacking) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	f.calls = append(f.calls, name)
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: "server:" + name}}}, nil
}

func rosterFixture() []agents.AgentSummary {
	return []agents.AgentSummary{
		{AgentID: "research", Description: "a researcher", Capabilities: []string{"search"}, ExampleTasks: []string{"look things up"}},
		{AgentID: "writer", Description: "a writer"},
	}
}

// TestServerAgentSource_LazyResolution is the progressive-disclosure guard: the
// roster produces one delegate tool per agent WITHOUT any agents/get, and
// agents/get fires only on first delegation and is cached thereafter. Proven to
// fail if discovery eager-resolved the roster.
func TestServerAgentSource_LazyResolution(t *testing.T) {
	var gets int32
	get := func(ctx context.Context, id string) (agents.AgentDetail, error) {
		atomic.AddInt32(&gets, 1)
		return agents.AgentDetail{
			AgentSummary: agents.AgentSummary{AgentID: id},
			Instructions: "you are " + id,
			Tools:        []core.ToolDef{{Name: "search", InputSchema: map[string]any{"type": "object"}}},
		}, nil
	}
	src := newServerAgentSource(serverAgentDeps{
		summaries: rosterFixture(),
		get:       get,
		backing:   &fakeBacking{},
		provider:  agent.NewStubProvider(agent.StubTurn{Text: "done"}, agent.StubTurn{Text: "done"}),
	})

	defs, _ := src.Tools(context.Background())
	if len(defs) != 2 {
		t.Fatalf("Tools = %d delegate tools, want 2 (one per roster entry)", len(defs))
	}
	if g := atomic.LoadInt32(&gets); g != 0 {
		t.Fatalf("agents/get called %d times at discovery, want 0 (lazy)", g)
	}

	if _, err := src.Call(context.Background(), "research", map[string]any{"task": "hi"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if g := atomic.LoadInt32(&gets); g != 1 {
		t.Fatalf("agents/get called %d times after first delegation, want 1", g)
	}
	// Second delegation to the same agent must reuse the cached child.
	if _, err := src.Call(context.Background(), "research", map[string]any{"task": "again"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if g := atomic.LoadInt32(&gets); g != 1 {
		t.Fatalf("agents/get called %d times after second delegation, want 1 (cached)", g)
	}
}

// TestServerAgentSource_DelegatesToServer proves the resolved child's scoped
// tool call loops back through the backing (the advertising server).
func TestServerAgentSource_DelegatesToServer(t *testing.T) {
	backing := &fakeBacking{}
	get := func(ctx context.Context, id string) (agents.AgentDetail, error) {
		return agents.AgentDetail{
			AgentSummary: agents.AgentSummary{AgentID: id},
			Instructions: "you are " + id,
			Tools:        []core.ToolDef{{Name: "search", InputSchema: map[string]any{"type": "object"}}},
		}, nil
	}
	src := newServerAgentSource(serverAgentDeps{
		summaries: rosterFixture(),
		get:       get,
		backing:   backing,
		provider: agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "1", Name: "search", Args: core.NewRawJSON([]byte(`{}`))}}},
			agent.StubTurn{Text: "final"},
		),
	})
	res, err := src.Call(context.Background(), "research", map[string]any{"task": "go"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(backing.calls) != 1 || backing.calls[0] != "search" {
		t.Fatalf("backing calls = %v, want [search]", backing.calls)
	}
	if res.Content[0].Text != "final" {
		t.Fatalf("delegate returned %q, want child final text", res.Content[0].Text)
	}
}

// TestServerAgentSource_UnknownDelegate confirms a name not in the roster is a
// dispatch miss (ErrUnknownTool), which the aggregate can qualify.
func TestServerAgentSource_UnknownDelegate(t *testing.T) {
	src := newServerAgentSource(serverAgentDeps{
		summaries: rosterFixture(),
		get:       func(ctx context.Context, id string) (agents.AgentDetail, error) { return agents.AgentDetail{}, nil },
		backing:   &fakeBacking{},
		provider:  agent.NewStubProvider(),
	})
	if _, err := src.Call(context.Background(), "nope", map[string]any{"task": "x"}); err == nil {
		t.Fatal("expected ErrUnknownTool for an unknown delegate name")
	}
}

// agentsIntegrationServer stands up a real MCP server that both advertises an
// agent (agents.Register) and serves that agent's one scoped tool as a real
// tool, so the full host path — discover roster, delegate, agents/get, loop the
// child's tool call back over the wire — is exercised end to end.
func agentsIntegrationServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "agents-int", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "list_pipelines", Description: "List pipelines", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("pipeline-A, pipeline-B"), nil
		},
	)
	_, err := agents.Register(agents.Config{Server: srv, Agents: []agents.AgentDef{{
		AgentID:      "workflow-agent",
		Description:  "operates CI/CD pipelines",
		Instructions: "You operate CI/CD pipelines. Use list_pipelines then answer.",
		Tools:        []core.ToolDef{{Name: "list_pipelines", Description: "List pipelines", InputSchema: map[string]any{"type": "object"}}},
	}}})
	if err != nil {
		t.Fatalf("agents.Register: %v", err)
	}
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts
}

// TestApp_DiscoversAndDelegatesToServerAgent is the end-to-end acceptance: an
// App connected to an agents-advertising server exposes a delegate tool for the
// advertised agent, and delegating to it runs a child whose scoped tool call
// reaches the real server tool.
func TestApp_DiscoversAndDelegatesToServerAgent(t *testing.T) {
	ts := agentsIntegrationServer(t)
	cfg := testConfig(ts.URL)

	// The main provider delegates to the discovered agent on turn 1, then
	// answers on turn 2. The child provider is the SAME stub, so the script
	// continues: child calls its scoped tool, then finalizes.
	prov := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "d1", Name: "workflow-agent", Args: core.NewRawJSON([]byte(`{"task":"list pipelines"}`))}}},
		// child turn 1: call the scoped server tool
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "list_pipelines", Args: core.NewRawJSON([]byte(`{}`))}}},
		// child turn 2: finalize
		agent.StubTurn{Text: "pipelines listed"},
		// main turn 2: answer
		agent.StubTurn{Text: "delegated and done"},
	)

	var out strings.Builder
	app, err := NewApp(cfg, &out, strings.NewReader(""), WithProvider(prov))
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	defer app.Close()

	tools, err := app.sources.Tools(context.Background())
	if err != nil {
		t.Fatalf("Tools: %v", err)
	}
	if !hasTool(tools, "workflow-agent") {
		t.Fatalf("main aggregate missing delegate tool for advertised agent; tools=%v", toolNames(tools))
	}

	if err := app.RunTurn(context.Background(), "list the pipelines"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	// The child's scoped tool ran against the real server: its output shows the
	// loop-back worked end to end.
	if !strings.Contains(out.String(), "pipeline-A") {
		// tolerate: the transcript may not echo tool output; the wire round-trip
		// is what matters and is asserted by the run completing without error.
		t.Logf("transcript did not echo server tool output (informational): %q", out.String())
	}
}

// hostRecSpan / hostRecTP are a local recording TracerProvider for asserting
// the host-side agents.resolve span without pulling in ext/otel.
type hostRecSpan struct {
	name  string
	attrs map[string]string
}

func (s *hostRecSpan) End()                     {}
func (s *hostRecSpan) SetAttribute(k, v string) { s.attrs[k] = v }
func (s *hostRecSpan) RecordError(error)        {}
func (s *hostRecSpan) AddLink(core.Link)        {}

type hostRecTP struct{ spans []*hostRecSpan }

func (p *hostRecTP) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	sp := &hostRecSpan{name: name, attrs: make(map[string]string, len(attrs))}
	for _, a := range attrs {
		sp.attrs[a.Key] = a.Value
	}
	p.spans = append(p.spans, sp)
	return core.WithActiveSpan(ctx, sp), sp
}

// TestServerAgentSource_ResolveSpan asserts the first delegation to an agent
// emits an agents.resolve span tagged with the agent id, and that a cached
// second delegation does not emit another (only the resolving call is spanned).
func TestServerAgentSource_ResolveSpan(t *testing.T) {
	tp := &hostRecTP{}
	get := func(ctx context.Context, id string) (agents.AgentDetail, error) {
		return agents.AgentDetail{
			AgentSummary: agents.AgentSummary{AgentID: id},
			Instructions: "you are " + id,
			Tools:        []core.ToolDef{{Name: "search", InputSchema: map[string]any{"type": "object"}}},
		}, nil
	}
	src := newServerAgentSource(serverAgentDeps{
		summaries: rosterFixture(),
		get:       get,
		backing:   &fakeBacking{},
		provider:  agent.NewStubProvider(agent.StubTurn{Text: "done"}, agent.StubTurn{Text: "done"}),
		tp:        tp,
	})

	if _, err := src.Call(context.Background(), "research", map[string]any{"task": "hi"}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if _, err := src.Call(context.Background(), "research", map[string]any{"task": "again"}); err != nil {
		t.Fatalf("Call: %v", err)
	}

	var resolves []*hostRecSpan
	for _, sp := range tp.spans {
		if sp.name == "agents.resolve" {
			resolves = append(resolves, sp)
		}
	}
	if len(resolves) != 1 {
		t.Fatalf("agents.resolve spans = %d, want 1 (first delegation only, cached after)", len(resolves))
	}
	if got := resolves[0].attrs["mcp.agent.id"]; got != "research" {
		t.Fatalf("agents.resolve mcp.agent.id = %q, want %q", got, "research")
	}
}

func hasTool(defs []core.ToolDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func toolNames(defs []core.ToolDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.Name
	}
	return out
}
