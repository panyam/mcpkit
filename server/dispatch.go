package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	conc "github.com/panyam/gocurrent"
	core "github.com/panyam/mcpkit/core"
	gohttp "github.com/panyam/servicekit/http"
	uritemplate "github.com/yosida95/uritemplate/v3"
)

// supportedProtocolVersions lists the MCP protocol versions this server supports,
// ordered newest-first. During initialization the server picks the client's requested
// version if it appears in this list; otherwise it rejects with the full list.
var supportedProtocolVersions = []string{"2025-11-25", "2025-03-26", "2024-11-05"}

// ErrCodeCancelled is the JSON-RPC error code for a cancelled request.
const ErrCodeCancelled = -32800

// pendingMap is a type alias for SyncMap used to track pending server-to-client requests.
type pendingMap = conc.SyncMap[string, *pendingServerRequest]

// Dispatcher routes JSON-RPC requests to the appropriate handler.
type Dispatcher struct {
	Reg        *Registry
	serverInfo core.ServerInfo

	// extensions registered via WithExtension, keyed by extension ID.
	extensions map[string]core.Extension

	// inflight tracks cancellable in-flight requests by ID.
	inflight conc.SyncMap[string, context.CancelFunc]

	// Session state set during initialization handshake.
	negotiatedVersion string             // set by initialize
	clientCaps        core.ClientCapabilities // set by initialize
	clientInfo        core.ClientInfo         // set by initialize
	initialized       bool               // set to true by notifications/initialized

	// Logging state (per-session).
	// logLevel stores the minimum log level set by the client via logging/setLevel.
	// nil means logging is disabled (client has not called logging/setLevel).
	// Accessed atomically because tool handlers read it from concurrent goroutines.
	logLevel atomic.Pointer[core.LogLevel]

	// notifyFunc is set by the transport to push server-to-client notifications.
	// nil means no push capability (e.g., Streamable HTTP without GET SSE stream).
	// Protected by notifyMu because the GET SSE handler may set it concurrently
	// with subscription notifications reading it.
	notifyFunc core.NotifyFunc
	notifyMu   sync.RWMutex

	// Subscription state (per-session).
	subscriptionsEnabled bool                    // advertise "subscribe": true in resources capability
	sessionID            string                  // set by transport, used as key in subscription registry
	subManager           *subscriptionRegistry   // shared pointer to Server's registry (nil if disabled)

	// Roots state — tracked per session.
	// rootsStale is set when the client sends notifications/roots/list_changed.
	// On the next tool call with a requestFunc, the server fetches roots/list.
	rootsStale    bool
	roots         []core.Root
	onRootsChanged func([]core.Root) // optional callback, set via WithOnRootsChanged

	// Server-to-client request infrastructure.
	// pushRequest pushes a raw JSON-RPC request to the client stream (set by transport).
	// pending tracks in-flight server-to-client requests awaiting responses.
	// nextServerReqID generates unique IDs for outgoing requests ("srv-1", "srv-2", ...).
	pushRequest func(json.RawMessage)
	pending     pendingMap
	requestIDs  gohttp.IDGen // generates unique IDs for server-to-client requests

	// eventIDs generates unique SSE event IDs for this session's streams.
	// Used by transports to assign id: fields to SSE events, enabling
	// client reconnection via Last-Event-ID.
	eventIDs gohttp.IDGen
}

// SetNotifyFunc sets the notification delivery function for this dispatcher.
// Thread-safe: can be called concurrently with getNotifyFunc reads.
func (d *Dispatcher) SetNotifyFunc(fn core.NotifyFunc) {
	d.notifyMu.Lock()
	d.notifyFunc = fn
	d.notifyMu.Unlock()
}

// getNotifyFunc returns the current notification delivery function.
// Thread-safe: can be called concurrently with SetNotifyFunc writes.
func (d *Dispatcher) getNotifyFunc() core.NotifyFunc {
	d.notifyMu.RLock()
	fn := d.notifyFunc
	d.notifyMu.RUnlock()
	return fn
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
	def     core.ToolDef
	handler core.ToolHandler
}

type resourceEntry struct {
	def     core.ResourceDef
	handler core.ResourceHandler
}

type templateEntry struct {
	def     core.ResourceTemplate
	handler core.TemplateHandler
}

