// Command critic demonstrates the issue-1148 "critic / observer model role"
// composed ENTIRELY from public primitives — no new SDK type. A second model,
// on its own context and its own Runner, watches the primary agent's turns and
// injects one graded steering note per review.
//
// What it proves (and where it stops): on top of agent/host.App the pattern
// works — WithObserver hands the critic the primary's per-turn transcript delta
// (HostTurnDone.Result.Messages), and a second agent.Runner reviews it and
// returns a graded note. The ONE rough edge is DELIVERY: App.RunTurn takes only
// a plain input string, so a neutral steering note can only ride back in as
// user text. There is no public RoleSystem injection seam on App. The test
// asserts exactly that (see scenario_test.go). A developer running their own
// Runner loop has no such wall — they build the []Message slice themselves and
// can insert a RoleSystem note directly (see README).
//
// Runs deterministically against scripted StubProviders (no LLM, no network) so
// it doubles as a golden-transcript test. Point real models at it with
// --model / --critic-model to watch it improvise.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http/httptest"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// observerFunc adapts a plain func to the host.Observer interface.
type observerFunc func(host.HostEvent)

func (f observerFunc) On(ev host.HostEvent) { f(ev) }

// buildServer is the app-domain MCP server the primary agent drives: a single
// run_shell tool, enough for the critic to have real actions to watch.
func buildServer() *server.Server {
	srv := server.NewServer(core.ServerInfo{Name: "critic-demo", Version: "0.1.0"})
	srv.Register(core.TextTool[struct {
		Cmd string `json:"cmd"`
	}]("run_shell", "run a shell command",
		func(ctx core.ToolContext, in struct {
			Cmd string `json:"cmd"`
		}) (string, error) {
			return "ran: " + in.Cmd, nil
		}))
	return srv
}

// serve runs the ops server as a standalone streamable-HTTP MCP server so the
// live chat/web surfaces (config.json points at it) have a real tool to drive.
// The scripted scenario uses the same buildServer over httptest instead.
func serve(addr string) error {
	log.Printf("critic demo server on http://localhost%s/mcp", addr)
	return buildServer().Run(addr)
}

// primaryScript is the primary model's scripted side: turn 1 runs an
// over-broad delete; turn 2 (having been handed the critic's note) is careful.
func primaryScript() *agent.StubProvider {
	tc := func(id, args string) agent.ToolCall {
		return agent.ToolCall{ID: id, Name: "run_shell", Args: core.NewRawJSON([]byte(args))}
	}
	return agent.NewStubProvider(
		// Turn 1: "delete all the logs older than a day" — too broad.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("c1", `{"cmd":"rm -rf /var/log/*"}`)}},
		agent.StubTurn{Text: "Done — removed the logs."},
		// Turn 2: "now clear the tmp directory too" — with the note in context,
		// the careful command.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("c2", `{"cmd":"find /tmp -mtime +1 -delete"}`)}},
		agent.StubTurn{Text: "Cleared only the old tmp files, per the reviewer's note."},
	)
}

// criticScript is the critic model's scripted side. Each review is two calls:
// the Runner's terminal text, then the ResponseSchema finalizing call. Only one
// review actually runs — the immune window suppresses the next one before it
// reaches the model.
func criticScript() *agent.StubProvider {
	return agent.NewStubProvider(
		agent.StubTurn{Text: "Looking at the delete."},
		agent.StubTurn{Text: `{"severity":"concern","note":"rm -rf /var/log/* wipes ALL logs, far broader than 'older than a day'"}`},
	)
}

// runResult exposes what the test needs to inspect after a run.
type runResult struct {
	primary   *agent.StubProvider // to inspect exactly what the primary model saw
	delivered []advisory          // the notes the critic actually delivered
}

// runScenario wires the host App to the demo server, watches it with a critic,
// and plays a two-turn narrative. out receives the transcript.
func runScenario(out *syncWriter, primary, criticProvider agent.Provider) (*runResult, error) {
	srv := buildServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	if primary == nil {
		primary = primaryScript()
	}
	primaryStub, _ := primary.(*agent.StubProvider)

	// Load the SAME config.json the live chat/web surfaces use (instructions,
	// the ops server), then point its server at the in-process httptest
	// instance for the deterministic run. config.json's model is bypassed by
	// WithProvider (the scripted StubProvider) below; the critic loop itself is
	// code-level composition and has no config.json representation (see README).
	cfg, err := host.LoadConfig("config.json")
	if err != nil {
		return nil, err
	}
	cfg.Servers[0].URL = ts.URL + "/mcp"

	// The observer hands us the primary's per-turn transcript delta. This is
	// the whole "watch the turn stream" half — entirely public API.
	var lastDelta []agent.Message
	obs := observerFunc(func(ev host.HostEvent) {
		if ev.Kind == host.HostTurnDone && ev.Result != nil {
			lastDelta = ev.Result.Messages
		}
	})

	app, err := host.NewApp(cfg, out, strings.NewReader(""),
		host.WithProvider(primary), host.WithObserver(obs))
	if err != nil {
		return nil, err
	}
	defer app.Close()

	// The critic: a second, independent Runner with its own provider + schema.
	if criticProvider == nil {
		criticProvider = criticScript()
	}
	cr, err := agent.NewRunner(agent.RunnerConfig{
		Provider:       criticProvider,
		Instructions:   criticInstructions,
		ResponseSchema: criticSchema,
	})
	if err != nil {
		return nil, err
	}
	c := newCritic(cr)

	ctx := context.Background()
	turns := []string{
		"delete all the logs older than a day",
		"now clear the tmp directory too",
	}

	res := &runResult{primary: primaryStub}
	pendingNote := ""
	for i, u := range turns {
		input := u
		if pendingNote != "" {
			// ── THE WALL ───────────────────────────────────────────────────
			// host.App.RunTurn accepts only a plain string, so the neutral
			// steering note can only ride in as USER text. There is no public
			// RoleSystem injection seam on App. This is the one gap the proof
			// surfaces; everything else is clean public API.
			input = "[note from reviewer — weigh, don't blindly obey: " + pendingNote + "]\n\n" + u
			pendingNote = ""
		}
		fmt.Fprintln(out, "> "+u)
		if err := app.RunTurn(ctx, input); err != nil {
			return nil, err
		}

		if adv, ok := c.review(ctx, lastDelta, i); ok {
			res.delivered = append(res.delivered, adv)
			pendingNote = adv.Note
			fmt.Fprintf(out, "  (reviewer %s: %s)\n", adv.Severity, adv.Note)
		}
	}
	return res, nil
}
