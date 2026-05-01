package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// TasksConfig holds the options for registering v2 tasks support on an MCP server.
type TasksConfig struct {
	// Store is the task state backend. If nil, an InMemoryTaskStore is used.
	Store TaskStore

	// Server is the MCP server to register tasks on.
	Server *Server

	// DefaultTTLSeconds is the default task TTL in seconds (v2 uses seconds, not ms).
	// Default: 300 (5 minutes).
	DefaultTTLSeconds int

	// DefaultPollMs is the suggested poll interval in milliseconds,
	// returned to clients in CreateTaskResult. Default: 1000 (1 second).
	DefaultPollMs int

	// RequestStateKey is the HMAC-SHA256 key the server uses to sign and
	// verify SEP-2322 requestState tokens. When non-nil, requestState is
	// returned as `<base64url-hmac>.<base64url-payload>` where the payload
	// is JSON {"taskId":"...", "exp":<unix-seconds>} — clients echo it
	// verbatim and the server rejects tampered or expired tokens with
	// -32602 on tasks/get / tasks/update / tasks/cancel.
	//
	// SEP-2663 says servers MUST treat requestState as attacker-controlled.
	// Production deployments SHOULD set this. nil = legacy plaintext mode
	// (requestState == taskID), kept for backward compat with existing
	// tests and minimal-config setups.
	RequestStateKey []byte

	// RequestStateTTL is how long a signed requestState stays valid. When
	// 0, defaults to 24h. Has no effect when RequestStateKey is nil.
	RequestStateTTL time.Duration
}

func (c *TasksConfig) defaults() {
	if c.Store == nil {
		c.Store = NewInMemoryStore()
	}
	if c.DefaultTTLSeconds <= 0 {
		c.DefaultTTLSeconds = 300
	}
	if c.DefaultPollMs <= 0 {
		c.DefaultPollMs = 1000
	}
	if c.RequestStateTTL <= 0 {
		c.RequestStateTTL = 24 * time.Hour
	}
}

// v2TaskRuntime holds per-registration state for v2 tasks, shared between
// middleware and handlers. Scoped to a single RegisterTasks call (C3).
type v2TaskRuntime struct {
	store    TaskStore
	registry *Registry

	// requestStateKey + requestStateTTL configure SEP-2322 requestState
	// signing. nil key = legacy plaintext mode (requestState == taskID).
	requestStateKey []byte
	requestStateTTL time.Duration

	mu sync.Mutex
	active   map[string]*activeTask
	// taskErrors stores protocol-level errors (TaskError) for failed tasks.
	// Separate from the store's result, which holds ToolResult for completed tasks.
	taskErrors map[string]*core.TaskError
	// creatorToolForTask maps taskID → tool name for callback dispatch.
	creatorToolForTask map[string]string
}

func newV2TaskRuntime(cfg TasksConfig) *v2TaskRuntime {
	return &v2TaskRuntime{
		store:              cfg.Store,
		registry:           cfg.Server.Registry(),
		requestStateKey:    cfg.RequestStateKey,
		requestStateTTL:    cfg.RequestStateTTL,
		active:             make(map[string]*activeTask),
		taskErrors:         make(map[string]*core.TaskError),
		creatorToolForTask: make(map[string]string),
	}
}

func (rt *v2TaskRuntime) register(taskID, tool string, at *activeTask) {
	rt.mu.Lock()
	rt.active[taskID] = at
	rt.creatorToolForTask[taskID] = tool
	rt.mu.Unlock()
}

func (rt *v2TaskRuntime) unregister(taskID string) {
	rt.mu.Lock()
	delete(rt.active, taskID)
	rt.mu.Unlock()
}

func (rt *v2TaskRuntime) setTaskError(taskID string, te *core.TaskError) {
	rt.mu.Lock()
	rt.taskErrors[taskID] = te
	rt.mu.Unlock()
}

func (rt *v2TaskRuntime) getTaskError(taskID string) *core.TaskError {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.taskErrors[taskID]
}

func (rt *v2TaskRuntime) cancelTask(taskID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if at := rt.active[taskID]; at != nil && at.cancel != nil {
		at.cancel()
	}
}

func (rt *v2TaskRuntime) getToolCallbacks(taskID string) *TaskCallbacks {
	rt.mu.Lock()
	name := rt.creatorToolForTask[taskID]
	rt.mu.Unlock()
	if name == "" || rt.registry == nil {
		return nil
	}
	return rt.registry.ToolCallbacks(name)
}