type promptEntry struct {
	def     core.PromptDef
	handler core.PromptHandler
}

// initializeParams is the params object sent by the client in an initialize request.
type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    core.ClientCapabilities `json:"capabilities"`
	ClientInfo      core.ClientInfo         `json:"clientInfo"`
}

// NewDispatcher creates a dispatcher with the given server identity.
func NewDispatcher(info core.ServerInfo) *Dispatcher {
	return &Dispatcher{
		Reg:        NewRegistry(),
		extensions: make(map[string]core.Extension),
		serverInfo: info,
	}
}

// newSession creates a new Dispatcher that shares the registry from d
// but has fresh session state (not initialized, no client info).
// The registry pointer is shared — all sessions see the same tools,
// resources, and prompts. Thread-safe via registry.mu.
func (d *Dispatcher) newSession() *Dispatcher {
	return &Dispatcher{
		Reg:                  d.Reg,
		extensions:           d.extensions,
		serverInfo:           d.serverInfo,
		subscriptionsEnabled: d.subscriptionsEnabled,
		subManager:           d.subManager,
		onRootsChanged:       d.onRootsChanged,
		eventIDs:             newEventIDGen(),
		requestIDs:           newEventIDGen(),
	}
}

// NegotiatedVersion returns the protocol version negotiated during initialization.
func (d *Dispatcher) NegotiatedVersion() string {
	return d.negotiatedVersion
}

// RegisterTool adds a tool to the dispatcher's registry.
func (d *Dispatcher) RegisterTool(def core.ToolDef, handler core.ToolHandler) {
	d.Reg.AddTool(def, handler)
}

// RegisterResource adds a resource to the dispatcher's registry.
func (d *Dispatcher) RegisterResource(def core.ResourceDef, handler core.ResourceHandler) {
	d.Reg.AddResource(def, handler)
}

// RegisterResourceTemplate adds a URI template resource to the dispatcher's registry.
func (d *Dispatcher) RegisterResourceTemplate(def core.ResourceTemplate, handler core.TemplateHandler) {
	d.Reg.AddResourceTemplate(def, handler)
}

// RegisterPrompt adds a prompt to the dispatcher's registry.
func (d *Dispatcher) RegisterPrompt(def core.PromptDef, handler core.PromptHandler) {
	d.Reg.AddPrompt(def, handler)
}

// RegisterCompletion registers a completion handler for a specific reference.
// refType is "ref/prompt" or "ref/resource". name is the prompt name or resource URI template.
func (d *Dispatcher) RegisterCompletion(refType, name string, handler core.CompletionHandler) {
	d.Reg.AddCompletion(refType, name, handler)
}

// Dispatch routes a JSON-RPC request and returns the response.
// Returns nil for notifications (no response expected).
func (d *Dispatcher) Dispatch(ctx context.Context, req *core.Request) *core.Response {
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

	case "notifications/roots/list_changed":
		d.rootsStale = true
		if d.onRootsChanged != nil {
			d.onRootsChanged(d.roots)
		}
		return nil

	case "ping":
		return core.NewResponse(id, core.PingResult{})

	default:
		if !d.initialized {
			return core.NewErrorResponse(id, core.ErrCodeInvalidRequest, "server not initialized")
		}

		// Track in-flight request for cancellation support.
		// Reject duplicate request IDs within the same session to prevent
		// cancellation confusion (the old cancelFn would be overwritten).
		ctx, cancel := context.WithCancel(ctx)
		reqID := string(id)
		if _, loaded := d.inflight.LoadOrStore(reqID, cancel); loaded {
			cancel()
			return core.NewErrorResponse(id, core.ErrCodeInvalidRequest, "duplicate request ID: "+reqID)
		}
		defer cancel()
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
			return core.NewErrorResponse(id, core.ErrCodeMethodNotFound, "method not found: "+req.Method)
		}
	}
}

