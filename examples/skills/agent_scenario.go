// Agent mode for the skills example: a scripted agent connects to this skills
// server, the host auto-loads the server's SEP-2640 skills (digest-verified)
// into the model's system instructions, and the agent answers using the team's
// conventions — no tool call needed, the knowledge rides in the prompt. The
// flow runs against a deterministic StubProvider (no LLM, no network) so it
// doubles as a golden-transcript test. Point --model at a real model to watch
// it actually follow the injected skills.
package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
	"github.com/panyam/mcpkit/server"
)

// buildAgentServer serves the bundled skills/ directory (file mode) the same
// way `make serve` does, so the agent loads the exact skill surface a real
// client would.
func buildAgentServer() (*server.Server, error) {
	provider, err := skills.NewProvider(skills.WithDirectory("skills"))
	if err != nil {
		return nil, err
	}
	srv := server.NewServer(core.ServerInfo{Name: "skills-demo", Version: "0.1.0"})
	provider.RegisterWith(srv)
	return srv, nil
}

// agentStubScript is the model's scripted side. The skills are injected into the
// system instructions before these turns run, so the scripted answers reflect
// the team's git conventions and the pdf-processing skill. A live model would
// derive the same answers from the injected blocks rather than reciting them.
func agentStubScript() *agent.StubProvider {
	return agent.NewStubProvider(
		agent.StubTurn{Text: "Following the team's Git workflow: open three PRs, one per logical change, each from its own feature branch off main."},
		agent.StubTurn{Text: "Per the pdf-processing skill, run scripts/extract.py to pull the text out of report.pdf."},
	)
}

// runAgentScenario wires the host App to the in-process skills server and plays
// the narrative. out receives the full transcript. provider nil uses the
// deterministic script; pass a live provider to watch a real model improvise.
func runAgentScenario(out *syncWriter, provider agent.Provider) error {
	srv, err := buildAgentServer()
	if err != nil {
		return err
	}
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	cfg := &host.Config{
		Model:   host.ModelConfig{BaseURL: "http://stub", Model: "stub"},
		Servers: []host.ServerConfig{{ID: "team", URL: ts.URL + "/mcp"}},
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
	fmt.Fprintln(out, "> I have three unrelated fixes staged. How should I open PRs?")
	if err := app.RunTurn(ctx, "I have three unrelated fixes staged. How should I open PRs?"); err != nil {
		return err
	}
	fmt.Fprintln(out, "> extract the text from report.pdf")
	if err := app.RunTurn(ctx, "extract the text from report.pdf"); err != nil {
		return err
	}
	return nil
}

// syncWriter is a concurrency-safe buffer for the transcript.
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
