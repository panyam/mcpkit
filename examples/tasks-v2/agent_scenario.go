// Agent mode for the tasks-v2 example: a scripted agent drives the same
// server-directed async tools through conversation. From the model's side a
// task-backed tool call looks like any other tool call; the SEP-2663 task
// machinery (create → run → poll → result) is handled by the host and never
// surfaces to the model. The whole flow runs against a deterministic
// StubProvider (no LLM, no network) so it doubles as a golden-transcript test.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// buildAgentServer is the tasks-v2 demo server, in-process: the same tools
// registerTasksV2DemoTools installs for `just serve`, so the agent drives the
// exact surface a real client would.
func buildAgentServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "tasks-v2-demo", Version: "0.1.0"})
	registerTasksV2DemoTools(srv)
	return srv
}

// agentStubScript is the model's scripted side: greet is sync-only, slow_compute
// is server-directed async. The model calls both the same way; only the server
// decides which runs as a task.
func agentStubScript() *agent.StubProvider {
	tc := func(id, name, args string) agent.ToolCall {
		return agent.ToolCall{ID: id, Name: name, Args: core.NewRawJSON(json.RawMessage(args))}
	}
	return agent.NewStubProvider(
		// A sync-only tool: no task, immediate answer.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("g1", "greet", `{"name":"Ada"}`)}},
		agent.StubTurn{Text: "Said hello to Ada."},
		// A task-backed tool: the server directs it async, the host runs the
		// task to completion within the grace window, and the result flows back
		// to the model as an ordinary tool result.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("c1", "slow_compute", `{"seconds":1,"label":"quarterly"}`)}},
		agent.StubTurn{Text: "The quarterly computation is done — the result is 42."},
	)
}

// runAgentScenario wires the host App to the in-process tasks-v2 server and
// plays the narrative. out receives the full transcript. provider nil uses the
// deterministic script; pass a live provider to watch a real model improvise.
func runAgentScenario(out *syncWriter, provider agent.Provider) error {
	srv := buildAgentServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	cfg := &host.Config{
		Model:   host.ModelConfig{BaseURL: "http://stub", Model: "stub"},
		Servers: []host.ServerConfig{{ID: "tasks", URL: ts.URL + "/mcp"}},
	}
	opts := []host.AppOption{}
	if provider != nil {
		opts = append(opts, host.WithProvider(provider))
	} else {
		opts = append(opts, host.WithProvider(agentStubScript()))
	}

	app, err := host.NewApp(cfg, out, strings.NewReader(""), opts...)
	if err != nil {
		return err
	}
	defer app.Close()

	ctx := context.Background()
	fmt.Fprintln(out, "> say hello to Ada")
	if err := app.RunTurn(ctx, "say hello to Ada"); err != nil {
		return err
	}
	fmt.Fprintln(out, "> run the quarterly computation")
	if err := app.RunTurn(ctx, "run the quarterly computation"); err != nil {
		return err
	}
	return nil
}

// syncWriter is a concurrency-safe buffer: task progress and completion can be
// rendered from background goroutines while the main flow writes prompt lines.
type syncWriter struct {
	mu sync.Mutex
	b  []byte
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}
