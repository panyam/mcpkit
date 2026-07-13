package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// Config holds the options for registering v2 tasks support on an MCP server.
type Config struct {
	// Store is the task state backend. If nil, an InMemoryTaskStore is used.
	Store server.TaskStore

	// Server is the MCP server to register tasks on.
	Server *server.Server

	// DefaultTTLMs is the default task TTL in integer milliseconds. Per
	// SEP-2663 the wire surfaces ttlMs and the store also uses ms, so this
	// value flows through unchanged. Default: 300000 (5 minutes).
	DefaultTTLMs int

	// DefaultPollMs is the suggested poll interval in milliseconds,
	// returned to clients in CreateTaskResult as pollIntervalMs. Default: 1000 (1 second).
	DefaultPollMs int

	// TracerProvider opts the runtime into SEP-414 P6 instrumentation of
	// the async task lifecycle (issue 659). When set, spawnGoAsyncTask
	// emits a `task.execute` span as a NEW root trace (so the long-running
	// task work doesn't render as a multi-hour child under a few-ms create
	// span) carrying a Link back to the originating tools/call create
	// span. Each tasks/get / tasks/update / tasks/cancel dispatch span
	// additionally Adds a Link to the originating create span so a backend
	// can pivot from any poll into the whole task lifecycle.
	//
	// Span attributes:
	//   - `mcp.task.id`     — task identifier
	//   - `mcp.task.status` — final status stamped at End (completed /
	//                         failed / cancelled / input_required)
	//
	// On the failure paths (handler error, protocol error, panic recover)
	// the span also gets RecordError so observability backends count it
	// as an error trace and surface the underlying message.
	//
	// Nil or core.NoopTracerProvider{} (the default) skips the install —
	// zero allocation, no spans emitted. ext/tasks depends on the core
	// abstraction only; no compile-time dep on ext/otel.
	TracerProvider core.TracerProvider
}

func (c *Config) defaults() {
	if c.Store == nil {
		c.Store = server.NewInMemoryStore()
	}
	if c.DefaultTTLMs <= 0 {
		c.DefaultTTLMs = 300000
	}
	if c.DefaultPollMs <= 0 {
		c.DefaultPollMs = 1000
	}
	if c.TracerProvider == nil {
		c.TracerProvider = core.NoopTracerProvider{}
	}
}

// v2TaskRuntime holds per-registration state for v2 tasks, shared between
// middleware and handlers. Scoped to a single Register call (C3).
type v2TaskRuntime struct {
	store    server.TaskStore
	registry *server.Registry

	// tp is the optional SEP-414 P6 TracerProvider (issue 659). Defaulted
	// to core.NoopTracerProvider{} by Config.defaults() so call sites can
	// unconditionally StartSpanLinked / StartSpan without nil-checking.
	tp core.TracerProvider

	mu sync.Mutex
	active   map[string]*activeTask
	// taskErrors stores protocol-level errors (TaskError) for failed tasks.
	// Separate from the store's result, which holds ToolResult for completed tasks.
	taskErrors map[string]*core.TaskError
	// creatorToolForTask maps taskID → tool name for callback dispatch.
	creatorToolForTask map[string]string
	// originTC stashes the originating tools/call create-span's trace
	// context per task. Survives terminal transition (the entry is NOT
	// cleared in unregister) so polls landing on a finished task still
	// AddLink back to the original create span. Matches the lifetime of
	// taskErrors / creatorToolForTask — same coarse "outlives the
	// goroutine" bucket of post-terminal lookups.
	originTC map[string]core.TraceContext
}

