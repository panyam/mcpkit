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
type Config struct {
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

func (c *Config) defaults() {
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

// taskRuntime holds the per-registration state shared between the middleware
// and the tasks/* method handlers. Scoped to a single Register() call —
// no package-level globals.
type taskRuntime struct {
	store   TaskStore
	queue   TaskMessageQueue
	mu      sync.Mutex
	channels map[string]chan sideChannelRequest // taskID → side-channel request channel
}

func newTaskRuntime(store TaskStore, queue TaskMessageQueue) *taskRuntime {
	return &taskRuntime{
		store:    store,
		queue:    queue,
		channels: make(map[string]chan sideChannelRequest),
	}
}

// registerChannel stores the side-channel request channel for a task.
func (rt *taskRuntime) registerChannel(taskID string, ch chan sideChannelRequest) {
	rt.mu.Lock()
	rt.channels[taskID] = ch
	rt.mu.Unlock()
}

// unregisterChannel removes the side-channel request channel for a task.
func (rt *taskRuntime) unregisterChannel(taskID string) {
	rt.mu.Lock()
	delete(rt.channels, taskID)
	rt.mu.Unlock()
}

// getChannel returns the side-channel request channel for a task, or nil.
func (rt *taskRuntime) getChannel(taskID string) chan sideChannelRequest {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.channels[taskID]
}

// Register hooks up tasks support on the given server:
//   - Installs middleware that intercepts tools/call for task-eligible requests
//   - Registers tasks/get, tasks/result, tasks/list, tasks/cancel handlers
//   - Advertises the tasks capability in the initialize response
//
// Must be called before accepting connections.
func Register(cfg Config) {
	cfg.defaults()
	srv := cfg.Server
	store := cfg.Store
	reg := srv.Registry()
	rt := newTaskRuntime(store, cfg.MessageQueue)

	// Install middleware for tools/call interception.
	srv.UseMiddleware(taskMiddleware(reg, rt, cfg))

	// Register tasks/* protocol methods.
	srv.HandleMethod("tasks/get", makeGetHandler(store))
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
func taskMiddleware(reg *Registry, rt *taskRuntime, cfg Config) Middleware {
	return func(ctx context.Context, req *core.Request, next MiddlewareFunc) *core.Response {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		// Parse the envelope to extract tool name and task hint.
		// Per spec: task hint is at params.task, NOT params._meta.task.
		var envelope struct {
			Name string    `json:"name"`
			Task *taskHint `json:"task"`
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
					fmt.Sprintf("tool %q requires task invocation (execution.taskSupport=required); include 'task' in params", envelope.Name))
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
				fmt.Sprintf("tool %q does not support task invocation", envelope.Name))
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
			return core.NewErrorResponse(req.ID, -32603, "failed to create task: "+err.Error())
		}

		// Run the tool asynchronously. Detach from client context so
		// the tool continues even if the client disconnects.
		// The sessionID is captured here so the background goroutine
		// uses the same session for all store operations.
		go func() {
			defer func() {
				rt.unregisterChannel(taskID)
				if r := recover(); r != nil {
					now := time.Now().UTC().Format(time.RFC3339)
					msg := fmt.Sprintf("panic: %v", r)
					store.SetResult(taskID, sessionID, core.ErrorResult(msg))
					store.Update(taskID, sessionID, func(t *core.TaskInfo) {
						t.Status = core.TaskFailed
						t.StatusMessage = msg
						t.LastUpdatedAt = now
					})
				}
			}()

			bgCtx := core.DetachForBackground(ctx)
			reqCh := make(chan sideChannelRequest, 1)
			tc := &TaskContext{taskID: taskID, sessionID: sessionID, store: store, requests: reqCh}
			bgCtx = WithTaskContext(bgCtx, tc)
			rt.registerChannel(taskID, reqCh)
			resp := next(bgCtx, req)

			now := time.Now().UTC().Format(time.RFC3339)

			if resp.Error != nil {
				store.SetResult(taskID, sessionID, core.ErrorResult(resp.Error.Message))
				store.Update(taskID, sessionID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = resp.Error.Message
					t.LastUpdatedAt = now
				})
				return
			}

			raw, err := json.Marshal(resp.Result)
			if err != nil {
				store.SetResult(taskID, sessionID, core.ErrorResult("failed to marshal tool result"))
				store.Update(taskID, sessionID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = "failed to marshal tool result"
					t.LastUpdatedAt = now
				})
				return
			}

			var toolResult core.ToolResult
			if err := json.Unmarshal(raw, &toolResult); err != nil {
				store.SetResult(taskID, sessionID, core.ErrorResult("failed to unmarshal tool result"))
				store.Update(taskID, sessionID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = "failed to unmarshal tool result"
					t.LastUpdatedAt = now
				})
				return
			}

			status := core.TaskCompleted
			if toolResult.IsError {
				status = core.TaskFailed
			}
			store.SetResult(taskID, sessionID, toolResult)
			store.Update(taskID, sessionID, func(t *core.TaskInfo) {
				t.Status = status
				t.LastUpdatedAt = now
			})
		}()

		return core.NewResponse(req.ID, core.CreateTaskResult{Task: info})
	}
}

// taskHint is the client's task creation hint from params.task.
type taskHint struct {
	TTL          int `json:"ttl,omitempty"`          // milliseconds
	PollInterval int `json:"pollInterval,omitempty"` // milliseconds
}

// --- Method Handlers ---

func makeGetHandler(store TaskStore) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		info, ok := store.Get(p.TaskID, ctx.SessionID())
		if !ok {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
		}
		return core.NewResponse(id, core.GetTaskResult{TaskInfo: info})
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
				result, _ := store.GetResult(p.TaskID, sid)

				rt.queue.DequeueAll(p.TaskID)

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
		return core.NewResponse(id, core.ListTasksResult{
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
		// Clean up any queued messages for the cancelled task.
		rt.queue.DequeueAll(p.TaskID)
		// Per spec: tasks/cancel returns flat Result & Task (no wrapper).
		return core.NewResponse(id, core.CancelTaskResult{TaskInfo: info})
	}
}

// --- Helpers ---

func generateTaskID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "task-" + hex.EncodeToString(b)
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
