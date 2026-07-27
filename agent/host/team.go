package host

import (
	"fmt"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// buildTeam assembles the handoff Team (Config.Team) from persona members over
// the shared provider + serverTools, wiring OnHandoff to a HostHandoff event so
// a surface can render the control transfer. Each member is built like a
// sub-agent (serverTools-only, memory-free per A7) with its handoff tools merged
// in by agent.NewTeam. The returned Team drives RunTurn instead of the single
// Runner; activeTeamAgent is seeded to Start.
func (a *App) buildTeam(serverTools *agent.MultiSource, provider agent.Provider, tp core.TracerProvider, mp core.MeterProvider) (*agent.Team, error) {
	tc := a.cfg.Team
	members := make([]agent.TeamMember, 0, len(tc.Members))
	for _, m := range tc.Members {
		members = append(members, agent.TeamMember{
			Name:      m.Name,
			Config:    a.personaRunnerConfig(m.SubAgentConfig, serverTools, provider, tp, mp),
			HandoffTo: m.HandoffTo,
		})
	}
	return agent.NewTeam(agent.TeamConfig{
		Members:     members,
		Start:       tc.Start,
		MaxHandoffs: tc.MaxHandoffs,
		OnHandoff:   func(from, to string) { a.emit(HostEvent{Kind: HostHandoff, From: from, To: to}) },
		// Each member's events render attributed by agent name (HostSubAgentEvent)
		// and are buffered for persistence via the per-turn sink, so the event log
		// is unchanged while the surface can tell which agent is speaking.
		OnEvent: func(e agent.SubAgentEvent) {
			if a.teamEventSink != nil {
				a.teamEventSink(e.Event)
			}
			a.emit(HostEvent{Kind: HostSubAgentEvent, SubAgent: e})
		},
	})
}

// validateTeamExclusive rejects a Team combined with the single-agent-only
// features. Team replaces the main agent's loop, so its per-runner facilities
// (memory, sub-agents, fan-out) have no single agent to attach to; integrating
// them into team members is a deferred follow-up. Fail loud rather than
// silently ignore a configured feature.
func validateTeamExclusive(cfg *Config) error {
	if cfg.Team == nil {
		return nil
	}
	var conflicts []string
	if len(cfg.SubAgents) > 0 {
		conflicts = append(conflicts, "subAgents")
	}
	if len(cfg.FanOut) > 0 {
		conflicts = append(conflicts, "fanOut")
	}
	if cfg.Memory != nil {
		conflicts = append(conflicts, "memory")
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("host: team mode cannot be combined with %v (team replaces the single main agent; those attach to it)", conflicts)
	}
	if len(cfg.Team.Members) == 0 {
		return fmt.Errorf("host: team requires at least one member")
	}
	return nil
}
