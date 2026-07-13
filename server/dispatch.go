package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
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
//
// "2026-07-28" is the in-flight 2026 protocol revision that carries the SEP-2663
// tasks extension, SEP-2575 stateless capability override, and SEP-2322 MRTR base
// types. Accepting it lets draft-aware conformance suites (panyam/mcpconformance
// feat/tasks-mrtr-extension, upstream PR 262) complete the initialize handshake
// without forcing them to lie about which version they speak.
var supportedProtocolVersions = []string{core.DraftProtocolVersion2026V1, "2025-11-25", "2025-03-26", "2024-11-05"}

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

	// validateFileInputs enables SEP-2356 file-input validation in the
	// tools/call path. When true, the dispatcher walks the tool's
	// inputSchema for `x-mcp-file` properties (single + array-items) and
	// runs `core.ValidateFileInput` on every matching argument BEFORE the
	// handler runs. Failures surface as -32602 with structured `data`
	// (`{reason, actualSize, maxSize}` for oversized; `{reason, mediaType,
	// accept, ...}` for MIME mismatches) — the wire shape is frozen by
	// panyam/mcpconformance `pending` (`src/scenarios/server/file-inputs/`). Set via WithFileInputValidation().
	validateFileInputs bool

	// tasksCap is the tasks capability to advertise during initialize.
	// nil means tasks are not enabled. Set via Server.SetTasksCap().
	tasksCap *core.TasksCap

	// mrtr is the SEP-2322 ephemeral MRTR runtime — signing key + TTL for
	// requestState tokens. Always non-nil (built with zero-value defaults
	// when the server didn't pass WithMRTRSigning); nil signingKey means
	// plaintext mode.
	mrtr *mrtrRuntime

	// SEP-2549 cache hints. listTTLMs / listCacheScope are attached to every
	// tools/list, prompts/list, resources/list, and resources/templates/list
	// response. readTTLMs / readCacheScope are the defaults for resources/read
	// (a handler may override either per-read via core.ResourceResult). A nil
	// *int or empty string omits the field. Set via WithListTTLMs /
	// WithListCacheControl / WithReadResourceCacheControl.
	listTTLMs      *int
	listCacheScope string
	readTTLMs      *int
	readCacheScope string

	// allowLegacyOnDraft is an opt-in back-compat escape hatch: when true,
	// the legacy initialize+session wire is accepted on 2026-07-28 without
	// per-request _meta enforcement. Default false — SEP-2575 (Accepted)
	// removes the initialize handshake on draft and mandates per-request
	// `params._meta.io.modelcontextprotocol/{protocolVersion,clientInfo,
	// clientCapabilities}` on every follow-up; missing _meta MUST be
	// rejected with -32602. Set via WithAllowLegacyOnDraft() for servers
	// that prefer leniency over strict spec conformance.
	allowLegacyOnDraft bool

	// configuredVersions overrides the package-level supportedProtocolVersions
	// for this server when non-empty (issue 419). Set via WithSupportedVersions;
	// read through protocolVersions() so negotiation, the MCP-Protocol-Version
	// header check, and the discover advertise all see the operator's set.
	configuredVersions []string

	// taskBucketKeyer derives the task-store isolation bucket for each request
	// (issue 485). Nil = default (session ID). Set via WithTaskBucketKeyer;
	// injected onto the request context in dispatchWithOpts and the stateless
	// POST handlers so the v1/v2 task surfaces resolve it via core.TaskBucketKey.
	taskBucketKeyer core.TaskBucketKeyer

	// allowReinitialize opts into accepting a second initialize on an
	// already-negotiated session (protocol re-negotiation). Default false:
	// once a session has negotiated a version, a duplicate initialize is
	// rejected with -32600 and the existing session state is preserved, so a
	// misbehaving or hostile client cannot rewrite the negotiated version or
	// advertised client identity mid-flight (issue 421). Set via
	// WithAllowReinitialize().
	allowReinitialize bool
}

