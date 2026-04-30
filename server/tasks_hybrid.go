package server

import (
	"encoding/json"

	"github.com/panyam/mcpkit/core"
)

// TasksHybridConfig wires both the v1 (RegisterTasksV1) and v2 (RegisterTasks)
// task surfaces onto a single server, with per-request dispatch based on which
// capability the client negotiated during initialize. Use this when you need
// to keep older v1 clients working while accepting newer v2 clients on the
// same endpoint — e.g., during a rolling-upgrade window.
//
// If your server only needs to support one path, prefer the dedicated
// RegisterTasks (v2) or RegisterTasksV1 (v1) entry points — they're simpler
// and have no dispatching cost per request.
type TasksHybridConfig struct {
	// Server is the MCP server to register tasks on. Required; the embedded
	// V1.Server / V2.Server fields are overridden so callers don't have to
	// thread the same pointer twice.
	Server *Server

	// V1 carries the v1-side configuration (TaskStore, TaskMessageQueue,
	// DefaultTTLMs, DefaultPollMs, MaxQueueSize). All optional — defaults
	// match RegisterTasksV1's standalone behavior.
	V1 TasksConfigV1

	// V2 carries the v2-side configuration (TaskStore, DefaultTTLSeconds,
	// DefaultPollMs). All optional — defaults match RegisterTasks's
	// standalone behavior.
	V2 TasksConfig
}

// RegisterTasksHybrid hooks up both v1 and v2 task surfaces on the given
// server. Capability advertisement is dual:
//
//   - capabilities.tasks (v1 ServerCapabilities.Tasks) is set so v1 clients
//     can negotiate the legacy path.
//   - capabilities.extensions[io.modelcontextprotocol/tasks] (v2) is set so
//     v2 clients can negotiate the extension path.
//
// Per-request routing:
//
//   - tools/call: both middlewares run (v2 first; v2 only fires if the
//     client negotiated the extension AND the tool is task-eligible; v1
//     only fires if the client sent a `task` hint AND the tool supports
//     it). A v2-aware client gets v2 task creation; a v1 client gets v1.
//     A client that negotiated both AND sent a task hint gets v2 (modern
//     wins).
//   - tasks/get / tasks/cancel: dispatched to the v2 handler when the
//     client negotiated the extension; otherwise the v1 handler.
//   - tasks/update: v2 only — gated on extension negotiation.
//   - tasks/result / tasks/list: v1 only — gated on the v1 capability
//     declaration so v2-only clients don't see them.
//
// Must be called before accepting connections. Calling RegisterTasksHybrid
// in addition to RegisterTasks or RegisterTasksV1 on the same server is
// undefined — the LAST HandleMethod registration wins.
func RegisterTasksHybrid(cfg TasksHybridConfig) {
	srv := cfg.Server

	// Both per-version configs need the same Server pointer + defaults filled.
	cfg.V1.Server = srv
	cfg.V1.defaults()
	cfg.V2.Server = srv
	cfg.V2.defaults()

	reg := srv.Registry()
	v1RT := newTaskRuntime(cfg.V1.Store, cfg.V1.MessageQueue, reg)
	v2RT := newV2TaskRuntime(cfg.V2)

	// Advertise BOTH so clients can pick. v1 SetTasksCap goes first so the
	// v1 cap shows up under the canonical capabilities.tasks slot; the v2
	// extension lives under capabilities.extensions independently.
	srv.SetTasksCap(&core.TasksCap{
		List:   &core.TasksCapMethod{},
		Cancel: &core.TasksCapMethod{},
		Requests: &core.TasksCapRequests{
			Tools: &core.TasksCapToolsMethods{Call: &core.TasksCapMethod{}},
		},
	})
	srv.RegisterExtension(tasksExtensionProvider{})

	// Both middlewares stack. v2 runs first because the extension takes
	// priority over the v1 task hint when a client negotiated both.
	// Each middleware is internally gated and is a no-op for the other path.
	srv.UseMiddleware(taskV2Middleware(reg, v2RT, cfg.V2))
	srv.UseMiddleware(taskMiddleware(reg, v1RT, cfg.V1))

	// Dispatching handlers: tasks/get and tasks/cancel route by negotiated
	// capability. tasks/update is v2-only; tasks/result and tasks/list are
	// v1-only.
	srv.HandleMethod("tasks/get", hybridDispatch(makeGetHandler(v1RT), makeV2GetHandler(v2RT)))
	srv.HandleMethod("tasks/cancel", hybridDispatch(makeCancelHandler(v1RT), makeV2CancelHandler(v2RT)))
	srv.HandleMethod("tasks/update", gateOnTasksExtension(makeV2UpdateHandler(v2RT)))
	srv.HandleMethod("tasks/result", rejectIfV2(makeResultHandler(v1RT)))
	srv.HandleMethod("tasks/list", rejectIfV2(makeListHandler(cfg.V1.Store)))
}

// hybridDispatch picks the v2 handler when the client negotiated the
// io.modelcontextprotocol/tasks extension; falls back to v1 otherwise.
// Sessions that negotiated NEITHER hit the v1 handler — which surfaces a
// v1-shaped "task not found" rather than -32601, matching what they'd see
// if only v1 were registered. Servers that want stricter behavior can stack
// gateOnV1TasksCap on the v1 branch too.
func hybridDispatch(v1, v2 MethodHandler) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		if ctx.ClientSupportsExtension(core.TasksExtensionID) {
			return v2(ctx, id, params)
		}
		return v1(ctx, id, params)
	}
}

// rejectIfV2 wraps a v1-only handler so clients that negotiated the v2
// extension see -32601 (method not found) — defense in depth so a buggy v2
// client that calls tasks/result or tasks/list doesn't get back v1 wire
// shapes its SDK can't parse. v1 clients (no extension declaration) pass
// through to the inner handler untouched, including v1 clients that simply
// send a `task` hint without explicitly declaring ClientTasksCap.
func rejectIfV2(inner MethodHandler) MethodHandler {
	return func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		if ctx.ClientSupportsExtension(core.TasksExtensionID) {
			return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
				"method not available to v2 clients (use tasks/get instead)")
		}
		return inner(ctx, id, params)
	}
}