func newV2TaskRuntime(cfg Config) *v2TaskRuntime {
	return &v2TaskRuntime{
		store:              cfg.Store,
		registry:           cfg.Server.Registry(),
		tp:                 cfg.TracerProvider,
		active:             make(map[string]*activeTask),
		taskErrors:         make(map[string]*core.TaskError),
		creatorToolForTask: make(map[string]string),
		originTC:           make(map[string]core.TraceContext),
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

// setOrigin records the originating tools/call create-span's trace
// identity for taskID. Called once from spawnGoAsyncTask BEFORE the
// background goroutine starts, so the goroutine and any later
// tasks/get / tasks/update / tasks/cancel handler can reach it.
func (rt *v2TaskRuntime) setOrigin(taskID string, tc core.TraceContext) {
	rt.mu.Lock()
	rt.originTC[taskID] = tc
	rt.mu.Unlock()
}

// getOrigin returns the originating create-span trace context for
// taskID, or a zero TraceContext when the task was never recorded
// (sync-as-completed path, unknown ID, or pre-tracing-install task).
// Adapters silently drop zero links so callers don't have to pre-filter.
func (rt *v2TaskRuntime) getOrigin(taskID string) core.TraceContext {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.originTC[taskID]
}

func (rt *v2TaskRuntime) cancelTask(taskID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if at := rt.active[taskID]; at != nil && at.cancel != nil {
		at.cancel()
	}
}

func (rt *v2TaskRuntime) getToolCallbacks(taskID string) *server.TaskCallbacks {
	rt.mu.Lock()
	name := rt.creatorToolForTask[taskID]
	rt.mu.Unlock()
	if name == "" || rt.registry == nil {
		return nil
	}
	return rt.registry.ToolCallbacks(name)
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
// route through here once a planned server.TaskCallbacks.OnInputResponse field
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
func (s *v2InputState) Enqueue(methodPrefix string, req core.InputRequest) (key string, waiter <-chan json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	key = fmt.Sprintf("%s-%d", methodPrefix, s.counter)
	ch := make(chan json.RawMessage, 1)
	s.pending[key] = &v2InputWait{request: req, waiter: ch}
	return key, ch
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

// HasPending reports whether any input requests are still awaiting a
// tasks/update response. Used by requestInputV2 to keep the task in
// input_required while a fan-out tool has only been partially answered.
func (s *v2InputState) HasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending) > 0
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
// Tasks extension. Register hands an instance to Server.RegisterExtension
// so the extension is advertised in capabilities.extensions during initialize.
type tasksExtensionProvider struct{}

// Extension declares the SEP-2663 Tasks extension.
//
//nolint:unused // referenced by Register only when the v2 path is wired
func (tasksExtensionProvider) Extension() core.Extension {
	return core.Extension{
		ID:          core.TasksExtensionID,
		SpecVersion: "draft", // SEP-2663 is in draft
		Stability:   core.Experimental,
	}
}

// Register hooks up v2 tasks support on the given server:
//   - Advertises the io.modelcontextprotocol/tasks extension in initialize
//     (replacing the v1 ServerCapabilities.Tasks declaration).
//   - Installs middleware that intercepts tools/call for task-eligible tools
//     (server-directed, no client task param needed). Task creation is
//     gated on the client supporting the extension — either at session
//     level (initialize handshake) or per-request (SEP-2575 _meta).
//   - Registers tasks/get, tasks/update, and tasks/cancel handlers, gated
//     on session-level extension support; otherwise the handlers return
//     -32021 (Missing Required Client Capability, SEP-2575) with a
//     machine-readable `requiredCapabilities` payload so unsupported
//     clients can self-describe what to add.
//   - Does NOT register tasks/result or tasks/list (removed in v2).
//   - Does NOT call SetTasksCap — v2 tasks live under capabilities.extensions,
//     not the v1 ServerCapabilities.Tasks slot.
//
// Must be called before accepting connections.
func Register(cfg Config) {
	srv := cfg.Server
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
// -32021 (Missing Required Client Capability, SEP-2575) instead of the real
// handler's response. The error data carries the same `requiredCapabilities`
// shape the required-task middleware emits, so a client that hits the
// gate can self-describe what to add and retry.
//
// Capability sourcing is two-layered: session-level declaration (legacy
// initialize handshake) OR per-request _meta.io.modelcontextprotocol/
// clientCapabilities override (SEP-2575 stateless wire). The latter is the
// only path available on the stateless wire — there is no initialize
// handshake to seed session caps from — so without it tasks/* would always
// emit -32021 on the stateless wire even when the client did declare the
// extension per-request.
func gateOnTasksExtension(inner server.MethodHandler) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var envelope struct {
			Meta *struct {
				ClientCapabilitiesRaw json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities,omitempty"`
			} `json:"_meta,omitempty"`
		}
		var perRequestCapsRaw json.RawMessage
		if err := json.Unmarshal(params, &envelope); err == nil && envelope.Meta != nil {
			perRequestCapsRaw = envelope.Meta.ClientCapabilitiesRaw
		}

		if !core.ClientSupportsExtensionForRequest(ctx, core.TasksExtensionID, perRequestCapsRaw) {
			return core.NewErrorResponseWithData(
				id,
				core.ErrCodeMissingRequiredClientCapability,
				"method requires extension "+core.TasksExtensionID,
				map[string]any{
					"requiredCapabilities": map[string]any{
						"extensions": map[string]any{
							core.TasksExtensionID: map[string]any{},
						},
					},
				},
			)
		}
		return inner(ctx, id, params)
	}
}

// --- server.Middleware ---

// taskV2Middleware intercepts tools/call requests. In v2, the server decides
// whether to create a task based on the tool's configuration AND on what the
// handler returns — the client does NOT send a `task` param.
//
// Flow (per the SEP-2663 + SEP-2322 composition contract):
//
//  1. taskSupport=forbidden/absent → pass through (sync execution).
//  2. taskSupport=optional/required + client hasn't negotiated the tasks
//     extension → -32021 for required, sync fallback for optional.
//  3. Otherwise run the handler synchronously via next() FIRST, then dispatch
//     on the returned result:
//     - core.InputRequiredResult → MRTR round; return as-is, no task created.
//       Lets a handler gather input via the MRTR loop and only later switch
//       to async (SEP-2663 451f5e1).
//     - core.ToolResult with GoAsync=true → mint a task, spawn a continuation
//       goroutine that re-invokes the handler with a [TaskContext] attached,
//       and return CreateTaskResult to the client. The handler's second
//       invocation detects the TaskContext via [GetTaskContext] and runs the
//       async branch (typically the "expensive work").
//     - core.ToolResult (sync return, no GoAsync) → mint a task that is born
//       terminal: StoreTerminalResult with TaskCompleted, fire one
//       notifications/tasks event, and return CreateTaskResult. No goroutine
//       runs. SEP-2663 G6 (no progress/log on task streams) only applies to
//       the goroutine continuation, since that's where the session-notify
//       filter is installed.
func taskV2Middleware(reg *server.Registry, rt *v2TaskRuntime, cfg Config) server.Middleware {
	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) (*core.Response, error) {
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
		if err := req.Params.Bind(&envelope); err != nil {
			return next(ctx, req)
		}

		def, toolFound := reg.ToolDef(envelope.Name)
		if !toolFound {
			return next(ctx, req)
		}

		// Determine effective taskSupport first. Absent Execution = forbidden.
		// In v2 this is a server-internal hint about which tools may be
		// surfaced as tasks — clients no longer opt in via a `task` param.
		effectiveSupport := core.TaskSupportForbidden
		if def.Execution != nil {
			effectiveSupport = def.Execution.TaskSupport
		}

		// forbidden/absent: pass through (sync), regardless of extension state.
		if effectiveSupport == core.TaskSupportForbidden {
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
			// Per the merged SEP-2663: if the server cannot service the
			// request without returning CreateTaskResult — i.e. the tool's
			// TaskSupport is `required` — it MUST return -32021 with a
			// machine-readable `requiredCapabilities` payload so the client
			// can self-describe what to add. For `optional`, the server still
			// CAN service the request without a task, so falling through to
			// sync remains correct.
			if effectiveSupport == core.TaskSupportRequired {
				return core.NewErrorResponseWithData(
					req.ID,
					core.ErrCodeMissingRequiredClientCapability,
					"client must declare extension "+core.TasksExtensionID,
					map[string]any{
						"requiredCapabilities": map[string]any{
							"extensions": map[string]any{
								core.TasksExtensionID: map[string]any{},
							},
						},
					},
				), nil
			}
			return next(ctx, req)
		}

		// SEP-2322 + SEP-2663 composition: run the handler synchronously so we
		// can observe whether it wants to stay in an MRTR loop, finish sync,
		// or escalate to a background task. This is the key inversion vs the
		// pre-Option-2 "create task up-front, run handler in goroutine" shape
		// — that pattern could not surface a round-1 InputRequiredResult on a
		// task-eligible tool, because the task was already minted before the
		// handler ran. Tracked / locked in on mcpkit#347.
		resp, mwErr := next(ctx, req)
		if mwErr != nil {
			return resp, mwErr
		}
		if resp == nil || resp.Error != nil {
			return resp, nil
		}

		switch r := resp.Result.(type) {
		case core.InputRequiredResult:
			// MRTR round — let the dispatcher's reshaped response flow back
			// to the client unchanged. No task is created during MRTR rounds;
			// task creation happens (if at all) on a later round when the
			// handler returns GoAsyncResult or a final sync result.
			return resp, nil

		case core.GoAsyncResult:
			var progressToken any
			if envelope.Meta != nil {
				progressToken = envelope.Meta.ProgressToken
			}
			return spawnGoAsyncTask(ctx, req, next, rt, cfg, envelope.Name, progressToken)

		case core.ToolResult:
			return wrapSyncAsCompletedTask(ctx, req, rt, cfg, r)

		default:
			// Some other result shape (e.g. an upstream middleware already
			// produced a CreateTaskResult, or a non-tool response sneaks
			// through). Pass through unchanged.
			_ = r
			return resp, nil
		}
	}
}

// spawnGoAsyncTask mints a v2 task, spawns the continuation goroutine that
// re-invokes the handler with TaskContext attached, and returns the
// CreateTaskResult envelope to the original caller.
//
// The handler is expected to be a state machine that detects the TaskContext
// on re-invocation (via [GetTaskContext]) and runs its async branch — e.g.
// the long-running work that was deferred after the MRTR loop completed.
func spawnGoAsyncTask(
	ctx context.Context,
	req *core.Request,
	next server.MiddlewareFunc,
	rt *v2TaskRuntime,
	cfg Config,
	toolName string,
	progressToken any,
) (*core.Response, error) {
	taskID := generateTaskID()
	now := time.Now().UTC().Format(time.RFC3339)
	ttlMs := cfg.DefaultTTLMs
	pollMs := cfg.DefaultPollMs

	info := core.TaskInfo{
		TaskID:        taskID,
		Status:        core.TaskWorking,
		CreatedAt:     now,
		LastUpdatedAt: now,
		TTL:           core.IntPtr(ttlMs),
		PollInterval:  pollMs,
	}
	store := rt.store
	sessionID := core.TaskBucketKey(ctx)
	if err := store.Create(info, sessionID); err != nil {
		return core.NewErrorResponse(req.ID, -32603, "failed to create task: "+err.Error()), nil
	}

	// Capture the originating tools/call dispatch span identity BEFORE
	// the goroutine spawns — ctx still carries the create span as its
	// active trace context. Stash on the runtime so the goroutine
	// (task.execute) and any later tasks/get / update / cancel handler
	// can build a Link back to it. A zero originTC is fine: adapters
	// silently drop zero-traceparent links downstream.
	originTC := core.TraceContextFromContext(ctx)
	rt.setOrigin(taskID, originTC)

	go func() {
		bgCtx := core.DetachForBackground(ctx)
		bgCtx, cancelFunc := context.WithCancel(bgCtx)

		// SEP-414 P6 (issue 659): task.execute is a NEW root trace
		// linked back to the create span, NOT a child. The work runs
		// long after the create span ends — nesting it would render as
		// a multi-hour child under a few-ms parent in observability
		// backends. WithNewRootSpan tells the adapter to scrub any
		// inherited parent (the create span's OTel span context
		// preserved by context.WithoutCancel inside DetachForBackground)
		// before StartSpanLinked. The link keeps the lifecycle
		// navigable across the boundary.
		bgCtx = core.WithNewRootSpan(bgCtx)
		var links []core.Link
		if !originTC.IsZero() {
			links = []core.Link{core.LinkFromTraceContext(originTC)}
		}
		bgCtx, taskSpan := core.StartSpanLinked(rt.tp, bgCtx, "task.execute", links,
			core.Attribute{Key: "mcp.task.id", Value: taskID},
		)

		// SEP-2663 input-request flow: each v2 task gets its own inputState.
		// TaskContext.TaskElicit / TaskSample stash pending requests here;
		// tasks/update delivers responses; tasks/get snapshots them for the
		// DetailedTask wire shape. Per the spec separation rules, this
		// inputState is scoped to the task lifetime — NOT carried over from
		// the preceding MRTR phase's inputResponses.
		inputState := newV2InputState()
		tc := &TaskContext{
			taskID:        taskID,
			sessionID:     sessionID,
			store:         store,
			inputState:    inputState,
			progressToken: progressToken,
		}
		bgCtx = WithTaskContext(bgCtx, tc)
		// SEP-2663 G6: notifications/progress and notifications/message MUST
		// NOT be sent on tasks. Filter at the session-notify boundary so any
		// tool handler that calls EmitProgress or EmitLog while it happens to
		// be running as the GoAsync continuation silently no-ops rather than
		// leaking onto the session stream.
		bgCtx = core.ApplySessionNotifyFilter(bgCtx,
			"notifications/progress",
			"notifications/message",
		)
		rt.register(taskID, toolName, &activeTask{cancel: cancelFunc, inputState: inputState})

		defer func() {
			cancelFunc()
			rt.unregister(taskID)
			if rec := recover(); rec != nil {
				msg := fmt.Sprintf("internal error: %v", rec)
				te := &core.TaskError{Code: -32603, Message: msg}
				rt.setTaskError(taskID, te)
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(msg), msg)
				notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
				taskSpan.RecordError(fmt.Errorf("%s", msg))
			}
			// Stamp final status from whatever the store ended up with —
			// covers every terminal path (completed / failed / cancelled)
			// without the defer having to know which branch ran. Then End
			// once. Safe even when the goroutine exits before any store
			// transition (the task stays Working in the store; we record
			// that and downstream backends see the abandoned span).
			if info, ok := store.Get(taskID, sessionID); ok {
				taskSpan.SetAttribute("mcp.task.status", string(info.Status))
			}
			taskSpan.End()
		}()

		resp, mwErr := next(bgCtx, req)
		if mwErr != nil {
			te := &core.TaskError{Code: -32603, Message: mwErr.Error()}
			rt.setTaskError(taskID, te)
			store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(mwErr.Error()), mwErr.Error())
			notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
			taskSpan.RecordError(mwErr)
			return
		}
		if resp.Error != nil {
			te := &core.TaskError{Code: resp.Error.Code, Message: resp.Error.Message}
			rt.setTaskError(taskID, te)
			store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(resp.Error.Message), resp.Error.Message)
			notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
			taskSpan.RecordError(fmt.Errorf("%s", resp.Error.Message))
			return
		}

		// The continuation handler is expected to return a sync ToolResult on
		// its final invocation. Other ToolResponse variants are programmer
		// errors at this point — they'd indicate the handler is trying to
		// re-enter the MRTR loop or re-spawn from inside an async task, which
		// SEP-2663 does not allow. We surface them as protocol failures.
		toolResult, ok := resp.Result.(core.ToolResult)
		if !ok {
			msg := fmt.Sprintf("tool returned unexpected %T inside async continuation; expected core.ToolResult", resp.Result)
			te := &core.TaskError{Code: -32603, Message: msg}
			rt.setTaskError(taskID, te)
			store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(msg), msg)
			notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
			taskSpan.RecordError(fmt.Errorf("%s", msg))
			return
		}

		// v2 error semantics: tool execution errors are "completed" with
		// isError:true. Only protocol errors (resp.Error != nil) are "failed".
		store.StoreTerminalResult(taskID, sessionID, core.TaskCompleted, toolResult, "")
		notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)
	}()

	core.SetResponseHeader(ctx, mcpNameHeader, taskID)
	wireTask := toTaskInfoV2(info)
	wireTask.TTLMs = core.IntPtr(ttlMs)
	return core.NewResponse(req.ID, core.CreateTaskResult{
		ResultType: core.ResultTypeTask,
		TaskInfoV2: wireTask,
	}), nil
}