// makeRequestState mints the SEP-2322 requestState string the server hands
// to clients on tasks/get responses and notifications/tasks/status events.
// When a signing key is configured (TasksConfig.RequestStateKey), the token
// is HMAC-SHA256 signed with an embedded expiry; otherwise it falls back to
// the bare taskID for backward compat with minimal-config setups and tests.
func (rt *v2TaskRuntime) makeRequestState(taskID string) string {
	if len(rt.requestStateKey) == 0 {
		return taskID
	}
	return core.SignRequestState(rt.requestStateKey, taskID, rt.requestStateTTL)
}

// verifyRequestState validates an incoming requestState against the signing
// key. When the key is unset we run in legacy plaintext mode: any incoming
// requestState that matches the taskID round-trips, anything else is
// rejected (matches the old "RequestState: p.TaskID" semantics). When the
// key is set, the token MUST decode + HMAC-verify + not be expired; mismatch
// returns -32602 at the handler boundary.
//
// expectedTaskID is the taskID the caller already pulled from the request
// body — we cross-check the token's embedded taskID against it so a token
// issued for task A can't be replayed against task B.
//
// Empty incoming requestState is allowed (legacy clients, or first call
// before the server has minted one). Callers that want stricter behavior
// can refuse on requestState=="" themselves.
func (rt *v2TaskRuntime) verifyRequestState(state, expectedTaskID string) error {
	if state == "" {
		return nil
	}
	if len(rt.requestStateKey) == 0 {
		if state != expectedTaskID {
			return core.ErrRequestStateInvalidSignature
		}
		return nil
	}
	gotTaskID, err := core.VerifyRequestState(rt.requestStateKey, state)
	if err != nil {
		return err
	}
	if gotTaskID != expectedTaskID {
		return core.ErrRequestStateInvalidSignature
	}
	return nil
}

// deliverInputResponses routes a tasks/update payload back to whichever
// goroutine is waiting on the corresponding SEP-2663 input request. For
// each response key that matches a pending request on the task's
// inputState, the payload is sent on the per-key waiter channel so
// TaskContext.TaskElicit / TaskSample can return. Unknown keys are
// silently dropped — clients may legitimately race tasks/update against
// status changes.
//
// External-backed tools (Temporal, Step Functions, SQS, ...) will also
// route through here once a planned TaskCallbacks.OnInputResponse field
// exists; until that lands, they won't have an inputState attached and
// this method is a no-op for them.
func (rt *v2TaskRuntime) deliverInputResponses(taskID string, responses core.InputResponses) {
	state := rt.inputStateFor(taskID)
	if state == nil {
		return
	}
	for key, payload := range responses {
		state.deliver(key, payload)
	}
}

// inputStateFor returns the per-task SEP-2663 input-state, or nil if the
// task has no active entry (already terminal, never created, or the
// underlying tool is external-backed without an inputState attached).
func (rt *v2TaskRuntime) inputStateFor(taskID string) *v2InputState {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if at := rt.active[taskID]; at != nil {
		return at.inputState
	}
	return nil
}

// v2InputState tracks SEP-2663 input requests in flight for one v2 task.
// Each enqueue mints a stable monotonic key ("elicit-1", "sample-2", ...)
// and returns a waiter channel; the matching tasks/update key delivers the
// raw response payload through that channel. Concurrency-safe.
type v2InputState struct {
	mu      sync.Mutex
	counter uint64
	pending map[string]*v2InputWait
}

func newV2InputState() *v2InputState {
	return &v2InputState{pending: make(map[string]*v2InputWait)}
}

// v2InputWait pairs a pending input request with the channel its caller
// (TaskContext.TaskElicit / TaskSample) is blocked on.
type v2InputWait struct {
	request core.InputRequest
	waiter  chan json.RawMessage
}

// enqueue mints a new monotonic key, stashes the request on pending, and
// returns the key plus the waiter channel the caller should block on.
// methodPrefix is a short tag ("elicit", "sample") used to make the keys
// readable when surfaced in tasks/get responses; uniqueness comes from
// the global counter, not the prefix.
//
// IMPORTANT: the key format ("<prefix>-<n>") is a server-internal readability
// choice, not a wire contract. Per SEP-2322 / SEP-2663 the InputRequests /
// InputResponses map keys are server-chosen and opaque to the client —
// clients MUST treat them as round-trip echo strings and MUST NOT parse them.
// We are free to change the generator (e.g., to UUIDs) without breaking any
// conformant client.
func (s *v2InputState) enqueue(methodPrefix string, req core.InputRequest) (key string, waiter chan json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	key = fmt.Sprintf("%s-%d", methodPrefix, s.counter)
	waiter = make(chan json.RawMessage, 1)
	s.pending[key] = &v2InputWait{request: req, waiter: waiter}
	return key, waiter
}

