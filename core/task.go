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

// IntPtr returns a pointer to the given int. Convenience for setting TaskInfo.TTL.
func IntPtr(v int) *int { return &v }

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
// TTL is required but nullable per spec: *int with null = unlimited.
type TaskInfo struct {
	TaskID        string     `json:"taskId"`
	Status        TaskStatus `json:"status"`
	StatusMessage string     `json:"statusMessage,omitempty"`
	CreatedAt     string     `json:"createdAt"`
	LastUpdatedAt string     `json:"lastUpdatedAt"`
	TTL           *int       `json:"ttl"`                    // milliseconds; null = unlimited
	PollInterval  int        `json:"pollInterval,omitempty"` // milliseconds
}

// CreateTaskResult is returned by tools/call when a task is created
// instead of the immediate tool result. Per spec: nested under "task" key.
type CreateTaskResult struct {
	Task TaskInfo `json:"task"`
}

// GetTaskResult is the response to tasks/get. Per spec: flat Result & Task
// intersection — task fields at the root level, no "task" wrapper.
type GetTaskResult struct {
	TaskInfo
}

// CancelTaskResult is the response to tasks/cancel. Per spec: flat Result & Task
// intersection — same as GetTaskResult.
type CancelTaskResult struct {
	TaskInfo
}

// ListTasksResult is the response to tasks/list with cursor pagination.
type ListTasksResult struct {
	Tasks      []TaskInfo `json:"tasks"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// --- Capability types ---

// TasksCapMethod is an empty struct used as a marker in capability negotiation.
type TasksCapMethod struct{}

// TasksCapToolsMethods declares which tool methods support task augmentation.
type TasksCapToolsMethods struct {
	Call *TasksCapMethod `json:"call,omitempty"`
}

// TasksCapSamplingMethods declares which sampling methods support task augmentation.
type TasksCapSamplingMethods struct {
	CreateMessage *TasksCapMethod `json:"createMessage,omitempty"`
}

// TasksCapElicitationMethods declares which elicitation methods support task augmentation.
type TasksCapElicitationMethods struct {
	Create *TasksCapMethod `json:"create,omitempty"`
}

// TasksCapRequests declares which request types support task-augmented responses.
type TasksCapRequests struct {
	Tools       *TasksCapToolsMethods       `json:"tools,omitempty"`
	Sampling    *TasksCapSamplingMethods    `json:"sampling,omitempty"`
	Elicitation *TasksCapElicitationMethods `json:"elicitation,omitempty"`
}

// TasksCap declares server support for the tasks capability in
// ServerCapabilities. Per spec: nested structure with list, cancel, requests.
type TasksCap struct {
	List     *TasksCapMethod   `json:"list,omitempty"`
	Cancel   *TasksCapMethod   `json:"cancel,omitempty"`
	Requests *TasksCapRequests `json:"requests,omitempty"`
}

// ClientTasksCap declares client support for tasks in ClientCapabilities.
// Mirrors TasksCap structure.
type ClientTasksCap struct {
	List     *TasksCapMethod   `json:"list,omitempty"`
	Cancel   *TasksCapMethod   `json:"cancel,omitempty"`
	Requests *TasksCapRequests `json:"requests,omitempty"`
}
