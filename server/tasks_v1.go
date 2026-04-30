package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Config holds the options for registering tasks support on an MCP server.
type TasksConfigV1 struct {
	// Store is the task state backend. If nil, an InMemoryTaskStore is used.
	Store TaskStore

	// MessageQueue is the per-task FIFO queue for side-channel messages
	// (elicitation/sampling requests delivered via tasks/result long-poll).
	// If nil, an InMemoryMessageQueue is used.
	MessageQueue TaskMessageQueue

	// Server is the MCP server to register tasks on.
	Server *Server

	// DefaultTTLMs is the default task TTL in milliseconds. Tasks are
	// cleaned up after this duration. Default: 300000 (5 minutes).
	DefaultTTLMs int

	// DefaultPollMs is the suggested poll interval in milliseconds,
	// returned to clients in CreateTaskResult. Default: 1000 (1 second).
	DefaultPollMs int

	// MaxQueueSize is the maximum number of messages per task queue.
	// 0 means unbounded.
	MaxQueueSize int
}

func (c *TasksConfigV1) defaults() {
	if c.Store == nil {
		c.Store = NewInMemoryStore()
	}
	if c.MessageQueue == nil {
		c.MessageQueue = NewInMemoryMessageQueue()
	}
	if c.DefaultTTLMs <= 0 {
		c.DefaultTTLMs = 300_000
	}
	if c.DefaultPollMs <= 0 {
		c.DefaultPollMs = 1000
	}
}

// activeTask holds per-task runtime state for a running async task (C2: consolidated struct).
type activeTask struct {
	requests chan sideChannelRequest // read by tasks/result handler for side-channel proxying
	cancel   context.CancelFunc     // cancels the background goroutine's context (Phase 5)
}

// taskRuntime holds the per-registration state shared between the middleware
// and the tasks/* method handlers. Scoped to a single Register() call —
// no package-level globals.
type taskRuntime struct {
	store    TaskStore
	queue    TaskMessageQueue
	registry *Registry // for looking up per-tool TaskCallbacks
	mu       sync.Mutex
	active   map[string]*activeTask
	creatorToolForTask map[string]string // taskID → tool name (for callback dispatch)
}

func newTaskRuntime(store TaskStore, queue TaskMessageQueue, reg *Registry) *taskRuntime {
	return &taskRuntime{
		store:    store,
		queue:    queue,
		registry: reg,
		active:   make(map[string]*activeTask),
		creatorToolForTask: make(map[string]string),
	}
}

// register stores the active task entry and its tool name.
func (rt *taskRuntime) register(taskID, tool string, at *activeTask) {
	rt.mu.Lock()
	rt.active[taskID] = at
	rt.creatorToolForTask[taskID] = tool
	rt.mu.Unlock()
}

// unregister removes the active task entry. The creatorToolForTask mapping
// is kept so that tasks/get and tasks/result can still dispatch to per-tool
// callbacks after the background goroutine finishes (matching TS SDK
// behavior where getTaskResult cleans up only after the handler resolves).
func (rt *taskRuntime) unregister(taskID string) {
	rt.mu.Lock()
	delete(rt.active, taskID)
	rt.mu.Unlock()
}

// cleanupToolName removes the creatorToolForTask mapping. Called after
// tasks/result handler resolves for a terminal task.
func (rt *taskRuntime) cleanupToolName(taskID string) {
	rt.mu.Lock()
	delete(rt.creatorToolForTask, taskID)
	rt.mu.Unlock()
}

// getToolCallbacks returns the TaskCallbacks for the tool that created
// the given task, or nil if no callbacks are registered.
func (rt *taskRuntime) getToolCallbacks(taskID string) *TaskCallbacks {
	rt.mu.Lock()
	name := rt.creatorToolForTask[taskID]
	rt.mu.Unlock()
	if name == "" || rt.registry == nil {
		return nil
	}
	return rt.registry.ToolCallbacks(name)
}

// getChannel returns the side-channel request channel for a task, or nil.
func (rt *taskRuntime) getChannel(taskID string) chan sideChannelRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if at := rt.active[taskID]; at != nil {
		return at.requests
	}
	return nil
}

// cancelTask cancels the background goroutine for a task (Phase 5).
func (rt *taskRuntime) cancelTask(taskID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if at := rt.active[taskID]; at != nil && at.cancel != nil {
		at.cancel()
	}
}