// deliver routes a tasks/update payload to the waiter for key, returning
// true if a waiter was found. The pending entry is removed regardless of
// whether the send succeeds (the caller's select on ctx.Done() may have
// already drained the slot). Buffered waiter chans guarantee deliver
// never blocks.
func (s *v2InputState) deliver(key string, payload json.RawMessage) bool {
	s.mu.Lock()
	wait, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
	}
	s.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case wait.waiter <- payload:
	default:
		// Waiter already abandoned the slot (cancelled / context done).
	}
	return true
}

// snapshot copies the current pending requests into the SEP-2663 wire
// shape for tasks/get DetailedTask responses. Returns nil when nothing
// is pending so the handler omits the field entirely.
func (s *v2InputState) snapshot() core.InputRequests {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := make(core.InputRequests, len(s.pending))
	for k, w := range s.pending {
		out[k] = w.request
	}
	return out
}

// cancelAll drops every pending request and closes the waiter channels so
// blocked goroutines unblock with a zero-value payload (callers detect
// this via the closed-channel receive). Called when a task transitions to
// a terminal state without delivering responses (cancel, panic, etc.).
func (s *v2InputState) cancelAll() {
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[string]*v2InputWait)
	s.mu.Unlock()
	for _, w := range pending {
		close(w.waiter)
	}
}

// tasksExtensionProvider implements core.ExtensionProvider for the SEP-2663
// Tasks extension. RegisterTasks hands an instance to Server.RegisterExtension
// so the extension is advertised in capabilities.extensions during initialize.
type tasksExtensionProvider struct{}

// Extension declares the SEP-2663 Tasks extension.
//
//nolint:unused // referenced by RegisterTasks only when the v2 path is wired
func (tasksExtensionProvider) Extension() core.Extension {
	return core.Extension{
		ID:          core.TasksExtensionID,
		SpecVersion: "draft", // SEP-2663 is in draft
		Stability:   core.Experimental,
	}
}

// RegisterTasks hooks up v2 tasks support on the given server:
//   - Advertises the io.modelcontextprotocol/tasks extension in initialize
//     (replacing the v1 ServerCapabilities.Tasks declaration).
//   - Installs middleware that intercepts tools/call for task-eligible tools
//     (server-directed, no client task param needed). Task creation is
//     gated on the client supporting the extension — either at session
//     level (initialize handshake) or per-request (SEP-2575 _meta).
//   - Registers tasks/get and tasks/cancel handlers, gated on session-level
//     extension support; otherwise the handlers return -32601 (method not
//     found) so unsupported clients don't see a tasks surface they didn't
//     ask for.
//   - Does NOT register tasks/result or tasks/list (removed in v2).
//   - Does NOT call SetTasksCap — v2 tasks live under capabilities.extensions,
//     not the v1 ServerCapabilities.Tasks slot.
//
// Must be called before accepting connections.
func RegisterTasks(cfg TasksConfig) {
	srv := cfg.Server
	// Inherit server-wide WithRequestStateSigning unless this RegisterTasks
	// call overrides explicitly. Sharing the key means production deployments
	// configure HMAC once and both MRTR (Dispatcher.mrtr) + Tasks (this
	// runtime) sign with the same secret.
	if len(cfg.RequestStateKey) == 0 && srv != nil && len(srv.options.requestStateKey) > 0 {
		cfg.RequestStateKey = srv.options.requestStateKey
	}
	if cfg.RequestStateTTL == 0 && srv != nil && srv.options.requestStateTTL > 0 {
		cfg.RequestStateTTL = srv.options.requestStateTTL
	}
	cfg.defaults()
	rt := newV2TaskRuntime(cfg)

	// Advertise the SEP-2663 Tasks extension.
	srv.RegisterExtension(tasksExtensionProvider{})

	// Install v2 middleware for tools/call interception (gated on extension).
	srv.UseMiddleware(taskV2Middleware(srv.Registry(), rt, cfg))

	// Register tasks/* protocol methods (SEP-2663: get + update + cancel),
	// gated on session-level extension support.
	srv.HandleMethod("tasks/get", gateOnTasksExtension(makeV2GetHandler(rt)))
	srv.HandleMethod("tasks/update", gateOnTasksExtension(makeV2UpdateHandler(rt)))
	srv.HandleMethod("tasks/cancel", gateOnTasksExtension(makeV2CancelHandler(rt)))
}

