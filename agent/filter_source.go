package agent

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// FilterSource wraps a ToolSource with a static allow/deny predicate: the
// per-profile allowlist shape. Filtered tools disappear from both listing and
// calling (a Call to a filtered name fails with ErrUnknownTool via the
// listing check), so a filter is a real capability boundary, not a
// presentation hint. For context-dependent narrowing use RunnerConfig.
// Selector instead; FilterSource is for policy that never varies by
// conversation.
type FilterSource struct {
	src  ToolSource
	keep func(core.ToolDef) bool
}

// NewFilterSource wraps src, keeping only tools for which keep returns true.
func NewFilterSource(src ToolSource, keep func(core.ToolDef) bool) *FilterSource {
	return &FilterSource{src: src, keep: keep}
}

// Tools lists the underlying source minus filtered entries.
func (f *FilterSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	defs, err := f.src.Tools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.ToolDef, 0, len(defs))
	for _, d := range defs {
		if f.keep(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

// Call dispatches to the underlying source only when the name survives the
// filter, so filtered tools cannot be invoked by guessing names.
func (f *FilterSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	defs, err := f.Tools(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		if d.Name == name {
			return f.src.Call(ctx, name, args)
		}
	}
	return nil, fmt.Errorf("%w: %q (filtered)", ErrUnknownTool, name)
}
