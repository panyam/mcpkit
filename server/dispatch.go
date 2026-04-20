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
	// pushRequest (below) is guarded by the same RWMutex because the two are
	// always wired and torn down together by every transport.
	notifyFunc core.NotifyFunc
	notifyMu   sync.RWMutex

	// Subscription state (per-session).
	subscriptionsEnabled bool                    // advertise "subscribe": true in resources capability
	sessionID            string                  // set by transport, used as key in subscription registry
	subManager           *subscriptionRegistry   // shared pointer to Server's registry (nil if disabled)

	// Roots state — tracked per session. See refreshRoots for the full state
	// machine. rootsMu guards roots, rootsStale, and rootsFetching; it must
	// NOT be held across user-callback invocations or network round trips.
	rootsMu            sync.Mutex
	roots              []core.Root
	rootsStale         bool
	rootsFetching      bool
	rootsFetchTimeout  time.Duration     // from WithRootsFetchTimeout; 0 = default 30s
	onRootsChanged     func([]core.Root) // optional callback, set via WithOnRootsChanged
	allowedRoots       []string          // static allowlist from WithAllowedRoots

	// Server-to-client request infrastructure.
	// pushRequest pushes a raw JSON-RPC request to the client stream (set by
	// transport, guarded by notifyMu — same lifecycle as notifyFunc).
	// pending tracks in-flight server-to-client requests awaiting responses.
	// requestIDs generates unique IDs for outgoing requests ("srv-1", ...).
	pushRequest func(json.RawMessage)
	pending     pendingMap
	requestIDs  gohttp.IDGen // generates unique IDs for server-to-client requests

	// customHandlers holds user-registered handlers for custom JSON-RPC methods.
	// Shared across sessions (read-only after startup).
	customHandlers map[string]MethodHandler

	// eventIDs generates unique SSE event IDs for this session's streams.
	// Used by transports to assign id: fields to SSE events, enabling
	// client reconnection via Last-Event-ID.
	eventIDs gohttp.IDGen

	// skipSchemaValidation disables call-time argument validation against
	// compiled schemas. Registration still compiles schemas (so malformed
	// schemas still fail fast), but handlers see raw arguments unchecked.
	// Set via WithSchemaValidation(false) for servers that prefer to handle
	// validation themselves. Default: validation enabled.
	skipSchemaValidation bool

	// tasksCap is the tasks capability to advertise during initialize.
	// nil means tasks are not enabled. Set via Server.SetTasksCap().
	tasksCap *core.TasksCap
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

// SetPushRequest installs the transport's push function for server-initiated
// JSON-RPC requests (e.g. roots/list, sampling/createMessage). Called once per
// persistent stream by stdio, in-process, and the SSE/Streamable-HTTP GET SSE
// handlers; not called by the per-POST request path (which wires its own
// request-scoped closure). Thread-safe.
func (d *Dispatcher) SetPushRequest(fn func(json.RawMessage)) {
	d.notifyMu.Lock()
	d.pushRequest = fn
	d.notifyMu.Unlock()
}

// getPushRequest returns the currently installed push function, or nil if
// the transport does not support server-initiated requests outside of POST
// response cycles. Thread-safe.
func (d *Dispatcher) getPushRequest() func(json.RawMessage) {
	d.notifyMu.RLock()
	fn := d.pushRequest
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
	// schema is the compiled InputSchema, used to validate incoming tool
	// arguments before the handler is invoked. nil if the tool declared
	// no schema (bypass validation).
	schema *compiledSchema
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
	// argSchemas maps argument name → compiled schema for prompt arguments
	// that declared a Schema field. Arguments without a schema are not in
	// the map and bypass validation.
	argSchemas map[string]*compiledSchema
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
		rootsFetchTimeout:    d.rootsFetchTimeout,
		allowedRoots:         d.allowedRoots,
		eventIDs:             newEventIDGen(),
		requestIDs:           newEventIDGen(),
		skipSchemaValidation: d.skipSchemaValidation,
		tasksCap:             d.tasksCap,
		customHandlers:       d.customHandlers,
	}
}