// gateOnTasksExtension wraps a tasks/* handler so unsupported clients get
// -32601 (method not found) instead of the real handler's response. This is
// the SEP-2663 contract: tasks/* methods MUST NOT exist for clients that did
// not negotiate the extension during initialize.
func gateOnTasksExtension(inner MethodHandler) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		if !ctx.ClientSupportsExtension(core.TasksExtensionID) {
			return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
				"method requires extension "+core.TasksExtensionID)
		}
		return inner(ctx, id, params)
	}
}

// --- Middleware ---

// taskV2Middleware intercepts tools/call requests. In v2, the server decides
// whether to create a task based on the tool's configuration — the client
// does NOT send a `task` param.
//
// Behavior by taskSupport:
//   - required: always create a task (no client hint needed)
//   - optional: create a task (server-directed)
//   - forbidden/absent: pass through (sync execution)
func taskV2Middleware(reg *Registry, rt *v2TaskRuntime, cfg TasksConfig) Middleware {
	return func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		// Parse the envelope. _meta carries two known keys for our purposes:
		// progressToken (forwarded to the background goroutine) and the
		// SEP-2575 per-request clientCapabilities override (kept as raw JSON
		// because the typed ClientCapabilities lives in core).
		var envelope struct {
			Name string `json:"name"`
			Meta *struct {
				ProgressToken         any             `json:"progressToken,omitempty"`
				ClientCapabilitiesRaw json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities,omitempty"`
			} `json:"_meta,omitempty"`
		}
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return next(ctx, req)
		}

		def, toolFound := reg.ToolDef(envelope.Name)
		if !toolFound {
			return next(ctx, req)
		}

		// SEP-2663: server MUST NOT return CreateTaskResult unless the client
		// negotiated the io.modelcontextprotocol/tasks extension, either at
		// session level (initialize handshake) or per-request (SEP-2575).
		var perRequestCapsRaw json.RawMessage
		if envelope.Meta != nil {
			perRequestCapsRaw = envelope.Meta.ClientCapabilitiesRaw
		}
		if !core.ClientSupportsExtensionForRequest(ctx, core.TasksExtensionID, perRequestCapsRaw) {
			return next(ctx, req)
		}

		// Determine effective taskSupport. Absent Execution = forbidden.
		// In v2 this is a server-internal hint about which tools should be
		// async — clients no longer opt in via a `task` param.
		effectiveSupport := core.TaskSupportForbidden
		if def.Execution != nil {
			effectiveSupport = def.Execution.TaskSupport
		}

		// In v2, forbidden/absent → pass through (sync).
		if effectiveSupport == core.TaskSupportForbidden {
			return next(ctx, req)
		}

		// required or optional → server creates a task.
		taskID := generateTaskID()
		now := time.Now().UTC().Format(time.RFC3339)

		// v2: TTL in seconds for the wire, but the shared TaskStore uses ms internally.
		ttlSec := cfg.DefaultTTLSeconds
		ttlMs := ttlSec * 1000
		pollMs := cfg.DefaultPollMs

		info := core.TaskInfo{
			TaskID:        taskID,
			Status:        core.TaskWorking,
			CreatedAt:     now,
			LastUpdatedAt: now,
			TTL:           core.IntPtr(ttlMs), // store uses ms internally
			PollInterval:  pollMs,
		}
		store := rt.store
		sessionID := core.GetSessionID(ctx)
		if err := store.Create(info, sessionID); err != nil {
			return core.NewErrorResponse(req.ID, -32603, "failed to create task: "+err.Error()), nil
		}

		var progressToken any
		if envelope.Meta != nil {
			progressToken = envelope.Meta.ProgressToken
		}

		// Run the tool asynchronously.
		go func() {
			bgCtx := core.DetachForBackground(ctx)
			bgCtx, cancelFunc := context.WithCancel(bgCtx)

			// SEP-2663 input-request flow: each v2 task gets its own
			// inputState. TaskContext.TaskElicit / TaskSample stash pending
			// requests here; tasks/update delivers responses; tasks/get
			// snapshots them for the DetailedTask wire shape.
			inputState := newV2InputState()
			tc := &TaskContext{
				taskID:        taskID,
				sessionID:     sessionID,
				store:         store,
				inputState:    inputState,
				progressToken: progressToken,
			}
			bgCtx = WithTaskContext(bgCtx, tc)
			rt.register(taskID, envelope.Name, &activeTask{cancel: cancelFunc, inputState: inputState})

			defer func() {
				cancelFunc()
				rt.unregister(taskID)
				if r := recover(); r != nil {
					// Panic = protocol-level error in v2.
					msg := fmt.Sprintf("internal error: %v", r)
					te := &core.TaskError{Code: -32603, Message: msg}
					rt.setTaskError(taskID, te)
					store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(msg), msg)
					notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
				}
			}()

			resp, mwErr := next(bgCtx, req)

			// If middleware returned a transport-level error, treat it as a task failure.
			if mwErr != nil {
				te := &core.TaskError{Code: -32603, Message: mwErr.Error()}
				rt.setTaskError(taskID, te)
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(mwErr.Error()), mwErr.Error())
				notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
				return
			}

			// Check if already cancelled — StoreTerminalResult rejects terminal→terminal.
			if resp.Error != nil {
				// Protocol/framework error (e.g., middleware failure, marshaling bug).
				te := &core.TaskError{
					Code:    resp.Error.Code,
					Message: resp.Error.Message,
				}
				rt.setTaskError(taskID, te)
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(resp.Error.Message), resp.Error.Message)
				notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
				return
			}

			raw, err := json.Marshal(resp.Result)
			if err != nil {
				te := &core.TaskError{Code: -32603, Message: "failed to marshal tool result"}
				rt.setTaskError(taskID, te)
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult("failed to marshal tool result"), "")
				notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
				return
			}

			var toolResult core.ToolResult
			if err := json.Unmarshal(raw, &toolResult); err != nil {
				te := &core.TaskError{Code: -32603, Message: "failed to unmarshal tool result"}
				rt.setTaskError(taskID, te)
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult("failed to unmarshal tool result"), "")
				notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
				return
			}

			// v2 error semantics: tool execution errors are "completed" with isError:true.
			// Only protocol errors (resp.Error != nil) are "failed".
			store.StoreTerminalResult(taskID, sessionID, core.TaskCompleted, toolResult, "")
			notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
		}()

		// SEP-2243: stage Mcp-Name on the HTTP response so transports / proxies
		// / observability can route or log against the task id without parsing
		// the JSON body. No-op for non-HTTP transports (stdio, in-process).
		core.SetResponseHeader(ctx, mcpNameHeader, taskID)

		// Build the v2 wire envelope. SEP-2663 defines CreateTaskResult as
		// `Result & Task` — a flat intersection — so the task fields are
		// inlined alongside resultType, NOT nested under a "task" key.
		// MUST NOT carry result/error/inputRequests/requestState (SEP-2663 —
		// those belong on DetailedTask returned by tasks/get).
		wireTask := toTaskInfoV2(info)
		wireTask.TTLSeconds = core.IntPtr(ttlSec)
		return core.NewResponse(req.ID, core.CreateTaskResult{
			ResultType: core.ResultTypeTask,
			TaskInfoV2: wireTask,
		}), nil
	}
}