// applyReadCacheControl fills the SEP-2549 ttlMs / cacheScope hints on a
// resources/read result. A handler that already set either field on its
// return value keeps that value (per-read override); the server-wide
// default from WithReadResourceCacheControl fills only the unset fields.
func (d *Dispatcher) applyReadCacheControl(r *core.ResourceResult) {
	if r.TTLMs == nil {
		r.TTLMs = d.readTTLMs
	}
	if r.CacheScope == "" {
		r.CacheScope = d.readCacheScope
	}
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
	// taskCallbacks are optional per-tool overrides for tasks/get and
	// tasks/result. Nil means use the TaskStore directly.
	taskCallbacks *TaskCallbacks
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
		mrtr:       &mrtrRuntime{},
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
		validateFileInputs:   d.validateFileInputs,
		tasksCap:             d.tasksCap,
		customHandlers:       d.customHandlers,
		mrtr:                 d.mrtr,
		listTTLMs:            d.listTTLMs,
		listCacheScope:       d.listCacheScope,
		readTTLMs:            d.readTTLMs,
		readCacheScope:       d.readCacheScope,
		allowLegacyOnDraft:   d.allowLegacyOnDraft,
		allowReinitialize:    d.allowReinitialize,
		configuredVersions:   d.configuredVersions,
		taskBucketKeyer:      d.taskBucketKeyer,
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
func (d *Dispatcher) Dispatch(ctx context.Context, req *core.Request) (resp *core.Response) {
	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}

	// A user-provided handler (or a framework bug) that panics must not crash
	// the host process. Recover here, at the single synchronous dispatch entry
	// point, so every tool/resource/prompt/completion/custom handler is covered.
	// A request (has an ID) gets a -32603 internal error; a notification gets
	// nil. Panic detail + stack go to the log, never to the wire (issue 420).
	defer func() {
		if r := recover(); r != nil {
			slog.Error("mcpkit: recovered panic in handler dispatch",
				"method", req.Method, "panic", r, "stack", string(debug.Stack()))
			if req.ID != nil {
				resp = core.NewErrorResponse(id, core.ErrCodeInternal, "internal error")
			} else {
				resp = nil
			}
		}
	}()

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

		// SEP-2575 (Accepted): on 2026-07-28, the initialize handshake is
		// removed; every request MUST carry the per-request _meta envelope
		// (params._meta.io.modelcontextprotocol/{protocolVersion, clientInfo,
		// clientCapabilities}). We retain initialize+session as a back-compat
		// entry point so clients pinned to older versions still negotiate, but
		// once the negotiated version is 2026-07-28 every follow-up request
		// must carry _meta or the server MUST reject with -32602 InvalidParams.
		// allowLegacyOnDraft (WithAllowLegacyOnDraft) is an opt-in escape
		// hatch for server authors who want to be forgiving — off by default.
		// The version gate is resolved via protocol_features.go so all
		// version-gated behavior lives in one table.
		if d.protocolFeatures().StatelessMetaRequired && !d.allowLegacyOnDraft {
			if _, err := core.DecodeRequestMetaFromRawJSON(req.ParamsLazy()); err != nil {
				field := "io.modelcontextprotocol/protocolVersion"
				if mve, ok := err.(*core.MetaValidationError); ok && mve.Field != "_meta" {
					field = "io.modelcontextprotocol/" + mve.Field
				}
				return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
					"Missing required metadata: "+field)
			}
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
	// Reject a duplicate initialize once the session has negotiated a version
	// (issue 421). Overwriting negotiatedVersion / clientCaps / clientInfo
	// would let a client downgrade the protocol or change its advertised
	// identity mid-session. The existing state is left untouched. Opt into
	// re-negotiation with WithAllowReinitialize().
	if d.negotiatedVersion != "" && !d.allowReinitialize {
		return core.NewErrorResponse(id, core.ErrCodeInvalidRequest,
			"session already initialized: duplicate initialize is not allowed")
	}

	var p initializeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "invalid initialize params: "+err.Error())
	}

	// A completely absent protocolVersion is a malformed initialize (the field
	// is REQUIRED); reject it. A present-but-unsupported version, by contrast,
	// is a normal negotiation outcome handled by negotiateProtocolVersion.
	if p.ProtocolVersion == "" {
		return core.NewErrorResponseWithData(id, core.ErrCodeInvalidParams,
			"missing required protocolVersion in initialize params",
			map[string]any{"supported": d.protocolVersions()})
	}

	negotiated := negotiateProtocolVersion(d.protocolVersions(), p.ProtocolVersion)

	d.negotiatedVersion = negotiated
	d.clientCaps = p.Capabilities
	d.clientInfo = p.ClientInfo

	// Advertise listChanged: true for tools, resources, and prompts so
	// clients know to listen for list_changed notifications. This is always
	// safe — it just means "I might send list_changed notifications." Servers
	// that never mutate the registry will simply never send them.
	//
	// Prompts is the exception: advertise it only when at least one prompt
	// is registered. Otherwise clients calling prompts/list see a non-empty
	// capability advertisement but get back an empty list, which fires as
	// drift against upstream's per-fixture servers that only declare
	// capabilities for what they actually register. Surfaced by the
	// apps/compat parity audit (conformance/RESOURCES_META_AUDIT.md);
	// matches the SEP-2575 stateless backend's existing conditional pattern.
	d.Reg.mu.RLock()
	hasPrompts := len(d.Reg.prompts) > 0
	d.Reg.mu.RUnlock()
	caps := core.ServerCapabilities{
		Tools:       &core.ToolsCap{ListChanged: true},
		Resources:   &core.ResourcesCap{ListChanged: true, Subscribe: d.subscriptionsEnabled},
		Logging:     &struct{}{},
		Completions: &struct{}{},
	}
	if hasPrompts {
		caps.Prompts = &core.PromptsCap{ListChanged: true}
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
				Config:      ext.Config,
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

	// SEP-2356: clients that did not declare the `fileInputs` capability
	// MUST NOT see the `x-mcp-file` keyword. Strip it (keeping the
	// underlying string/uri property visible so the tool stays callable
	// on legacy clients). Wire shape locked by panyam/mcpconformance `pending` (`src/scenarios/server/file-inputs/`).
	if d.clientCaps.FileInputs == nil {
		page = stripFileInputsFromTools(page)
	}

	return core.NewResponse(id, core.ToolsListResult{Tools: page, NextCursor: nextCursor, TTLMs: d.listTTLMs, CacheScope: d.listCacheScope})
}

