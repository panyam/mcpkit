package server_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	. "github.com/panyam/mcpkit/server"
)

// TestTaskCallbacksGetTask verifies that a tool with a custom GetTask
// callback is consulted by the tasks/get handler before the TaskStore.
func TestTaskCallbacksGetTask(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "cb-test", Version: "0.0.1"})

	// Tool with custom GetTask callback that augments the status message.
	var getTaskCalled atomic.Int32
	unblock := make(chan struct{}, 1)

	srv.Register(Tool{
		ToolDef: core.ToolDef{
			Name:        "proxy-job",
			Description: "Simulates an external job with custom getTask",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			select {
			case <-unblock:
			case <-ctx.Done():
			}
			return core.TextResult("done"), nil
		},
		TaskCallbacks: &TaskCallbacks{
			GetTask: func(ctx core.MethodContext, taskID string) (core.GetTaskResultV1, bool) {
				getTaskCalled.Add(1)
				return core.GetTaskResultV1{
					TaskInfo: core.TaskInfo{
						TaskID:        taskID,
						Status:        core.TaskWorking,
						StatusMessage: "custom-override",
					},
				}, true
			},
		},
	})

	RegisterTasksV1(TasksConfigV1{Server: srv})

	c := connectClient(t, srv)

	// Create a task.
	created, err := client.ToolCallAsTaskV1(c, "proxy-job", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCallAsTask: %v", err)
	}

	// Poll via tasks/get — should hit the custom callback.
	got, err := client.GetTaskV1(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	if getTaskCalled.Load() == 0 {
		t.Error("GetTask callback was not invoked")
	}
	if got.StatusMessage != "custom-override" {
		t.Errorf("StatusMessage = %q, want %q", got.StatusMessage, "custom-override")
	}

	// Unblock so the goroutine can finish cleanly.
	close(unblock)
}

// TestTaskCallbacksGetResult verifies that a tool with a custom GetResult
// callback is consulted by the tasks/result handler before the TaskStore.
func TestTaskCallbacksGetResult(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "cb-test", Version: "0.0.1"})

	var getResultCalled atomic.Int32

	srv.Register(Tool{
		ToolDef: core.ToolDef{
			Name:        "proxy-result",
			Description: "Simulates an external job with custom getResult",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("store-result"), nil
		},
		TaskCallbacks: &TaskCallbacks{
			GetResult: func(ctx core.MethodContext, taskID string) (core.ToolResult, bool) {
				getResultCalled.Add(1)
				return core.ToolResult{
					Content: []core.Content{{Type: "text", Text: "custom-result"}},
				}, true
			},
		},
	})

	RegisterTasksV1(TasksConfigV1{Server: srv})

	c := connectClient(t, srv)

	// Create task — tool returns immediately, task completes fast.
	created, err := client.ToolCallAsTaskV1(c, "proxy-result", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCallAsTask: %v", err)
	}

	// Wait for terminal state via tasks/get polling.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.WaitForTaskV1(ctx, c, created.Task.TaskID, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForTask: %v", err)
	}

	// Now call tasks/result — should hit the custom GetResult callback.
	result, _, err := client.GetTaskPayloadV1(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskPayload: %v", err)
	}

	if getResultCalled.Load() == 0 {
		t.Error("GetResult callback was not invoked")
	}

	// The custom callback returns "custom-result", not "store-result".
	if len(result.Content) == 0 || result.Content[0].Text != "custom-result" {
		var got string
		if len(result.Content) > 0 {
			got = result.Content[0].Text
		}
		t.Errorf("result text = %q, want %q", got, "custom-result")
	}
}

// TestTaskCallbacksFallthrough verifies that when a callback returns false,
// the handler falls through to the TaskStore.
func TestTaskCallbacksFallthrough(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "cb-test", Version: "0.0.1"})

	var getTaskCalled atomic.Int32

	srv.Register(Tool{
		ToolDef: core.ToolDef{
			Name:        "fallthrough-job",
			Description: "Callback returns false to fall through",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		Handler: func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("real-result"), nil
		},
		TaskCallbacks: &TaskCallbacks{
			GetTask: func(ctx core.MethodContext, taskID string) (core.GetTaskResultV1, bool) {
				getTaskCalled.Add(1)
				return core.GetTaskResultV1{}, false // fall through to store
			},
		},
	})

	RegisterTasksV1(TasksConfigV1{Server: srv})

	c := connectClient(t, srv)

	created, err := client.ToolCallAsTaskV1(c, "fallthrough-job", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCallAsTask: %v", err)
	}

	// Wait for completion so the store has the task.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client.WaitForTaskV1(ctx, c, created.Task.TaskID, 50*time.Millisecond)

	// Poll via tasks/get — callback returns false, so store handles it.
	got, err := client.GetTaskV1(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}

	if getTaskCalled.Load() == 0 {
		t.Error("GetTask callback should still be called (before fallthrough)")
	}
	// Store has the real status — completed (since tool returned successfully).
	if got.Status != core.TaskCompleted {
		t.Errorf("status = %q, want %q", got.Status, core.TaskCompleted)
	}
}

// TestTaskCallbacksNoCallbacks verifies that tools without TaskCallbacks
// work exactly as before (pure TaskStore path).
func TestTaskCallbacksNoCallbacks(t *testing.T) {
	srv, unblock := newTaskServer(t) // uses default tools, no callbacks
	c := connectClient(t, srv)

	created, err := client.ToolCallAsTaskV1(c, "slow", map[string]any{"data": "x"})
	if err != nil {
		t.Fatalf("ToolCallAsTask: %v", err)
	}

	// Should still work via TaskStore.
	got, err := client.GetTaskV1(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != core.TaskWorking {
		t.Errorf("status = %q, want working", got.Status)
	}

	close(unblock)
}
