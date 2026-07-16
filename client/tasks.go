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
	"errors"
	"fmt"
	"sync"
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

	// OnStatus observes every polled snapshot (WaitForTask and
	// WaitForTaskWithInput), including the input_required snapshot before
	// any handler runs. Progress display, logging, and metrics hang off
	// this; returning is the only control (observation, not steering).
	// Nil skips.
	OnStatus func(*core.DetailedTask)
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
	// Map (not a typed struct) so core.DeriveMcpName can read taskId and
	// emit the Mcp-Name routing header the server requires for task ops on
	// the SEP-2575 stateless wire (no session to route by). Same reason
	// ToolCall passes a map. See core.DeriveMcpName.
	resp, err := c.Call("tasks/get", map[string]any{"taskId": taskID})
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
	// Map form so DeriveMcpName can read taskId for the Mcp-Name header
	// (see GetTask). inputResponses is included only when non-empty to
	// mirror the struct's `omitempty`.
	params := map[string]any{"taskId": req.TaskID}
	if len(req.InputResponses) > 0 {
		params["inputResponses"] = req.InputResponses
	}
	if _, err := c.Call("tasks/update", params); err != nil {
		return err
	}
	return nil
}

// CancelTask cancels a running task. Returns nil on success — the server
// response is an empty ack per SEP-2663 (no task state). Issue GetTask if
// you need to observe the resulting "cancelled" status.
func CancelTask(c *Client, taskID string) error {
	if _, err := c.Call("tasks/cancel", map[string]any{"taskId": taskID}); err != nil {
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
		if len(opts) > 0 && opts[0].OnStatus != nil {
			opts[0].OnStatus(dt)
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

// WaitForTaskWithInput polls like WaitForTask but also services
// input_required pauses: the pending inputRequests are resolved through
// handler (the same InputHandler shape CallToolWithInputs uses for
// ephemeral MRTR, so one handler covers mid-call input on both the
// ephemeral and task-backed paths), delivered via tasks/update, and polling
// continues to a terminal state. A handler error aborts the wait; the task
// keeps running server-side and can be resumed by a later call or
// cancelled explicitly.
//
// This is the "tighter loop" WaitForTask's doc alludes to: WaitForTask
// deliberately never answers input (its callers poll fire-and-forget
// workloads), so its behavior is unchanged.
func WaitForTaskWithInput(ctx context.Context, c *Client, taskID string, handler InputHandler, opts ...WaitOptions) (*core.DetailedTask, error) {
	if handler == nil {
		return nil, errors.New("WaitForTaskWithInput: handler is required")
	}
	var override time.Duration
	var onStatus func(*core.DetailedTask)
	if len(opts) > 0 {
		override = opts[0].PollInterval
		onStatus = opts[0].OnStatus
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		dt, err := GetTask(c, taskID)
		if err != nil {
			return nil, err
		}
		if onStatus != nil {
			onStatus(dt)
		}
		if dt.Status.IsTerminal() {
			return dt, nil
		}
		if dt.Status == core.TaskInputRequired && len(dt.InputRequests) > 0 {
			responses, err := handler(ctx, dt.InputRequests)
			if err != nil {
				return nil, fmt.Errorf("task %s input: %w", taskID, err)
			}
			if err := UpdateTask(c, core.UpdateTaskRequest{TaskID: taskID, InputResponses: responses}); err != nil {
				return nil, err
			}
			continue
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

// DefaultTaskGrace is the recommended grace window for callers that opt into
// background detach via WaitForTaskOrBackground.
const DefaultTaskGrace = 10 * time.Second

// BackgroundTask is a handle to a task poll that outlived its grace window
// and detached (see WaitForTaskOrBackground). The poll keeps running on a
// context independent of the caller's, so later input_required pauses still
// reach the same InputHandler; the terminal outcome is retained and Done
// signals its arrival.
//
// Safe for concurrent use.
type BackgroundTask struct {
	// TaskID and Tool identify the task for surfaces (task listings); Tool
	// is caller-supplied (the tool name that created it) and may be empty.
	TaskID string
	Tool   string
	// StartedAt is when the originating call began.
	StartedAt time.Time

	c          *Client
	cancelPoll context.CancelFunc

	mu     sync.Mutex
	status core.TaskStatus
	result *core.DetailedTask
	err    error

	done chan struct{}
}

// Status returns the most recent polled status.
func (bt *BackgroundTask) Status() core.TaskStatus {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return bt.status
}

// Done closes when the task reaches a terminal state or the poll aborts.
func (bt *BackgroundTask) Done() <-chan struct{} { return bt.done }

// Result returns the terminal DetailedTask (valid after Done). The error
// covers a poll abort or transport failure; task-level failed/cancelled
// status rides the DetailedTask.
func (bt *BackgroundTask) Result() (*core.DetailedTask, error) {
	bt.mu.Lock()
	defer bt.mu.Unlock()
	return bt.result, bt.err
}

// Cancel requests server-side cancellation (tasks/cancel) and stops the
// poll. Done still closes with the resulting (cancelled) outcome, so
// observers see one lifecycle regardless of how it ended.
func (bt *BackgroundTask) Cancel() error {
	err := CancelTask(bt.c, bt.TaskID)
	bt.cancelPoll()
	return err
}

// WaitForTaskOrBackground races a task against a grace window. It polls (and
// services input_required through handler) like WaitForTaskWithInput, but
// returns as soon as EITHER the task reaches a terminal state within the
// grace (non-nil *core.DetailedTask, nil handle) OR the grace expires with
// the task still running (nil DetailedTask, non-nil *BackgroundTask whose
// poll continues in the background).
//
// The grace HOLDS while the task is actively in input_required: an
// interactive pause is not a reason to detach, so a task that parks for
// input within the window stays inline until it is answered. A grace <= 0
// is equivalent to WaitForTaskWithInput (never detaches).
//
// The tool argument is stored on the returned handle for display only.
func WaitForTaskOrBackground(ctx context.Context, c *Client, taskID, tool string, handler InputHandler, grace time.Duration, opts ...WaitOptions) (*core.DetailedTask, *BackgroundTask, error) {
	if handler == nil {
		return nil, nil, errors.New("WaitForTaskOrBackground: handler is required")
	}
	if grace <= 0 {
		dt, err := WaitForTaskWithInput(ctx, c, taskID, handler, opts...)
		return dt, nil, err
	}

	var userOpts WaitOptions
	if len(opts) > 0 {
		userOpts = opts[0]
	}
	// Poll on a context detached from the caller so a detach is a handoff,
	// not a restart. An inline turn cancel still aborts it via cancelPoll.
	pollCtx, cancelPoll := context.WithCancel(context.WithoutCancel(ctx))
	bt := &BackgroundTask{
		TaskID: taskID, Tool: tool, StartedAt: time.Now(),
		c: c, cancelPoll: cancelPoll, done: make(chan struct{}),
	}
	onStatus := func(dt *core.DetailedTask) {
		bt.mu.Lock()
		bt.status = dt.Status
		bt.mu.Unlock()
		if userOpts.OnStatus != nil {
			userOpts.OnStatus(dt)
		}
	}
	go func() {
		dt, err := WaitForTaskWithInput(pollCtx, c, taskID, handler,
			WaitOptions{PollInterval: userOpts.PollInterval, OnStatus: onStatus})
		bt.mu.Lock()
		bt.result, bt.err = dt, err
		bt.mu.Unlock()
		close(bt.done)
	}()

	timer := time.NewTimer(grace)
	defer timer.Stop()
	for {
		select {
		case <-bt.done:
			dt, err := bt.Result()
			return dt, nil, err
		case <-ctx.Done():
			cancelPoll()
			<-bt.done
			return nil, nil, ctx.Err()
		case <-timer.C:
			if bt.Status() == core.TaskInputRequired {
				timer.Reset(grace)
				continue
			}
			return nil, bt, nil
		}
	}
}
