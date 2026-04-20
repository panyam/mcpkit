package tasks

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/core"
)

// TaskContext is a typed context for tool handlers running as background tasks.
// It embeds core.ToolContext and adds task-specific methods:
//   - TaskID() — the task's unique identifier
//   - TaskElicit() — elicitation with automatic input_required status transitions
//   - TaskSample() — sampling with automatic input_required status transitions
//
// Tool handlers retrieve this via GetTaskContext(ctx). It is nil for
// synchronous (non-task) tool invocations.
//
// Usage:
//
//	func myToolHandler(ctx core.ToolContext, input MyInput) (string, error) {
//	    tc := tasks.GetTaskContext(ctx)
//	    if tc != nil {
//	        // Running as a task — can use async elicitation/sampling.
//	        result, err := tc.TaskElicit(core.ElicitationRequest{...})
//	    }
//	    return "done", nil
//	}
type TaskContext struct {
	core.ToolContext
	taskID string
	store  TaskStore
}

type taskContextKey struct{}

// WithTaskContext returns a context carrying the TaskContext.
// Used by the tasks middleware to inject into the tool handler's context.
func WithTaskContext(ctx context.Context, tc *TaskContext) context.Context {
	return context.WithValue(ctx, taskContextKey{}, tc)
}

// GetTaskContext retrieves the TaskContext from a tool handler's context,
// binding it to the provided ToolContext for elicitation/sampling.
// Returns nil if the tool was not invoked as a task.
//
// Usage in a tool handler:
//
//	func handler(ctx core.ToolContext, input MyInput) (string, error) {
//	    tc := tasks.GetTaskContext(ctx)
//	    if tc != nil {
//	        result, _ := tc.TaskElicit(core.ElicitationRequest{...})
//	    }
//	    return "done", nil
//	}
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

// TaskElicit sends an elicitation request to the client, transitioning the
// task to input_required while waiting for the response. Returns the
// elicitation result or an error.
//
// This wraps ctx.Elicit() with automatic status transitions:
//
//	working → input_required → (wait for client) → working
func (tc *TaskContext) TaskElicit(req core.ElicitationRequest) (core.ElicitationResult, error) {
	if err := tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskInputRequired
	}); err != nil {
		return core.ElicitationResult{}, fmt.Errorf("task %s: failed to set input_required: %w", tc.taskID, err)
	}

	// Inject related-task metadata so the client can correlate this
	// elicitation request with the originating task.
	if req.Meta == nil {
		req.Meta = &core.ElicitationMeta{}
	}
	req.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: tc.taskID}

	result, err := tc.Elicit(req)

	// Transition back to working regardless of success/failure.
	tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskWorking
	})

	return result, err
}

// TaskSample sends a sampling request to the client, transitioning the task
// to input_required while waiting for the response. Returns the sampling
// result or an error.
//
// This wraps ctx.Sample() with automatic status transitions:
//
//	working → input_required → (wait for client) → working
func (tc *TaskContext) TaskSample(req core.CreateMessageRequest) (core.CreateMessageResult, error) {
	if err := tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskInputRequired
	}); err != nil {
		return core.CreateMessageResult{}, fmt.Errorf("task %s: failed to set input_required: %w", tc.taskID, err)
	}

	// Inject related-task metadata so the client can correlate this
	// sampling request with the originating task.
	if req.Meta == nil {
		req.Meta = &core.SamplingMeta{}
	}
	req.Meta.RelatedTask = &core.RelatedTaskMeta{TaskID: tc.taskID}

	result, err := tc.Sample(req)

	// Transition back to working regardless of success/failure.
	tc.store.Update(tc.taskID, func(t *core.TaskInfo) {
		t.Status = core.TaskWorking
	})

	return result, err
}
