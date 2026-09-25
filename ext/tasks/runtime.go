package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// generateTaskID mints a fresh task identifier. Format: "task-<24-char-hex>"
// — 96 bits of entropy. Duplicated from server/ for the v2 surface so this
// package has no cross-version coupling; v1 keeps its own copy.
func generateTaskID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "task-" + hex.EncodeToString(b)
}

// activeTask holds per-task runtime state for a running v2 async task.
// Distinct from server/ v1's activeTask (which carries a side-channel
// requests channel that v2 doesn't use); kept separate so v1 retirement
// is a pure delete in server/.
type activeTask struct {
	cancel     context.CancelFunc
	inputState *v2InputState
}

// TaskContext is the typed context for tool handlers running as a v2
// background task. It embeds core.ToolContext and adds task-specific
// methods (TaskID, ProgressToken, TaskElicit, TaskSample, SetStatus,
// SetStatusMessage).
//
// Tool handlers retrieve this via GetTaskContext(ctx). It is nil for
// synchronous (non-task) tool invocations.
//
// Usage:
//
//	func myToolHandler(ctx core.ToolContext, input MyInput) (string, error) {
//	    tc := tasks.GetTaskContext(ctx)
//	    if tc != nil {
//	        result, err := tc.TaskElicit(core.ElicitationRequest{...})
//	    }
//	    return "done", nil
//	}
//
// Distinct from server.TaskContext (which serves the v1 surface). A tool
// registered for both v1 and v2 (via RegisterHybrid) sees the appropriate
// type when invoked under each runtime.
type TaskContext struct {
	core.ToolContext
	taskID        string
	sessionID     string
	store         server.TaskStore
	inputState    *v2InputState // SEP-2663 input request flow
	progressToken any           // original _meta.progressToken from client
}

type taskContextKey struct{}

// WithTaskContext returns a context carrying the TaskContext.
func WithTaskContext(ctx context.Context, tc *TaskContext) context.Context {
	return context.WithValue(ctx, taskContextKey{}, tc)
}

// GetTaskContext retrieves the TaskContext from a tool handler's context,
// binding it to the provided ToolContext for elicitation/sampling.
// Returns nil if the tool was not invoked as a v2 task.
func GetTaskContext(ctx core.ToolContext) *TaskContext {
	tc, _ := ctx.Value(taskContextKey{}).(*TaskContext)
	if tc != nil {
		tc.ToolContext = ctx
	}
	return tc
}

// TaskID returns the task's unique identifier.
func (tc *TaskContext) TaskID() string {
	return tc.taskID
}

// ProgressToken returns the client's original _meta.progressToken from the
// tools/call request. Use this instead of TaskID() for EmitProgress so
// progress notifications correlate with what the client expects.
// Returns nil if the client didn't send a progressToken.
func (tc *TaskContext) ProgressToken() any {
	return tc.progressToken
}

// ErrTaskTerminal is returned by SetStatus and SetStatusMessage when the task
// has already completed, failed or been cancelled. The task is left as it is.
var ErrTaskTerminal = errors.New("tasks: task is already in a terminal state")

// SetStatus transitions the task to a new status and emits a notifications/tasks
// status event so polling/streaming clients pick up the new state. It returns
// ErrTaskTerminal, and changes nothing, once the task is terminal.
func (tc *TaskContext) SetStatus(status core.TaskStatus) error {
	return tc.update(func(t *core.TaskInfo) {
		t.Status = status
	})
}

// SetStatusMessage sets the task's statusMessage and emits notifications/tasks,
// leaving the status as it is. SEP-2663 forbids notifications/progress on a
// task, so this is how a task reports how far along it is:
//
//	for i, item := range items {
//	    tc.SetStatusMessage(fmt.Sprintf("Processing %d/%d", i+1, len(items)))
//	    ...
//	}
//
// It returns ErrTaskTerminal, and changes nothing, once the task is terminal,
// so a progress update racing tasks/cancel cannot overwrite the cancellation.
func (tc *TaskContext) SetStatusMessage(msg string) error {
	return tc.update(func(t *core.TaskInfo) {
		t.StatusMessage = msg
	})
}

