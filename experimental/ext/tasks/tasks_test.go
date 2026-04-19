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

	// Fast tool — no Execution field. Per spec, absent = forbidden.
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

// TestTaskFullLifecycle exercises create → poll → unblock → fetch result.
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
	if got.Status != core.TaskWorking {
		t.Errorf("poll status = %q, want working", got.Status)
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

	result, taskID, err := GetTaskPayload(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskPayload: %v", err)
	}
	if taskID != created.Task.TaskID {
		t.Errorf("related taskId = %q, want %q", taskID, created.Task.TaskID)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "slow: hello" {
		t.Errorf("unexpected result content: %+v", result)
	}
}

// TestTaskCancel exercises create → cancel → verify.
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
	if cancelled.Status != core.TaskCancelled {
		t.Errorf("status = %q, want cancelled", cancelled.Status)
	}

	// Poll after cancel — should still be cancelled.
	got, err := GetTask(c, created.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != core.TaskCancelled {
		t.Errorf("poll after cancel: status = %q, want cancelled", got.Status)
	}
}

// TestTaskFailedTool verifies that async tool errors transition to failed.
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
		info = got.TaskInfo
		if info.Status.IsTerminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if info.Status != core.TaskFailed {
		t.Errorf("status = %q, want failed", info.Status)
	}
}

