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
// Designed for extensibility: v2 (SEP-2557) will add OnCancel and
// OnInputResponse fields without structural changes.
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
//	        GetTask: func(ctx core.MethodContext, taskID string) (core.GetTaskResult, bool) {
//	            // Query external system for task state
//	            state, err := stepFunctions.DescribeExecution(taskID)
//	            if err != nil {
//	                return core.GetTaskResult{}, false // fall through to store
//	            }
//	            return core.GetTaskResult{TaskInfo: toTaskInfo(state)}, true
//	        },
//	    },
//	})
type TaskCallbacks struct {
	// GetTask overrides the default TaskStore lookup for tasks/get.
	// Return (result, true) to use the override response.
	// Return (_, false) to fall through to the TaskStore.
	GetTask func(ctx core.MethodContext, taskID string) (core.GetTaskResult, bool)

	// GetResult overrides the default TaskStore lookup for tasks/result.
	// Return (result, true) to use the override response.
	// Return (_, false) to fall through to the TaskStore.
	GetResult func(ctx core.MethodContext, taskID string) (core.ToolResult, bool)
}
