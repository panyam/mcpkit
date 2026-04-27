package core

// Tasks v2 types (SEP-2557).
//
// Key differences from v1:
//   - resultType: "task" discriminator on CreateTaskResult
//   - tasks/get returns GetTaskResultV2 with inlined result/error
//   - tasks/result and tasks/list removed
//   - TTL in seconds (not milliseconds)
//   - requestState for stateless deployments
//   - Tool errors = completed + isError:true; protocol errors = failed + error field

// ResultType is the discriminator on a CreateTaskResult response.
// Always "task" for v2 task creation responses.
type ResultType string

const ResultTypeTask ResultType = "task"

// CreateTaskResultV2 is returned by tools/call when a task is created in v2.
// Includes the resultType discriminator per SEP-2557.
type CreateTaskResultV2 struct {
	ResultType ResultType `json:"resultType"`
	Task       TaskInfo   `json:"task"`
}

// GetTaskResultV2 is the consolidated tasks/get response for v2.
// Inlines result, error, or inputRequests depending on status.
// All TaskInfo fields are at the root level (flat).
type GetTaskResultV2 struct {
	TaskInfo

	// Result is inlined when status is "completed" (including tool errors with isError:true).
	Result *ToolResult `json:"result,omitempty"`

	// Error is inlined when status is "failed" (protocol-level errors only).
	Error *TaskError `json:"error,omitempty"`

	// InputRequests is a map of pending input requests when status is "input_required".
	// Keys are request identifiers; values are the request payloads.
	InputRequests map[string]any `json:"inputRequests,omitempty"`

	// RequestState is an opaque string for stateless deployments.
	// Server generates it; client echoes in subsequent requests.
	RequestState string `json:"requestState,omitempty"`
}

// TaskError is the error shape for protocol-level failures (status: failed).
// Mirrors the JSON-RPC error object shape.
type TaskError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// CancelTaskResultV2 is the response to tasks/cancel in v2.
// Same flat shape as GetTaskResultV2.
type CancelTaskResultV2 struct {
	TaskInfo
	RequestState string `json:"requestState,omitempty"`
}
