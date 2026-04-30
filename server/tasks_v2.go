package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/core"
)

// TasksV2Config holds the options for registering v2 tasks support on an MCP server.
type TasksV2Config struct {
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

}

func (c *TasksV2Config) defaults() {
	if c.Store == nil {
		c.Store = NewInMemoryStore()
	}
	if c.DefaultTTLSeconds <= 0 {
		c.DefaultTTLSeconds = 300
	}
	if c.DefaultPollMs <= 0 {
		c.DefaultPollMs = 1000
	}
}

// v2TaskRuntime holds per-registration state for v2 tasks, shared between
// middleware and handlers. Scoped to a single RegisterTasksV2 call (C3).
type v2TaskRuntime struct {
	store    TaskStore
	registry *Registry
	mu       sync.Mutex
	active   map[string]*activeTask
	// taskErrors stores protocol-level errors (TaskError) for failed tasks.
	// Separate from the store's result, which holds ToolResult for completed tasks.
	taskErrors map[string]*core.TaskError
	// creatorToolForTask maps taskID → tool name for callback dispatch.
	creatorToolForTask map[string]string
}

func newV2TaskRuntime(store TaskStore, reg *Registry) *v2TaskRuntime {
	return &v2TaskRuntime{
		store:              store,
		registry:           reg,
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

// tasksExtensionProvider implements core.ExtensionProvider for the SEP-2663
// Tasks extension. RegisterTasksV2 hands an instance to Server.RegisterExtension
// so the extension is advertised in capabilities.extensions during initialize.
type tasksExtensionProvider struct{}

// Extension declares the SEP-2663 Tasks extension.
//
//nolint:unused // referenced by RegisterTasksV2 only when the v2 path is wired
func (tasksExtensionProvider) Extension() core.Extension {
	return core.Extension{
		ID:          core.TasksExtensionID,
		SpecVersion: "draft", // SEP-2663 is in draft
		Stability:   core.Experimental,
	}
}

// RegisterTasksV2 hooks up v2 tasks support on the given server:
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
func RegisterTasksV2(cfg TasksV2Config) {
	cfg.defaults()
	srv := cfg.Server
	store := cfg.Store
	reg := srv.Registry()
	rt := newV2TaskRuntime(store, reg)

	// Advertise the SEP-2663 Tasks extension.
	srv.RegisterExtension(tasksExtensionProvider{})

	// Install v2 middleware for tools/call interception (gated on extension).
	srv.UseMiddleware(taskV2Middleware(reg, rt, cfg))

	// Register tasks/* protocol methods (v2: only get + cancel),
	// gated on session-level extension support.
	srv.HandleMethod("tasks/get", gateOnTasksExtension(makeV2GetHandler(rt)))
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
func taskV2Middleware(reg *Registry, rt *v2TaskRuntime, cfg TasksV2Config) Middleware {
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

		// Build the v2 wire envelope. CreateTaskResult MUST NOT carry result/
		// error/inputRequests/requestState (SEP-2663 — those belong on
		// DetailedTask returned by tasks/get).
		wireTask := toTaskInfoV2(info)
		wireTask.TTLSeconds = core.IntPtr(ttlSec)
		return core.NewResponse(req.ID, core.CreateTaskResult{
			ResultType: core.ResultTypeTask,
			Task:       wireTask,
		}), nil
	}
}

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

		result := core.DetailedTask{
			TaskInfoV2: toTaskInfoV2(info),
			// Generate requestState (opaque token for stateless deployments).
			RequestState: p.TaskID,
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

func makeV2CancelHandler(rt *v2TaskRuntime) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var p struct {
			TaskID       string `json:"taskId"`
			RequestState string `json:"requestState,omitempty"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
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
func notifyV2TaskStatus(ctx context.Context, rt *v2TaskRuntime, store TaskStore, taskID, sessionID string) {
	info, ok := store.Get(taskID, sessionID)
	if !ok {
		return
	}

	payload := core.DetailedTask{TaskInfoV2: toTaskInfoV2(info)}

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
func notifyV2TaskStatusFromInfo(ctx core.MethodContext, rt *v2TaskRuntime, info core.TaskInfo) {
	payload := core.DetailedTask{TaskInfoV2: toTaskInfoV2(info)}
	if info.Status == core.TaskFailed {
		if te := rt.getTaskError(info.TaskID); te != nil {
			payload.Error = te
		}
	}
	ctx.Notify("notifications/tasks/status", payload)
}
