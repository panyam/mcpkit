package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// sideChannelRequest is sent by TaskElicit/TaskSample to the tasks/result
// handler via the TaskContext's request channel. The handler proxies the
// request through its live connection and sends the response back.
type sideChannelRequest struct {
	Method string          // "elicitation/create" or "sampling/createMessage"
	Params json.RawMessage // serialized request params
	Result chan sideChannelResponse
}

type sideChannelResponse struct {
	Raw json.RawMessage
	Err error
}

// TaskContext is a typed context for tool handlers running as background tasks.
// It embeds core.ToolContext and adds task-specific methods:
//   - TaskID() — the task's unique identifier
//   - TaskElicit() — elicitation via the tasks/result side-channel
//   - TaskSample() — sampling via the tasks/result side-channel
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
type TaskContext struct {
	core.ToolContext
	taskID        string
	sessionID     string
	store         TaskStore
	requests      chan sideChannelRequest // v1 only: read by tasks/result handler
	inputState    *v2InputState           // v2 only: SEP-2663 input request flow
	progressToken any                     // original _meta.progressToken from client
}

type taskContextKey struct{}

// WithTaskContext returns a context carrying the TaskContext.
func WithTaskContext(ctx context.Context, tc *TaskContext) context.Context {
	return context.WithValue(ctx, taskContextKey{}, tc)
}

// GetTaskContext retrieves the TaskContext from a tool handler's context,
// binding it to the provided ToolContext for elicitation/sampling.
// Returns nil if the tool was not invoked as a task.
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

// ProgressToken returns the client's original _meta.progressToken from
// the tools/call request. Use this instead of TaskID() for EmitProgress
// so progress notifications correlate with what the client expects.
// Returns nil if the client didn't send a progressToken.
func (tc *TaskContext) ProgressToken() any {
	return tc.progressToken
}

// SetStatus transitions the task to a new status and sends a
// notifications/tasks/status notification (Phase 6).
func (tc *TaskContext) SetStatus(status core.TaskStatus) error {
	err := tc.store.Update(tc.taskID, tc.sessionID, func(t *core.TaskInfo) {
		t.Status = status
	})
	if err == nil {
		notifyTaskStatus(tc.Context, tc.store, tc.taskID, tc.sessionID)
	}
	return err
}

// TaskElicit asks the client for elicitation input from inside a running task.
//
// Behavior depends on which task runtime owns this TaskContext:
//
//   - v2 (SEP-2663): the request is stashed on the task's inputState under a
//     monotonic key, the task transitions to input_required, and the goroutine
//     blocks on a per-key waiter channel. The client observes the pending
//     request via tasks/get's DetailedTask.InputRequests and resumes the task
//     by sending the matching response via tasks/update. ctx cancellation
//     unblocks the wait with the corresponding context error.
//   - v1 (legacy): the request is enqueued on a side-channel proxied by the
//     tasks/result long-poll handler.
//
// Status transitions for both paths: working → input_required → working.
func (tc *TaskContext) TaskElicit(req core.ElicitationRequest) (core.ElicitationResult, error) {
	// Inject related-task metadata so out-of-band consumers can correlate.
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
// inside a running task. See TaskElicit for the v1 vs v2 routing.
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

// requestInput is the routing layer behind TaskElicit / TaskSample. It picks
// the v2 SEP-2663 path when an inputState is attached and falls back to the
// v1 side-channel otherwise. methodPrefix is a short readable tag used to
// mint stable keys ("elicit-1", "sample-2") on the v2 path; jsonRpcMethod
// is the actual JSON-RPC method name routed to the client.
func (tc *TaskContext) requestInput(methodPrefix, jsonRpcMethod string, params json.RawMessage) (json.RawMessage, error) {
	if tc.inputState != nil {
		return tc.requestInputV2(methodPrefix, jsonRpcMethod, params)
	}
	return tc.requestInputV1(jsonRpcMethod, params)
}

// requestInputV2 implements the SEP-2663 input-request flow.
func (tc *TaskContext) requestInputV2(methodPrefix, jsonRpcMethod string, params json.RawMessage) (json.RawMessage, error) {
	_, waiter := tc.inputState.enqueue(methodPrefix, core.InputRequest{
		Method: jsonRpcMethod,
		Params: params,
	})

	// Transition to input_required and notify status so polling clients
	// pick up the new pending request on the next tasks/get.
	if err := tc.store.Update(tc.taskID, tc.sessionID, func(t *core.TaskInfo) {
		t.Status = core.TaskInputRequired
	}); err != nil {
		return nil, fmt.Errorf("task %s: set input_required: %w", tc.taskID, err)
	}
	notifyTaskStatus(tc.Context, tc.store, tc.taskID, tc.sessionID)

	// Wait for either tasks/update delivery or context cancellation.
	var payload json.RawMessage
	select {
	case payload = <-waiter:
		// Closed-channel receive yields nil payload + ok=false; treat as cancel.
		if payload == nil {
			return nil, fmt.Errorf("task %s: input wait cancelled", tc.taskID)
		}
	case <-tc.Context.Done():
		return nil, tc.Context.Err()
	}

	// Transition back to working before returning — but only if no other
	// inputs are still pending. A fan-out tool that has multiple TaskElicit /
	// TaskSample calls in flight must stay in input_required until every
	// one has been answered, otherwise tasks/get would briefly report
	// "working" while some inputs are still un-fulfilled (and a polling
	// client could miss the partial-fulfillment window). Best-effort: if
	// the task has already gone terminal, the store's terminal guard
	// rejects this.
	if !tc.inputState.hasPending() {
		tc.store.Update(tc.taskID, tc.sessionID, func(t *core.TaskInfo) {
			t.Status = core.TaskWorking
		})
		notifyTaskStatus(tc.Context, tc.store, tc.taskID, tc.sessionID)
	}

	return payload, nil
}

// requestInputV1 implements the legacy v1 side-channel flow (tasks/result
// long-poll proxies the request through its live connection).
func (tc *TaskContext) requestInputV1(jsonRpcMethod string, params json.RawMessage) (json.RawMessage, error) {
	if err := tc.store.Update(tc.taskID, tc.sessionID, func(t *core.TaskInfo) {
		t.Status = core.TaskInputRequired
	}); err != nil {
		return nil, fmt.Errorf("task %s: set input_required: %w", tc.taskID, err)
	}

	resp, err := tc.sendSideChannel(jsonRpcMethod, params)

	tc.store.Update(tc.taskID, tc.sessionID, func(t *core.TaskInfo) {
		t.Status = core.TaskWorking
	})
	return resp, err
}

// sendSideChannel enqueues a request for the v1 tasks/result handler to
// proxy, then blocks until the response arrives.
func (tc *TaskContext) sendSideChannel(method string, params json.RawMessage) (json.RawMessage, error) {
	respCh := make(chan sideChannelResponse, 1)
	tc.requests <- sideChannelRequest{
		Method: method,
		Params: params,
		Result: respCh,
	}

	resp := <-respCh
	return resp.Raw, resp.Err
}
