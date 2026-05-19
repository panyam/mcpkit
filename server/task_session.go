package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// V1-RETIREMENT: this entire file becomes pure deletion fodder when v1
// retires. ext/tasks defines its own v2-shaped TaskContext + WithTaskContext +
// GetTaskContext; there is no v1/v2 sharing here. The whole side-channel
// machinery (sideChannelRequest / sideChannelResponse / requestInputV1 /
// sendSideChannel) is v1-only by construction.

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
	requests      chan sideChannelRequest
	progressToken any // original _meta.progressToken from client
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

// TaskElicit asks the client for elicitation input from inside a running v1
// task. The request is enqueued on a side-channel proxied by the
// tasks/result long-poll handler.
//
// Status transitions: working → input_required → working.
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

	resp, err := tc.requestInputV1("elicitation/create", params)
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
// inside a running v1 task. See TaskElicit for the side-channel flow.
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

	resp, err := tc.requestInputV1("sampling/createMessage", params)
	if err != nil {
		return core.CreateMessageResult{}, err
	}
	var result core.CreateMessageResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("unmarshal sampling result: %w", err)
	}
	return result, nil
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
