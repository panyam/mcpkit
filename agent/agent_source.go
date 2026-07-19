package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/panyam/mcpkit/core"
)

// DefaultMaxAgentDepth bounds how deep a sub-agent call tree may nest when an
// AgentSource is constructed without a MaxDepth — the runaway-recursion
// backstop (an agent that keeps delegating to itself).
const DefaultMaxAgentDepth = 3

// AgentSource exposes a child Runner to a parent agent as a single tool: the
// agent-as-tool pattern. Calling the tool runs the child over its OWN
// isolated conversation (a fresh slice seeded with the task) and returns the
// child's final text. Isolation is structural — a separate []Message — so the
// Runner never changes; supervision falls out for free by putting several
// AgentSources in a MultiSource (the existing aggregation, collision, and
// Selector routing all apply).
//
// It implements ToolSource, so it drops into a RunnerConfig.Tools (directly or
// via MultiSource) like any other source. A6: it is model-facing (a tool the
// parent model calls), so it lives in agent/.
type AgentSource struct {
	cfg      AgentSourceConfig
	def      core.ToolDef
	maxDepth int
}

// AgentSourceConfig configures an AgentSource.
type AgentSourceConfig struct {
	// Name is the tool name the parent model sees and calls. Required.
	Name string

	// Description tells the parent model when to delegate to this sub-agent.
	// Required.
	Description string

	// Runner is the child agent. Required. One Runner instance serves
	// concurrent calls — Run is stateless over the history it is handed, so
	// each call gets its own isolated slice without a per-call Runner.
	Runner *Runner

	// MaxDepth caps sub-agent nesting depth (this source's calls plus any
	// deeper sub-agent calls the child makes). Zero uses DefaultMaxAgentDepth.
	MaxDepth int

	// OnEvent, when set, receives the child's event stream wrapped in a
	// SubAgentEvent envelope (scope + depth + the flat Event), so a surface
	// can render the sub-agent's turn nested under the parent's. Nil drops
	// the child's events (the sub-agent runs invisibly). Wire OnEvent on
	// every AgentSource in a tree to the same sink and the scope/depth
	// disambiguate nested runs.
	OnEvent func(SubAgentEvent)
}

// SubAgentEvent is the envelope that carries a sub-agent's event to the
// parent surface. The scope and depth live on the envelope, NOT on Event, so
// Event stays flat and wire-serializable (constraint A2): a surface adds
// sub-agent framing without the core event vocabulary growing a nesting
// field.
type SubAgentEvent struct {
	// Scope is the slash-joined sub-agent path, e.g. "researcher" at the top
	// level or "researcher/summarizer" one level deeper.
	Scope string
	// Depth is the nesting depth: 1 for a top-level sub-agent, 2 for a
	// sub-agent it spawns, and so on.
	Depth int
	// Event is the child's event, unchanged.
	Event Event
}

type agentTaskArgs struct {
	// Task is the instruction handed to the sub-agent as its user turn.
	Task string `json:"task"`
}

// NewAgentSource validates cfg and builds the tool definition. Name and
// Runner are required.
func NewAgentSource(cfg AgentSourceConfig) (*AgentSource, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("agent: AgentSource requires a Name")
	}
	if cfg.Runner == nil {
		return nil, fmt.Errorf("agent: AgentSource %q requires a Runner", cfg.Name)
	}
	var schema any
	if err := json.Unmarshal(core.GenerateSchema[agentTaskArgs](), &schema); err != nil {
		return nil, fmt.Errorf("agent: schema for AgentSource %q: %w", cfg.Name, err)
	}
	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = DefaultMaxAgentDepth
	}
	return &AgentSource{
		cfg:      cfg,
		def:      core.ToolDef{Name: cfg.Name, Description: cfg.Description, InputSchema: schema},
		maxDepth: maxDepth,
	}, nil
}

// Tools returns the single sub-agent tool.
func (s *AgentSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{s.def}, nil
}

// Call runs the child over an isolated conversation seeded with the task and
// returns its final text. A child that errors or is refused by a guard
// surfaces as an IsError result (fed back to the parent model, which can
// react), not a dispatch error — only an unknown name is a dispatch error, so
// the parent's turn never aborts on a sub-agent problem.
func (s *AgentSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name != s.cfg.Name {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}

	// Depth guard: refuse before running when the tree is already too deep.
	depth := agentDepth(ctx)
	if depth >= s.maxDepth {
		return errorToolResult(fmt.Sprintf("sub-agent %q refused: max depth %d reached", s.cfg.Name, s.maxDepth)), nil
	}
	// Aggregate call budget (optional, ctx-threaded, shared across the whole
	// tree): bounds total sub-agent invocations regardless of shape.
	if b := agentCallBudget(ctx); b != nil && b.Add(-1) < 0 {
		return errorToolResult(fmt.Sprintf("sub-agent %q refused: call budget exhausted", s.cfg.Name)), nil
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("agent: encode args for sub-agent %q: %w", s.cfg.Name, err)
	}
	var in agentTaskArgs
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Task) == "" {
		return errorToolResult(fmt.Sprintf("sub-agent %q requires a non-empty 'task'", s.cfg.Name)), nil
	}

	// Extend both ctx threads for the child: depth (guards) and scope (the
	// path this sub-agent's events are tagged with).
	childScope := s.cfg.Name
	if parent := agentScope(ctx); parent != "" {
		childScope = parent + "/" + s.cfg.Name
	}
	childCtx := withAgentScope(withAgentDepth(ctx, depth+1), childScope)

	emit := func(Event) {}
	if s.cfg.OnEvent != nil {
		childDepth := depth + 1
		emit = func(e Event) { s.cfg.OnEvent(SubAgentEvent{Scope: childScope, Depth: childDepth, Event: e}) }
	}
	result, err := s.cfg.Runner.Run(childCtx, []Message{{Role: RoleUser, Text: in.Task}}, emit)
	if err != nil {
		return errorToolResult(fmt.Sprintf("sub-agent %q failed: %v", s.cfg.Name, err)), nil
	}
	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: result.Text}}}, nil
}

type agentDepthKey struct{}
type agentBudgetKey struct{}
type agentScopeKey struct{}

// withAgentScope stamps the slash-joined sub-agent path on ctx so a nested
// AgentSource can prefix its own name and tag its child's events with the
// full path.
func withAgentScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, agentScopeKey{}, scope)
}

// agentScope reads the current sub-agent path ("" at the top level).
func agentScope(ctx context.Context) string {
	s, _ := ctx.Value(agentScopeKey{}).(string)
	return s
}

// withAgentDepth stamps the current sub-agent nesting depth on ctx; each
// AgentSource increments it before running its child.
func withAgentDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, agentDepthKey{}, d)
}

// agentDepth reads the current nesting depth (0 at the top level).
func agentDepth(ctx context.Context) int {
	d, _ := ctx.Value(agentDepthKey{}).(int)
	return d
}

// WithAgentCallBudget caps the TOTAL number of sub-agent invocations allowed
// under ctx, shared across the whole call tree (a shape-independent cost
// guard, complementary to the per-source depth cap). Each AgentSource call
// consumes one; when the budget is exhausted, further calls are refused with
// an IsError result. Absent, only the depth cap applies.
func WithAgentCallBudget(ctx context.Context, n int) context.Context {
	var b atomic.Int64
	b.Store(int64(n))
	return context.WithValue(ctx, agentBudgetKey{}, &b)
}

func agentCallBudget(ctx context.Context) *atomic.Int64 {
	b, _ := ctx.Value(agentBudgetKey{}).(*atomic.Int64)
	return b
}