// update applies fn to a non-terminal task, stamps lastUpdatedAt and notifies.
// TaskStore.Update itself has no terminal guard (only StoreTerminalResult
// does), so without this check a late SetStatus(TaskWorking) would move a
// cancelled task back to working.
func (tc *TaskContext) update(fn func(*core.TaskInfo)) error {
	terminal := false
	err := tc.store.Update(tc.taskID, tc.sessionID, func(t *core.TaskInfo) {
		if t.Status.IsTerminal() {
			terminal = true
			return
		}
		fn(t)
		t.LastUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	})
	if err != nil {
		return err
	}
	if terminal {
		return ErrTaskTerminal
	}
	notifyTransitionStatus(tc.Context, tc.store, tc.taskID, tc.sessionID)
	return nil
}

// TaskElicit asks the client for elicitation input from inside a running
// v2 task. The request is stashed on the task's inputState under a
// monotonic key, the task transitions to input_required, and the
// goroutine blocks on a per-key waiter channel. The client observes the
// pending request via tasks/get's DetailedTask.InputRequests and resumes
// the task by sending the matching response via tasks/update. ctx
// cancellation unblocks the wait with the corresponding context error.
//
// Status transitions: working → input_required → working.
func (tc *TaskContext) TaskElicit(req core.ElicitationRequest) (core.ElicitationResult, error) {
	if req.Meta == nil {
		req.Meta = &core.ElicitationMeta{}
	}
	req.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: tc.taskID}

	params, err := core.MarshalJSON(req)
	if err != nil {
		return core.ElicitationResult{}, fmt.Errorf("marshal elicitation request: %w", err)
	}

	resp, err := tc.requestInput("elicit", "elicitation/create", params)
	if err != nil {
		return core.ElicitationResult{}, err
	}
	var result core.ElicitationResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return core.ElicitationResult{}, fmt.Errorf("unmarshal elicitation result: %w", err)
	}
	return result, nil
}

// TaskSample asks the client for a sampling/createMessage response from
// inside a running v2 task. See TaskElicit for the input-state flow.
//
// Status transitions: working → input_required → working.
func (tc *TaskContext) TaskSample(req core.CreateMessageRequest) (core.CreateMessageResult, error) {
	if req.Meta == nil {
		req.Meta = &core.SamplingMeta{}
	}
	req.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: tc.taskID}

	params, err := core.MarshalJSON(req)
	if err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("marshal sampling request: %w", err)
	}

	resp, err := tc.requestInput("sample", "sampling/createMessage", params)
	if err != nil {
		return core.CreateMessageResult{}, err
	}
	var result core.CreateMessageResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("unmarshal sampling result: %w", err)
	}
	return result, nil
}

// requestInput implements the SEP-2663 input-request flow used by
// TaskElicit / TaskSample. methodPrefix is a short readable tag used to
// mint stable keys ("elicit-1", "sample-2"); jsonRpcMethod is the actual
// JSON-RPC method name routed to the client.
func (tc *TaskContext) requestInput(methodPrefix, jsonRpcMethod string, params json.RawMessage) (json.RawMessage, error) {
	_, waiter := tc.inputState.Enqueue(methodPrefix, core.InputRequest{
		Method: jsonRpcMethod,
		Params: params,
	})

	// Transition to input_required and notify status so polling clients
	// pick up the new pending request on the next tasks/get.
	if err := tc.SetStatus(core.TaskInputRequired); err != nil {
		return nil, fmt.Errorf("task %s: set input_required: %w", tc.taskID, err)
	}

	// Wait for either tasks/update delivery or context cancellation.
	var payload json.RawMessage
	select {
	case payload = <-waiter:
		if payload == nil {
			return nil, fmt.Errorf("task %s: input wait cancelled", tc.taskID)
		}
	case <-tc.Context.Done():
		return nil, tc.Context.Err()
	}

	// Transition back to working before returning — but only if no other
	// inputs are still pending. A fan-out tool that has multiple TaskElicit /
	// TaskSample calls in flight must stay in input_required until every
	// one has been answered. If the task went terminal meanwhile (cancelled
	// while the answer was in flight), SetStatus leaves it terminal.
	if !tc.inputState.HasPending() {
		tc.SetStatus(core.TaskWorking)
	}

	return payload, nil
}

// notifyTransitionStatus emits a notifications/tasks event for a status
// transition (working ↔ input_required). For terminal statuses the goroutine
// uses notifyV2TaskStatus (which inlines result/error via the runtime); this
// helper is the rt-free path used during in-progress transitions.
func notifyTransitionStatus(ctx context.Context, store server.TaskStore, taskID, sessionID string) {
	info, ok := store.Get(taskID, sessionID)
	if !ok {
		return
	}
	payload := core.DetailedTask{
		TaskInfoV2: toTaskInfoV2(info),
	}
	core.Notify(ctx, "notifications/tasks", payload)
}