func (d *Dispatcher) handleInitialize(id json.RawMessage, params json.RawMessage) *core.Response {
	var p initializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "invalid initialize params: "+err.Error())
	}

	negotiated := ""
	for _, sv := range supportedProtocolVersions {
		if sv == p.ProtocolVersion {
			negotiated = sv
			break
		}
	}
	if negotiated == "" {
		return core.NewErrorResponseWithData(id, core.ErrCodeInvalidParams,
			"unsupported protocol version: "+p.ProtocolVersion,
			map[string]any{"supported": supportedProtocolVersions})
	}

	d.negotiatedVersion = negotiated
	d.clientCaps = p.Capabilities
	d.clientInfo = p.ClientInfo

	// Advertise listChanged: true for tools, resources, and prompts so
	// clients know to listen for list_changed notifications. This is always
	// safe — it just means "I might send list_changed notifications." Servers
	// that never mutate the registry will simply never send them.
	caps := core.ServerCapabilities{
		Tools:       &core.ToolsCap{ListChanged: true},
		Resources:   &core.ResourcesCap{ListChanged: true, Subscribe: d.subscriptionsEnabled},
		Prompts:     &core.PromptsCap{ListChanged: true},
		Logging:     &struct{}{},
		Completions: &struct{}{},
	}
	if len(d.extensions) > 0 {
		exts := make(map[string]core.ExtensionCapability, len(d.extensions))
		for id, ext := range d.extensions {
			exts[id] = core.ExtensionCapability{
				SpecVersion: ext.SpecVersion,
				Stability:   string(ext.Stability),
			}
		}
		caps.Extensions = exts
	}

	return core.NewResponse(id, core.InitializeResult{
		ProtocolVersion: negotiated,
		Capabilities:    caps,
		ServerInfo:      d.serverInfo,
	})
}

func (d *Dispatcher) handleToolsList(id json.RawMessage, params json.RawMessage) *core.Response {
	cursor, _ := parsePaginationParams(params)

	d.Reg.mu.RLock()
	tools := make([]core.ToolDef, 0, len(d.Reg.toolOrder))
	for _, name := range d.Reg.toolOrder {
		if entry, ok := d.Reg.tools[name]; ok {
			tools = append(tools, entry.def)
		}
	}
	d.Reg.mu.RUnlock()

	page, nextCursor, _ := paginate(tools, cursor, defaultPageSize)
	return core.NewResponse(id, core.ToolsListResult{Tools: page, NextCursor: nextCursor})
}

// parsePaginationParams extracts cursor from request params.
func parsePaginationParams(params json.RawMessage) (cursor string, pageSize int) {
	if params == nil {
		return "", defaultPageSize
	}
	var p struct {
		Cursor string `json:"cursor"`
	}
	json.Unmarshal(params, &p)
	return p.Cursor, defaultPageSize
}

