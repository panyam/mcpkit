// Command multi-agent demonstrates mcpkit's Phase 3 multi-agent composition:
//
//   - Sub-agents as tools (AgentSource): a supervisor delegates a subtask to a
//     child agent, which runs over its own isolated conversation and returns
//     an answer. The child's event stream surfaces to the transcript nested
//     under the parent (SubAgentEvent scope/depth).
//   - Handoff (Team/Orchestrator): a triage agent transfers control of the
//     conversation to a specialist, who takes over the same thread — transfer,
//     not call-and-return.
//
// It runs deterministically against scripted StubProviders (no LLM, no
// network), so it doubles as a golden-transcript test. Point a real model at
// it with --model to watch it improvise.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// nestedRenderer writes a sub-agent's events into the transcript, indented by
// nesting depth and tagged with the agent's scope — the SubAgentEvent envelope
// (scope/depth on the wrapper, the Event itself flat) turned into readable
// output. A real surface would render this in a TUI; here it is plain text.
func nestedRenderer(w io.Writer) func(agent.SubAgentEvent) {
	return func(sa agent.SubAgentEvent) {
		indent := strings.Repeat("  ", sa.Depth)
		switch sa.Event.Kind {
		case agent.EventToolBegin:
			if sa.Event.ToolCall != nil {
				fmt.Fprintf(w, "%s[%s] · calls %s\n", indent, sa.Scope, sa.Event.ToolCall.Name)
			}
		case agent.EventTurnEnd:
			if sa.Event.Result != nil && sa.Event.Result.Text != "" {
				fmt.Fprintf(w, "%s[%s] → %s\n", indent, sa.Scope, sa.Event.Result.Text)
			}
		}
	}
}

// buildResearcher is a sub-agent with a web_search tool; it looks something up
// and reports back. Scripted to search once, then answer.
func buildResearcher(w io.Writer) (*agent.AgentSource, error) {
	tools := agent.NewFuncSource()
	if err := agent.AddFunc(tools, "web_search", "search the web for a query",
		func(ctx context.Context, in struct {
			Query string `json:"query"`
		}) (string, error) {
			return "Go generics (added in 1.18) let functions and types take type parameters.", nil
		}); err != nil {
		return nil, err
	}
	runner, err := agent.NewRunner(agent.RunnerConfig{
		Instructions: "You are a researcher. Search, then answer concisely.",
		Tools:        tools,
		Provider: agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "s1", Name: "web_search", Args: core.NewRawJSON(json.RawMessage(`{"query":"Go generics"}`))}}},
			agent.StubTurn{Text: "Go generics let you write type-parameterized functions and types (since 1.18)."},
		),
	})
	if err != nil {
		return nil, err
	}
	return agent.NewAgentSource(agent.AgentSourceConfig{
		Name: "researcher", Description: "researches a topic and reports findings",
		Runner: runner, OnEvent: sinkFrom(w),
	})
}

// buildCoder is a sub-agent with a run_code tool; it writes and checks a
// snippet. Scripted to run code once, then answer.
func buildCoder(w io.Writer) (*agent.AgentSource, error) {
	tools := agent.NewFuncSource()
	if err := agent.AddFunc(tools, "run_code", "compile and run a Go snippet",
		func(ctx context.Context, in struct {
			Code string `json:"code"`
		}) (string, error) {
			return "compiled and ran successfully", nil
		}); err != nil {
		return nil, err
	}
	runner, err := agent.NewRunner(agent.RunnerConfig{
		Instructions: "You are a coder. Write a snippet, verify it runs, then present it.",
		Tools:        tools,
		Provider: agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "run_code", Args: core.NewRawJSON(json.RawMessage(`{"code":"func Max..."}`))}}},
			agent.StubTurn{Text: "func Max[T cmp.Ordered](a, b T) T { if a > b { return a }; return b }"},
		),
	})
	if err != nil {
		return nil, err
	}
	return agent.NewAgentSource(agent.AgentSourceConfig{
		Name: "coder", Description: "writes and verifies code",
		Runner: runner, OnEvent: sinkFrom(w),
	})
}

// sinkFrom is a package-level indirection so tests can swap the renderer; it
// just returns the nested renderer bound to w.
func sinkFrom(w io.Writer) func(agent.SubAgentEvent) { return nestedRenderer(w) }

// runSupervisor drives the agent-as-tool half: a supervisor delegates to the
// researcher then the coder and synthesizes their answers.
func runSupervisor(w io.Writer, provider agent.Provider) error {
	researcher, err := buildResearcher(w)
	if err != nil {
		return err
	}
	coder, err := buildCoder(w)
	if err != nil {
		return err
	}
	team := agent.NewMultiSource()
	if err := team.Add("researcher", researcher); err != nil {
		return err
	}
	if err := team.Add("coder", coder); err != nil {
		return err
	}

	sup := provider
	if sup == nil {
		sup = agent.NewStubProvider(
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "d1", Name: "researcher", Args: core.NewRawJSON(json.RawMessage(`{"task":"Explain Go generics"}`))}}},
			agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "d2", Name: "coder", Args: core.NewRawJSON(json.RawMessage(`{"task":"Write a generic Max"}`))}}},
			agent.StubTurn{Text: "Generics add type parameters (1.18); here's a generic Max function."},
		)
	}
	runner, err := agent.NewRunner(agent.RunnerConfig{
		Instructions: "You are a supervisor. Delegate research and coding to your sub-agents, then synthesize.",
		Tools:        team,
		Provider:     sup,
	})
	if err != nil {
		return err
	}

	fmt.Fprintln(w, "── Supervisor (sub-agents as tools) ──")
	fmt.Fprintln(w, "user: Research Go generics and write an example.")
	result, err := runner.Run(context.Background(), []agent.Message{{Role: agent.RoleUser, Text: "Research Go generics and write an example."}}, func(agent.Event) {})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "supervisor → %s\n\n", result.Text)
	return nil
}

// runTeam drives the handoff half: triage transfers the conversation to a
// billing specialist, who takes it over.
func runTeam(w io.Writer) error {
	fmt.Fprintln(w, "── Team (handoff) ──")
	fmt.Fprintln(w, "user: I was double-charged and need a refund.")
	team, err := agent.NewTeam(agent.TeamConfig{
		Start: "triage",
		Members: []agent.TeamMember{
			{Name: "triage", HandoffTo: []string{"billing"}, Config: agent.RunnerConfig{
				Instructions: "You triage requests. Transfer billing questions to billing.",
				Provider: agent.NewStubProvider(
					agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "t1", Name: "transfer_to_billing", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
					agent.StubTurn{Text: "Connecting you to billing."},
				),
			}},
			{Name: "billing", Config: agent.RunnerConfig{
				Instructions: "You are billing. Resolve refund requests.",
				Provider:     agent.NewStubProvider(agent.StubTurn{Text: "I've refunded the duplicate charge — you'll see it in 3-5 days."}),
			}},
		},
		OnHandoff: func(from, to string) { fmt.Fprintf(w, "→ handed off: %s → %s\n", from, to) },
	})
	if err != nil {
		return err
	}
	result, err := team.Run(context.Background(), "I was double-charged and need a refund.", func(agent.Event) {})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "billing → %s\n", result.Text)
	return nil
}

// runScenario runs both halves into w. A non-nil provider drives the
// supervisor with a live model; the sub-agents and team stay scripted.
func runScenario(w io.Writer, provider agent.Provider) error {
	if err := runSupervisor(w, provider); err != nil {
		return err
	}
	return runTeam(w)
}