// mcpNameHeader is the SEP-2243 HTTP response header carrying the taskId
// when a task-creating tools/call response is returned. Transports surface
// it so downstream HTTP infrastructure (proxies, observability) can route
// or log against the task identifier without parsing the JSON body.
const mcpNameHeader = "Mcp-Name"

// --- Method Handlers ---

func makeV2GetHandler(rt *v2TaskRuntime) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID       string `json:"taskId"`
			RequestState string `json:"requestState,omitempty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		sid := ctx.SessionID()
		info, ok := rt.store.Get(p.TaskID, sid)
		if !ok {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
		}

		// SEP-2322: validate any echoed requestState — tampered, expired,
		// or cross-task tokens are rejected with -32602 so attackers can't
		// drive the loop with forged state. Empty is allowed (first call).
		if err := rt.verifyRequestState(p.RequestState, p.TaskID); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"invalid requestState: "+err.Error())
		}

		result := core.DetailedTask{
			TaskInfoV2: toTaskInfoV2(info),
			// SEP-2322 requestState — opaque session-continuation token. The
			// same helper feeds notifyV2TaskStatus so polling and SSE stay
			// in sync (HMAC-signed when RequestStateKey is configured).
			RequestState: rt.makeRequestState(p.TaskID),
		}

		// SEP-2663: when the task is awaiting MRTR input, surface the
		// pending requests on DetailedTask.InputRequests so the client
		// knows which keys to satisfy via tasks/update.
		if info.Status == core.TaskInputRequired {
			if state := rt.inputStateFor(p.TaskID); state != nil {
				result.InputRequests = state.snapshot()
			}
		}

		// Inline result or error depending on terminal status.
		if info.Status == core.TaskCompleted {
			toolResult, found := rt.store.GetResult(p.TaskID, sid)
			if found {
				// v2: No related-task _meta on tasks/get inlined results.
				if toolResult.Meta != nil {
					toolResult.Meta.RelatedTask = nil
					// If Meta is now empty, nil it out.
					if toolResult.Meta.RelatedTask == nil {
						toolResult.Meta = nil
					}
				}
				result.Result = &toolResult
			}
		} else if info.Status == core.TaskFailed {
			te := rt.getTaskError(p.TaskID)
			if te != nil {
				result.Error = te
			} else {
				// Fallback: construct from store's status message.
				result.Error = &core.TaskError{
					Code:    -32603,
					Message: info.StatusMessage,
				}
			}
		}

		return core.NewResponse(id, result)
	}
}

// makeV2UpdateHandler implements SEP-2663 tasks/update — the resume path for
// MRTR input rounds. The client supplies inputResponses keyed to the
// inputRequests previously surfaced via tasks/get's DetailedTask, optionally
// echoing requestState (SEP-2322) for stateless deployments.
//
// This Phase 4 implementation is a validating shell: it parses the request,
// confirms the task exists and is non-terminal, hands the responses to the
// runtime's deliveryInputResponses helper, and returns an empty ack
// (UpdateTaskResult{}) per SEP-2663. Phase 5 wires the actual delivery — for
// in-process tasks, that means matching keys to per-key channels on the
// taskEntry and unblocking the waiting goroutine; for external-backed tools
// (Temporal, Step Functions, SQS, ...), Phase 5 routes through a planned
// TaskCallbacks.OnInputResponse extension point so the proxy can forward the
// payload to the orchestrator. Either way, the handler shape stays the same.
func makeV2UpdateHandler(rt *v2TaskRuntime) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p core.UpdateTaskRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if p.TaskID == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "missing taskId")
		}

		sid := ctx.SessionID()
		info, ok := rt.store.Get(p.TaskID, sid)
		if !ok {
			// Open spec question (PLAN.md): tasks/update for unknown taskId
			// may end up as a silent ack. For now treat it as -32602 so
			// callers find out immediately; flip to silent if the spec
			// settles that way.
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
		}
		// SEP-2322: validate echoed requestState before doing any work —
		// the resume side of the MRTR loop is the most attractive target
		// for forged state since it directly drives goroutine wake-ups.
		if err := rt.verifyRequestState(p.RequestState, p.TaskID); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"invalid requestState: "+err.Error())
		}
		if info.Status.IsTerminal() {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"task "+p.TaskID+" is in terminal state "+string(info.Status))
		}

		// Hand off to the runtime. Phase 4 only logs/queues; Phase 5 wires
		// the actual per-key delivery and goroutine unblock.
		rt.deliverInputResponses(p.TaskID, p.InputResponses)

		return core.NewResponse(id, core.UpdateTaskResult{})
	}
}

func makeV2CancelHandler(rt *v2TaskRuntime) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID       string `json:"taskId"`
			RequestState string `json:"requestState,omitempty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		// SEP-2322: validate echoed requestState before mutating task state.
		if err := rt.verifyRequestState(p.RequestState, p.TaskID); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams,
				"invalid requestState: "+err.Error())
		}

		info, err := rt.store.Cancel(p.TaskID, ctx.SessionID())
		if err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		// Stop the background goroutine.
		rt.cancelTask(p.TaskID)

		// Send status notification with the v2 wire-format task. Clients learn
		// the new state via tasks/get; the cancel response itself is an empty
		// ack per SEP-2663.
		notifyV2TaskStatusFromInfo(ctx, rt, info)

		return core.NewResponse(id, core.CancelTaskResult{})
	}
}

