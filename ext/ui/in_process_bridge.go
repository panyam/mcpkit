package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/panyam/mcpkit/core"
)

// InProcessAppBridge implements AppBridge for testing by dispatching to
// registered Go handler functions. No iframe, no postMessage — tool
// registration and dispatch happen entirely in-process.
type InProcessAppBridge struct {
	mu    sync.RWMutex
	tools map[string]*inProcessTool

	started bool
	closed  bool

	// Handlers set by AppHost via SetRequestHandler / SetNotificationHandler.
	reqHandler    func(ctx context.Context, req *core.Request) *core.Response
	notifyHandler func(method string, params json.RawMessage)
}

type inProcessTool struct {
	def     core.ToolDef
	handler func(args map[string]any) (any, error)
}

// NewInProcessAppBridge creates a bridge for testing app-provided tools
// without an iframe or postMessage transport.
func NewInProcessAppBridge() *InProcessAppBridge {
	return &InProcessAppBridge{
		tools: make(map[string]*inProcessTool),
	}
}

// RegisterTool simulates app-side tool registration (the Go equivalent of
// MCPApp.registerTool in the bridge JS). It adds a tool and fires a
// notifications/tools/list_changed notification to the host.
func (b *InProcessAppBridge) RegisterTool(name string, def core.ToolDef, handler func(args map[string]any) (any, error)) {
	b.mu.Lock()
	def.Name = name
	b.tools[name] = &inProcessTool{def: def, handler: handler}
	b.mu.Unlock()

	b.sendToolListChanged()
}

// RemoveTool unregisters a tool and fires notifications/tools/list_changed.
func (b *InProcessAppBridge) RemoveTool(name string) {
	b.mu.Lock()
	delete(b.tools, name)
	b.mu.Unlock()

	b.sendToolListChanged()
}

func (b *InProcessAppBridge) sendToolListChanged() {
	b.mu.RLock()
	handler := b.notifyHandler
	b.mu.RUnlock()

	if handler != nil {
		handler("notifications/tools/list_changed", nil)
	}
}

// Send implements AppBridge. It dispatches tools/list and tools/call to the
// internal tool registry, mirroring handleHostRequest in mcp-app-bridge.ts.
func (b *InProcessAppBridge) Send(ctx context.Context, req *core.Request) (*core.Response, error) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return nil, fmt.Errorf("bridge is closed")
	}
	b.mu.RUnlock()

	switch req.Method {
	case "tools/list":
		return b.handleToolsList(req)
	case "tools/call":
		return b.handleToolsCall(req)
	default:
		return &core.Response{
			ID:    req.ID,
			Error: &core.Error{Code: core.ErrCodeMethodNotFound, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}, nil
	}
}

func (b *InProcessAppBridge) handleToolsList(req *core.Request) (*core.Response, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	tools := make([]core.ToolDef, 0, len(b.tools))
	for _, t := range b.tools {
		tools = append(tools, t.def)
	}

	result, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return nil, fmt.Errorf("marshal tools list: %w", err)
	}
	return &core.Response{ID: req.ID, Result: result}, nil
}

func (b *InProcessAppBridge) handleToolsCall(req *core.Request) (*core.Response, error) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &core.Response{
			ID:    req.ID,
			Error: &core.Error{Code: core.ErrCodeInvalidParams, Message: fmt.Sprintf("invalid params: %v", err)},
		}, nil
	}

	b.mu.RLock()
	tool, ok := b.tools[params.Name]
	b.mu.RUnlock()

	if !ok {
		return &core.Response{
			ID:    req.ID,
			Error: &core.Error{Code: core.ErrCodeInvalidParams, Message: fmt.Sprintf("unknown tool: %s", params.Name)},
		}, nil
	}

	handlerResult, err := tool.handler(params.Arguments)
	if err != nil {
		// Tool handler error → JSON-RPC success with isError: true (matches MCP semantics).
		result, _ := json.Marshal(core.ToolResult{
			Content: []core.Content{{Type: "text", Text: err.Error()}},
			IsError: true,
		})
		return &core.Response{ID: req.ID, Result: result}, nil
	}

	result, merr := json.Marshal(handlerResult)
	if merr != nil {
		return nil, fmt.Errorf("marshal tool result: %w", merr)
	}
	return &core.Response{ID: req.ID, Result: result}, nil
}

// SendToHost simulates an app→host request (e.g., MCPApp.callTool() calling
// a server-side tool). The request is forwarded to the handler set by AppHost.
func (b *InProcessAppBridge) SendToHost(ctx context.Context, method string, params any) (*core.Response, error) {
	b.mu.RLock()
	handler := b.reqHandler
	b.mu.RUnlock()

	if handler == nil {
		return nil, fmt.Errorf("no request handler set on bridge")
	}

	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	resp := handler(ctx, &core.Request{
		Method: method,
		Params: raw,
	})
	return resp, nil
}

// SetRequestHandler implements AppBridge.
func (b *InProcessAppBridge) SetRequestHandler(fn func(ctx context.Context, req *core.Request) *core.Response) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reqHandler = fn
}

// SetNotificationHandler implements AppBridge.
func (b *InProcessAppBridge) SetNotificationHandler(fn func(method string, params json.RawMessage)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifyHandler = fn
}

// Start implements AppBridge. For in-process bridges this is a no-op.
func (b *InProcessAppBridge) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.started = true
	return nil
}

// Close implements AppBridge.
func (b *InProcessAppBridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}
