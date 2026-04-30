package client

// V2 task client helpers — SEP-2663 (Tasks Extension), SEP-2557 (result_type
// discriminator), SEP-2322 (MRTR).
//
// Differences from the v1 helpers in tasks_v1.go:
//   - No client-side task hint: the server decides whether tools/call returns
//     a sync ToolResult or a CreateTaskResult. ToolCall returns a polymorphic
//     ToolCallResult so callers can branch on which shape arrived.
//   - tasks/get returns DetailedTask with inlined result/error/inputRequests.
//   - tasks/update is the new resume path for MRTR input rounds.
//   - tasks/cancel returns an empty ack.
//   - WaitForTask polls tasks/get and honors the server's
//     PollIntervalMilliseconds hint, automatically echoing requestState.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
)

// --- Public option types ---

// TaskOptions configures a single tasks/get or tasks/cancel call.
// The zero value sends no requestState; pass an explicit value to echo a
// requestState the server returned on a previous response (SEP-2322 stateless
// deployments). Polling helpers (WaitForTask) thread requestState
// automatically and don't require callers to pass it manually.
type TaskOptions struct {
	// RequestState is the opaque session-continuation token the server
	// returned on the most recent DetailedTask. Echoed verbatim — clients
	// MUST treat it as opaque.
	RequestState string
}

// WaitOptions configures WaitForTask.
type WaitOptions struct {
	// PollInterval overrides the server's PollIntervalMilliseconds hint.
	// 0 (the default) means: respect whatever the server returned, with a
	// 1-second floor and a 30-second cap if the server didn't say.
	PollInterval time.Duration

	// RequestState seeds the echo loop. When the server returns an updated
	// requestState in a poll response, WaitForTask switches to using it on
	// the next call.
	RequestState string
}

// ToolCallResult is the discriminated union returned by ToolCall. Exactly
// one of Sync, Task, or Incomplete is non-nil — branch on which is set
// (or use the IsTask / IsIncomplete helpers).
type ToolCallResult struct {
	// Sync is populated when the server ran the tool to completion in the
	// same request and returned a ToolResult directly (result_type:
	// "complete" or absent).
	Sync *core.ToolResult

	// Task is populated when the server elected to create a task (the
	// result_type: "task" discriminator was present on the response).
	Task *core.CreateTaskResult

	// Incomplete is populated when the server returned an SEP-2322
	// IncompleteResult — it needs more input before it can produce a
	// final result. Callers using the bare ToolCall must handle this
	// themselves (resolve inputRequests, retry tools/call with
	// inputResponses + requestState); CallToolWithInputs handles the
	// loop automatically.
	Incomplete *core.IncompleteResult
}

// IsTask reports whether the result is the task-creation variant.
func (r *ToolCallResult) IsTask() bool { return r != nil && r.Task != nil }

// IsIncomplete reports whether the server returned an IncompleteResult
// (SEP-2322 ephemeral MRTR). Callers needing the auto-retry loop should
// use CallToolWithInputs instead of inspecting this directly.
func (r *ToolCallResult) IsIncomplete() bool { return r != nil && r.Incomplete != nil }

// --- Wire-format params ---

type tasksGetParams struct {
	TaskID       string `json:"taskId"`
	RequestState string `json:"requestState,omitempty"`
}

type tasksCancelParams struct {
	TaskID       string `json:"taskId"`
	RequestState string `json:"requestState,omitempty"`
}

// --- Polymorphic tools/call ---

// ToolCall invokes a tool, transparently handling both the sync ToolResult
// and the SEP-2663 CreateTaskResult shapes. Branch on result.IsTask() (or
// the Sync / Task fields directly) to know which arrived.
//
// Servers gate task creation on the io.modelcontextprotocol/tasks extension,
// so this only ever returns a Task result if the client declared the
// extension during initialize (or per-request via SEP-2575 _meta).
func ToolCall(c *Client, name string, args any) (*ToolCallResult, error) {
	resp, err := c.Call("tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	})
	if err != nil {
		return nil, err
	}
	return parseToolCallResult(resp.Raw)
}

// parseToolCallResult inspects the result_type discriminator on a tools/call
// response and decodes into the matching typed shape. Exposed as a top-level
// helper so other callers (e.g. typed wrappers) can reuse the dispatch.
func parseToolCallResult(raw json.RawMessage) (*ToolCallResult, error) {
	var probe struct {
		ResultType core.ResultType `json:"result_type"`
	}
	// Probe failure is harmless — fall through to the sync shape, which
	// will surface its own decode error if the response is genuinely malformed.
	_ = json.Unmarshal(raw, &probe)

	switch probe.ResultType {
	case core.ResultTypeTask:
		var r core.CreateTaskResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("unmarshal CreateTaskResult: %w", err)
		}
		return &ToolCallResult{Task: &r}, nil
	case core.ResultTypeIncomplete:
		var r core.IncompleteResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("unmarshal IncompleteResult: %w", err)
		}
		return &ToolCallResult{Incomplete: &r}, nil
	}

	var r core.ToolResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal ToolResult: %w", err)
	}
	return &ToolCallResult{Sync: &r}, nil
}

