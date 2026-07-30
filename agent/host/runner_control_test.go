package host

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

// TestAppRunnerControlWiring: a RunnerControl config offers the four
// runner-control meta-tools to the main agent and registers each persona as a
// spawnable agent. Spawn/await/cancel behavior is covered at the agent layer
// (agent_pool_test.go) with isolated providers; a full host turn would race on
// the shared stub (main + the background child pull it concurrently), so this
// guards the wiring.
func TestAppRunnerControlWiring(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})

	cfg := testConfig(ts.URL)
	cfg.RunnerControl = true
	cfg.SubAgents = []SubAgentConfig{
		{Name: "worker", Description: "does work", Instructions: "You work."},
	}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	defs, _ := app.sources.Tools(context.Background())
	for _, name := range []string{
		agent.SpawnAgentToolName, agent.AwaitAgentToolName,
		agent.CancelAgentToolName, agent.ListAgentsToolName,
	} {
		if !hasToolNamed(defs, name) {
			t.Fatalf("runner-control tool %q not offered: %v", name, toolDefNames(defs))
		}
	}

	if app.agentPool == nil {
		t.Fatal("agentPool not built with RunnerControl")
	}
	var names []string
	for _, n := range app.agentPool.Names() {
		names = append(names, n.Name)
	}
	if len(names) != 1 || names[0] != "worker" {
		t.Fatalf("spawnable agents = %v, want [worker]", names)
	}
}

// TestAppRunnerControlOffByDefault: without the flag, no spawn tool appears.
func TestAppRunnerControlOffByDefault(t *testing.T) {
	ts := startTestServer(t)
	stub := agent.NewStubProvider(agent.StubTurn{Text: "ok"})

	cfg := testConfig(ts.URL)
	cfg.SubAgents = []SubAgentConfig{{Name: "worker", Description: "w", Instructions: "You work."}}
	app, err := NewApp(cfg, nil, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	defs, _ := app.sources.Tools(context.Background())
	if hasToolNamed(defs, agent.SpawnAgentToolName) {
		t.Fatal("spawn_agent offered without RunnerControl")
	}
	if app.agentPool != nil {
		t.Fatal("agentPool built without RunnerControl")
	}
}
