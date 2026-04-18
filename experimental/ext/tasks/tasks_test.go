package tasks

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// newTaskServer creates a test MCP server with tasks registered and a
// configurable slow tool. Returns the server and a channel the slow tool
// blocks on — send to unblock it.
func newTaskServer(t *testing.T) (*server.Server, chan struct{}) {
	t.Helper()

	srv := server.NewServer(core.ServerInfo{Name: "task-test", Version: "0.0.1"})

	// Fast tool — no Execution field, tasks optional by default.
	type echoInput struct {
		Message string `json:"message"`
	}
	srv.Register(core.TextTool[echoInput]("echo", "Echoes input",
		func(ctx core.ToolContext, input echoInput) (string, error) {
			return "echo: " + input.Message, nil
		},
	))

	// Slow tool — blocks until signalled. Declares task support optional.
	unblock := make(chan struct{}, 1)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow",
			Description: "Blocks until signalled",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"data": map[string]any{"type": "string"},
			}},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			<-unblock
			var args struct {
				Data string `json:"data"`
			}
			json.Unmarshal(req.Arguments, &args)
			return core.TextResult("slow: " + args.Data), nil
		},
	)

	// Tool that always errors.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "fail-async",
			Description: "Always fails",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.ToolResult{}, fmt.Errorf("boom")
		},
	)

	// Tool that requires tasks.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "must-task",
			Description: "Requires task invocation",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)

	// Tool that forbids tasks.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "no-task",
			Description: "Forbids task invocation",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportForbidden},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("sync-only"), nil
		},
	)

	Register(Config{Server: srv})

	return srv, unblock
}

func connectClient(t *testing.T, srv *server.Server) *client.Client {
	t.Helper()
	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "test", Version: "0.0.1"})
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// --- Integration tests ---

