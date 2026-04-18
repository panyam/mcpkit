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

	// Advertise tasks capability.
	srv.SetTasksCap(&core.TasksCap{
		Requests: map[string]struct{}{"tools/call": {}},
	})
}

// --- Middleware ---

// taskMiddleware intercepts tools/call requests. When the client sends a task
// hint and the tool supports tasks, the middleware creates a task, runs the
// tool asynchronously, and returns CreateTaskResult immediately.
func taskMiddleware(reg *server.Registry, store TaskStore, cfg Config) server.Middleware {
	return func(ctx context.Context, req *core.Request, next server.MiddlewareFunc) *core.Response {
		if req.Method != "tools/call" {
			return next(ctx, req)
		}

		// Parse the envelope to extract tool name and task hint.
		var envelope struct {
			Name string          `json:"name"`
			Meta *taskCallMeta   `json:"_meta"`
		}
		if err := json.Unmarshal(req.Params, &envelope); err != nil {
			return next(ctx, req) // let dispatch handle the parse error
		}

		// Check for task hint from client.
		if envelope.Meta == nil || envelope.Meta.Task == nil {
			// No task hint — check if tool requires tasks.
			def, ok := reg.ToolDef(envelope.Name)
			if ok && def.Execution != nil && def.Execution.TaskSupport == core.TaskSupportRequired {
				return core.NewErrorResponse(req.ID, core.ErrCodeInvalidParams,
					fmt.Sprintf("tool %q requires task invocation (execution.taskSupport=required); include _meta.task in params", envelope.Name))
			}
			return next(ctx, req)
		}

		// Task hint present — check if tool forbids tasks.
		def, ok := reg.ToolDef(envelope.Name)
		if !ok {
			return next(ctx, req) // let dispatch handle unknown tool
		}
		if def.Execution != nil && def.Execution.TaskSupport == core.TaskSupportForbidden {
			// Tool explicitly forbids tasks — ignore the hint, run sync.
			return next(ctx, req)
		}

		// Tool supports tasks (optional or required, or no Execution field
		// with an explicit hint — treat as optional).
		taskID := generateTaskID()
		now := time.Now().UTC().Format(time.RFC3339)

		ttlMs := cfg.DefaultTTLMs
		if envelope.Meta.Task.TTL > 0 {
			ttlMs = envelope.Meta.Task.TTL
		}

		info := core.TaskInfo{
			TaskID:        taskID,
			Status:        core.TaskWorking,
			CreatedAt:     now,
			LastUpdatedAt: now,
			TTL:           ttlMs,
			PollInterval:  cfg.DefaultPollMs,
		}
		if err := store.Create(info); err != nil {
			return core.NewErrorResponse(req.ID, -32603, "failed to create task: "+err.Error())
		}

		// Run the tool asynchronously. Detach from client context so
		// the tool continues even if the client disconnects.
		go func() {
			bgCtx := context.WithoutCancel(ctx)
			resp := next(bgCtx, req)

			now := time.Now().UTC().Format(time.RFC3339)

			if resp.Error != nil {
				store.Update(taskID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = resp.Error.Message
					t.LastUpdatedAt = now
				})
				store.SetResult(taskID, core.ErrorResult(resp.Error.Message))
				return
			}

			// resp.Result is any — marshal then unmarshal to get ToolResult.
			raw, err := json.Marshal(resp.Result)
			if err != nil {
				store.Update(taskID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = "failed to marshal tool result"
					t.LastUpdatedAt = now
				})
				store.SetResult(taskID, core.ErrorResult("failed to marshal tool result"))
				return
			}

			var toolResult core.ToolResult
			if err := json.Unmarshal(raw, &toolResult); err != nil {
				store.Update(taskID, func(t *core.TaskInfo) {
					t.Status = core.TaskFailed
					t.StatusMessage = "failed to unmarshal tool result"
					t.LastUpdatedAt = now
				})
				store.SetResult(taskID, core.ErrorResult("failed to unmarshal tool result"))
				return
			}

			status := core.TaskCompleted
			if toolResult.IsError {
				status = core.TaskFailed
			}
			store.Update(taskID, func(t *core.TaskInfo) {
				t.Status = status
				t.LastUpdatedAt = now
			})
			store.SetResult(taskID, toolResult)
		}()

		return core.NewResponse(req.ID, core.CreateTaskResult{Task: info})
	}
}

// taskCallMeta is the _meta field in tools/call params when a task hint is present.
type taskCallMeta struct {
	Task *taskHint `json:"task"`
}

// taskHint is the client's task creation hint.
type taskHint struct {
	TTL int `json:"ttl,omitempty"` // milliseconds
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
		return core.NewResponse(id, core.GetTaskResult{Task: info})
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

		// WaitForResult blocks until terminal.
		result, info, err := store.WaitForResult(p.TaskID)
		if err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
		}

		if info.Status == core.TaskFailed {
			return core.NewErrorResponse(id, core.ErrCodeToolExecutionError, info.StatusMessage)
		}
		if info.Status == core.TaskCancelled {
			return core.NewErrorResponse(id, -32800, "task was cancelled")
		}

		return core.NewResponse(id, core.GetTaskPayloadResult{
			Task:   info,
			Result: result,
		})
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
		return core.NewResponse(id, core.CancelTaskResult{Task: info})
	}
}

// --- Helpers ---

func generateTaskID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "task-" + hex.EncodeToString(b)
}