// wrapSyncAsCompletedTask handles the case where the handler returned a sync
// ToolResult without the GoAsync sentinel. The task is born terminal: we mint
// it, immediately StoreTerminalResult with TaskCompleted, fire one
// notifications/tasks event on the session-level stream, and return the
// CreateTaskResult envelope. No goroutine ever runs; the work is already done.
func wrapSyncAsCompletedTask(
	ctx context.Context,
	req *core.Request,
	rt *v2TaskRuntime,
	cfg Config,
	result core.ToolResult,
) (*core.Response, error) {
	taskID := generateTaskID()
	now := time.Now().UTC().Format(time.RFC3339)
	ttlMs := cfg.DefaultTTLMs
	pollMs := cfg.DefaultPollMs

	// Create in TaskWorking so the immediately-following StoreTerminalResult
	// can transition to completed and persist the result; the store rejects
	// terminal→terminal transitions to guard against cancel/complete races.
	info := core.TaskInfo{
		TaskID:        taskID,
		Status:        core.TaskWorking,
		CreatedAt:     now,
		LastUpdatedAt: now,
		TTL:           core.IntPtr(ttlMs),
		PollInterval:  pollMs,
	}
	store := rt.store
	sessionID := core.TaskBucketKey(ctx)
	if err := store.Create(info, sessionID); err != nil {
		return core.NewErrorResponse(req.ID, -32603, "failed to create task: "+err.Error()), nil
	}
	if err := store.StoreTerminalResult(taskID, sessionID, core.TaskCompleted, result, ""); err != nil {
		return core.NewErrorResponse(req.ID, -32603, "failed to store sync result: "+err.Error()), nil
	}
	// Reflect the just-stored terminal status in the wire envelope so the
	// CreateTaskResult shows status="completed" instead of the transient
	// "working" we used to bootstrap the store entry.
	info.Status = core.TaskCompleted

	// Route the lifecycle notification through the session-level GET SSE
	// stream so clients holding a long-lived listener still see the
	// terminal transition (matches the GoAsync path).
	bgCtx := core.DetachForBackground(ctx)
	notifyV2TaskStatus(bgCtx, rt, store, taskID, sessionID)

	core.SetResponseHeader(ctx, mcpNameHeader, taskID)
	wireTask := toTaskInfoV2(info)
	wireTask.TTLMs = core.IntPtr(ttlMs)
	return core.NewResponse(req.ID, core.CreateTaskResult{
		ResultType: core.ResultTypeTask,
		TaskInfoV2: wireTask,
	}), nil
}