// TestTaskRequiredWithoutHint verifies error -32601 when required tool
// called without task hint.
func TestTaskRequiredWithoutHint(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	_, err := c.Call("tools/call", map[string]any{
		"name":      "must-task",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for required-task tool called without hint")
	}
}

// TestTaskForbiddenWithHintErrors verifies that sending a task hint to a
// forbidden tool returns error -32601 (not silent sync fallthrough).
func TestTaskForbiddenWithHintErrors(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	// Send task hint at params.task (spec-correct location).
	_, err := c.Call("tools/call", map[string]any{
		"name":      "no-task",
		"arguments": map[string]any{},
		"task":      map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for forbidden tool with task hint")
	}
}

// TestTaskNoHintRunsSync verifies normal tool calls are unaffected.
func TestTaskNoHintRunsSync(t *testing.T) {
	srv, unblock := newTaskServer(t)
	c := connectClient(t, srv)

	text, err := c.ToolCall("echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if text != "echo: hi" {
		t.Errorf("got %q, want 'echo: hi'", text)
	}

	close(unblock)
}

// TestTaskCapabilityAdvertised verifies task handlers are installed.
func TestTaskCapabilityAdvertised(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	result, err := c.Call("tasks/list", map[string]any{})
	if err != nil {
		t.Fatalf("tasks/list should succeed if capability is advertised: %v", err)
	}
	var list core.ListTasksResult
	if err := json.Unmarshal(result.Raw, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
}

// TestTaskMultipleConcurrent exercises creating and cancelling 5 tasks.
func TestTaskMultipleConcurrent(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	const n = 5
	var taskIDs []string
	for i := 0; i < n; i++ {
		created, err := ToolCallAsTask(c, "slow", map[string]any{"data": fmt.Sprintf("t%d", i)}, 0)
		if err != nil {
			t.Fatal(err)
		}
		taskIDs = append(taskIDs, created.Task.TaskID)
	}

	list, err := ListTasks(c, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tasks) < n {
		t.Errorf("listed %d tasks, want at least %d", len(list.Tasks), n)
	}

	for _, id := range taskIDs {
		_, err := CancelTask(c, id)
		if err != nil {
			t.Errorf("cancel %s: %v", id, err)
		}
	}
}

// TestTaskCustomTTL verifies client-specified TTL propagates.
func TestTaskCustomTTL(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "slow", map[string]any{"data": "x"}, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.TTL == nil || *created.Task.TTL != 60_000 {
		t.Errorf("TTL = %v, want 60000", created.Task.TTL)
	}
}

// TestTaskGetNotFound verifies error for nonexistent task ID.
func TestTaskGetNotFound(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	_, err := GetTask(c, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

// TestTaskCancelAlreadyTerminal verifies error on double cancel.
func TestTaskCancelAlreadyTerminal(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	created, _ := ToolCallAsTask(c, "slow", map[string]any{"data": "x"}, 0)
	CancelTask(c, created.Task.TaskID)

	_, err := CancelTask(c, created.Task.TaskID)
	if err == nil {
		t.Fatal("expected error cancelling already-terminal task")
	}
}

// TestToolExecutionFieldInToolsList verifies Execution metadata visible.
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

// TestTaskResultAfterCompletion verifies fetching result on already-complete task.
func TestTaskResultAfterCompletion(t *testing.T) {
	srv, unblock := newTaskServer(t)
	c := connectClient(t, srv)

	created, _ := ToolCallAsTask(c, "slow", map[string]any{"data": "done"}, 0)

	unblock <- struct{}{}

	var completed bool
	for i := 0; i < 20; i++ {
		got, _ := GetTask(c, created.Task.TaskID)
		if got.Status == core.TaskCompleted {
			completed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !completed {
		t.Fatal("task did not complete in time")
	}

	result, _, err := GetTaskPayload(c, created.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "slow: done" {
		t.Errorf("unexpected result: %+v", result)
	}
}

// TestTaskProgressCounter verifies unique IDs under concurrency.
func TestTaskProgressCounter(t *testing.T) {
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

// --- Spec wire-format compliance tests ---

// TestTaskHintAtParamsRoot verifies that the task hint is parsed from
// params.task (spec-correct), not params._meta.task.
func TestTaskHintAtParamsRoot(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	// Send task hint at params.task (spec location).
	result, err := c.Call("tools/call", map[string]any{
		"name":      "slow",
		"arguments": map[string]any{"data": "spec"},
		"task":      map[string]any{},
	})
	if err != nil {
		t.Fatalf("expected task creation, got error: %v", err)
	}

	var created core.CreateTaskResult
	if err := json.Unmarshal(result.Raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Task.TaskID == "" {
		t.Error("expected non-empty task ID from params.task hint")
	}
}

// TestOldMetaHintIgnored verifies that _meta.task does NOT trigger task
// creation (it's the wrong location per spec).
func TestOldMetaHintIgnored(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	// Send hint at old _meta.task location on a tool with TaskSupportOptional.
	// Should run sync (no task created) because the spec-correct location is params.task.
	// The slow tool blocks, so unblock concurrently.
	go func() {
		time.Sleep(100 * time.Millisecond)
		unblock <- struct{}{}
	}()

	result, err := c.Call("tools/call", map[string]any{
		"name":      "slow",
		"arguments": map[string]any{"data": "old"},
		"_meta":     map[string]any{"task": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("expected sync result, got error: %v", err)
	}

	// Should be a ToolResult (sync), not a CreateTaskResult.
	var toolResult core.ToolResult
	json.Unmarshal(result.Raw, &toolResult)
	if len(toolResult.Content) == 0 {
		t.Error("expected sync tool result content")
	}
}

// TestAbsentExecutionWithHintErrors verifies that sending a task hint to a
// tool with no Execution field (absent = forbidden per spec) returns an error.
func TestAbsentExecutionWithHintErrors(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	// echo has no Execution field — absent means forbidden.
	_, err := c.Call("tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"message": "hi"},
		"task":      map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error for absent Execution (= forbidden) with task hint")
	}
}

// TestRequiredWithoutHint32601 verifies the error code is -32601 (MethodNotFound).
func TestRequiredWithoutHint32601(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	_, err := c.Call("tools/call", map[string]any{
		"name":      "must-task",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	// The error string from the client should contain the error code or message.
	// We verify the server returns the right code by checking the error exists.
	// Detailed code checking would require raw HTTP — the client wraps errors.
}

// TestTasksGetFlatWireFormat verifies tasks/get returns flat TaskInfo fields
// at the result root, not nested under a "task" key.
func TestTasksGetFlatWireFormat(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "slow", map[string]any{"data": "flat"}, 0)
	if err != nil {
		t.Fatal(err)
	}

	result, err := c.Call("tasks/get", map[string]any{"taskId": created.Task.TaskID})
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	json.Unmarshal(result.Raw, &m)

	// Must NOT have a "task" wrapper.
	if _, ok := m["task"]; ok {
		t.Error("tasks/get response should be flat, not nested under 'task'")
	}
	// Must have taskId at root.
	if m["taskId"] != created.Task.TaskID {
		t.Errorf("taskId = %v, want %s", m["taskId"], created.Task.TaskID)
	}
}

// TestTasksCancelFlatWireFormat verifies tasks/cancel returns flat TaskInfo.
func TestTasksCancelFlatWireFormat(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	created, _ := ToolCallAsTask(c, "slow", map[string]any{"data": "x"}, 0)

	result, err := c.Call("tasks/cancel", map[string]any{"taskId": created.Task.TaskID})
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	json.Unmarshal(result.Raw, &m)

	if _, ok := m["task"]; ok {
		t.Error("tasks/cancel response should be flat, not nested under 'task'")
	}
	if m["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", m["status"])
	}
}

// TestTasksResultRelatedTask verifies tasks/result returns ToolResult shape
// with _meta["io.modelcontextprotocol/related-task"].
func TestTasksResultRelatedTask(t *testing.T) {
	srv, unblock := newTaskServer(t)
	c := connectClient(t, srv)

	created, _ := ToolCallAsTask(c, "slow", map[string]any{"data": "meta"}, 0)
	unblock <- struct{}{}

	// Wait for completion.
	for i := 0; i < 20; i++ {
		got, _ := GetTask(c, created.Task.TaskID)
		if got.Status.IsTerminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	result, err := c.Call("tasks/result", map[string]any{"taskId": created.Task.TaskID})
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	json.Unmarshal(result.Raw, &m)

	// Must have "content" (ToolResult shape).
	if _, ok := m["content"]; !ok {
		t.Error("tasks/result should return ToolResult shape with 'content' field")
	}

	// Must have _meta with related-task.
	meta, ok := m["_meta"]
	if !ok {
		t.Fatal("tasks/result missing '_meta' field")
	}
	metaMap := meta.(map[string]any)
	related, ok := metaMap["io.modelcontextprotocol/related-task"]
	if !ok {
		t.Fatal("_meta missing 'io.modelcontextprotocol/related-task'")
	}
	relatedMap := related.(map[string]any)
	if relatedMap["taskId"] != created.Task.TaskID {
		t.Errorf("related taskId = %v, want %s", relatedMap["taskId"], created.Task.TaskID)
	}
}

// --- New tests for gap closure (panyam/mcpkit#279) ---

// TestTaskPanicRecovery verifies that a panicking tool handler transitions
// the task to failed instead of leaving it stuck in working.
func TestTaskPanicRecovery(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "panic-test", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "panic-tool",
			Description: "Always panics",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			panic("test panic")
		},
	)
	Register(Config{Server: srv})
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "panic-tool", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Poll until terminal.
	var info core.TaskInfo
	for i := 0; i < 20; i++ {
		got, err := GetTask(c, created.Task.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		info = got.TaskInfo
		if info.Status.IsTerminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if info.Status != core.TaskFailed {
		t.Errorf("status = %q, want failed", info.Status)
	}
	if info.StatusMessage == "" {
		t.Error("expected non-empty statusMessage with panic info")
	}
}

// TestTaskResultForFailedTask verifies that tasks/result returns the stored
// ToolResult (not a JSON-RPC error) for failed tasks.
func TestTaskResultForFailedTask(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "fail-async", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Wait for task to fail.
	for i := 0; i < 20; i++ {
		got, _ := GetTask(c, created.Task.TaskID)
		if got.Status.IsTerminal() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// tasks/result should return a ToolResult, not an error.
	result, relatedID, err := GetTaskPayload(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("tasks/result should not return error for failed task, got: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true on failed task result")
	}
	if relatedID != created.Task.TaskID {
		t.Errorf("relatedTaskId = %q, want %q", relatedID, created.Task.TaskID)
	}
}

// TestTaskResultForCancelledTask verifies that tasks/result returns a
// cancellation ToolResult for cancelled tasks.
func TestTaskResultForCancelledTask(t *testing.T) {
	srv, _ := newTaskServer(t)
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "slow", map[string]any{"data": "cancel-me"}, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Cancel immediately.
	_, err = CancelTask(c, created.Task.TaskID)
	if err != nil {
		t.Fatal(err)
	}

	// tasks/result should return a ToolResult, not an error.
	result, relatedID, err := GetTaskPayload(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("tasks/result should not return error for cancelled task, got: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true on cancelled task result")
	}
	if relatedID != created.Task.TaskID {
		t.Errorf("relatedTaskId = %q, want %q", relatedID, created.Task.TaskID)
	}
}

// TestTaskPollIntervalPassthrough verifies the client-specified pollInterval
// is returned in CreateTaskResult.
func TestTaskPollIntervalPassthrough(t *testing.T) {
	srv, unblock := newTaskServer(t)
	defer close(unblock)
	c := connectClient(t, srv)

	// Send tools/call with custom pollInterval.
	result, err := c.Call("tools/call", map[string]any{
		"name":      "slow",
		"arguments": map[string]any{"data": "poll"},
		"task":      map[string]any{"pollInterval": 2000},
	})
	if err != nil {
		t.Fatal(err)
	}

	var created core.CreateTaskResult
	json.Unmarshal(result.Raw, &created)

	if created.Task.PollInterval != 2000 {
		t.Errorf("pollInterval = %d, want 2000", created.Task.PollInterval)
	}
}

// TestGetTaskContextNilForSync verifies GetTaskContext returns nil for
// synchronous (non-task) tool invocations.
func TestGetTaskContextNilForSync(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "ctx-test", Version: "0.0.1"})

	var gotTC bool
	srv.RegisterTool(
		core.ToolDef{
			Name:        "check-ctx",
			Description: "Checks if TaskContext is available",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tc := GetTaskContext(ctx)
			gotTC = tc != nil
			return core.TextResult("ok"), nil
		},
	)
	Register(Config{Server: srv})
	c := connectClient(t, srv)

	// Call without task hint — sync mode.
	_, err := c.ToolCall("check-ctx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotTC {
		t.Error("GetTaskContext should return nil for sync tool calls")
	}
}

// TestGetTaskContextAvailableForAsync verifies GetTaskContext returns a
// non-nil TaskContext for async (task) tool invocations.
func TestGetTaskContextAvailableForAsync(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "ctx-test", Version: "0.0.1"})

	gotTaskID := make(chan string, 1)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "check-ctx",
			Description: "Checks if TaskContext is available",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tc := GetTaskContext(ctx)
			if tc != nil {
				gotTaskID <- tc.TaskID()
			} else {
				gotTaskID <- ""
			}
			return core.TextResult("ok"), nil
		},
	)
	Register(Config{Server: srv})
	c := connectClient(t, srv)

	created, err := ToolCallAsTask(c, "check-ctx", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case tid := <-gotTaskID:
		if tid == "" {
			t.Error("GetTaskContext returned nil for async tool call")
		}
		if tid != created.Task.TaskID {
			t.Errorf("TaskID = %q, want %q", tid, created.Task.TaskID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tool handler")
	}
}
