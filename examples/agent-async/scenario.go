// Command agent-async demonstrates an agent managing its own asynchronous
// work through conversation: it subscribes to an event stream, installs a
// standing "when X happens, do Y" trigger, runs a task tool, and is later
// woken by the event to act on it — all through host meta-tools, no config.
//
// It runs the whole flow deterministically against a scripted StubProvider
// (no LLM, no network) so it doubles as a golden-transcript test — the agent
// module's scriptable-loop story. Point a real model at the same server with
// --model to watch it improvise. (Background-detach on long tasks — the task
// runs past its grace window, then notifies via a task.completed event — is
// exercised in the agentchat tests; here the report finishes fast.)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/agent/host"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
	tasksext "github.com/panyam/mcpkit/ext/tasks"
	"github.com/panyam/mcpkit/server"
)

type userData struct {
	Email string `json:"email"`
}

// buildServer is the app-domain MCP server the agent drives: a user.created
// event source, a send_email tool, and a task-backed long_report tool.
func buildServer() (*server.Server, func(context.Context, userData) error, *[]string) {
	srv := server.NewServer(core.ServerInfo{Name: "agent-async-demo", Version: "0.1.0"})

	src, yield := events.NewYieldingSource[userData](events.EventDef{
		Name: "user.created", Description: "a new user signed up",
	})
	events.Register(events.Config{Sources: []events.EventSource{src}, Server: srv, UnsafeAnonymousPrincipal: "demo"})

	var sent []string
	srv.Register(core.TextTool[struct {
		To string `json:"to"`
	}]("send_email", "send an email to an address",
		func(ctx core.ToolContext, in struct {
			To string `json:"to"`
		}) (string, error) {
			sent = append(sent, in.To)
			return "email sent to " + in.To, nil
		}))

	srv.Register(core.TypedTool[struct{}, core.ToolResponse]("long_report",
		"build a long report (runs as a background task)",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResponse, error) {
			tc := tasksext.GetTaskContext(ctx)
			if tc == nil {
				return core.GoAsyncResult{}, nil
			}
			time.Sleep(50 * time.Millisecond) // stand in for real work
			return core.TextResult("report ready: 42 pages"), nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))
	tasksext.Register(tasksext.Config{Server: srv})

	return srv, yield, &sent
}

// stubScript is the model's scripted side of the conversation: the moves a
// real model would make, pinned so the demo is deterministic.
func stubScript() *agent.StubProvider {
	tc := func(id, name, args string) agent.ToolCall {
		return agent.ToolCall{ID: id, Name: name, Args: core.NewRawJSON(json.RawMessage(args))}
	}
	return agent.NewStubProvider(
		// User: "email me when users are created". Model subscribes + installs a trigger.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("s1", "subscribe_events", `{"server":"app","name":"user.created"}`)}},
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("t1", "create_trigger",
			`{"event":"user.created","instructions":"send a welcome email to the new user's address","label":"welcome"}`)}},
		agent.StubTurn{Text: "Set — I'll welcome-email every new user."},
		// User: "start the quarterly report". Model kicks off the task.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("r1", "long_report", `{}`)}},
		agent.StubTurn{Text: "The quarterly report is ready — 42 pages."},
		// Proactive turn: user.created fired → model sends the welcome email.
		agent.StubTurn{ToolCalls: []agent.ToolCall{tc("e1", "send_email", `{"to":"ada@example.com"}`)}},
		agent.StubTurn{Text: "Welcomed ada@example.com."},
	)
}

// runScenario wires the host App to the demo server and plays the narrative,
// yielding the event between the setup turns and the report kickoff. out
// receives the full transcript. provider nil uses the deterministic script.
func runScenario(out *syncWriter, provider agent.Provider) error {
	srv, yield, _ := buildServer()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	defer ts.Close()

	cfg := &host.Config{
		Model:     host.ModelConfig{BaseURL: "http://stub", Model: "stub"},
		MetaTools: true,
		Servers:   []host.ServerConfig{{ID: "app", URL: ts.URL + "/mcp"}},
	}
	opts := []host.AppOption{}
	if provider != nil {
		opts = append(opts, host.WithProvider(provider))
	} else {
		opts = append(opts, host.WithProvider(stubScript()))
	}

	app, err := host.NewApp(cfg, out, strings.NewReader(""), opts...)
	if err != nil {
		return err
	}
	defer app.Close()

	ctx := context.Background()
	fmt.Fprintln(out, "> email me whenever a user is created")
	if err := app.RunTurn(ctx, "email me whenever a user is created"); err != nil {
		return err
	}
	fmt.Fprintln(out, "> start the quarterly report")
	if err := app.RunTurn(ctx, "start the quarterly report"); err != nil {
		return err
	}

	// A user signs up: the standing trigger wakes the agent, which emails them.
	fmt.Fprintln(out, "(a user signs up...)")
	if err := yield(ctx, userData{Email: "ada@example.com"}); err != nil {
		return err
	}
	waitForOutput(out, "Welcomed", 3*time.Second)
	return nil
}
