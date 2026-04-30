package core

import "encoding/json"

// Tasks v2 types — SEP-2663 (Tasks Extension), SEP-2557 (resultType
// discriminator), and MRTR base types from SEP-2322.
//
// Wire-format differences from v1:
//   - tools/call carries a resultType discriminator: "task" (this file),
//     "complete" / "incomplete" (MRTR — see ResultType constants).
//   - tasks/get returns DetailedTask: a discriminated union by status that
//     inlines result / error / inputRequests / requestState in one trip.
//   - tasks/cancel and tasks/update return empty acks (no task state).
//   - Task wire fields renamed: ttlSeconds, pollIntervalMilliseconds.
//     Internal stores still use ms; conversion happens at the wire boundary.
//   - parentTaskId removed (SEP-2663 does not model task parentage).
//   - Tool errors: status "completed" with result.isError == true.
//   - Protocol errors: status "failed" with the error inlined.

// ResultType is the discriminator on a tools/call response.
//
// "task" indicates a task-based response (CreateTaskResult). The "complete"
// and "incomplete" values come from MRTR (Multi-Round Tool Result, SEP-2322)
// and signal whether a tool result is final or expects further input rounds.
type ResultType string

const (
	ResultTypeTask       ResultType = "task"
	ResultTypeComplete   ResultType = "complete"   // SEP-2322
	ResultTypeIncomplete ResultType = "incomplete" // SEP-2322
)

// InputRequest is a single MRTR input request enqueued by a server during
// tool execution: a method (e.g., "elicitation/create", "sampling/createMessage")
// plus opaque params encoded per that method's request schema. // SEP-2322
type InputRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// InputRequests is the wire-format map from request key to InputRequest.
// Keys are server-chosen identifiers (e.g., "elicit-1") that the client
// echoes verbatim in the matching InputResponses entry. // SEP-2322
type InputRequests = map[string]InputRequest

// InputResponses is the wire-format map from request key to opaque response
// payload. Keys MUST match those previously returned in InputRequests. The
// payload shape is defined by the original request method. // SEP-2322
type InputResponses = map[string]json.RawMessage

// TaskInfoV2 is the v2 wire shape for task metadata. Differences from
// (v1) TaskInfo:
//   - ttl renamed to ttlSeconds (units now part of the field name).
//   - pollInterval renamed to pollIntervalMilliseconds.
//   - parentTaskId removed; SEP-2663 does not expose task parentage.
//
// Internally, the TaskStore uses TaskInfo (ttl in milliseconds). Conversion
// happens at the v2 wire boundary in the tasks_v2 server handlers.
type TaskInfoV2 struct {
	TaskID                   string     `json:"taskId"`
	Status                   TaskStatus `json:"status"`
	StatusMessage            string     `json:"statusMessage,omitempty"`
	CreatedAt                string     `json:"createdAt"`
	LastUpdatedAt            string     `json:"lastUpdatedAt"`
	TTLSeconds               *int       `json:"ttlSeconds"`                         // required+nullable; null = unlimited
	PollIntervalMilliseconds *int       `json:"pollIntervalMilliseconds,omitempty"` // optional
}

// CreateTaskResult is returned by tools/call when the server elects to handle
// the call as an async task (resultType: "task"). Per SEP-2663, this envelope
// MUST NOT carry result, error, inputRequests, or requestState — those belong
// on tasks/get's DetailedTask response.
type CreateTaskResult struct {
	ResultType ResultType `json:"resultType"`
	Task       TaskInfoV2 `json:"task"`
}

// DetailedTask is the SEP-2663 discriminated union returned by tasks/get.
// The Status field discriminates which optional fields are populated:
//
//   - working          → no inlined payload
//   - input_required   → InputRequests populated
//   - completed        → Result populated
//   - failed           → Error populated
//   - cancelled        → no inlined payload
//
// RequestState is opaque session-continuation state for stateless deployments;
// servers MAY include it on any status, clients MUST echo it back on the
// next tasks/get / tasks/update / tasks/cancel for the same task. // SEP-2322
type DetailedTask struct {
	TaskInfoV2

	// Result is inlined when Status == TaskCompleted. Includes the original
	// ToolResult (with isError flag for tool-side errors).
	Result *ToolResult `json:"result,omitempty"`

	// Error is inlined when Status == TaskFailed. Mirrors the JSON-RPC error
	// shape and represents protocol-level failures only.
	Error *TaskError `json:"error,omitempty"`

	// InputRequests is populated when Status == TaskInputRequired and lists
	// the MRTR input requests the client must satisfy via tasks/update. // SEP-2322
	InputRequests InputRequests `json:"inputRequests,omitempty"`

	// RequestState is the opaque session-continuation token. // SEP-2322
	RequestState string `json:"requestState,omitempty"`
}

// SEP-2663 narrowed aliases — convenient names for callers that have already
// branched on status. The wire shape is identical to DetailedTask; the alias
// just communicates which fields the caller expects to be populated.
type (
	WorkingTask       = DetailedTask
	InputRequiredTask = DetailedTask
	CompletedTask     = DetailedTask
	FailedTask        = DetailedTask
	CancelledTask     = DetailedTask
)

// GetTaskResult is the response shape for tasks/get. Identical to DetailedTask.
type GetTaskResult = DetailedTask

// TaskError is the error shape for protocol-level failures (status: failed).
// Mirrors the JSON-RPC error object shape.
type TaskError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// UpdateTaskRequest is the params payload for tasks/update — the MRTR-driven
// resume path. The client supplies InputResponses keyed by the same ids
// returned in DetailedTask.InputRequests, optionally echoing RequestState. // SEP-2663
type UpdateTaskRequest struct {
	TaskID         string         `json:"taskId"`
	InputResponses InputResponses `json:"inputResponses,omitempty"`
	RequestState   string         `json:"requestState,omitempty"` // SEP-2322
}

// UpdateTaskResult is the (empty) ack returned by tasks/update. Servers
// resume task execution asynchronously; clients learn the outcome via the
// next tasks/get. // SEP-2663
type UpdateTaskResult struct{}

// CancelTaskResult is the (empty) ack returned by tasks/cancel. Per SEP-2663,
// cancellation does not return task state — the client should issue tasks/get
// if it wants to observe the resulting "cancelled" status.
type CancelTaskResult struct{}