// Register hooks up tasks support on the given server:
//   - Installs middleware that intercepts tools/call for task-eligible requests
//   - Registers tasks/get, tasks/result, tasks/list, tasks/cancel handlers
//   - Advertises the tasks capability in the initialize response
//
// Must be called before accepting connections.
func RegisterTasksV1(cfg TasksConfigV1) {
	cfg.defaults()
	srv := cfg.Server
	store := cfg.Store
	reg := srv.Registry()
	rt := newTaskRuntime(store, cfg.MessageQueue, reg)

	// Install middleware for tools/call interception.
	srv.UseMiddleware(taskMiddleware(reg, rt, cfg))

	// Register tasks/* protocol methods.
	srv.HandleMethod("tasks/get", makeGetHandler(rt))
	srv.HandleMethod("tasks/result", makeResultHandler(rt))
	srv.HandleMethod("tasks/list", makeListHandler(store))
	srv.HandleMethod("tasks/cancel", makeCancelHandler(rt))

	// Advertise tasks capability with nested structure per spec.
	srv.SetTasksCap(&core.TasksCap{
		List:   &core.TasksCapMethod{},
		Cancel: &core.TasksCapMethod{},
		Requests: &core.TasksCapRequests{
			Tools: &core.TasksCapToolsMethods{
				Call: &core.TasksCapMethod{},
			},
		},
	})
}

// --- Middleware ---