// stripFileInputsFromTools returns a copy of `tools` with every
// occurrence of `x-mcp-file` removed from each tool's InputSchema.
// Keeps the registry's stored ToolDefs untouched (a different client on
// the same server might declare the cap and need the keyword back).
func stripFileInputsFromTools(tools []core.ToolDef) []core.ToolDef {
	out := make([]core.ToolDef, len(tools))
	for i, t := range tools {
		t.InputSchema = core.StripFileInputKeywords(t.InputSchema)
		out[i] = t
	}
	return out
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
	var envelope toolsCallEnvelope
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
	// the handler. Failures surface as a SUCCESSFUL JSON-RPC response
	// carrying a tool result with isError: true and a descriptive content
	// block. Skipped when the tool declared no schema or WithSchemaValidation(false)
	// is set.
	//
	// Why a tool result instead of a -32602 JSON-RPC error: tool input is
	// largely shaped by hosts and apps (basic-host's input form, an iframe's
	// bridge call, an LLM's structured output). Surfacing those failures as
	// hard JSON-RPC errors means the host treats them as protocol-level
	// faults — basic-host renders a "MCP error -32602" banner that pre-empts
	// the iframe's own error UI. Wrapping as `isError: true` lets the host
	// hand the message back to the model / app for self-correction the same
	// way a runtime handler error would. Matches upstream's TypeScript SDK
	// behavior; verified via apps/compat parity testing against
	// modelcontextprotocol/ext-apps.
	//
	// Consumers that want the prior -32602 wire shape can use
	// WithSchemaValidation(false) and perform validation in their own
	// handler, returning whatever response shape they need.
	if !d.skipSchemaValidation && entry.schema != nil {
		if ve := entry.schema.validate(envelope.Arguments); ve != nil {
			return core.NewResponse(id, toolValidationErrorResult(envelope.Name, ve))
		}
	}

	// SEP-2356 file-input validation. Walk the tool's inputSchema for
	// `x-mcp-file` properties (single string/uri AND array-items shapes)
	// and run `core.ValidateFileInput` on each matching argument. Failures
	// surface as -32602 with the structured `data` payload locked by the
	// panyam/mcpconformance `pending` (`src/scenarios/server/file-inputs/`) suite. Skipped unless
	// WithFileInputValidation() is enabled.
	if d.validateFileInputs {
		if resp := d.validateFileInputArgs(id, entry.def.InputSchema, envelope.Arguments); resp != nil {
			return resp
		}
	}

	// SEP-2322: validate any echoed requestState and pull out the
	// accumulated inputResponses from previous rounds. A tampered or
	// expired token gets rejected here so the handler never sees a
	// forged round.
	prevState, err := d.mrtr.verifyRequestState(envelope.RequestState, envelope.Name)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"invalid requestState: "+err.Error())
	}

	// Multi-round merge: accumulated answers from previous rounds + the
	// current round's inputResponses. Current round wins on key collision
	// so a client correcting an earlier answer can re-send under the same
	// key. Handler always sees the unified map regardless of round count.
	mergedResponses := mergeInputResponses(prevState.Answered, envelope.InputResponses)

	// Apply per-tool timeout if set (overrides server-wide WithToolTimeout)
	if entry.def.Timeout > 0 {
		tctx, cancel := context.WithTimeout(ctx, entry.def.Timeout)
		defer cancel()
		ctx = tctx
	}

	req := core.ToolRequest{
		Name:           envelope.Name,
		Arguments:      envelope.Arguments,
		RequestID:      id,
		InputResponses: mergedResponses,
		RequestState:   envelope.RequestState,
	}
	var progressToken any
	if envelope.Meta != nil {
		req.ProgressToken = envelope.Meta.ProgressToken
		progressToken = envelope.Meta.ProgressToken
	}

	tc := core.NewToolContextWithMRTR(ctx, progressToken, mergedResponses, envelope.RequestState)
	resp, hErr := entry.handler(tc, req)
	if hErr != nil {
		resp = core.ErrorResult(fmt.Sprintf("tool %q: %v", envelope.Name, hErr))
	}

	// Type-switch on the sealed ToolResponse interface. SEP-2322
	// InputRequiredResult gets a freshly-minted requestState that carries the
	// merged accumulated answers so the next round sees them too. Every other
	// variant — ToolResult, CreateTaskResult, GoAsyncResult — flows through
	// to the response as-is. GoAsyncResult is in-process plumbing the
	// ext/tasks middleware intercepts before the response reaches the wire;
	// if no middleware is installed and a handler still emits GoAsyncResult,
	// it marshals to "{}" which is a programmer error to be diagnosed at
	// registration time, not silently absorbed here.
	switch r := resp.(type) {
	case core.InputRequiredResult:
		return core.NewResponse(id, core.InputRequiredResult{
			InputRequests: r.InputRequests,
			RequestState:  d.mrtr.mintRequestState(envelope.Name, mergedResponses),
		})
	default:
		return core.NewResponse(id, resp)
	}
}