// --- tasks/get / tasks/update / tasks/cancel ---

// GetTask fetches the current state of a v2 task as a DetailedTask, with
// inlined result / error / inputRequests / requestState depending on status.
// Idempotent — safe to call as often as needed; servers gate this method on
// the io.modelcontextprotocol/tasks extension being negotiated.
func GetTask(c *Client, taskID string, opts ...TaskOptions) (*core.DetailedTask, error) {
	params := tasksGetParams{TaskID: taskID}
	if len(opts) > 0 {
		params.RequestState = opts[0].RequestState
	}
	resp, err := c.Call("tasks/get", params)
	if err != nil {
		return nil, err
	}
	var dt core.DetailedTask
	if err := json.Unmarshal(resp.Raw, &dt); err != nil {
		return nil, fmt.Errorf("unmarshal DetailedTask: %w", err)
	}
	return &dt, nil
}

// UpdateTask resumes a task parked in input_required by delivering the
// inputResponses the server's pending inputRequests are waiting on. The
// server matches keys, hands the payloads to the waiting goroutine, and
// the task transitions back to working (or directly to a terminal state
// if the tool finishes immediately after).
//
// Returns nil on success — the server response is an empty ack per
// SEP-2663. Observe the resulting state via the next GetTask poll.
func UpdateTask(c *Client, req core.UpdateTaskRequest) error {
	if req.TaskID == "" {
		return fmt.Errorf("UpdateTask: missing TaskID")
	}
	if _, err := c.Call("tasks/update", req); err != nil {
		return err
	}
	return nil
}

// CancelTask cancels a running task. Returns nil on success — the server
// response is an empty ack per SEP-2663 (no task state). Issue GetTask if
// you need to observe the resulting "cancelled" status.
func CancelTask(c *Client, taskID string, opts ...TaskOptions) error {
	params := tasksCancelParams{TaskID: taskID}
	if len(opts) > 0 {
		params.RequestState = opts[0].RequestState
	}
	if _, err := c.Call("tasks/cancel", params); err != nil {
		return err
	}
	return nil
}

// --- Polling helpers ---

const (
	// defaultPollInterval is the floor used when neither the caller nor the
	// server suggests one. Tuned for "feels responsive in tests, doesn't
	// hammer in production."
	defaultPollInterval = 1 * time.Second
	// maxPollInterval caps server-suggested intervals so a misconfigured
	// server can't park a poll loop indefinitely.
	maxPollInterval = 30 * time.Second
)

// WaitForTask polls tasks/get until the task reaches a terminal state or
// ctx fires. Each iteration honors:
//
//   - opts[0].PollInterval if non-zero (caller override),
//   - else the server's PollIntervalMilliseconds on the most recent response,
//   - else the 1-second default.
//
// requestState is threaded automatically: each poll echoes the requestState
// the server returned on the previous response. Returns the final
// DetailedTask snapshot (which inlines the result / error / inputRequests
// per SEP-2663). Note that input_required is NOT terminal — callers wanting
// to handle the MRTR resume should poll until terminal or use a tighter
// loop that checks for input_required and calls UpdateTask in between.
func WaitForTask(ctx context.Context, c *Client, taskID string, opts ...WaitOptions) (*core.DetailedTask, error) {
	var override time.Duration
	state := ""
	if len(opts) > 0 {
		override = opts[0].PollInterval
		state = opts[0].RequestState
	}

	for {
		dt, err := GetTask(c, taskID, TaskOptions{RequestState: state})
		if err != nil {
			return nil, err
		}
		if dt.Status.IsTerminal() {
			return dt, nil
		}
		if dt.RequestState != "" {
			state = dt.RequestState
		}

		wait := nextPollWait(override, dt.PollIntervalMilliseconds)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// nextPollWait picks the next poll interval, applying the caller override,
// the server hint, and the floor/ceiling.
func nextPollWait(override time.Duration, serverHintMs *int) time.Duration {
	if override > 0 {
		return override
	}
	if serverHintMs != nil && *serverHintMs > 0 {
		hint := time.Duration(*serverHintMs) * time.Millisecond
		if hint > maxPollInterval {
			return maxPollInterval
		}
		return hint
	}
	return defaultPollInterval
}
