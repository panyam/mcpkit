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
var supportedProtocolVersions = []string{"2025-11-25", "2025-03-26", "2024-11-05"}

// ErrCodeCancelled is the JSON-RPC error code for a cancelled request.
const ErrCodeCancelled = -32800

// pendingMap is a typed alias for sync.Map used to track pending server-to-client requests.
type pendingMap = sync.Map

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

	completions map[string]CompletionHandler // key: "ref/prompt:name" or "ref/resource:uri"

	// extensions registered via WithExtension, keyed by extension ID.
	extensions map[string]Extension

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

	// Subscription state (per-session).
	subscriptionsEnabled bool                    // advertise "subscribe": true in resources capability
	sessionID            string                  // set by transport, used as key in subscription registry
	subManager           *subscriptionRegistry   // shared pointer to Server's registry (nil if disabled)

	// Server-to-client request infrastructure.
	// pushRequest pushes a raw JSON-RPC request to the client stream (set by transport).
	// pending tracks in-flight server-to-client requests awaiting responses.
	// nextServerReqID generates unique IDs for outgoing requests ("srv-1", "srv-2", ...).
	pushRequest    func(json.RawMessage)
	pending        pendingMap
	nextServerReqID atomic.Int64
}

// Close tears down all per-session state on the Dispatcher. Transports must call
// this when a session disconnects (SSE stream closes, DELETE request, client close).
// Centralizes cleanup so that adding new per-session state (subscriptions, sampling,
// elicitation) only requires updating this method, not every transport.
// Safe to call multiple times and on dispatchers with no session state.
func (d *Dispatcher) Close() {
	if d.subManager != nil && d.sessionID != "" {
		d.subManager.unsubscribeAll(d.sessionID)
	}
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

// initializeParams is the params object sent by the client in an initialize request.
type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// NewDispatcher creates a dispatcher with the given server identity.
func NewDispatcher(info ServerInfo) *Dispatcher {
	return &Dispatcher{
		tools:       make(map[string]toolEntry),
		resources:   make(map[string]resourceEntry),
		templates:   make(map[string]templateEntry),
		prompts:     make(map[string]promptEntry),
		completions: make(map[string]CompletionHandler),
		extensions:  make(map[string]Extension),
		serverInfo:  info,
	}
}

// newSession creates a new Dispatcher that shares all registries from d
// but has fresh session state (not initialized, no client info).
// Registries are shared by reference — safe because they are populated
// before serving begins and never modified after.
func (d *Dispatcher) newSession() *Dispatcher {
	return &Dispatcher{
		tools:                d.tools,
		toolOrder:            d.toolOrder,
		resources:            d.resources,
		resourceOrder:        d.resourceOrder,
		templates:            d.templates,
		templateOrder:        d.templateOrder,
		prompts:              d.prompts,
		promptOrder:          d.promptOrder,
		completions:          d.completions,
		extensions:           d.extensions,
		serverInfo:           d.serverInfo,
		subscriptionsEnabled: d.subscriptionsEnabled,
		subManager:           d.subManager,
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

// RegisterCompletion registers a completion handler for a specific reference.
// refType is "ref/prompt" or "ref/resource". name is the prompt name or resource URI template.
func (d *Dispatcher) RegisterCompletion(refType, name string, handler CompletionHandler) {
	d.completions[refType+":"+name] = handler
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
		case "resources/subscribe":
			return d.handleResourcesSubscribe(id, req.Params)
		case "resources/unsubscribe":
			return d.handleResourcesUnsubscribe(id, req.Params)
		case "prompts/list":
			return d.handlePromptsList(id, req.Params)
		case "prompts/get":
			return d.handlePromptsGet(ctx, id, req.Params)
		case "logging/setLevel":
			return d.handleLoggingSetLevel(id, req.Params)
		case "completion/complete":
			return d.handleCompletionComplete(ctx, id, req.Params)
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
		"tools":       map[string]any{},
		"logging":     map[string]any{},
		"completions": map[string]any{},
	}
	if len(d.resources) > 0 || len(d.templates) > 0 {
		resCap := map[string]any{}
		if d.subscriptionsEnabled {
			resCap["subscribe"] = true
		}
		caps["resources"] = resCap
	}
	if len(d.prompts) > 0 {
		caps["prompts"] = map[string]any{}
	}
	if len(d.extensions) > 0 {
		exts := make(map[string]any, len(d.extensions))
		for id, ext := range d.extensions {
			exts[id] = map[string]any{
				"specVersion": ext.SpecVersion,
				"stability":  string(ext.Stability),
			}
		}
		caps["extensions"] = exts
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
		Meta      *struct {
			ProgressToken any `json:"progressToken"`
		} `json:"_meta"`
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
	if envelope.Meta != nil {
		req.ProgressToken = envelope.Meta.ProgressToken
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

// --- Resource Subscriptions ---

// handleResourcesSubscribe registers the current session's interest in a resource URI.
// The server will send notifications/resources/updated to this session when the
// resource changes (triggered by Server.NotifyResourceUpdated).
func (d *Dispatcher) handleResourcesSubscribe(id json.RawMessage, params json.RawMessage) *Response {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, err.Error())
	}
	if d.subManager != nil {
		d.subManager.subscribe(d.sessionID, d, p.URI)
	}
	return NewResponse(id, map[string]any{})
}

// handleResourcesUnsubscribe removes the current session's subscription for a resource URI.
// The server will no longer send notifications/resources/updated for this URI to this session.
func (d *Dispatcher) handleResourcesUnsubscribe(id json.RawMessage, params json.RawMessage) *Response {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, err.Error())
	}
	if d.subManager != nil {
		d.subManager.unsubscribe(d.sessionID, p.URI)
	}
	return NewResponse(id, map[string]any{})
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

// --- Completion ---

// handleCompletionComplete handles the completion/complete method.
// It looks up a registered CompletionHandler by ref type + name/URI,
// calls it, and returns the completion suggestions. If no handler is
// registered, returns an empty result (graceful fallback).
func (d *Dispatcher) handleCompletionComplete(ctx context.Context, id json.RawMessage, params json.RawMessage) *Response {
	var p struct {
		Ref      CompletionRef      `json:"ref"`
		Argument CompletionArgument `json:"argument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return NewErrorResponse(id, ErrCodeInvalidParams, "invalid completion/complete params: "+err.Error())
	}

	// Build lookup key from ref type + name or URI
	key := p.Ref.Type + ":"
	if p.Ref.Name != "" {
		key += p.Ref.Name
	} else {
		key += p.Ref.URI
	}

	handler, ok := d.completions[key]
	if !ok {
		// No handler registered — return empty completion (graceful fallback)
		return NewResponse(id, map[string]any{
			"completion": CompletionResult{Values: []string{}, HasMore: false},
		})
	}

	result, err := handler(ctx, p.Ref, p.Argument)
	if err != nil {
		return NewErrorResponse(id, ErrCodeInternal, fmt.Sprintf("completion %q: %v", key, err))
	}

	return NewResponse(id, map[string]any{"completion": result})
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

// --- Server-to-client requests ---

// RouteResponse routes an incoming JSON-RPC response from the client to a
// pending server-to-client request. Returns true if matched, false if no
// pending request was found for the response ID.
func (d *Dispatcher) RouteResponse(resp *Response) bool {
	return routeServerResponse(&d.pending, resp)
}

// makeRequestFunc builds a RequestFunc that uses sendServerRequest with the
// dispatcher's pending map and ID counter, and the given push function.
func (d *Dispatcher) makeRequestFunc(pushFunc func(json.RawMessage)) RequestFunc {
	return func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		return sendServerRequest(ctx, method, params, &d.nextServerReqID, &d.pending, pushFunc)
	}
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