// --- Helpers ---

// toTaskInfoV2 converts the internal TaskInfo (TTL stored as milliseconds) to
// the v2 wire shape (ttlSeconds, pollIntervalMilliseconds; no parentTaskId).
// nil TTL stays nil ("unlimited"); positive ms round down to whole seconds
// with a 1-second floor so sub-second TTLs don't collapse to "expired".
func toTaskInfoV2(info core.TaskInfo) core.TaskInfoV2 {
	out := core.TaskInfoV2{
		TaskID:        info.TaskID,
		Status:        info.Status,
		StatusMessage: info.StatusMessage,
		CreatedAt:     info.CreatedAt,
		LastUpdatedAt: info.LastUpdatedAt,
	}
	if info.TTL != nil && *info.TTL > 0 {
		sec := *info.TTL / 1000
		if sec <= 0 {
			sec = 1
		}
		out.TTLSeconds = &sec
	}
	if info.PollInterval > 0 {
		pi := info.PollInterval
		out.PollIntervalMilliseconds = &pi
	}
	return out
}

// notifyV2TaskStatus sends a v2-style status notification with the full
// DetailedTask (inlined result/error) read fresh from the store. Best-effort.
//
// SEP-2322: the notification carries the same requestState the next
// tasks/get would have minted. Clients update their tracked requestState
// from notifications so a stateless deployment can pick the conversation
// back up without an extra tasks/get round-trip.
//
// Wire fields: payload embeds TaskInfoV2, so the JSON keys are `ttlSeconds`
// and `pollIntervalMilliseconds` (renamed with units per pja-ant's accepted
// feedback). The spec's notification example in commit ed4c83e still shows
// the older `ttl`/`pollInterval` keys — that example is stale; the
// normative TaskInfo schema uses the renamed fields and our output matches.
//
// Stream routing (Streamable HTTP): the bgCtx passed in here was produced
// by core.DetachForBackground in taskV2Middleware, which swaps the dead
// POST-scoped notifyFunc for the session-level one wired by sseWiring on
// the persistent GET SSE stream. So notifications fan out on the client's
// long-lived GET SSE connection, NOT on any per-request POST SSE stream.
//
// The post-Apr-30 spec language ("MUST send it on an SSE stream associated
// with a tasks/get request") strictly read would require holding open the
// SSE response from a tasks/get POST and pushing here, which is a much
// larger transport change (per-task SSE-stream registry, tasks/get
// hold-open semantics). Tracked as mcpkit issue 346; current behavior
// matches what the v2-18 conformance test allows.
func notifyV2TaskStatus(ctx context.Context, rt *v2TaskRuntime, store TaskStore, taskID, sessionID string) {
	info, ok := store.Get(taskID, sessionID)
	if !ok {
		return
	}

	payload := core.DetailedTask{
		TaskInfoV2:   toTaskInfoV2(info),
		RequestState: rt.makeRequestState(taskID),
	}

	if info.Status == core.TaskCompleted {
		toolResult, found := store.GetResult(taskID, sessionID)
		if found {
			// Strip related-task _meta for v2.
			if toolResult.Meta != nil {
				toolResult.Meta.RelatedTask = nil
				if toolResult.Meta.RelatedTask == nil {
					toolResult.Meta = nil
				}
			}
			payload.Result = &toolResult
		}
	} else if info.Status == core.TaskFailed {
		te := rt.getTaskError(taskID)
		if te != nil {
			payload.Error = te
		}
	}

	core.Notify(ctx, "notifications/tasks/status", payload)
}

// notifyV2TaskStatusFromInfo sends a status notification through the live
// MethodContext for callers that already have fresh TaskInfo (e.g., the
// cancel handler, which gets info back from store.Cancel and doesn't need
// to re-read it).
//
// SEP-2322: carries requestState matching what the next tasks/get would
// return — same rationale as notifyV2TaskStatus.
func notifyV2TaskStatusFromInfo(ctx core.MethodContext, rt *v2TaskRuntime, info core.TaskInfo) {
	payload := core.DetailedTask{
		TaskInfoV2:   toTaskInfoV2(info),
		RequestState: rt.makeRequestState(info.TaskID),
	}
	if info.Status == core.TaskFailed {
		if te := rt.getTaskError(info.TaskID); te != nil {
			payload.Error = te
		}
	}
	ctx.Notify("notifications/tasks/status", payload)
}