// toolValidationErrorResult builds the tool result returned when a
// tools/call request's arguments fail schema validation. The result
// carries IsError: true plus a human-readable text content block
// describing what went wrong, and the structured ValidationErrors
// payload is preserved in StructuredContent so machine-readable
// consumers (LLMs, programmatic clients) can self-correct.
//
// The message format leads with the canonical "Invalid arguments" hint
// so that an agent reading the text content learns what shape of error
// it's looking at; the structured `errors` array is the spec-recommended
// payload for re-prompting.
func toolValidationErrorResult(toolName string, ve *ValidationErrors) core.ToolResult {
	var lines []string
	lines = append(lines, fmt.Sprintf("Invalid arguments for tool %q:", toolName))
	for _, e := range ve.Errors {
		lines = append(lines, fmt.Sprintf("  - %s", e.Message))
	}
	return core.ToolResult{
		Content: []core.Content{
			{Type: "text", Text: strings.Join(lines, "\n")},
		},
		IsError:           true,
		StructuredContent: ve,
	}
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
	return core.NewResponse(id, core.ResourcesListResult{Resources: page, NextCursor: nextCursor, TTLMs: d.listTTLMs, CacheScope: d.listCacheScope})
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
		d.applyReadCacheControl(&result)
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
		d.applyReadCacheControl(&result)
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
	return core.NewResponse(id, core.ResourceTemplatesListResult{ResourceTemplates: page, NextCursor: nextCursor, TTLMs: d.listTTLMs, CacheScope: d.listCacheScope})
}

