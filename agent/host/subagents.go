package host

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// signalPolicyFor maps a Config.SignalPolicy string to an agent.SignalPolicy:
// "abort_on_escalate" -> agent.AbortOnEscalate; "" / "inject" / anything else
// -> nil (inject-and-continue, the default).
func signalPolicyFor(name string) agent.SignalPolicy {
	if name == "abort_on_escalate" {
		return agent.AbortOnEscalate
	}
	return nil
}

// registerSubAgents builds each configured persona as an agent.AgentSource and
// adds it to the aggregate, so the main agent delegates to it as a tool. Each
// persona runs a child Runner on the SHARED provider over a FilterSource-
// narrowed view of serverTools (server tools only — never the meta-tools or a
// sibling persona), with its own instructions. Its event stream is forwarded
// to the host observers as a HostSubAgentEvent for nested rendering.
//
// Personas are built over serverTools, NOT the main aggregate `multi`: a child
// deliberately gets no working memory (the MemorySource lives on `multi`) and
// no ambient parent state. This is the issue 1151 decision — a sub-agent's
// location is not guaranteed, so shared parent memory would assume a
// co-location that A2 wire-serializability forbids; parent-to-child is params +
// injection only. A child that needs memory owns its own (its Runner's config),
// never a WithMemoryNamespaceFunc(a.currentRunID) into the parent's namespace.
// Guarded by TestSubAgentCannotReachParentMemory.
func (a *App) registerSubAgents(multi, serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) error {
	for _, sub := range a.cfg.SubAgents {
		var src agent.ToolSource
		var err error
		if sub.Async {
			src, err = a.buildAsyncPersonaSource(sub, serverTools, provider, tp, mp)
		} else {
			src, err = a.buildPersonaSource(sub, serverTools, provider, tp, mp)
		}
		if err != nil {
			return err
		}
		if err := multi.Add("subagent:"+sub.Name, src); err != nil {
			return err
		}
	}
	return nil
}

// buildAsyncPersonaSource builds an async (Task-form) persona: an
// agent.AsyncAgentSource whose delegate tool acks immediately and runs the child
// in the background, its result injected on a later turn via onAsyncComplete.
// The child is a persona like any other (serverTools-only, memory-free per A7).
func (a *App) buildAsyncPersonaSource(sub SubAgentConfig, serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) (*agent.AsyncAgentSource, error) {
	child, err := agent.NewRunner(a.personaRunnerConfig(sub, serverTools, provider, tp, mp))
	if err != nil {
		return nil, err
	}
	return agent.NewAsyncAgentSource(agent.AsyncAgentSourceConfig{
		Name:        sub.Name,
		Description: sub.Description,
		Runner:      child,
		MaxDepth:    sub.MaxDepth,
		InputSchema: sub.InputSchema,
		OnEvent:     func(e agent.SubAgentEvent) { a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e}) },
		OnComplete:  a.onAsyncComplete,
	})
}

// onAsyncComplete delivers a finished async sub-agent's result back into the
// conversation, mirroring the background-task completion path (tasks_bg.go):
// build a subagent.completed event, Ingest it so it is injected on the next turn
// (or a trigger fires a proactive turn), and notify the surface. Runs on the
// child's goroutine — injection.Ingest, emit, and runProactiveTurn are each
// safe to call from there.
func (a *App) onAsyncComplete(name string, result *agent.TurnResult, err error) {
	payload := map[string]any{"subAgent": name, "status": "completed"}
	switch {
	case err != nil:
		payload["status"] = "failed"
		payload["error"] = err.Error()
	case result != nil:
		payload["result"] = result.Text
	}
	raw, _ := json.Marshal(payload)
	ev := agent.IncomingEvent{
		Server: name,
		Name:   "subagent.completed",
		ID:     name,
		Time:   time.Now(),
		Data:   core.NewRawJSON(raw),
	}
	a.injection.Ingest(ev)
	a.emit(HostEvent{Kind: HostMessage, Message: fmt.Sprintf("sub-agent %q finished; its result will be in context on your next turn", name)})
	if firing := a.triggers.OnEvent(ev); firing != nil {
		a.runProactiveTurn(context.Background(), firing)
	}
}

