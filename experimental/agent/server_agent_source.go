package agent

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// ServerAgentConfig bridges a decoded server-advertised agent definition (the
// experimental/ext/agents agents/get result: instructions + a scoped tool set)
// into the existing composition surface. NewServerAgentSource turns it into an
// AgentSource the parent delegates to exactly like any other sub-agent.
//
// The deliberate non-coupling: this config takes the decoded pieces
// (Instructions string, Tools []core.ToolDef) plus a Backing ToolSource, NOT
// the experimental agents.AgentDetail wire type. That keeps agent/ free of the
// experimental extension module — the host layer decodes the wire object and
// hands the parts here (agent/CONSTRAINTS.md A6: this is agent-layer because
// constructing a Runner needs a model + a turn).
type ServerAgentConfig struct {
	// Name is the delegate tool name the parent model sees and calls. It is
	// what the supervisor routes on; the scoped Tools never enter the parent's
	// context. Required.
	Name string

	// Description tells the parent model when to delegate to this agent. The
	// host folds the roster fields (description, capabilities, example tasks)
	// into it. Required.
	Description string

	// Instructions is the agent's system prompt from agents/get. It seeds the
	// child Runner, so the child behaves as the server author declared.
	Instructions string

	// Tools is the agent's scoped tool set from agents/get: the schemas the
	// child Runner advertises to its own model. These are the ONLY tools the
	// child can call — the capability boundary that keeps a server-advertised
	// agent to its declared scope, not the server's full tools/list. Their
	// names must match tools the Backing source can dispatch (the same server's
	// tools, per the WG execution model: the host loops the child's tool calls
	// back to the server).
	Tools []core.ToolDef

	// Backing executes a scoped tool call. In the host it is the ClientSource
	// for the server that advertised this agent, so a call the child makes is
	// dispatched via tools/call back to that server. Only its Call is used;
	// its own Tools list is ignored (the scoped Tools above are authoritative).
	// Required.
	Backing ToolSource

	// Provider is the LLM the child Runner uses. Required. In the host it is
	// the shared main provider, mirroring the persona sub-agents.
	Provider Provider

	// MaxSteps caps the child's model calls per turn. Zero uses the Runner
	// default.
	MaxSteps int

	// MaxDepth caps sub-agent nesting depth. Zero uses DefaultMaxAgentDepth.
	MaxDepth int

	// OnEvent, when set, receives the child's event stream wrapped in a
	// SubAgentEvent envelope for nested rendering, exactly as for a persona
	// AgentSource. Nil runs the child invisibly.
	OnEvent func(SubAgentEvent)

	// TracerProvider and MeterProvider opt the child Runner into the same
	// SEP-414 spans / OTel metrics as any other Runner. Nil means zero
	// overhead. The dedicated spans for the agents extension itself are issue
	// 1145; this just threads the existing Runner instrumentation.
	TracerProvider core.TracerProvider
	MeterProvider  core.MeterProvider

	// ResponseSchema, when set, coerces the child's final answer into
	// structured output (AgentSource.Call then returns the coerced JSON).
	// Empty keeps the plain-text answer.
	ResponseSchema core.RawJSON
}

// NewServerAgentSource builds an AgentSource from a decoded server-advertised
// agent definition: a child Runner on cfg.Provider whose ToolSource advertises
// the agent's scoped Tools and dispatches every call back through cfg.Backing
// (the advertising server), seeded with the agent's Instructions. The parent
// then delegates to the returned source like any other AgentSource — depth,
// call budget, scope, and signal plumbing all apply unchanged.
//
// Name, Provider, and Backing are required. The child is deliberately built
// over a scoped source (not cfg.Backing directly) so it can call only the
// tools the agent's definition scoped it to; a tool the server has but the
// definition did not scope is unreachable from the child.
func NewServerAgentSource(cfg ServerAgentConfig) (*AgentSource, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("agent: ServerAgentSource requires a Name")
	}
	if cfg.Provider == nil {
		return nil, fmt.Errorf("agent: ServerAgentSource %q requires a Provider", cfg.Name)
	}
	if cfg.Backing == nil {
		return nil, fmt.Errorf("agent: ServerAgentSource %q requires a Backing ToolSource", cfg.Name)
	}
	child, err := NewRunner(RunnerConfig{
		Provider:       cfg.Provider,
		Tools:          newScopedSource(cfg.Tools, cfg.Backing),
		Instructions:   cfg.Instructions,
		MaxSteps:       cfg.MaxSteps,
		TracerProvider: cfg.TracerProvider,
		MeterProvider:  cfg.MeterProvider,
		ResponseSchema: cfg.ResponseSchema,
	})
	if err != nil {
		return nil, err
	}
	return NewAgentSource(AgentSourceConfig{
		Name:        cfg.Name,
		Description: cfg.Description,
		Runner:      child,
		MaxDepth:    cfg.MaxDepth,
		OnEvent:     cfg.OnEvent,
	})
}

// scopedSource advertises a fixed set of tool schemas (an agents/get result's
// scoped Tools) and dispatches a call to a backing ToolSource ONLY when the
// name is in that set. A name outside the set is ErrUnknownTool even if the
// backing would serve it — the capability boundary that keeps a
// server-advertised agent to its declared scope rather than the server's whole
// tools/list. It is a leaf source: its Tools are authoritative (the backing's
// own Tools list is never consulted).
type scopedSource struct {
	defs    []core.ToolDef
	names   map[string]bool
	backing ToolSource
}

func newScopedSource(defs []core.ToolDef, backing ToolSource) *scopedSource {
	cp := make([]core.ToolDef, len(defs))
	copy(cp, defs)
	names := make(map[string]bool, len(defs))
	for _, d := range defs {
		names[d.Name] = true
	}
	return &scopedSource{defs: cp, names: names, backing: backing}
}

// Tools returns the scoped definitions (a snapshot copy).
func (s *scopedSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call dispatches to the backing source only for a scoped name; any other name
// is ErrUnknownTool, so the child can never reach an un-scoped server tool.
func (s *scopedSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if !s.names[name] {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	return s.backing.Call(ctx, name, args)
}
