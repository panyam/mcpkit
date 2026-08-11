package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// FanOutResult is one member's outcome in a fan-out. Name identifies the
// member; Text is its final answer, or an error message when IsError is set.
type FanOutResult struct {
	Name    string
	Text    string
	IsError bool
}

// FanOutConfig configures a FanOutSource.
type FanOutConfig struct {
	// Name is the tool the parent calls to broadcast a task. Required.
	Name string

	// Description tells the parent model when to fan out.
	Description string

	// Members are the sub-agents the task is broadcast to. Each runs over its
	// own isolated slice (an AgentSource), so depth guard, aggregate call
	// budget, and scope threading apply per member. At least one required.
	Members []*AgentSource

	// Aggregate reduces the members' results into the single text returned to
	// the parent. Nil uses defaultFanOutAggregate (labeled sections in member
	// order). Results are always passed in member order regardless of which
	// member finished first.
	Aggregate func([]FanOutResult) string
}

// FanOutSource is a leaf ToolSource whose single tool broadcasts a task to every
// member sub-agent CONCURRENTLY and returns their results aggregated in member
// order. One tool call fans to N children in parallel and returns one combined
// result, so the parent model delegates an ensemble in a single step instead of
// emitting N calls and stitching N results itself.
//
// It reuses AgentSource wholesale: each member is an AgentSource, so the same
// depth guard, ctx-threaded aggregate call budget (WithAgentCallBudget), and
// event scope apply. A member that fails or is refused by a guard is isolated —
// its FanOutResult is marked IsError and folded into the aggregate, and the
// other members still run; the fan-out tool itself does not error. Only an
// unknown tool name is a dispatch error.
//
// Concurrency note: members run in their own goroutines, so a member's
// AgentSource.OnEvent handler may be called concurrently with its siblings' —
// wire it to a serialized sink (the host's emit is mutex-guarded).
type FanOutSource struct {
	cfg FanOutConfig
	def core.ToolDef
}

// NewFanOutSource validates cfg and builds the tool definition. Name and at
// least one member are required.
func NewFanOutSource(cfg FanOutConfig) (*FanOutSource, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("agent: FanOutSource requires a Name")
	}
	if len(cfg.Members) == 0 {
		return nil, fmt.Errorf("agent: FanOutSource %q requires at least one member", cfg.Name)
	}
	if cfg.Aggregate == nil {
		cfg.Aggregate = defaultFanOutAggregate
	}
	var schema any
	if err := json.Unmarshal(core.GenerateSchema[agentTaskArgs](), &schema); err != nil {
		return nil, fmt.Errorf("agent: schema for FanOutSource %q: %w", cfg.Name, err)
	}
	return &FanOutSource{
		cfg: cfg,
		def: core.ToolDef{Name: cfg.Name, Description: cfg.Description, InputSchema: schema},
	}, nil
}

// Tools returns the single fan-out tool.
func (s *FanOutSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	return []core.ToolDef{s.def}, nil
}

// Call broadcasts the task to every member concurrently and returns their
// aggregated results. Member failures are isolated (marked in the aggregate),
// never a dispatch error; only an unknown name errors.
func (s *FanOutSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	if name != s.cfg.Name {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("agent: encode args for fan-out %q: %w", s.cfg.Name, err)
	}
	var in agentTaskArgs
	if err := json.Unmarshal(raw, &in); err != nil || strings.TrimSpace(in.Task) == "" {
		return errorToolResult(fmt.Sprintf("fan-out %q requires a non-empty 'task'", s.cfg.Name)), nil
	}

	// One goroutine per member; each writes its own result slot (no shared-slice
	// race), so the aggregate stays in member order even though members finish
	// in any order.
	results := make([]FanOutResult, len(s.cfg.Members))
	var wg sync.WaitGroup
	for i, m := range s.cfg.Members {
		wg.Add(1)
		go func(i int, m *AgentSource) {
			defer wg.Done()
			fr := FanOutResult{Name: m.cfg.Name}
			res, err := m.Call(ctx, m.cfg.Name, args)
			switch {
			case err != nil:
				fr.IsError, fr.Text = true, err.Error()
			case res != nil:
				fr.IsError, fr.Text = res.IsError, toolResultText(res)
			}
			results[i] = fr
		}(i, m)
	}
	wg.Wait()

	return &core.ToolResult{Content: []core.Content{{Type: "text", Text: s.cfg.Aggregate(results)}}}, nil
}

// defaultFanOutAggregate renders one labeled section per member, in member
// order, marking any member that errored.
func defaultFanOutAggregate(rs []FanOutResult) string {
	var b strings.Builder
	for i, r := range rs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("[" + r.Name)
		if r.IsError {
			b.WriteString(" (error)")
		}
		b.WriteString("]\n")
		b.WriteString(r.Text)
	}
	return b.String()
}