// --- Resource Subscriptions ---

// handleResourcesSubscribe registers the current session's interest in a resource URI.
// The server will send notifications/resources/updated to this session when the
// resource changes (triggered by Server.NotifyResourceUpdated).
//
// Returns ErrCodeSubscriptionLimitExceeded (-32010) when the session has hit
// the per-session cap or rate limit configured via WithSubscriptionCap /
// WithSubscriptionRateLimit. The error.data carries a `reason` field
// ("cap_exceeded" or "rate_limited") for client-side adaptive backoff.
func (d *Dispatcher) handleResourcesSubscribe(id json.RawMessage, params json.RawMessage) *core.Response {
	var p struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}
	if d.subManager != nil {
		if err := d.subManager.subscribe(d.sessionID, d, p.URI); err != nil {
			reason := "cap_exceeded"
			if errors.Is(err, ErrSubscriptionRateLimited) {
				reason = "rate_limited"
			}
			return core.NewErrorResponseWithData(id,
				core.ErrCodeSubscriptionLimitExceeded,
				err.Error(),
				map[string]any{"reason": reason, "uri": p.URI},
			)
		}
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
	return core.NewResponse(id, core.PromptsListResult{Prompts: page, NextCursor: nextCursor, TTLMs: d.listTTLMs, CacheScope: d.listCacheScope})
}

// promptsGetEnvelope is the MRTR-aware prompts/get request shape. The
// MRTR fields (inputResponses, requestState) live alongside name +
// arguments at the params top level — same shape as toolsCallEnvelope.
type promptsGetEnvelope struct {
	Name           string              `json:"name"`
	Arguments      map[string]any      `json:"arguments,omitempty"`
	InputResponses core.InputResponses `json:"inputResponses,omitempty"`
	RequestState   string              `json:"requestState,omitempty"`
}

func (d *Dispatcher) handlePromptsGet(ctx context.Context, id json.RawMessage, params json.RawMessage) *core.Response {
	var envelope promptsGetEnvelope
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

	// SEP-2322: verify any echoed requestState (rejects tampered tokens
	// before the handler runs) and pull out the accumulated inputResponses
	// from prior rounds. Symmetric with the tools/call MRTR flow above —
	// the same mrtrRuntime + token shape is shared across both surfaces.
	prevState, err := d.mrtr.verifyRequestState(envelope.RequestState, envelope.Name)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
			"invalid requestState: "+err.Error())
	}
	mergedResponses := mergeInputResponses(prevState.Answered, envelope.InputResponses)

	req := core.PromptRequest{
		Name:           envelope.Name,
		Arguments:      envelope.Arguments,
		InputResponses: mergedResponses,
		RequestState:   envelope.RequestState,
	}

	pc := core.NewPromptContextWithMRTR(ctx, mergedResponses, envelope.RequestState)
	result, err := entry.handler(pc, req)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodePromptError, fmt.Sprintf("prompt %q: %v", envelope.Name, err))
	}

	// SEP-2322 reshape: InputRequiredResult variants get a fresh requestState
	// carrying the merged answers forward. Every other PromptResponse
	// (today only PromptResult) flows through as-is.
	switch r := result.(type) {
	case core.InputRequiredResult:
		return core.NewResponse(id, core.InputRequiredResult{
			InputRequests: r.InputRequests,
			RequestState:  d.mrtr.mintRequestState(envelope.Name, mergedResponses),
		})
	default:
		return core.NewResponse(id, result)
	}
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
