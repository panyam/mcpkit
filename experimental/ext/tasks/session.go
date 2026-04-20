package tasks

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
	taskID   string
	store    TaskStore
	requests chan sideChannelRequest // read by tasks/result handler
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

// TaskElicit sends an elicitation request to the client via the tasks/result
// side-channel. The request is proxied by the tasks/result long-poll handler
// through its live connection.
//
// Status transitions: working → input_required → (wait for client) → working
func (tc *TaskContext) TaskElicit(req core.ElicitationRequest) (core.ElicitationResult, error) {
	// Transition to input_required.
	if err := tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskInputRequired
	}); err != nil {
		return core.ElicitationResult{}, fmt.Errorf("task %s: failed to set input_required: %w", tc.taskID, err)
	}

	// Inject related-task metadata.
	if req.Meta == nil {
		req.Meta = &core.ElicitationMeta{}
	}
	req.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: tc.taskID}

	// Serialize params.
	params, err := core.MarshalJSON(req)
	if err != nil {
		tc.store.Update(tc.taskID, func(t *core.TaskInfo) { t.Status = core.TaskWorking })
		return core.ElicitationResult{}, fmt.Errorf("marshal elicitation request: %w", err)
	}

	// Send to the tasks/result handler and wait for response.
	resp, err := tc.sendSideChannel("elicitation/create", params)

	// Transition back to working.
	tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskWorking
	})

	if err != nil {
		return core.ElicitationResult{}, err
	}

	var result core.ElicitationResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return core.ElicitationResult{}, fmt.Errorf("unmarshal elicitation result: %w", err)
	}
	return result, nil
}

// TaskSample sends a sampling request to the client via the tasks/result
// side-channel.
//
// Status transitions: working → input_required → (wait for client) → working
func (tc *TaskContext) TaskSample(req core.CreateMessageRequest) (core.CreateMessageResult, error) {
	// Transition to input_required.
	if err := tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskInputRequired
	}); err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("task %s: failed to set input_required: %w", tc.taskID, err)
	}

	// Inject related-task metadata.
	if req.Meta == nil {
		req.Meta = &core.SamplingMeta{}
	}
	req.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: tc.taskID}

	// Serialize params.
	params, err := core.MarshalJSON(req)
	if err != nil {
		tc.store.Update(tc.taskID, func(t *core.TaskInfo) { t.Status = core.TaskWorking })
		return core.CreateMessageResult{}, fmt.Errorf("marshal sampling request: %w", err)
	}

	// Send to the tasks/result handler and wait for response.
	resp, err := tc.sendSideChannel("sampling/createMessage", params)

	// Transition back to working.
	tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskWorking
	})

	if err != nil {
		return core.CreateMessageResult{}, err
	}

	var result core.CreateMessageResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("unmarshal sampling result: %w", err)
	}
	return result, nil
}

// sendSideChannel enqueues a request for the tasks/result handler to proxy,
// then blocks until the response arrives.
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