func (d *Dispatcher) handleToolsCall(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	var envelope struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      *struct {
			ProgressToken any `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}

	d.Reg.mu.RLock()
	entry, ok := d.Reg.tools[envelope.Name]
	d.Reg.mu.RUnlock()
	if !ok {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "unknown tool: "+envelope.Name)
	}

	// Apply per-tool timeout if set (overrides server-wide WithToolTimeout)
	if entry.def.Timeout > 0 {
		tctx, cancel := context.WithTimeout(ctx, entry.def.Timeout)
		defer cancel()
		ctx = tctx
	}

	req := core.ToolRequest{
		Name:      envelope.Name,
		Arguments: envelope.Arguments,
		RequestID: id,
	}
	if envelope.Meta != nil {
		req.ProgressToken = envelope.Meta.ProgressToken
	}

	result, err := entry.handler(ctx, req)
	if err != nil {
		result = core.ErrorResult(fmt.Sprintf("tool %q: %v", envelope.Name, err))
	}

	return core.NewResponse(id, result)
}

// --- Resources ---

func (d *Dispatcher) handleResourcesList(id json.RawMessage, params json.RawMessage) *core.Response {
	cursor, _ := parsePaginationParams(params)

	d.Reg.mu.RLock()
	resources := make([]core.ResourceDef, 0, len(d.Reg.resourceOrder))
	for _, uri := range d.Reg.resourceOrder {
		if entry, ok := d.Reg.resources[uri]; ok {
			resources = append(resources, entry.def)
		}
	}
	d.Reg.mu.RUnlock()

	page, nextCursor, _ := paginate(resources, cursor, defaultPageSize)
	return core.NewResponse(id, core.ResourcesListResult{Resources: page, NextCursor: nextCursor})
}

func (d *Dispatcher) handleResourcesRead(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	var envelope struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}

	// Look up handler under RLock, execute outside lock
	d.Reg.mu.RLock()
	if entry, ok := d.Reg.resources[envelope.URI]; ok {
		d.Reg.mu.RUnlock()
		if entry.def.Timeout > 0 {
			tctx, cancel := context.WithTimeout(ctx, entry.def.Timeout)
			defer cancel()
			ctx = tctx
		}
		result, err := entry.handler(ctx, core.ResourceRequest{URI: envelope.URI})
		if err != nil {
			return core.NewErrorResponse(id, core.ErrCodeResourceError, fmt.Sprintf("resource %q: %v", envelope.URI, err))
		}
		return core.NewResponse(id, result)
	}

	// Try template match
	var matchedHandler core.TemplateHandler
	var matchedURI, matchedTmplURI string
	var matchedParams map[string]string
	var matchedTmplTimeout time.Duration
	for _, tmplURI := range d.Reg.templateOrder {
		entry := d.Reg.templates[tmplURI]
		if p, matched := matchTemplate(entry.def.URITemplate, envelope.URI); matched {
			matchedHandler = entry.handler
			matchedURI = envelope.URI
			matchedTmplURI = tmplURI
			matchedParams = p
			matchedTmplTimeout = entry.def.Timeout
			break
		}
	}
	d.Reg.mu.RUnlock()

	if matchedHandler != nil {
		if matchedTmplTimeout > 0 {
			tctx, cancel := context.WithTimeout(ctx, matchedTmplTimeout)
			defer cancel()
			ctx = tctx
		}
		result, err := matchedHandler(ctx, matchedURI, matchedParams)
		if err != nil {
			return core.NewErrorResponse(id, core.ErrCodeResourceError, fmt.Sprintf("resource template %q: %v", matchedTmplURI, err))
		}
		return core.NewResponse(id, result)
	}

	return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "unknown resource: "+envelope.URI)
}

func (d *Dispatcher) handleResourcesTemplatesList(id json.RawMessage, params json.RawMessage) *core.Response {
	cursor, _ := parsePaginationParams(params)

	d.Reg.mu.RLock()
	templates := make([]core.ResourceTemplate, 0, len(d.Reg.templateOrder))
	for _, uri := range d.Reg.templateOrder {
		if entry, ok := d.Reg.templates[uri]; ok {
			templates = append(templates, entry.def)
		}
	}
	d.Reg.mu.RUnlock()

	page, nextCursor, _ := paginate(templates, cursor, defaultPageSize)
	return core.NewResponse(id, core.ResourceTemplatesListResult{ResourceTemplates: page, NextCursor: nextCursor})
}

// --- Resource Subscriptions ---

// handleResourcesSubscribe registers the current session's interest in a resource URI.
// The server will send notifications/resources/updated to this session when the
// resource changes (triggered by Server.NotifyResourceUpdated).
func (d *Dispatcher) handleResourcesSubscribe(id json.RawMessage, params json.RawMessage) *core.Response {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}
	if d.subManager != nil {
		d.subManager.subscribe(d.sessionID, d, p.URI)
	}
	return core.NewResponse(id, struct{}{})
}

// handleResourcesUnsubscribe removes the current session's subscription for a resource URI.
// The server will no longer send notifications/resources/updated for this URI to this session.
func (d *Dispatcher) handleResourcesUnsubscribe(id json.RawMessage, params json.RawMessage) *core.Response {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}
	if d.subManager != nil {
		d.subManager.unsubscribe(d.sessionID, p.URI)
	}
	return core.NewResponse(id, struct{}{})
}

// --- Prompts ---

func (d *Dispatcher) handlePromptsList(id json.RawMessage, params json.RawMessage) *core.Response {
	cursor, _ := parsePaginationParams(params)

	d.Reg.mu.RLock()
	prompts := make([]core.PromptDef, 0, len(d.Reg.promptOrder))
	for _, name := range d.Reg.promptOrder {
		if entry, ok := d.Reg.prompts[name]; ok {
			prompts = append(prompts, entry.def)
		}
	}
	d.Reg.mu.RUnlock()

	page, nextCursor, _ := paginate(prompts, cursor, defaultPageSize)
	return core.NewResponse(id, core.PromptsListResult{Prompts: page, NextCursor: nextCursor})
}

func (d *Dispatcher) handlePromptsGet(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	var envelope struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}

	d.Reg.mu.RLock()
	entry, ok := d.Reg.prompts[envelope.Name]
	d.Reg.mu.RUnlock()
	if !ok {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "unknown prompt: "+envelope.Name)
	}

	if entry.def.Timeout > 0 {
		tctx, cancel := context.WithTimeout(ctx, entry.def.Timeout)
		defer cancel()
		ctx = tctx
	}

	req := core.PromptRequest{
		Name:      envelope.Name,
		Arguments: envelope.Arguments,
	}

	result, err := entry.handler(ctx, req)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodePromptError, fmt.Sprintf("prompt %q: %v", envelope.Name, err))
	}

	return core.NewResponse(id, result)
}

// --- Completion ---

// handleCompletionComplete handles the completion/complete method.
// It looks up a registered core.CompletionHandler by ref type + name/URI,
// calls it, and returns the completion suggestions. If no handler is
// registered, returns an empty result (graceful fallback).
func (d *Dispatcher) handleCompletionComplete(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	var p struct {
		Ref      core.CompletionRef      `json:"ref"`
		Argument core.CompletionArgument `json:"argument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "invalid completion/complete params: "+err.Error())
	}

	// Build lookup key from ref type + name or URI
	key := p.Ref.Type + ":"
	if p.Ref.Name != "" {
		key += p.Ref.Name
	} else {
		key += p.Ref.URI
	}

	d.Reg.mu.RLock()
	handler, ok := d.Reg.completions[key]
	d.Reg.mu.RUnlock()
	if !ok {
		// No handler registered — return empty completion (graceful fallback)
		return core.NewResponse(id, core.CompletionCompleteResult{
			Completion: core.CompletionResult{Values: []string{}, HasMore: false},
		})
	}

	result, err := handler(ctx, p.Ref, p.Argument)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeCompletionError, fmt.Sprintf("completion %q: %v", key, err))
	}

	return core.NewResponse(id, core.CompletionCompleteResult{Completion: result})
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
		cancelFn()
	}
}