// buildPersonaSource constructs one persona as an agent.AgentSource: a child
// Runner on the SHARED provider over a serverTools-only view (Allow-narrowed),
// with its own instructions, forwarding its events as HostSubAgentEvent. Shared
// by registerSubAgents and registerFanOut so the two build personas identically
// — including the serverTools-only rule that keeps a child memory-free (A7).
func (a *App) buildPersonaSource(sub SubAgentConfig, serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) (*agent.AgentSource, error) {
	child, err := agent.NewRunner(a.personaRunnerConfig(sub, serverTools, provider, tp, mp))
	if err != nil {
		return nil, err
	}

	return agent.NewAgentSource(agent.AgentSourceConfig{
		Name:        sub.Name,
		Description: sub.Description,
		Runner:      child,
		MaxDepth:    sub.MaxDepth,
		InputSchema: sub.InputSchema,
		OnEvent:     func(e agent.SubAgentEvent) { a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e}) },
	})
}

// personaRunnerConfig builds the RunnerConfig shared by every persona shape
// (sub-agent, fan-out member, team member): the child runs on the SHARED
// provider over a serverTools-only view (Allow-narrowed), with its own
// instructions — the serverTools-only rule keeping a child memory-free (A7).
// Team needs the RunnerConfig (it builds the Runner itself, merging handoff
// tools); the others wrap it in an AgentSource.
func (a *App) personaRunnerConfig(sub SubAgentConfig, serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) agent.RunnerConfig {
	var tools agent.ToolSource = serverTools
	if len(sub.Allow) > 0 {
		allow := make(map[string]bool, len(sub.Allow))
		for _, name := range sub.Allow {
			allow[name] = true
		}
		tools = agent.NewFilterSource(serverTools, func(d core.ToolDef) bool { return allow[d.Name] })
	}
	// A signalling persona gets the signal_parent control tool alongside its
	// (filtered) server tools. It is a control channel, not ambient parent state,
	// so it does not violate A7 (memory-free personas).
	if sub.CanSignal && !sub.Async {
		agg := agent.NewMultiSource()
		_ = agg.Add("persona-tools", tools)
		_ = agg.Add("signal", agent.NewSignalSource())
		tools = agg
	}
	return agent.RunnerConfig{
		Provider:       provider,
		Tools:          tools,
		Instructions:   sub.Instructions,
		MaxSteps:       a.cfg.MaxSteps,
		TracerProvider: tp,
		MeterProvider:  mp,
		ResponseSchema: core.NewRawJSON(sub.ResponseSchema),
	}
}

// registerFanOut builds each configured fan-out group as an agent.FanOutSource
// over persona members (built like sub-agents, so serverTools-only / memory-free
// per A7) and adds it to the aggregate as a single tool. The main agent calls it
// once to broadcast a task to every member concurrently and get one aggregated
// result. Members are NOT also exposed as individual delegate tools — the group
// is self-contained.
func (a *App) registerFanOut(multi, serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) error {
	for _, group := range a.cfg.FanOut {
		members := make([]*agent.AgentSource, 0, len(group.Members))
		for _, sub := range group.Members {
			src, err := a.buildPersonaSource(sub, serverTools, provider, tp, mp)
			if err != nil {
				return err
			}
			members = append(members, src)
		}
		fo, err := agent.NewFanOutSource(agent.FanOutConfig{
			Name:        group.Name,
			Description: group.Description,
			Members:     members,
		})
		if err != nil {
			return err
		}
		if err := multi.Add("fanout:"+group.Name, fo); err != nil {
			return err
		}
	}
	return nil
}
