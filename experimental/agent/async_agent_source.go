package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// AsyncAgentSourceConfig configures an AsyncAgentSource.
type AsyncAgentSourceConfig struct {
	// Name is the tool the parent model calls to spawn the sub-agent. Required.
	Name string

	// Description tells the parent when to spawn it (and that it returns later).
	Description string

	// Runner is the child agent. Required. Run is stateless over the history it
	// is handed, so one Runner serves concurrent spawns.
	Runner *Runner

	// MaxDepth caps sub-agent nesting (checked at spawn time). Zero uses
	// DefaultMaxAgentDepth.
	MaxDepth int

	// InputSchema, when set, replaces the default {task} schema so a parent
	// spawns a TYPED subtask; the child is seeded with the raw args JSON. Nil
	// keeps {task: string} (mirrors AgentSourceConfig.InputSchema).
	InputSchema json.RawMessage

	// OnEvent, when set, receives the child's event stream (scoped/depth in a
	// SubAgentEvent) WHILE it runs in the background, so a surface can still
	// render the sub-agent's activity after the spawning turn has ended. Nil
	// drops it.
	OnEvent func(SubAgentEvent)

	// OnComplete is called on the child's goroutine when it finishes, with the
	// sub-agent name, its result (nil on error), and any run error. Required —
	// it is how the result rejoins the conversation: a host wires it to its
	// injection seam (Ingest as a subagent.completed event), so the parent picks
	// the result up on a later turn. Without it the result is dropped.
	OnComplete func(name string, result *TurnResult, err error)
}

// AsyncAgentSource is the Task form of a sub-agent: the spawn-and-continue
// counterpart to AgentSource's blocking Tool form. Call returns an ack
// IMMEDIATELY ("sub-agent X started") and runs the child on a detached
// goroutine; when the child finishes, OnComplete delivers its result, which a
// host injects so the parent picks it up on a later turn. The spawning turn does
// not wait for the subtree — right for long-running or fan-out-and-continue work
// (contrast AgentSource: call-and-block, answer this turn).
//
// It is NOT an MCP task: there is no wire presence, no model-visible poll/cancel,
// and the goroutine is ephemeral (it dies with the process). For controllable or
// restart-surviving background work, use a real server-side task instead.
//
// Depth and the ctx-threaded aggregate call budget still apply (checked at spawn
// time, before the goroutine starts). SubAgentEvent nesting still surfaces the
// child's stream while it runs in the background.
type AsyncAgentSource struct {
	cfg      AsyncAgentSourceConfig
	def      core.ToolDef
	maxDepth int
}

// NewAsyncAgentSource validates cfg and builds the tool definition. Name,
// Runner, and OnComplete are required.
func NewAsyncAgentSource(cfg AsyncAgentSourceConfig) (*AsyncAgentSource, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("agent: AsyncAgentSource requires a Name")
	}
	if cfg.Runner == nil {
		return nil, fmt.Errorf("agent: AsyncAgentSource %q requires a Runner", cfg.Name)
	}
	if cfg.OnComplete == nil {
		return nil, fmt.Errorf("agent: AsyncAgentSource %q requires an OnComplete (else the result is dropped)", cfg.Name)
	}
	schemaJSON := cfg.InputSchema
	if schemaJSON == nil {
		schemaJSON = core.GenerateSchema[agentTaskArgs]()
	}
	var schema any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return nil, fmt.Errorf("agent: schema for AsyncAgentSource %q: %w", cfg.Name, err)
	}
	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxAgentDepth
	}
	return &AsyncAgentSource{
		cfg:      cfg,
		def:      core.ToolDef{Name: cfg.Name, Description: cfg.Description, InputSchema: schema},
		maxDepth: maxDepth,
	}, nil
}

// Tools returns the single spawn tool.
func (s *AsyncAgentSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{s.def}, nil
}

// Call spawns the child on a detached goroutine and returns an ack immediately.
// Guards (depth, budget) are checked before the spawn and refuse as an IsError
// result; only an unknown name is a dispatch error.
func (s *AsyncAgentSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name != s.cfg.Name {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	depth := agentDepth(ctx)
	if depth >= s.maxDepth {
		return errorToolResult(fmt.Sprintf("sub-agent %q refused: max depth %d reached", s.cfg.Name, s.maxDepth)), nil
	}
	if b := agentCallBudget(ctx); b != nil && b.Add(-1) < 0 {
		return errorToolResult(fmt.Sprintf("sub-agent %q refused: call budget exhausted", s.cfg.Name)), nil
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("agent: encode args for sub-agent %q: %w", s.cfg.Name, err)
	}
	seed := string(raw)
	if s.cfg.InputSchema == nil {
		var in agentTaskArgs
		if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Task) == "" {
			return errorToolResult(fmt.Sprintf("sub-agent %q requires a non-empty 'task'", s.cfg.Name)), nil
		}
		seed = in.Task
	}

	childScope := agentScope(ctx).Child(s.cfg.Name)
	// Detach for background: the child outlives the spawning turn AND calls MCP
	// server tools, so it needs the session-level push (DetachForBackground),
	// not a plain context.WithoutCancel. Depth/scope are threaded onto the
	// detached ctx so nested spawns and event tagging still compose.
	childCtx := withAgentScope(withAgentDepth(core.DetachForBackground(ctx), depth+1), childScope)

	emit := func(Event) {}
	if s.cfg.OnEvent != nil {
		childDepth := depth + 1
		emit = func(e Event) { s.cfg.OnEvent(SubAgentEvent{Scope: childScope.String(), Depth: childDepth, Event: e}) }
	}

	go func() {
		result, err := s.cfg.Runner.Run(childCtx, []Message{{Role: RoleUser, Text: seed}}, emit)
		s.cfg.OnComplete(s.cfg.Name, result, err)
	}()

	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: fmt.Sprintf(
		"sub-agent %q started in the background; its result will arrive as context when it finishes.", s.cfg.Name)}}}, nil
}
