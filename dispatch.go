package mcpkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// supportedProtocolVersions lists the MCP protocol versions this server supports,
// ordered newest-first. During initialization the server picks the client's requested
// version if it appears in this list; otherwise it rejects with the full list.
var supportedProtocolVersions = []string{"2025-11-25", "2024-11-05"}

// ErrCodeCancelled is the JSON-RPC error code for a cancelled request.
const ErrCodeCancelled = -32800

// Dispatcher routes JSON-RPC requests to the appropriate handler.
type Dispatcher struct {
	tools      map[string]toolEntry
	toolOrder  []string // preserves registration order for tools/list
	serverInfo ServerInfo

	resources     map[string]resourceEntry
	resourceOrder []string
	templates     map[string]templateEntry
	templateOrder []string

	prompts     map[string]promptEntry
	promptOrder []string

	// inflight tracks cancellable in-flight requests by ID.
	inflight sync.Map // requestID (string) → context.CancelFunc

	// Session state set during initialization handshake.
	negotiatedVersion string             // set by initialize
	clientCaps        ClientCapabilities // set by initialize
	clientInfo        ClientInfo         // set by initialize
	initialized       bool               // set to true by notifications/initialized

	// Logging state (per-session).
	// logLevel stores the minimum log level set by the client via logging/setLevel.
	// nil means logging is disabled (client has not called logging/setLevel).
	// Accessed atomically because tool handlers read it from concurrent goroutines.
	logLevel atomic.Pointer[LogLevel]

	// notifyFunc is set by the transport to push server-to-client notifications.
	// nil means no push capability (e.g., Streamable HTTP without GET SSE stream).
	notifyFunc NotifyFunc
}

type toolEntry struct {
	def     ToolDef
	handler ToolHandler
}

type resourceEntry struct {
	def     ResourceDef
	handler ResourceHandler
}

type templateEntry struct {
	def     ResourceTemplate
	handler TemplateHandler
}

type promptEntry struct {
	def     PromptDef
	handler PromptHandler
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
		tools:     make(map[string]toolEntry),
		resources: make(map[string]resourceEntry),
		templates: make(map[string]templateEntry),
		prompts:   make(map[string]promptEntry),
		serverInfo: info,
	}
}

// newSession creates a new Dispatcher that shares all registries from d
// but has fresh session state (not initialized, no client info).
// Registries are shared by reference — safe because they are populated
// before serving begins and never modified after.
func (d *Dispatcher) newSession() *Dispatcher {
	return &Dispatcher{
		tools:         d.tools,
		toolOrder:     d.toolOrder,
		resources:     d.resources,
		resourceOrder: d.resourceOrder,
		templates:     d.templates,
		templateOrder: d.templateOrder,
		prompts:       d.prompts,
		promptOrder:   d.promptOrder,
		serverInfo:    d.serverInfo,
	}
}

// NegotiatedVersion returns the protocol version negotiated during initialization.
func (d *Dispatcher) NegotiatedVersion() string {
	return d.negotiatedVersion
}

// RegisterTool adds a tool to the dispatcher.
func (d *Dispatcher) RegisterTool(def ToolDef, handler ToolHandler) {
	d.tools[def.Name] = toolEntry{def: def, handler: handler}
	d.toolOrder = append(d.toolOrder, def.Name)
}

// RegisterResource adds a resource to the dispatcher.
func (d *Dispatcher) RegisterResource(def ResourceDef, handler ResourceHandler) {
	d.resources[def.URI] = resourceEntry{def: def, handler: handler}
	d.resourceOrder = append(d.resourceOrder, def.URI)
}

// RegisterResourceTemplate adds a URI template resource to the dispatcher.
func (d *Dispatcher) RegisterResourceTemplate(def ResourceTemplate, handler TemplateHandler) {
	d.templates[def.URITemplate] = templateEntry{def: def, handler: handler}
	d.templateOrder = append(d.templateOrder, def.URITemplate)
}

// RegisterPrompt adds a prompt to the dispatcher.
func (d *Dispatcher) RegisterPrompt(def PromptDef, handler PromptHandler) {
	d.prompts[def.Name] = promptEntry{def: def, handler: handler}
	d.promptOrder = append(d.promptOrder, def.Name)
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

	case "notifications/cancelled":
		d.handleCancelled(req.Params)
		return nil

	case "ping":
		return NewResponse(id, map[string]any{})

	default:
		if !d.initialized {
			return NewErrorResponse(id, ErrCodeInvalidRequest, "server not initialized")
		}

		// Track in-flight request for cancellation support
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		reqID := string(id)
		d.inflight.Store(reqID, cancel)
		defer d.inflight.Delete(reqID)

		switch req.Method {
		case "tools/list":
			return d.handleToolsList(id, req.Params)
		case "tools/call":
			return d.handleToolsCall(ctx, id, req.Params)
		case "resources/list":
			return d.handleResourcesList(id, req.Params)
		case "resources/read":
			return d.handleResourcesRead(ctx, id, req.Params)
		case "resources/templates/list":
			return d.handleResourcesTemplatesList(id, req.Params)
		case "prompts/list":
			return d.handlePromptsList(id, req.Params)
		case "prompts/get":
			return d.handlePromptsGet(ctx, id, req.Params)
		case "logging/setLevel":
			return d.handleLoggingSetLevel(id, req.Params)
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
		"tools":   map[string]any{},
		"logging": map[string]any{},
	}
	if len(d.resources) > 0 || len(d.templates) > 0 {
		caps["resources"] = map[string]any{}
	}
	if len(d.prompts) > 0 {
		caps["prompts"] = map[string]any{}
	}

	result := map[string]any{
		"protocolVersion": negotiated,
		"capabilities":    caps,
		"serverInfo":      d.serverInfo,
	}
	return NewResponse(id, result)
}

