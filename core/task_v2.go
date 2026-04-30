package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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

	// RequestState is the opaque session-continuation token. // SEP-2322
	RequestState string `json:"requestState,omitempty"`
}

// MarshalJSON defaults ResultType to ResultTypeComplete when empty so every
// tasks/get response carries the SEP-2322 polymorphic discriminator without
// every call site having to set it. The task lifecycle stays on Status.
func (d DetailedTask) MarshalJSON() ([]byte, error) {
	type alias DetailedTask
	if d.ResultType == "" {
		d.ResultType = ResultTypeComplete
	}
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
// returned in DetailedTask.InputRequests, optionally echoing RequestState. // SEP-2663
type UpdateTaskRequest struct {
	TaskID         string         `json:"taskId"`
	InputResponses InputResponses `json:"inputResponses,omitempty"`
	RequestState   string         `json:"requestState,omitempty"` // SEP-2322
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
	if u.ResultType == "" {
		u.ResultType = ResultTypeComplete
	}
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
	if c.ResultType == "" {
		c.ResultType = ResultTypeComplete
	}
	return json.Marshal(alias(c))
}

// --- SEP-2322 requestState signing ---
//
// SEP-2663 says servers MUST treat requestState as attacker-controlled. The
// helpers below implement an HMAC-SHA256-signed encoding so a server that
// minted a token can verify the same token came back unmodified, with no
// per-task state on the server side (stateless deployments).
//
// Wire format: "<base64url-signature>.<base64url-payload>" where payload is
// the JSON encoding of requestStatePayload {taskId, exp}. base64url is
// chosen to keep the token URL/header-safe without escaping.

// ErrRequestStateMalformed indicates the encoded requestState couldn't be
// parsed (missing separator, bad base64, bad JSON inside the payload).
var ErrRequestStateMalformed = errors.New("requestState malformed")

// ErrRequestStateInvalidSignature indicates the HMAC signature did not match
// — either tampered payload or wrong key.
var ErrRequestStateInvalidSignature = errors.New("requestState signature invalid")

// ErrRequestStateExpired indicates the payload's expiry is in the past.
var ErrRequestStateExpired = errors.New("requestState expired")

// requestStatePayload is the JSON body wrapped inside a signed
// requestState token. Compact (two fields) so the encoded form stays small.
type requestStatePayload struct {
	TaskID string `json:"taskId"`
	Exp    int64  `json:"exp"` // unix seconds
}

// SignRequestState produces an HMAC-SHA256-signed requestState token for
// the given taskID, valid for ttl. The encoded form is opaque to clients
// and round-trippable via VerifyRequestState. Panics if key is empty —
// callers MUST check before invoking (see v2TaskRuntime.makeRequestState
// which falls back to plaintext when the key is unset).
func SignRequestState(key []byte, taskID string, ttl time.Duration) string {
	if len(key) == 0 {
		// Indicates a programming error — the runtime should fall back to
		// plain taskID in legacy mode rather than calling Sign with no key.
		panic("core.SignRequestState: empty key")
	}
	payload := requestStatePayload{
		TaskID: taskID,
		Exp:    time.Now().Add(ttl).Unix(),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		// json.Marshal of a tiny struct can't realistically fail.
		panic(fmt.Sprintf("core.SignRequestState: marshal payload: %v", err))
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sig) + "." +
		base64.RawURLEncoding.EncodeToString(payloadBytes)
}

// VerifyRequestState checks an incoming requestState token against the
// signing key and current time. Returns the embedded taskID on success.
// Errors:
//   - ErrRequestStateMalformed: structural parse failures (split, base64, JSON)
//   - ErrRequestStateInvalidSignature: signature mismatch (tampered or wrong key)
//   - ErrRequestStateExpired: payload exp is in the past
//
// Uses hmac.Equal for constant-time signature comparison.
func VerifyRequestState(key []byte, state string) (taskID string, err error) {
	if len(key) == 0 {
		return "", ErrRequestStateMalformed
	}
	dot := strings.IndexByte(state, '.')
	if dot < 0 {
		return "", ErrRequestStateMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(state[:dot])
	if err != nil {
		return "", ErrRequestStateMalformed
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(state[dot+1:])
	if err != nil {
		return "", ErrRequestStateMalformed
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(payloadBytes)
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return "", ErrRequestStateInvalidSignature
	}
	var payload requestStatePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", ErrRequestStateMalformed
	}
	if payload.Exp > 0 && time.Now().Unix() > payload.Exp {
		return "", ErrRequestStateExpired
	}
	return payload.TaskID, nil
}
