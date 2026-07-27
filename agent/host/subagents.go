package host

import (
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

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
		src, err := a.buildPersonaSource(sub, serverTools, provider, tp, mp)
		if err != nil {
			return err
		}
		if err := multi.Add("subagent:"+sub.Name, src); err != nil {
			return err
		}
	}
	return nil
}

// buildPersonaSource constructs one persona as an agent.AgentSource: a child
// Runner on the SHARED provider over a serverTools-only view (Allow-narrowed),
// with its own instructions, forwarding its events as HostSubAgentEvent. Shared
// by registerSubAgents and registerFanOut so the two build personas identically
// — including the serverTools-only rule that keeps a child memory-free (A7).
func (a *App) buildPersonaSource(sub SubAgentConfig, serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) (*agent.AgentSource, error) {
	var tools agent.ToolSource = serverTools
	if len(sub.Allow) > 0 {
		allow := make(map[string]bool, len(sub.Allow))
		for _, name := range sub.Allow {
			allow[name] = true
		}
		tools = agent.NewFilterSource(serverTools, func(d core.ToolDef) bool { return allow[d.Name] })
	}

	child, err := agent.NewRunner(agent.RunnerConfig{
		Provider:       provider,
		Tools:          tools,
		Instructions:   sub.Instructions,
		MaxSteps:       a.cfg.MaxSteps,
		TracerProvider: tp,
		MeterProvider:  mp,
	})
	if err != nil {
		return nil, err
	}

	return agent.NewAgentSource(agent.AgentSourceConfig{
		Name:        sub.Name,
		Description: sub.Description,
		Runner:      child,
		MaxDepth:    sub.MaxDepth,
		OnEvent:     func(e agent.SubAgentEvent) { a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e}) },
	})
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
		// The tool name (group.Name) may be snake_case; the MultiSource source
		// id may not contain underscores, so sanitize the id only.
		id := "fanout:" + strings.ReplaceAll(group.Name, "_", "-")
		if err := multi.Add(id, fo); err != nil {
			return err
		}
	}
	return nil
}