func (d *Dispatcher) handleToolsList(id json.RawMessage, params json.RawMessage) *Response {
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

// --- Resources ---

func (d *Dispatcher) handleResourcesList(id json.RawMessage, params json.RawMessage) *Response {
	resources := make([]ResourceDef, 0, len(d.resourceOrder))
	for _, uri := range d.resourceOrder {
		if entry, ok := d.resources[uri]; ok {
			resources = append(resources, entry.def)
		}
	}
	return NewResponse(id, map[string]any{"resources": resources})
}

func (d *Dispatcher) handleResourcesRead(ctx context.Context, id json.RawMessage, params json.RawMessage) *Response {
	var envelope struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, err.Error())
	}

	// Try exact match first
	if entry, ok := d.resources[envelope.URI]; ok {
		result, err := entry.handler(ctx, ResourceRequest{URI: envelope.URI})
		if err != nil {
			return NewErrorResponse(id, ErrCodeInternal, fmt.Sprintf("resource %q: %v", envelope.URI, err))
		}
		return NewResponse(id, result)
	}

	// Try template match
	for _, tmplURI := range d.templateOrder {
		entry := d.templates[tmplURI]
		if params, matched := matchTemplate(entry.def.URITemplate, envelope.URI); matched {
			result, err := entry.handler(ctx, envelope.URI, params)
			if err != nil {
				return NewErrorResponse(id, ErrCodeInternal, fmt.Sprintf("resource template %q: %v", tmplURI, err))
			}
			return NewResponse(id, result)
		}
	}

	return NewErrorResponse(id, ErrCodeInvalidParams, "unknown resource: "+envelope.URI)
}

func (d *Dispatcher) handleResourcesTemplatesList(id json.RawMessage, params json.RawMessage) *Response {
	templates := make([]ResourceTemplate, 0, len(d.templateOrder))
	for _, uri := range d.templateOrder {
		if entry, ok := d.templates[uri]; ok {
			templates = append(templates, entry.def)
		}
	}
	return NewResponse(id, map[string]any{"resourceTemplates": templates})
}

// --- Prompts ---

func (d *Dispatcher) handlePromptsList(id json.RawMessage, params json.RawMessage) *Response {
	prompts := make([]PromptDef, 0, len(d.promptOrder))
	for _, name := range d.promptOrder {
		if entry, ok := d.prompts[name]; ok {
			prompts = append(prompts, entry.def)
		}
	}
	return NewResponse(id, map[string]any{"prompts": prompts})
}

func (d *Dispatcher) handlePromptsGet(ctx context.Context, id json.RawMessage, params json.RawMessage) *Response {
	var envelope struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, err.Error())
	}

	entry, ok := d.prompts[envelope.Name]
	if !ok {
		return NewErrorResponse(id, ErrCodeInvalidParams, "unknown prompt: "+envelope.Name)
	}

	req := PromptRequest{
		Name:      envelope.Name,
		Arguments: envelope.Arguments,
	}

	result, err := entry.handler(ctx, req)
	if err != nil {
		return NewErrorResponse(id, ErrCodeInternal, fmt.Sprintf("prompt %q: %v", envelope.Name, err))
	}

	return NewResponse(id, result)
}

// --- Cancellation ---

func (d *Dispatcher) handleCancelled(params json.RawMessage) {
	if params == nil {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	if cancelFn, ok := d.inflight.LoadAndDelete(string(p.RequestID)); ok {
		cancelFn.(context.CancelFunc)()
	}
}

// --- Logging ---

// handleLoggingSetLevel handles the logging/setLevel method.
// It sets the minimum log level for this session. After this call, the server
// will send notifications/message for log entries at or above the specified level.
func (d *Dispatcher) handleLoggingSetLevel(id json.RawMessage, params json.RawMessage) *Response {
	var p struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, "invalid logging/setLevel params: "+err.Error())
	}
	level, ok := ParseLogLevel(p.Level)
	if !ok {
		return NewErrorResponse(id, ErrCodeInvalidParams, "unknown log level: "+p.Level)
	}
	d.logLevel.Store(&level)
	return NewResponse(id, map[string]any{})
}

// --- Template matching ---

// matchTemplate matches a URI against a simple URI template like "test://template/{id}/data".
// Returns the extracted parameters and whether it matched.
func matchTemplate(template, uri string) (map[string]string, bool) {
	params := make(map[string]string)
	tParts := strings.Split(template, "/")
	uParts := strings.Split(uri, "/")

	if len(tParts) != len(uParts) {
		return nil, false
	}

	for i, tp := range tParts {
		if strings.HasPrefix(tp, "{") && strings.HasSuffix(tp, "}") {
			key := tp[1 : len(tp)-1]
			params[key] = uParts[i]
		} else if tp != uParts[i] {
			return nil, false
		}
	}

	return params, true
}