// NegotiatedVersion returns the protocol version negotiated during initialization.
func (d *Dispatcher) NegotiatedVersion() string {
	return d.negotiatedVersion
}

// RegisterTool adds a tool to the dispatcher's registry.
// Panics if def.InputSchema is set but invalid — schema compilation failures
// at registration time are programmer errors (the schema is hard-coded in the
// server binary), so fail-fast is preferred. Use [Registry.AddTool] directly
// if you need to handle schema errors programmatically.
func (d *Dispatcher) RegisterTool(def core.ToolDef, handler core.ToolHandler) {
	if err := d.Reg.AddTool(def, handler); err != nil {
		panic(err)
	}
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
// Panics if any argument Schema is set but invalid — see [Dispatcher.RegisterTool]
// for rationale. Use [Registry.AddPrompt] directly to handle schema errors.
func (d *Dispatcher) RegisterPrompt(def core.PromptDef, handler core.PromptHandler) {
	if err := d.Reg.AddPrompt(def, handler); err != nil {
		panic(err)
	}
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
		d.handleRootsListChanged()
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
			if h, ok := d.customHandlers[req.Method]; ok {
				return h(core.NewMethodContext(ctx), id, req.Params)
			}
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
	if d.tasksCap != nil {
		caps.Tasks = d.tasksCap
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

	// Validate arguments against the advertised InputSchema before invoking
	// the handler. Failures return -32602 Invalid Params with structured
	// error data so agents can self-correct on the next call. Skipped when
	// the tool declared no schema or WithSchemaValidation(false) is set.
	if !d.skipSchemaValidation && entry.schema != nil {
		if ve := entry.schema.validate(envelope.Arguments); ve != nil {
			return core.NewErrorResponseWithData(id, core.ErrCodeInvalidParams,
				"argument validation failed", ve)
		}
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
	var progressToken any
	if envelope.Meta != nil {
		req.ProgressToken = envelope.Meta.ProgressToken
		progressToken = envelope.Meta.ProgressToken
	}

	result, err := entry.handler(core.NewToolContextWithProgress(ctx, progressToken), req)
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
		result, err := entry.handler(core.NewResourceContext(ctx), core.ResourceRequest{URI: envelope.URI})
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
		result, err := matchedHandler(core.NewResourceContext(ctx), matchedURI, matchedParams)
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

	// Validate arguments against declared schemas. Arguments without a
	// declared schema bypass validation; arguments declared in the schema
	// but missing from the request are validated as missing (the per-arg
	// compiled schema sees a nil value). All violations are collected so
	// the caller gets every mistake at once rather than one-at-a-time.
	if !d.skipSchemaValidation && len(entry.argSchemas) > 0 {
		var allErrors []ValidationError
		for argName, sch := range entry.argSchemas {
			ve := sch.validateValue(envelope.Arguments[argName])
			if ve == nil {
				continue
			}
			// Prefix each error path with the argument name so the client
			// can tell which field failed.
			for _, e := range ve.Errors {
				e.Path = "/" + argName + e.Path
				allErrors = append(allErrors, e)
			}
		}
		if len(allErrors) > 0 {
			return core.NewErrorResponseWithData(id, core.ErrCodeInvalidParams,
				"argument validation failed", &ValidationErrors{Errors: allErrors})
		}
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

	result, err := entry.handler(core.NewPromptContext(ctx), req)
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

	result, err := handler(core.NewPromptContext(ctx), p.Ref, p.Argument)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeCompletionError, fmt.Sprintf("completion %q: %v", key, err))
	}

	// MCP spec: completion.values has maxItems=100.
	if len(result.Values) > core.MaxCompletionValues {
		if result.Total == 0 {
			result.Total = len(result.Values)
		}
		result.Values = result.Values[:core.MaxCompletionValues]
		result.HasMore = true
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