// taskMiddleware intercepts tools/call requests. When the client sends a task
// hint at params.task (per MCP spec 2025-11-25) and the tool supports tasks,
// the middleware creates a task, runs the tool asynchronously, and returns
// CreateTaskResult immediately.
func taskMiddleware(reg *Registry, rt *taskRuntime, cfg TasksConfigV1) Middleware {
	return func(ctx context.Context, req *core.Request, next MiddlewareFunc) (*core.Response, error) {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		// Parse the envelope to extract tool name, task hint, and progressToken.
		var envelope struct {
			Name string    `json:"name"`
			Task *taskHint `json:"task"`
			Meta *struct {
				ProgressToken any `json:"progressToken"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return next(ctx, req) // let dispatch handle the parse error
		}

		// Look up the tool to check its Execution.TaskSupport.
		def, toolFound := reg.ToolDef(envelope.Name)

		// Determine effective taskSupport. Per spec: absent Execution = forbidden.
		effectiveSupport := core.TaskSupportForbidden
		if toolFound && def.Execution != nil {
			effectiveSupport = def.Execution.TaskSupport
		}

		if envelope.Task == nil {
			// No task hint. Check if tool requires tasks.
			if effectiveSupport == core.TaskSupportRequired {
				return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound,
					fmt.Sprintf("tool %q requires task invocation (execution.taskSupport=required); include 'task' in params", envelope.Name)), nil
			}
			return next(ctx, req)
		}

		// Task hint present.
		if !toolFound {
			return next(ctx, req) // let dispatch handle unknown tool
		}

		// Forbidden or absent Execution with hint → error per spec.
		if effectiveSupport == core.TaskSupportForbidden {
			return core.NewErrorResponse(req.ID, core.ErrCodeMethodNotFound,
				fmt.Sprintf("tool %q does not support task invocation", envelope.Name)), nil
		}

		// Tool supports tasks (optional or required with hint present).
		taskID := generateTaskID()
		now := time.Now().UTC().Format(time.RFC3339)

		ttlMs := cfg.DefaultTTLMs
		if envelope.Task.TTL > 0 {
			ttlMs = envelope.Task.TTL
		}
		pollMs := cfg.DefaultPollMs
		if envelope.Task.PollInterval > 0 {
			pollMs = envelope.Task.PollInterval
		}

		info := core.TaskInfo{
			TaskID:        taskID,
			Status:        core.TaskWorking,
			CreatedAt:     now,
			LastUpdatedAt: now,
			TTL:           core.IntPtr(ttlMs),
			PollInterval:  pollMs,
		}
		store := rt.store
		sessionID := core.GetSessionID(ctx)
		if err := store.Create(info, sessionID); err != nil {
			return core.NewErrorResponse(req.ID, -32603, "failed to create task: "+err.Error()), nil
		}

		// Extract progressToken from _meta for the background goroutine (Phase 7c).
		var progressToken any
		if envelope.Meta != nil {
			progressToken = envelope.Meta.ProgressToken
		}

		// Run the tool asynchronously. context.WithCancel (Phase 5) so
		// Cancel() can stop the goroutine.
		go func() {
			bgCtx := core.DetachForBackground(ctx)
			bgCtx, cancelFunc := context.WithCancel(bgCtx)

			reqCh := make(chan sideChannelRequest, 1)
			tc := &TaskContext{
				taskID:        taskID,
				sessionID:     sessionID,
				store:         store,
				requests:      reqCh,
				progressToken: progressToken,
			}
			bgCtx = WithTaskContext(bgCtx, tc)
			rt.register(taskID, envelope.Name, &activeTask{requests: reqCh, cancel: cancelFunc})

			defer func() {
				cancelFunc()
				rt.unregister(taskID)
				if r := recover(); r != nil {
					msg := fmt.Sprintf("panic: %v", r)
					// Use StoreTerminalResult (Phase 4) — atomic + terminal guard.
					store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(msg), msg)
					notifyTaskStatus(bgCtx, store, taskID, sessionID)
				}
			}()

			resp, mwErr := next(bgCtx, req)

			// If middleware returned a transport-level error, treat it as task failure.
			if mwErr != nil {
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(mwErr.Error()), mwErr.Error())
				notifyTaskStatus(bgCtx, store, taskID, sessionID)
				return
			}

			// If the task was already cancelled (Phase 5), StoreTerminalResult
			// will reject the transition (terminal guard).
			if resp.Error != nil {
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult(resp.Error.Message), resp.Error.Message)
				notifyTaskStatus(bgCtx, store, taskID, sessionID)
				return
			}

			raw, err := json.Marshal(resp.Result)
			if err != nil {
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult("failed to marshal tool result"), "failed to marshal tool result")
				notifyTaskStatus(bgCtx, store, taskID, sessionID)
				return
			}

			var toolResult core.ToolResult
			if err := json.Unmarshal(raw, &toolResult); err != nil {
				store.StoreTerminalResult(taskID, sessionID, core.TaskFailed, core.ErrorResult("failed to unmarshal tool result"), "failed to unmarshal tool result")
				notifyTaskStatus(bgCtx, store, taskID, sessionID)
				return
			}

			status := core.TaskCompleted
			if toolResult.IsError {
				status = core.TaskFailed
			}
			store.StoreTerminalResult(taskID, sessionID, status, toolResult, "")
			notifyTaskStatus(bgCtx, store, taskID, sessionID)
		}()

		return core.NewResponse(req.ID, core.CreateTaskResultV1{Task: info}), nil
	}
}

// taskHint is the client's task creation hint from params.task.
type taskHint struct {
	TTL          int `json:"ttl,omitempty"`          // milliseconds
	PollInterval int `json:"pollInterval,omitempty"` // milliseconds
}

// --- Method Handlers ---

func makeGetHandler(rt *taskRuntime) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		// Check per-tool callbacks first (external proxy pattern).
		if cb := rt.getToolCallbacks(p.TaskID); cb != nil && cb.GetTask != nil {
			if result, ok := cb.GetTask(ctx, p.TaskID); ok {
				return core.NewResponse(id, result)
			}
		}

		// Fall through to TaskStore.
		info, ok := rt.store.Get(p.TaskID, ctx.SessionID())
		if !ok {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
		}
		return core.NewResponse(id, core.GetTaskResultV1{TaskInfo: info})
	}
}

func makeResultHandler(rt *taskRuntime) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		store := rt.store

		// Long-poll loop: wait for the task to reach a terminal state.
		// Between iterations, proxy any pending side-channel requests
		// (elicitation/sampling) through this handler's live connection.
		for {
			// 1. Check for pending side-channel requests from the background
			//    goroutine. If one is waiting, proxy it through our live context.
			if reqCh := rt.getChannel(p.TaskID); reqCh != nil {
				select {
				case scReq := <-reqCh:
					// Proxy the request through this handler's live context.
					// ctx (MethodContext) has a working requestFunc because
					// this handler is inside an active POST SSE response.
					raw, err := proxySideChannel(ctx, scReq)
					scReq.Result <- sideChannelResponse{Raw: raw, Err: err}
					continue // loop back to check for more
				default:
					// No pending request — fall through to status check.
				}
			}

			// 2. Check if the task is terminal.
			sid := ctx.SessionID()
			info, found := store.Get(p.TaskID, sid)
			if !found {
				return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
			}

			if info.Status.IsTerminal() {
				// Check per-tool GetResult callback first (external proxy pattern).
				var result core.ToolResult
				if cb := rt.getToolCallbacks(p.TaskID); cb != nil && cb.GetResult != nil {
					if overrideResult, ok := cb.GetResult(ctx, p.TaskID); ok {
						result = overrideResult
					} else {
						result, _ = store.GetResult(p.TaskID, sid)
					}
				} else {
					result, _ = store.GetResult(p.TaskID, sid)
				}

				rt.queue.DequeueAll(p.TaskID)
				// Clean up creatorToolForTask mapping after result is served (matches
				// TS SDK: delete _taskToTool after handler resolves).
				rt.cleanupToolName(p.TaskID)

				// Send status notification from this live handler context (Phase 6).
				// The background goroutine's context may have a dead notifyFunc,
				// but this handler's MethodContext is always alive.
				ctx.Notify("notifications/tasks/status", info)

				if result.Meta == nil {
					result.Meta = &core.ToolResultMeta{}
				}
				result.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: p.TaskID}
				return core.NewResponse(id, result)
			}

			// 3. Wait for a task update or context cancellation.
			if err := store.WaitForUpdate(ctx, p.TaskID, sid); err != nil {
				return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
			}
		}
	}
}

func makeListHandler(store TaskStore) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			Cursor string `json:"cursor"`
		}
		if params != nil {
			json.Unmarshal(params, &p)
		}
		tasks, nextCursor := store.List(p.Cursor, 50, ctx.SessionID())
		if tasks == nil {
			tasks = []core.TaskInfo{}
		}
		return core.NewResponse(id, core.ListTasksResultV1{
			Tasks:      tasks,
			NextCursor: nextCursor,
		})
	}
}

func makeCancelHandler(rt *taskRuntime) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		info, err := rt.store.Cancel(p.TaskID, ctx.SessionID())
		if err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		// Stop the background goroutine (Phase 5).
		rt.cancelTask(p.TaskID)
		// Clean up any queued messages for the cancelled task.
		rt.queue.DequeueAll(p.TaskID)
		// Send status notification (Phase 6).
		ctx.Notify("notifications/tasks/status", info)
		// Per spec: tasks/cancel returns flat Result & Task (no wrapper).
		return core.NewResponse(id, core.CancelTaskResultV1{TaskInfo: info})
	}
}

// --- Helpers ---

func generateTaskID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "task-" + hex.EncodeToString(b)
}

// notifyTaskStatus sends a notifications/tasks/status notification with the
// current task state. Called after every status change (Phase 6).
// Best-effort — silently drops if no notification channel is available.
func notifyTaskStatus(ctx context.Context, store TaskStore, taskID, sessionID string) {
	info, ok := store.Get(taskID, sessionID)
	if !ok {
		return
	}
	core.Notify(ctx, "notifications/tasks/status", info)
}

// proxySideChannel proxies a side-channel request (elicitation/sampling)
// through the tasks/result handler's live MethodContext. The handler calls
// ctx.Elicit or ctx.Sample, which routes through the active POST SSE stream.
func proxySideChannel(ctx core.MethodContext, req sideChannelRequest) (json.RawMessage, error) {
	switch req.Method {
	case "elicitation/create":
		var elicitReq core.ElicitationRequest
		if err := json.Unmarshal(req.Params, &elicitReq); err != nil {
			return nil, fmt.Errorf("unmarshal elicitation params: %w", err)
		}
		result, err := ctx.Elicit(elicitReq)
		if err != nil {
			return nil, err
		}
		return core.MarshalJSON(result)

	case "sampling/createMessage":
		var sampleReq core.CreateMessageRequest
		if err := json.Unmarshal(req.Params, &sampleReq); err != nil {
			return nil, fmt.Errorf("unmarshal sampling params: %w", err)
		}
		result, err := ctx.Sample(sampleReq)
		if err != nil {
			return nil, err
		}
		return core.MarshalJSON(result)

	default:
		return nil, fmt.Errorf("unknown side-channel method: %s", req.Method)
	}
}
