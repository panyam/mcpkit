package client

// V2 task client helpers — SEP-2663 (Tasks Extension), SEP-2557 (resultType
// discriminator), SEP-2322 (MRTR).
//
// Differences from the v1 helpers in tasks_v1.go:
//   - No client-side task hint: the server decides whether tools/call returns
//     a sync ToolResult or a CreateTaskResult. ToolCall returns a polymorphic
//     ToolCallResult so callers can branch on which shape arrived.
//   - tasks/get returns DetailedTask with inlined result/error/inputRequests.
//   - tasks/update is the new resume path for MRTR input rounds.
//   - tasks/cancel returns an empty ack.
//   - WaitForTask polls tasks/get and honors the server's PollIntervalMs
//     hint. Per SEP-2663 a caller that has issued tasks/cancel may abort
//     the poll loop without waiting for "cancelled" status to surface;
//     cancel the ctx passed to WaitForTask and it returns context.Canceled.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
)

// --- Public option types ---

// WaitOptions configures WaitForTask.
type WaitOptions struct {
	// PollInterval overrides the server's PollIntervalMs hint.
	// 0 (the default) means: respect whatever the server returned, with a
	// 1-second floor and a 30-second cap if the server didn't say.
	PollInterval time.Duration
}

// ToolCallResult is the discriminated union returned by ToolCall. Exactly
// one of Sync, Task, or InputRequired is non-nil — branch on which is set
// (or use the IsTask / IsInputRequired helpers).
type ToolCallResult struct {
	// Sync is populated when the server ran the tool to completion in the
	// same request and returned a ToolResult directly (resultType:
	// "complete" or absent).
	Sync *core.ToolResult

	// Task is populated when the server elected to create a task (the
	// resultType: "task" discriminator was present on the response).
	Task *core.CreateTaskResult

	// InputRequired is populated when the server returned an SEP-2322
	// InputRequiredResult — it needs more input before it can produce a
	// final result. Callers using the bare ToolCall must handle this
	// themselves (resolve inputRequests, retry tools/call with
	// inputResponses + requestState); CallToolWithInputs handles the
	// loop automatically. Renamed from Incomplete in lockstep with
	// SEP-2322. The `requestState` echo on retry remains valid for the
	// MRTR surface even though the tasks-v2 wire no longer carries it.
	InputRequired *core.InputRequiredResult
}

// IsTask reports whether the result is the task-creation variant.
func (r *ToolCallResult) IsTask() bool { return r != nil && r.Task != nil }

// IsInputRequired reports whether the server returned an InputRequiredResult
// (SEP-2322 ephemeral MRTR). Callers needing the auto-retry loop should
// use CallToolWithInputs instead of inspecting this directly. Renamed
// from IsIncomplete to track the SEP-2322 wire-variant rename.
func (r *ToolCallResult) IsInputRequired() bool {
	return r != nil && r.InputRequired != nil
}

// --- Wire-format params ---

type tasksGetParams struct {
	TaskID string `json:"taskId"`
}

type tasksCancelParams struct {
	TaskID string `json:"taskId"`
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

// parseToolCallResult inspects the resultType discriminator on a tools/call
// response and decodes into the matching typed shape. Exposed as a top-level
// helper so other callers (e.g. typed wrappers) can reuse the dispatch.
func parseToolCallResult(raw json.RawMessage) (*ToolCallResult, error) {
	var probe struct {
		ResultType core.ResultType `json:"resultType"`
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
	case core.ResultTypeInputRequired:
		var r core.InputRequiredResult
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("unmarshal InputRequiredResult: %w", err)
		}
		return &ToolCallResult{InputRequired: &r}, nil
	}

	var r core.ToolResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("unmarshal ToolResult: %w", err)
	}
	return &ToolCallResult{Sync: &r}, nil
}

// --- tasks/get / tasks/update / tasks/cancel ---

// GetTask fetches the current state of a v2 task as a DetailedTask, with
// inlined result / error / inputRequests depending on status. Idempotent —
// safe to call as often as needed; servers gate this method on the
// io.modelcontextprotocol/tasks extension being negotiated.
func GetTask(c *Client, taskID string) (*core.DetailedTask, error) {
	resp, err := c.Call("tasks/get", tasksGetParams{TaskID: taskID})
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
func CancelTask(c *Client, taskID string) error {
	if _, err := c.Call("tasks/cancel", tasksCancelParams{TaskID: taskID}); err != nil {
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
//   - else the server's PollIntervalMs on the most recent response,
//   - else the 1-second default.
//
// Returns the final DetailedTask snapshot (which inlines the result / error
// / inputRequests per SEP-2663). Note that input_required is NOT terminal —
// callers wanting to handle the MRTR resume should poll until terminal or
// use a tighter loop that checks for input_required and calls UpdateTask in
// between.
//
// Cancel-poll abort (SEP-2663): a caller that issues tasks/cancel may stop
// polling immediately without waiting for the server to surface "cancelled"
// status. The recommended pattern is to derive a child context for the wait
// and cancel it after CancelTask returns:
//
//	pollCtx, stopPoll := context.WithCancel(ctx)
//	defer stopPoll()
//	go func() {
//	    <-userCancelSignal
//	    client.CancelTask(c, taskID)
//	    stopPoll()
//	}()
//	dt, err := client.WaitForTask(pollCtx, taskID)  // err == context.Canceled on abort
func WaitForTask(ctx context.Context, c *Client, taskID string, opts ...WaitOptions) (*core.DetailedTask, error) {
	var override time.Duration
	if len(opts) > 0 {
		override = opts[0].PollInterval
	}

	for {
		// Honor an already-cancelled ctx before issuing the next poll so a
		// caller that cancels just after a poll returns short-circuits
		// without one more tasks/get round-trip.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		dt, err := GetTask(c, taskID)
		if err != nil {
			return nil, err
		}
		if dt.Status.IsTerminal() {
			return dt, nil
		}

		wait := nextPollWait(override, dt.PollIntervalMs)
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