// mcpNameHeader is the SEP-2243 HTTP response header carrying the taskId
// when a task-creating tools/call response is returned. Transports surface
// it so downstream HTTP infrastructure (proxies, observability) can route
// or log against the task identifier without parsing the JSON body.
const mcpNameHeader = "Mcp-Name"

// --- Method Handlers ---

func makeV2GetHandler(rt *v2TaskRuntime) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		// SEP-414 P6: link this dispatch span back to the originating
		// tools/call create span so a backend can pivot from any poll
		// into the task's whole lifecycle. SpanFromContext returns the
		// dispatch span when TracerProvider is configured, a no-op span
		// otherwise; AddLink with a zero origin is silently dropped.
		core.SpanFromContext(ctx).AddLink(core.LinkFromTraceContext(rt.getOrigin(p.TaskID)))

		sid := core.TaskBucketKey(ctx)
		info, ok := rt.store.Get(p.TaskID, sid)
		if !ok {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
		}

		result := core.DetailedTask{
			TaskInfoV2: toTaskInfoV2(info),
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
// inputRequests previously surfaced via tasks/get's DetailedTask.
//
// This Phase 4 implementation is a validating shell: it parses the request,
// confirms the task exists and is non-terminal, hands the responses to the
// runtime's deliveryInputResponses helper, and returns an empty ack
// (UpdateTaskResult{}) per SEP-2663. Phase 5 wires the actual delivery — for
// in-process tasks, that means matching keys to per-key channels on the
// taskEntry and unblocking the waiting goroutine; for external-backed tools
// (Temporal, Step Functions, SQS, ...), Phase 5 routes through a planned
// server.TaskCallbacks.OnInputResponse extension point so the proxy can forward the
// payload to the orchestrator. Either way, the handler shape stays the same.
func makeV2UpdateHandler(rt *v2TaskRuntime) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p core.UpdateTaskRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		if p.TaskID == "" {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "missing taskId")
		}

		// SEP-414 P6: link dispatch span back to the create span. See
		// makeV2GetHandler for the rationale.
		core.SpanFromContext(ctx).AddLink(core.LinkFromTraceContext(rt.getOrigin(p.TaskID)))

		sid := core.TaskBucketKey(ctx)
		info, ok := rt.store.Get(p.TaskID, sid)
		if !ok {
			// Open spec question (PLAN.md): tasks/update for unknown taskId
			// may end up as a silent ack. For now treat it as -32602 so
			// callers find out immediately; flip to silent if the spec
			// settles that way.
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
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

func makeV2CancelHandler(rt *v2TaskRuntime) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		// SEP-414 P6: link dispatch span back to the create span. See
		// makeV2GetHandler for the rationale.
		core.SpanFromContext(ctx).AddLink(core.LinkFromTraceContext(rt.getOrigin(p.TaskID)))

		// SEP-2663 cancel is idempotent — if the task is already terminal,
		// return the same empty ack as on an active task so clients don't
		// have to race observation vs cancel. Skip the store mutation in
		// that case.
		if info, ok := rt.store.Get(p.TaskID, core.TaskBucketKey(ctx)); ok && info.Status.IsTerminal() {
			return core.NewResponse(id, core.CancelTaskResult{})
		}

		info, err := rt.store.Cancel(p.TaskID, core.TaskBucketKey(ctx))
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

// toTaskInfoV2 converts the internal TaskInfo to the v2 wire shape (ttlMs,
// pollIntervalMs, no parentTaskId). The store uses milliseconds internally
// and the wire surface uses milliseconds, so this is a pass-through with
// no unit conversion. nil TTL stays nil ("unlimited") per SEP-2663.
func toTaskInfoV2(info core.TaskInfo) core.TaskInfoV2 {
	out := core.TaskInfoV2{
		TaskID:        info.TaskID,
		Status:        info.Status,
		StatusMessage: info.StatusMessage,
		CreatedAt:     info.CreatedAt,
		LastUpdatedAt: info.LastUpdatedAt,
	}
	if info.TTL != nil && *info.TTL > 0 {
		ms := *info.TTL
		out.TTLMs = &ms
	}
	if info.PollInterval > 0 {
		pi := info.PollInterval
		out.PollIntervalMs = &pi
	}
	return out
}

// notifyV2TaskStatus sends a v2-style status notification with the full
// DetailedTask (inlined result/error) read fresh from the store. Best-effort.
//
// Wire fields: payload embeds TaskInfoV2, so the JSON keys are `ttlMs` and
// `pollIntervalMs`. All duration fields are integer milliseconds (SEP-2663).
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
func notifyV2TaskStatus(ctx context.Context, rt *v2TaskRuntime, store server.TaskStore, taskID, sessionID string) {
	info, ok := store.Get(taskID, sessionID)
	if !ok {
		return
	}

	payload := core.DetailedTask{
		TaskInfoV2: toTaskInfoV2(info),
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

	core.Notify(ctx, "notifications/tasks", payload)
}

// notifyV2TaskStatusFromInfo sends a status notification through the live
// MethodContext for callers that already have fresh TaskInfo (e.g., the
// cancel handler, which gets info back from store.Cancel and doesn't need
// to re-read it).
func notifyV2TaskStatusFromInfo(ctx core.MethodContext, rt *v2TaskRuntime, info core.TaskInfo) {
	payload := core.DetailedTask{
		TaskInfoV2: toTaskInfoV2(info),
	}
	if info.Status == core.TaskFailed {
		if te := rt.getTaskError(info.TaskID); te != nil {
			payload.Error = te
		}
	}
	ctx.Notify("notifications/tasks", payload)
}
