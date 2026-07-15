package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// FuncSource serves host-local Go functions as tools, so an agent can carry
// small utilities (dates, math, environment lookups) without running an MCP
// server for them. Registration is typed via AddFunc; schemas come from
// core.GenerateSchema on the input struct.
type FuncSource struct {
	mu       sync.RWMutex
	defs     []core.ToolDef
	handlers map[string]func(ctx context.Context, args map[string]any) (*core.ToolResult, error)
}

// NewFuncSource returns an empty source; register tools with AddFunc or
// AddToolFunc before handing it to a Runner.
func NewFuncSource() *FuncSource {
	return &FuncSource{
		handlers: make(map[string]func(ctx context.Context, args map[string]any) (*core.ToolResult, error)),
	}
}

// AddFunc registers a typed function as a tool. Arguments are decoded into
// In via JSON round-trip and validated only by that decoding; the returned
// string becomes a single text content item. A handler error becomes an
// IsError tool result (the model sees the failure), not a dispatch error.
// Registering a duplicate name returns an error.
func AddFunc[In any](s *FuncSource, name, description string, fn func(ctx context.Context, in In) (string, error)) error {
	var schema any
	if err := json.Unmarshal(core.GenerateSchema[In](), &schema); err != nil {
		return fmt.Errorf("agent: schema generation for %q: %w", name, err)
	}
	return s.AddToolFunc(core.ToolDef{Name: name, Description: description, InputSchema: schema},
		func(ctx context.Context, args map[string]any) (*core.ToolResult, error) {
			var in In
			raw, err := json.Marshal(args)
			if err != nil {
				return nil, fmt.Errorf("agent: encode args for %q: %w", name, err)
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return errorToolResult(fmt.Sprintf("invalid arguments: %v", err)), nil
			}
			out, err := fn(ctx, in)
			if err != nil {
				return errorToolResult(err.Error()), nil
			}
			return &core.ToolResult{Content: []core.Content{{Type: "text", Text: out}}}, nil
		})
}

// AddToolFunc registers a tool with full control over the definition and the
// result shape. Use this when the tool needs structured content, a custom
// output schema, or non-text content items.
func (s *FuncSource) AddToolFunc(def core.ToolDef, fn func(ctx context.Context, args map[string]any) (*core.ToolResult, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.handlers[def.Name]; exists {
		return fmt.Errorf("agent: tool %q already registered", def.Name)
	}
	s.defs = append(s.defs, def)
	s.handlers[def.Name] = fn
	return nil
}

// Tools returns the registered definitions in registration order.
func (s *FuncSource) Tools(ctx context.Context) ([]core.ToolDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]core.ToolDef, len(s.defs))
	copy(out, s.defs)
	return out, nil
}

// Call dispatches to the registered handler. Unknown names are dispatch
// errors; handler failures registered via AddFunc surface as IsError results.
func (s *FuncSource) Call(ctx context.Context, name string, args map[string]any) (*core.ToolResult, error) {
	s.mu.RLock()
	fn, ok := s.handlers[name]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	return fn(ctx, args)
}

func errorToolResult(msg string) *core.ToolResult {
	return &core.ToolResult{
		IsError: true,
		Content: []core.Content{{Type: "text", Text: msg}},
	}
}
