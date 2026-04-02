package mcpkit

import (
	"context"
	"encoding/json"
	"fmt"
)

// Dispatcher routes JSON-RPC requests to the appropriate handler.
type Dispatcher struct {
	tools      map[string]toolEntry
	toolOrder  []string // preserves registration order for tools/list
	serverInfo ServerInfo
	// TODO: resources, prompts registries
}

type toolEntry struct {
	def     ToolDef
	handler ToolHandler
}

// ServerInfo identifies this MCP server in the initialize response.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// NewDispatcher creates a dispatcher with the given server identity.
func NewDispatcher(info ServerInfo) *Dispatcher {
	return &Dispatcher{
		tools:      make(map[string]toolEntry),
		serverInfo: info,
	}
}

// RegisterTool adds a tool to the dispatcher.
func (d *Dispatcher) RegisterTool(def ToolDef, handler ToolHandler) {
	d.tools[def.Name] = toolEntry{def: def, handler: handler}
	d.toolOrder = append(d.toolOrder, def.Name)
}

// Dispatch routes a JSON-RPC request and returns the response.
// Returns nil for notifications (no response expected).
func (d *Dispatcher) Dispatch(ctx context.Context, req *Request) *Response {
	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}

	switch req.Method {
	case "initialize":
		return d.handleInitialize(id)

	case "notifications/initialized", "initialized":
		return nil

	case "ping":
		return NewResponse(id, map[string]any{})

	case "tools/list":
		return d.handleToolsList(id)

	case "tools/call":
		return d.handleToolsCall(ctx, id, req.Params)

	default:
		return NewErrorResponse(id, ErrCodeMethodNotFound, "method not found: "+req.Method)
	}
}

func (d *Dispatcher) handleInitialize(id json.RawMessage) *Response {
	caps := map[string]any{
		"tools": map[string]any{},
	}
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    caps,
		"serverInfo":      d.serverInfo,
	}
	return NewResponse(id, result)
}

func (d *Dispatcher) handleToolsList(id json.RawMessage) *Response {
	tools := make([]ToolDef, 0, len(d.toolOrder))
	for _, name := range d.toolOrder {
		if entry, ok := d.tools[name]; ok {
			tools = append(tools, entry.def)
		}
	}
	return NewResponse(id, map[string]any{"tools": tools})
}

func (d *Dispatcher) handleToolsCall(ctx context.Context, id json.RawMessage, params json.RawMessage) *Response {
	var envelope struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, err.Error())
	}

	entry, ok := d.tools[envelope.Name]
	if !ok {
		return NewErrorResponse(id, ErrCodeInvalidParams, "unknown tool: "+envelope.Name)
	}

	req := ToolRequest{
		Name:      envelope.Name,
		Arguments: envelope.Arguments,
		RequestID: id,
	}

	result, err := entry.handler(ctx, req)
	if err != nil {
		return NewErrorResponse(id, ErrCodeInternal, fmt.Sprintf("tool %q: %v", envelope.Name, err))
	}

	return NewResponse(id, result)
}
