package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Config holds the options for registering tasks support on an MCP server.
type Config struct {
	// Store is the task state backend. If nil, an InMemoryTaskStore is used.
	// Tasks are session-scoped and ephemeral by default — the in-memory store
	// is the only implementation shipped. The interface exists so production
	// deployments can swap in durable storage for multi-node scenarios
	// (e.g., load-balanced servers sharing task state via Redis).
	Store TaskStore

	// Server is the MCP server to register tasks on.
	Server *server.Server

	// DefaultTTLMs is the default task TTL in milliseconds. Tasks are
	// cleaned up after this duration. Default: 300000 (5 minutes).
	DefaultTTLMs int

	// DefaultPollMs is the suggested poll interval in milliseconds,
	// returned to clients in CreateTaskResult. Default: 1000 (1 second).
	DefaultPollMs int
}

func (c *Config) defaults() {
	if c.Store == nil {
		c.Store = NewInMemoryStore()
	}
	if c.DefaultTTLMs <= 0 {
		c.DefaultTTLMs = 300_000
	}
	if c.DefaultPollMs <= 0 {
		c.DefaultPollMs = 1000
	}
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

	// Install middleware for tools/call interception.
	srv.UseMiddleware(taskMiddleware(reg, store, cfg))

	// Register tasks/* protocol methods.
	srv.HandleMethod("tasks/get", makeGetHandler(store))
	srv.HandleMethod("tasks/result", makeResultHandler(store))
	srv.HandleMethod("tasks/list", makeListHandler(store))
	srv.HandleMethod("tasks/cancel", makeCancelHandler(store))

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
func taskMiddleware(reg *server.Registry, store TaskStore, cfg Config) server.Middleware {
	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) *core.Response {
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
		if err := store.Create(info); err != nil {
			return core.NewErrorResponse(req.ID, -32603, "failed to create task: "+err.Error())
		}

		// Run the tool asynchronously. Detach from client context so
		// the tool continues even if the client disconnects.
		go func() {
			defer func() {
				if r := recover(); r != nil {
					now := time.Now().UTC().Format(time.RFC3339)
					msg := fmt.Sprintf("panic: %v", r)
					store.SetResult(taskID, core.ErrorResult(msg))
					store.Update(taskID, func(t *core.TaskInfo) {
						t.Status = core.TaskFailed
						t.StatusMessage = msg
						t.LastUpdatedAt = now
					})
				}
			}()

			bgCtx := context.WithoutCancel(ctx)
			// Inject TaskContext so tool handlers can call TaskElicit/TaskSample.
			// The ToolContext is constructed downstream by dispatch — we inject
			// into the raw context here, and GetTaskContext(ctx) retrieves it.
			tc := &TaskContext{taskID: taskID, store: store}
			bgCtx = WithTaskContext(bgCtx, tc)
			resp := next(bgCtx, req)

			now := time.Now().UTC().Format(time.RFC3339)

			// Store result BEFORE updating status to terminal. Update broadcasts
			// to WaitForResult waiters, so the result must be available first.
			if resp.Error != nil {
				store.SetResult(taskID, core.ErrorResult(resp.Error.Message))
				store.Update(taskID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = resp.Error.Message
					t.LastUpdatedAt = now
				})
				return
			}

			// resp.Result is any — marshal then unmarshal to get ToolResult.
			raw, err := json.Marshal(resp.Result)
			if err != nil {
				store.SetResult(taskID, core.ErrorResult("failed to marshal tool result"))
				store.Update(taskID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = "failed to marshal tool result"
					t.LastUpdatedAt = now
				})
				return
			}

			var toolResult core.ToolResult
			if err := json.Unmarshal(raw, &toolResult); err != nil {
				store.SetResult(taskID, core.ErrorResult("failed to unmarshal tool result"))
				store.Update(taskID, func(t *core.TaskInfo) {
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
			store.SetResult(taskID, toolResult)
			store.Update(taskID, func(t *core.TaskInfo) {
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

func makeGetHandler(store TaskStore) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		info, ok := store.Get(p.TaskID)
		if !ok {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, "task not found: "+p.TaskID)
		}
		// Per spec: tasks/get returns flat Result & Task (no wrapper).
		return core.NewResponse(id, core.GetTaskResult{TaskInfo: info})
	}
}

func makeResultHandler(store TaskStore) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		// WaitForResult blocks until terminal, respecting context cancellation
		// (e.g., HTTP disconnect aborts the long-poll).
		result, _, err := store.WaitForResult(ctx, p.TaskID)
		if err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		// Per spec: tasks/result returns the stored ToolResult for ALL terminal
		// states (completed, failed, cancelled). The client checks isError or
		// polls tasks/get for the status. We do NOT return JSON-RPC errors for
		// failed/cancelled tasks — the result IS the payload.
		if result.Meta == nil {
			result.Meta = &core.ToolResultMeta{}
		}
		result.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: p.TaskID}

		return core.NewResponse(id, result)
	}
}

func makeListHandler(store TaskStore) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			Cursor string `json:"cursor"`
		}
		if params != nil {
			json.Unmarshal(params, &p)
		}
		tasks, nextCursor := store.List(p.Cursor, 50)
		if tasks == nil {
			tasks = []core.TaskInfo{}
		}
		return core.NewResponse(id, core.ListTasksResult{
			Tasks:      tasks,
			NextCursor: nextCursor,
		})
	}
}

func makeCancelHandler(store TaskStore) server.MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
		info, err := store.Cancel(p.TaskID)
		if err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}
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
