package host

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// collectObserver records every HostEvent for assertions.
type collectObserver struct {
	mu     sync.Mutex
	events []HostEvent
}

func (c *collectObserver) On(ev HostEvent) {
	c.mu.Lock()
	c.events = append(c.events, ev)
	c.mu.Unlock()
}

func (c *collectObserver) kinds(k HostEventKind) []HostEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []HostEvent
	for _, e := range c.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// TestAppSubAgentPersonaDelegatesAndSurfaces is the 1031 payoff: a SubAgents
// config builds a persona the main agent delegates to (StubProvider), and the
// child's events surface as HostSubAgentEvent (scoped), for nested rendering.
func TestAppSubAgentPersonaDelegatesAndSurfaces(t *testing.T) {
	ts := startTestServer(t)

	// One StubProvider serves both the main agent and the sub-agent (shared
	// turn counter): main delegates, then the sub-agent answers, then main
	// synthesizes.
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "d1", Name: "researcher",
			Args: core.NewRawJSON(json.RawMessage(`{"task":"look it up"}`)),
		}}},
		agent.StubTurn{Text: "the researcher found it"},    // sub-agent's answer
		agent.StubTurn{Text: "here is what my team found"}, // main synthesizes
	)

	cfg := testConfig(ts.URL)
	cfg.SubAgents = []SubAgentConfig{{
		Name: "researcher", Description: "researches a topic", Instructions: "You research.",
	}}
	obs := &collectObserver{}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// the persona is exposed to the main agent as a tool
	defs, _ := app.sources.Tools(context.Background())
	if !hasToolNamed(defs, "researcher") {
		t.Fatalf("sub-agent tool 'researcher' not offered to the main agent: %v", toolDefNames(defs))
	}

	if err := app.RunTurn(context.Background(), "research this"); err != nil {
		t.Fatal(err)
	}

	// the child's activity surfaced as HostSubAgentEvent, scoped to the persona
	sub := obs.kinds(HostSubAgentEvent)
	if len(sub) == 0 {
		t.Fatal("no HostSubAgentEvent emitted; sub-agent ran invisibly")
	}
	var sawScoped bool
	for _, e := range sub {
		if e.SubAgent.Scope == "researcher" && e.SubAgent.Depth == 1 {
			sawScoped = true
		}
	}
	if !sawScoped {
		t.Fatalf("sub-agent events not scoped to 'researcher' at depth 1: %+v", sub)
	}
}

// TestAppSubAgentSnakeCaseName guards the registration bug where a snake_case
// persona name (e.g. deep_researcher) built a "subagent:deep_researcher" source
// id, which MultiSource.Add rejects because "_" is the qualified-name separator.
// The id is sanitized (underscores -> dashes) while the model-visible tool name
// keeps its underscores.
func TestAppSubAgentSnakeCaseName(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})

	cfg := testConfig(ts.URL)
	cfg.SubAgents = []SubAgentConfig{{
		Name: "deep_researcher", Description: "researches deeply", Instructions: "You research.",
	}}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatalf("NewApp rejected a snake_case sub-agent name: %v", err)
	}
	defer app.Close()

	defs, _ := app.sources.Tools(context.Background())
	if !hasToolNamed(defs, "deep_researcher") {
		t.Fatalf("snake_case sub-agent tool not offered under its own name: %v", toolDefNames(defs))
	}
}

// TestAppSubAgentSignalsSurface: a CanSignal persona raises signal_parent, and
// the main agent surfaces it as an EventSignal (wrapped in HostRunnerEvent) and
// continues (default inject-and-continue policy). Guards the host wiring of
// issue 1165 — the persona gets the control tool and the main Runner reacts.
func TestAppSubAgentSignalsSurface(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "d1", Name: "worker",
			Args: core.NewRawJSON(json.RawMessage(`{"task":"go"}`))}}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "s1", Name: agent.SignalParentToolName,
			Args: core.NewRawJSON(json.RawMessage(`{"kind":"escalate","note":"found it"}`))}}},
		agent.StubTurn{Text: "child done"},
		agent.StubTurn{Text: "synthesized"},
	)

	cfg := testConfig(ts.URL)
	cfg.SubAgents = []SubAgentConfig{{
		Name: "worker", Description: "does work", Instructions: "You work.", CanSignal: true,
	}}
	obs := &collectObserver{}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub), WithObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "delegate to worker"); err != nil {
		t.Fatal(err)
	}

	var found *agent.Signal
	for _, e := range obs.kinds(HostRunnerEvent) {
		if e.RunnerEvent.Kind == agent.EventSignal {
			found = e.RunnerEvent.Signal
		}
	}
	if found == nil {
		t.Fatal("no EventSignal surfaced; the persona could not signal the main agent")
	}
	if found.Kind != agent.SignalEscalate || found.Source != "worker" {
		t.Fatalf("surfaced signal = %+v, want escalate from 'worker'", found)
	}
}

func hasToolNamed(defs []core.ToolDef, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func toolDefNames(defs []core.ToolDef) string {
	var n []string
	for _, d := range defs {
		n = append(n, d.Name)
	}
	return strings.Join(n, ",")
}
