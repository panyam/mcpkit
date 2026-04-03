package mcpkit

import (
	"context"
	"encoding/json"
	"fmt"
)

// supportedProtocolVersions lists the MCP protocol versions this server supports,
// ordered newest-first. During initialization the server picks the client's requested
// version if it appears in this list; otherwise it rejects with the full list.
var supportedProtocolVersions = []string{"2025-11-25", "2024-11-05"}

// Dispatcher routes JSON-RPC requests to the appropriate handler.
type Dispatcher struct {
	tools      map[string]toolEntry
	toolOrder  []string // preserves registration order for tools/list
	serverInfo ServerInfo

	// Session state set during initialization handshake.
	negotiatedVersion string             // set by initialize
	clientCaps        ClientCapabilities // set by initialize
	clientInfo        ClientInfo         // set by initialize
	initialized       bool               // set to true by notifications/initialized
	// TODO: resources, prompts registries
}

type toolEntry struct {
	def     ToolDef
	handler ToolHandler
}

// ServerInfo identifies this MCP server in the initialize response.
type ServerInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	WebsiteURL   string `json:"websiteUrl,omitempty"`
}

// ClientInfo identifies the MCP client from the initialize request.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities describes features the client supports.
// These are used for server-to-client requests (sampling, elicitation, roots).
type ClientCapabilities struct {
	Sampling    *struct{} `json:"sampling,omitempty"`
	Roots       *RootsCap `json:"roots,omitempty"`
	Elicitation *struct{} `json:"elicitation,omitempty"`
}

// RootsCap describes the client's roots capability.
type RootsCap struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// initializeParams is the params object sent by the client in an initialize request.
type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// NewDispatcher creates a dispatcher with the given server identity.
func NewDispatcher(info ServerInfo) *Dispatcher {
	return &Dispatcher{
		tools:      make(map[string]toolEntry),
		serverInfo: info,
	}
}

// newSession creates a new Dispatcher that shares the tool registry from d
// but has fresh session state (not initialized, no client info).
// The tool registry is shared by reference — safe because tools are registered
// before serving begins and never modified after.
func (d *Dispatcher) newSession() *Dispatcher {
	return &Dispatcher{
		tools:      d.tools,
		toolOrder:  d.toolOrder,
		serverInfo: d.serverInfo,
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
		return d.handleInitialize(id, req.Params)

	case "notifications/initialized", "initialized":
		d.initialized = true
		return nil

	case "ping":
		// Ping is allowed at any time, even before initialization.
		return NewResponse(id, map[string]any{})

	default:
		// All other methods require a completed initialization handshake.
		if !d.initialized {
			return NewErrorResponse(id, ErrCodeInvalidRequest, "server not initialized")
		}

		switch req.Method {
		case "tools/list":
			return d.handleToolsList(id)
		case "tools/call":
			return d.handleToolsCall(ctx, id, req.Params)
		default:
			return NewErrorResponse(id, ErrCodeMethodNotFound, "method not found: "+req.Method)
		}
	}
}

func (d *Dispatcher) handleInitialize(id json.RawMessage, params json.RawMessage) *Response {
	var p initializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, "invalid initialize params: "+err.Error())
	}

	// Negotiate protocol version: accept if client's version is in our supported list.
	negotiated := ""
	for _, sv := range supportedProtocolVersions {
		if sv == p.ProtocolVersion {
			negotiated = sv
			break
		}
	}
	if negotiated == "" {
		return NewErrorResponseWithData(id, ErrCodeInvalidParams,
			"unsupported protocol version: "+p.ProtocolVersion,
			map[string]any{"supported": supportedProtocolVersions})
	}

	d.negotiatedVersion = negotiated
	d.clientCaps = p.Capabilities
	d.clientInfo = p.ClientInfo

	caps := map[string]any{
		"tools": map[string]any{},
	}
	result := map[string]any{
		"protocolVersion": negotiated,
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
		result = ErrorResult(fmt.Sprintf("tool %q: %v", envelope.Name, err))
	}

	return NewResponse(id, result)
}
