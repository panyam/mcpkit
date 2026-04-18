package core

// Task protocol types for MCP spec 2025-11-25.
// Tasks enable "call-now, fetch-later" async execution: a tool call returns
// a task reference immediately; the client polls for status and results.

// TaskStatus is the lifecycle state of an MCP task.
type TaskStatus string

const (
	// TaskWorking means the task is actively executing.
	TaskWorking TaskStatus = "working"

	// TaskInputRequired means the task is paused, awaiting client input
	// (e.g., via elicitation or sampling).
	TaskInputRequired TaskStatus = "input_required"

	// TaskCompleted means the task succeeded (terminal).
	TaskCompleted TaskStatus = "completed"

	// TaskFailed means the task errored (terminal).
	TaskFailed TaskStatus = "failed"

	// TaskCancelled means the task was cancelled by the client (terminal).
	TaskCancelled TaskStatus = "cancelled"
)

// IsTerminal reports whether the status is a terminal state.
func (s TaskStatus) IsTerminal() bool {
	return s == TaskCompleted || s == TaskFailed || s == TaskCancelled
}

// TaskSupport declares how a tool interacts with the tasks capability.
// Set on ToolDef.Execution.TaskSupport.
type TaskSupport string

const (
	// TaskSupportRequired means clients SHOULD invoke as a task; servers
	// MUST return an error if clients ignore the requirement.
	TaskSupportRequired TaskSupport = "required"

	// TaskSupportOptional means clients may choose either synchronous
	// or task-based invocation.
	TaskSupportOptional TaskSupport = "optional"

	// TaskSupportForbidden means clients must NOT invoke as a task.
	TaskSupportForbidden TaskSupport = "forbidden"
)

// ToolExecution describes task-related execution metadata on a tool definition.
// Per MCP spec: declared at the tool level, not in annotations (because it
// implies binding guarantees about behavior, not hints).
type ToolExecution struct {
	TaskSupport TaskSupport `json:"taskSupport"`
}

// TaskInfo is the wire-format task object returned by tasks/* methods.
type TaskInfo struct {
	TaskID        string     `json:"taskId"`
	Status        TaskStatus `json:"status"`
	StatusMessage string     `json:"statusMessage,omitempty"`
	CreatedAt     string     `json:"createdAt"`
	LastUpdatedAt string     `json:"lastUpdatedAt"`
	TTL           int        `json:"ttl,omitempty"`          // milliseconds
	PollInterval  int        `json:"pollInterval,omitempty"` // milliseconds
}

// CreateTaskResult is returned by tools/call when a task is created
// instead of the immediate tool result.
type CreateTaskResult struct {
	Task TaskInfo `json:"task"`
}

// GetTaskResult is the response to tasks/get (non-blocking status poll).
type GetTaskResult struct {
	Task TaskInfo `json:"task"`
}

// GetTaskPayloadResult is the response to tasks/result.
// Blocks until the task reaches a terminal state.
type GetTaskPayloadResult struct {
	Task   TaskInfo   `json:"task"`
	Result ToolResult `json:"result"`
}

// ListTasksResult is the response to tasks/list with cursor pagination.
type ListTasksResult struct {
	Tasks      []TaskInfo `json:"tasks"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// CancelTaskResult is the response to tasks/cancel.
type CancelTaskResult struct {
	Task TaskInfo `json:"task"`
}

// TasksCap declares server support for the tasks capability in
// ServerCapabilities. The Requests map keys are method names that support
// task-augmented responses (e.g., "tools/call").
type TasksCap struct {
	Requests map[string]struct{} `json:"requests,omitempty"`
}

// ClientTasksCap declares client support for tasks in ClientCapabilities.
type ClientTasksCap struct {
	Requests map[string]struct{} `json:"requests,omitempty"`
}