// --- Logging ---

// handleLoggingSetLevel handles the logging/setLevel method.
// It sets the minimum log level for this session. After this call, the server
// will send notifications/message for log entries at or above the specified level.
func (d *Dispatcher) handleLoggingSetLevel(id json.RawMessage, params json.RawMessage) *core.Response {
	var p struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "invalid logging/setLevel params: "+err.Error())
	}
	level, ok := core.ParseLogLevel(p.Level)
	if !ok {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "unknown log level: "+p.Level)
	}
	d.logLevel.Store(&level)
	return core.NewResponse(id, struct{}{})
}

// --- Server-to-client requests ---

// RouteResponse routes an incoming JSON-RPC response from the client to a
// pending server-to-client request. Returns true if matched, false if no
// pending request was found for the response ID.
func (d *Dispatcher) RouteResponse(resp *core.Response) bool {
	return routeServerResponse(&d.pending, resp)
}

// makeRequestFunc builds a core.RequestFunc that uses sendServerRequest with the
// dispatcher's pending map and ID counter, and the given push function.
func (d *Dispatcher) makeRequestFunc(pushFunc func(json.RawMessage)) core.RequestFunc {
	return func(ctx context.Context, method string, params any) (json.RawMessage, error) {
		return sendServerRequest(ctx, method, params, d.requestIDs, &d.pending, pushFunc)
	}
}

// --- Template matching ---

// matchTemplate matches a URI against an RFC 6570 URI template using the
// yosida95/uritemplate library. Returns extracted parameters and whether
// the URI matched. Supports RFC 6570 Level 4 expressions including
// backreference detection, reserved expansion, and modifiers.
func matchTemplate(template, uri string) (map[string]string, bool) {
	tmpl, err := uritemplate.New(template)
	if err != nil {
		return nil, false
	}
	match := tmpl.Match(uri)
	if match == nil {
		return nil, false
	}
	params := make(map[string]string)
	for _, name := range tmpl.Varnames() {
		if v := match.Get(name); v.Valid() {
			params[name] = v.String()
		}
	}
	return params, true
}
