package core

import (
	"context"
	"encoding/json"
)

// TasksExtensionID is the protocol extension identifier for SEP-2663 Tasks.
// Servers advertise it under capabilities.extensions in the initialize
// response; clients declare support under capabilities.extensions in the
// initialize request (or per-request via SEP-2575 _meta).
const TasksExtensionID = "io.modelcontextprotocol/tasks"

// ClientSupportsTasks checks whether the connected client declared support
// for the SEP-2663 Tasks extension during the initialize handshake.
// Equivalent to ClientSupportsExtension(ctx, TasksExtensionID).
func ClientSupportsTasks(ctx context.Context) bool {
	return ClientSupportsExtension(ctx, TasksExtensionID)
}

// SEP-2663 (Tasks Extension) wire types.
//
// Tasks-v2 builds on the SEP-2322 MRTR base types in core/mrtr.go — the
// ResultType discriminator, InputRequest / InputRequests / InputResponses
// shapes, and the requestState signing helpers all live there because
// SEP-2322 owns them.
//
// Wire-format differences from v1:
//   - tools/call carries a ResultType discriminator: "task" (this file),
//     "complete" / "input_required" (MRTR — see core/mrtr.go).
//   - tasks/get returns DetailedTask: a discriminated union by status that
//     inlines result / error / inputRequests in one trip.
//   - tasks/cancel and tasks/update return empty acks (no task state).
//   - Task wire fields use the Ms suffix: ttlMs, pollIntervalMs (both
//     integer milliseconds). Internal stores already use ms, so the v2
//     wire boundary is a pass-through (no unit conversion).
//   - parentTaskId removed (SEP-2663 does not model task parentage).
//   - Tool errors: status "completed" with result.isError == true.
//   - Protocol errors: status "failed" with the error inlined.

// TaskInfoV2 is the v2 wire shape for task metadata. Differences from
// (v1) TaskInfo:
//   - ttl renamed to ttlMs. Units in the field name. Integer milliseconds.
//   - pollInterval renamed to pollIntervalMs. Integer milliseconds.
//   - parentTaskId removed. SEP-2663 does not expose task parentage.
//
// The TaskStore already uses milliseconds internally, so the v2 wire surface
// is a pass-through (no unit conversion at the boundary). Per SEP-2663 the
// server MAY change ttlMs over the lifetime of a task (e.g. reset on each
// tasks/get to extend liveness while a client is observing it).
type TaskInfoV2 struct {
	TaskID         string     `json:"taskId"`
	Status         TaskStatus `json:"status"`
	StatusMessage  string     `json:"statusMessage,omitempty"`
	CreatedAt      string     `json:"createdAt"`
	LastUpdatedAt  string     `json:"lastUpdatedAt"`
	TTLMs          *int       `json:"ttlMs"`                    // required+nullable, null = unlimited
	PollIntervalMs *int       `json:"pollIntervalMs,omitempty"` // optional
}

// CreateTaskResult is returned by tools/call when the server elects to handle
// the call as an async task. Per SEP-2663 it is `Result & Task` — a flat
// intersection where the discriminator and the task fields share one object
// (taskId / status / ttlMs / ... at the top level alongside resultType).
//
// Per SEP-2663, this envelope MUST NOT carry result, error, inputRequests,
// or requestState — those belong on tasks/get's DetailedTask response.
//
// Wire shape (flat — no `task` wrapper):
//
//	{"resultType": "task", "taskId": "...", "status": "working",
//	 "createdAt": "...", "lastUpdatedAt": "...", "ttlMs": 60000,
//	 "pollIntervalMs": 1000}
//
// TaskInfoV2 is embedded so encoding/json promotes its fields to the parent
// (same trick DetailedTask uses); no custom MarshalJSON is needed.
type CreateTaskResult struct {
	ResultType ResultType `json:"resultType"`
	TaskInfoV2
}

// toolResponse marks CreateTaskResult as a [ToolResponse] variant. A handler
// can return this directly when it has already minted a task identifier and
// wants to hand the response off to the client without going through the
// middleware-driven goroutine spawn path (rare — most handlers return
// [GoAsyncResult] and let the tasks middleware do the work).
func (CreateTaskResult) toolResponse() {}

// DetailedTask is the SEP-2663 discriminated union returned by tasks/get.
// The Status field discriminates which optional fields are populated:
//
//   - working          → no inlined payload
//   - input_required   → InputRequests populated
//   - completed        → Result populated
//   - failed           → Error populated
//   - cancelled        → no inlined payload
type DetailedTask struct {
	// ResultType is the SEP-2322 polymorphic-dispatch discriminator. For
	// tasks/get responses it is always "complete" — the JSON-RPC request
	// itself completes with this response, even when the underlying task
	// is still running. (The task lifecycle is on the Status field.)
	// MarshalJSON defaults this to ResultTypeComplete when empty so
	// existing struct literals don't have to set it.
	ResultType ResultType `json:"resultType"`

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
}

// MarshalJSON defaults ResultType to ResultTypeComplete when empty so every
// tasks/get response carries the SEP-2322 polymorphic discriminator without
// every call site having to set it. The task lifecycle stays on Status.
func (d DetailedTask) MarshalJSON() ([]byte, error) {
	type alias DetailedTask
	defaultResultType(&d.ResultType, ResultTypeComplete)
	return json.Marshal(alias(d))
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
// returned in DetailedTask.InputRequests. // SEP-2663
type UpdateTaskRequest struct {
	TaskID         string         `json:"taskId"`
	InputResponses InputResponses `json:"inputResponses,omitempty"`
}

// UpdateTaskResult is the (essentially empty) ack returned by tasks/update.
// Servers resume task execution asynchronously; clients learn the outcome
// via the next tasks/get. The wire payload carries only the SEP-2322
// resultType discriminator so polymorphic dispatch on tools/call vs
// tasks/update vs tasks/get is uniform across the protocol. // SEP-2663
type UpdateTaskResult struct {
	// ResultType is the SEP-2322 polymorphic-dispatch discriminator. Always
	// "complete" for the tasks/update ack (defaulted by MarshalJSON when empty).
	ResultType ResultType `json:"resultType"`
}

// MarshalJSON defaults ResultType to ResultTypeComplete so callers using
// the zero value (UpdateTaskResult{}) emit the spec-compliant wire shape
// without thinking about it.
func (u UpdateTaskResult) MarshalJSON() ([]byte, error) {
	type alias UpdateTaskResult
	defaultResultType(&u.ResultType, ResultTypeComplete)
	return json.Marshal(alias(u))
}

// CancelTaskResult is the (essentially empty) ack returned by tasks/cancel.
// Per SEP-2663, cancellation does not return task state — the client should
// issue tasks/get if it wants to observe the resulting "cancelled" status.
// Like UpdateTaskResult, the wire payload carries only the SEP-2322
// resultType discriminator.
type CancelTaskResult struct {
	// ResultType is the SEP-2322 polymorphic-dispatch discriminator. Always
	// "complete" for the tasks/cancel ack (defaulted by MarshalJSON when empty).
	ResultType ResultType `json:"resultType"`
}

// MarshalJSON defaults ResultType to ResultTypeComplete so the zero value
// (CancelTaskResult{}) emits the spec-compliant wire shape automatically.
func (c CancelTaskResult) MarshalJSON() ([]byte, error) {
	type alias CancelTaskResult
	defaultResultType(&c.ResultType, ResultTypeComplete)
	return json.Marshal(alias(c))
}
