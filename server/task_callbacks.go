package server

import "github.com/panyam/mcpkit/core"

// TaskCallbacks provides optional per-tool overrides for task protocol
// handlers. When a tool registers callbacks, the tasks/get and tasks/result
// handlers consult them before falling through to the TaskStore.
//
// This enables the "external proxy" pattern where a tool wraps an external
// job system (e.g., AWS Step Functions, CI/CD pipelines) and the external
// system is the source of truth for task state — not the in-memory store.
//
// Designed for extensibility: v2 (SEP-2557/SEP-2663) will add OnCancel and
// OnInputResponse fields without structural changes.
//
// Currently scoped to v1 — the GetTask callback returns the v1 wire shape
// (GetTaskResultV1). The v2 task runtime does not yet consult these callbacks.
//
// Example:
//
//	srv.Register(server.Tool{
//	    ToolDef: core.ToolDef{
//	        Name:      "deploy",
//	        Execution: &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
//	    },
//	    Handler: deployHandler,
//	    TaskCallbacks: &server.TaskCallbacks{
//	        GetTask: func(ctx core.MethodContext, taskID string) (core.GetTaskResultV1, bool) {
//	            // Query external system for task state
//	            state, err := stepFunctions.DescribeExecution(taskID)
//	            if err != nil {
//	                return core.GetTaskResultV1{}, false // fall through to store
//	            }
//	            return core.GetTaskResultV1{TaskInfo: toTaskInfo(state)}, true
//	        },
//	    },
//	})
type TaskCallbacks struct {
	// GetTask overrides the default TaskStore lookup for the v1 tasks/get
	// handler. Return (result, true) to use the override response.
	// Return (_, false) to fall through to the TaskStore.
	GetTask func(ctx core.MethodContext, taskID string) (core.GetTaskResultV1, bool)

	// GetResult overrides the default TaskStore lookup for the v1 tasks/result
	// handler. Return (result, true) to use the override response.
	// Return (_, false) to fall through to the TaskStore.
	GetResult func(ctx core.MethodContext, taskID string) (core.ToolResult, bool)
}