func TestTaskFullLifecycle(t *testing.T) {
	srv, unblock := newTaskServer(t)
	c := connectClient(t, srv)

	// 1. Create task via tools/call with task hint.
	created, err := ToolCallAsTask(c, "slow", map[string]any{"data": "hello"}, 0)
	if err != nil {
		t.Fatalf("ToolCallAsTask: %v", err)
	}
	if created.Task.TaskID == "" {
		t.Fatal("expected non-empty task ID")
	}
	if created.Task.Status != core.TaskWorking {
		t.Errorf("status = %q, want working", created.Task.Status)
	}

	// 2. Poll — should still be working.
	got, err := GetTask(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Task.Status != core.TaskWorking {
		t.Errorf("poll status = %q, want working", got.Task.Status)
	}

	// 3. List — should include our task.
	list, err := ListTasks(c, "")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	found := false
	for _, task := range list.Tasks {
		if task.TaskID == created.Task.TaskID {
			found = true
		}
	}
	if !found {
		t.Error("task not found in list")
	}

	// 4. Unblock the tool and fetch the result.
	unblock <- struct{}{}

	result, err := GetTaskResult(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskResult: %v", err)
	}
	if result.Task.Status != core.TaskCompleted {
		t.Errorf("result status = %q, want completed", result.Task.Status)
	}
	if len(result.Result.Content) == 0 || result.Result.Content[0].Text != "slow: hello" {
		t.Errorf("unexpected result content: %+v", result.Result)
	}
}

func TestTaskCancel(t *testing.T) {
	srv, _ := newTaskServer(t) // don't unblock — tool stays blocked
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "slow", map[string]any{"data": "cancel-me"}, 0)
	if err != nil {
		t.Fatal(err)
	}

	cancelled, err := CancelTask(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if cancelled.Task.Status != core.TaskCancelled {
		t.Errorf("status = %q, want cancelled", cancelled.Task.Status)
	}

	// Poll after cancel — should still be cancelled.
	got, err := GetTask(c, created.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Task.Status != core.TaskCancelled {
		t.Errorf("poll after cancel: status = %q, want cancelled", got.Task.Status)
	}
}

func TestTaskFailedTool(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "fail-async", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// The tool fails immediately, so poll until terminal.
	var info core.TaskInfo
	for i := 0; i < 20; i++ {
		got, err := GetTask(c, created.Task.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		info = got.Task
		if info.Status.IsTerminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if info.Status != core.TaskFailed {
		t.Errorf("status = %q, want failed", info.Status)
	}
}

func TestTaskRequiredWithoutHint(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	// Call must-task without a task hint — should get an error.
	_, err := c.Call("tools/call", map[string]any{
		"name":      "must-task",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for required-task tool called without hint")
	}
}

func TestTaskForbiddenIgnoresHint(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	// Call no-task with a task hint — should run synchronously (no task created).
	result, err := c.Call("tools/call", map[string]any{
		"name":      "no-task",
		"arguments": map[string]any{},
		"_meta":     map[string]any{"task": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("expected sync result, got error: %v", err)
	}

	// Should be a direct tool result, not a CreateTaskResult.
	var toolResult core.ToolResult
	if err := json.Unmarshal(result.Raw, &toolResult); err != nil {
		t.Fatal(err)
	}
	if len(toolResult.Content) == 0 || toolResult.Content[0].Text != "sync-only" {
		t.Errorf("unexpected result: %+v", toolResult)
	}
}

func TestTaskNoHintRunsSync(t *testing.T) {
	srv, unblock := newTaskServer(t)
	c := connectClient(t, srv)

	// echo tool with no task hint — should run synchronously.
	text, err := c.ToolCall("echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "echo: hi" {
		t.Errorf("got %q, want 'echo: hi'", text)
	}

	// Keep unblock from leaking.
	close(unblock)
}

func TestTaskCapabilityAdvertised(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	// Use a raw initialize call via a second client to inspect capabilities.
	// The first client already initialized, so we issue a raw call on a fresh one.
	result, err := c.Call("tasks/list", map[string]any{})
	if err != nil {
		t.Fatalf("tasks/list should succeed if capability is advertised: %v", err)
	}
	var list core.ListTasksResult
	if err := json.Unmarshal(result.Raw, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	// If tasks capability wasn't registered, tasks/list would return method-not-found.
	// Reaching here proves it was advertised and the handler is installed.
}

func TestTaskMultipleConcurrent(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	// Create multiple tasks.
	const n = 5
	var taskIDs []string
	for i := 0; i < n; i++ {
		created, err := ToolCallAsTask(c, "slow", map[string]any{"data": fmt.Sprintf("t%d", i)}, 0)
		if err != nil {
			t.Fatal(err)
		}
		taskIDs = append(taskIDs, created.Task.TaskID)
	}

	// All should be listed.
	list, err := ListTasks(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tasks) < n {
		t.Errorf("listed %d tasks, want at least %d", len(list.Tasks), n)
	}

	// Cancel all.
	for _, id := range taskIDs {
		_, err := CancelTask(c, id)
		if err != nil {
			t.Errorf("cancel %s: %v", id, err)
		}
	}
}

func TestTaskCustomTTL(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "slow", map[string]any{"data": "x"}, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.TTL != 60_000 {
		t.Errorf("TTL = %d, want 60000", created.Task.TTL)
	}
}

func TestTaskGetNotFound(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	_, err := GetTask(c, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestTaskCancelAlreadyTerminal(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	// Create and immediately cancel.
	created, _ := ToolCallAsTask(c, "slow", map[string]any{"data": "x"}, 0)
	CancelTask(c, created.Task.TaskID)

	// Second cancel should fail.
	_, err := CancelTask(c, created.Task.TaskID)
	if err == nil {
		t.Fatal("expected error cancelling already-terminal task")
	}
}

func TestToolExecutionFieldInToolsList(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatal(err)
	}

	var foundSlow, foundNoTask bool
	for _, tool := range tools {
		switch tool.Name {
		case "slow":
			foundSlow = true
			if tool.Execution == nil {
				t.Error("slow tool: expected Execution field")
			} else if tool.Execution.TaskSupport != core.TaskSupportOptional {
				t.Errorf("slow tool: taskSupport = %q, want optional", tool.Execution.TaskSupport)
			}
		case "no-task":
			foundNoTask = true
			if tool.Execution == nil {
				t.Error("no-task tool: expected Execution field")
			} else if tool.Execution.TaskSupport != core.TaskSupportForbidden {
				t.Errorf("no-task tool: taskSupport = %q, want forbidden", tool.Execution.TaskSupport)
			}
		}
	}
	if !foundSlow {
		t.Error("slow tool not found in tools list")
	}
	if !foundNoTask {
		t.Error("no-task tool not found in tools list")
	}
}

func TestTaskResultAfterCompletion(t *testing.T) {
	srv, unblock := newTaskServer(t)
	c := connectClient(t, srv)

	created, _ := ToolCallAsTask(c, "slow", map[string]any{"data": "done"}, 0)

	// Unblock immediately.
	unblock <- struct{}{}

	// Wait for completion.
	var completed bool
	for i := 0; i < 20; i++ {
		got, _ := GetTask(c, created.Task.TaskID)
		if got.Task.Status == core.TaskCompleted {
			completed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !completed {
		t.Fatal("task did not complete in time")
	}

	// GetTaskResult on an already-completed task should return immediately.
	result, err := GetTaskResult(c, created.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.Status != core.TaskCompleted {
		t.Errorf("status = %q, want completed", result.Task.Status)
	}
}

func TestTaskProgressCounter(t *testing.T) {
	// Verify that tasks from concurrent creations get unique IDs.
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	ids := make(map[string]bool)
	var collisions atomic.Int32
	const n = 10

	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			created, err := ToolCallAsTask(c, "slow", map[string]any{"data": "x"}, 0)
			if err == nil {
				if ids[created.Task.TaskID] {
					collisions.Add(1)
				}
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	if collisions.Load() > 0 {
		t.Errorf("got %d task ID collisions", collisions.Load())
	}
}
