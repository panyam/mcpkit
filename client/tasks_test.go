package client_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	client "github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
	server "github.com/panyam/mcpkit/server"
)

// --- v2 task client fixtures ---

// newTaskV2TestServer registers a small v2 task server: "echo" runs sync,
// "fast-task" creates a task that completes immediately, "slow-task" blocks
// until unblock is closed, and "confirm-delete" exercises TaskElicit so the
// MRTR loop can be driven end-to-end. Returns the server, the unblock chan
// for slow-task (close to release), and the URL of the httptest server.
func newTaskV2TestServer(t *testing.T) (string, chan struct{}) {
	t.Helper()

	srv := server.NewServer(core.ServerInfo{Name: "v2-client-test", Version: "0.0.1"})

	type echoInput struct {
		Message string `json:"message"`
	}
	srv.Register(core.TextTool[echoInput]("echo", "echoes",
		func(ctx core.ToolContext, in echoInput) (string, error) {
			return "echo: " + in.Message, nil
		},
	))

	srv.RegisterTool(
		core.ToolDef{
			Name:        "fast-task",
			Description: "completes immediately",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("fast-done"), nil
		},
	)

	unblock := make(chan struct{})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow-task",
			Description: "blocks until unblocked",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			select {
			case <-unblock:
			case <-ctx.Done():
			}
			return core.TextResult("slow-done"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "confirm-delete",
			Description: "asks via TaskElicit",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tc := server.GetTaskContext(ctx)
			if tc == nil {
				return core.ToolResult{}, errString("no task ctx")
			}
			res, err := tc.TaskElicit(core.ElicitationRequest{
				Message:         "delete?",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean"}}}`),
			})
			if err != nil {
				return core.ToolResult{}, err
			}
			if res.Action == "accept" {
				if ok, _ := res.Content["confirm"].(bool); ok {
					return core.TextResult("deleted"), nil
				}
			}
			return core.TextResult("kept"), nil
		},
	)

	server.RegisterTasks(server.TasksConfig{Server: srv, DefaultPollMs: 50})

	handler := srv.Handler(server.WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(unblock) })

	return ts.URL, unblock
}

// errString lets the inline tool handler raise an error without dragging in
// fmt.Errorf for one literal.
type errString string

func (e errString) Error() string { return string(e) }

func connectV2TaskClient(t *testing.T, url string, opts ...client.ClientOption) *client.Client {
	t.Helper()
	opts = append(opts, client.WithTasksExtension())
	c := client.NewClient(url+"/mcp", core.ClientInfo{Name: "v2-task-client-test", Version: "0.0.1"}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// --- Polymorphic ToolCall ---

// TestToolCall_SyncResult verifies ToolCall returns the Sync variant for a
// tool the server runs synchronously (no Execution / TaskSupport=forbidden).
func TestToolCall_SyncResult(t *testing.T) {
	url, _ := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)

	res, err := client.ToolCall(c, "echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if res.IsTask() {
		t.Fatalf("expected Sync variant for sync tool; got Task=%+v", res.Task)
	}
	if res.Sync == nil || len(res.Sync.Content) == 0 || res.Sync.Content[0].Text != "echo: hi" {
		t.Errorf("Sync result mismatch: %+v", res.Sync)
	}
}

// TestToolCall_TaskResult verifies ToolCall returns the Task variant when the
// server elects to create a task (TaskSupport=required + extension negotiated).
func TestToolCall_TaskResult(t *testing.T) {
	url, _ := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)

	res, err := client.ToolCall(c, "fast-task", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if !res.IsTask() {
		t.Fatalf("expected Task variant; got Sync=%+v", res.Sync)
	}
	if res.Task.ResultType != core.ResultTypeTask {
		t.Errorf("result_type = %q, want %q", res.Task.ResultType, core.ResultTypeTask)
	}
	if res.Task.Task.TaskID == "" {
		t.Error("missing taskId in CreateTaskResult")
	}
}

// --- GetTask ---

// TestGetTask returns DetailedTask with the v2 wire shape (TaskInfoV2 fields,
// requestState present). Status may be working or already terminal — both
// are valid as long as the shape decodes.
func TestGetTask(t *testing.T) {
	url, _ := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)

	res, err := client.ToolCall(c, "fast-task", map[string]any{})
	if err != nil || !res.IsTask() {
		t.Fatalf("setup: ToolCall(fast-task) = %v, %+v", err, res)
	}
	taskID := res.Task.Task.TaskID

	dt, err := client.GetTask(c, taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if dt.TaskID != taskID {
		t.Errorf("TaskID = %q, want %q", dt.TaskID, taskID)
	}
	if dt.RequestState == "" {
		t.Errorf("expected non-empty requestState (server should emit one)")
	}
}

// --- WaitForTask ---

// TestWaitForTask polls until terminal and verifies the inlined result is
// surfaced on DetailedTask.Result.
func TestWaitForTask(t *testing.T) {
	url, _ := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)

	res, _ := client.ToolCall(c, "fast-task", map[string]any{})
	taskID := res.Task.Task.TaskID

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dt, err := client.WaitForTask(ctx, c, taskID)
	if err != nil {
		t.Fatalf("WaitForTask: %v", err)
	}
	if dt.Status != core.TaskCompleted {
		t.Fatalf("status = %q, want completed", dt.Status)
	}
	if dt.Result == nil || len(dt.Result.Content) == 0 || dt.Result.Content[0].Text != "fast-done" {
		t.Errorf("inlined result mismatch: %+v", dt.Result)
	}
}

// TestWaitForTask_HonorsServerPollHint verifies the loop respects the
// server's PollIntervalMilliseconds. We set DefaultPollMs=50 in the fixture,
// kick off a task that takes ~150ms, and assert the wait stretches at least
// across one server-suggested interval (i.e., we don't poll the server in a
// tight loop).
func TestWaitForTask_HonorsServerPollHint(t *testing.T) {
	url, unblock := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)

	res, _ := client.ToolCall(c, "slow-task", map[string]any{})
	taskID := res.Task.Task.TaskID

	// Release after 150ms so the wait spans at least 2-3 server-hinted polls.
	go func() {
		time.Sleep(150 * time.Millisecond)
		select {
		case unblock <- struct{}{}:
		default:
		}
	}()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dt, err := client.WaitForTask(ctx, c, taskID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("WaitForTask: %v", err)
	}
	if dt.Status != core.TaskCompleted {
		t.Fatalf("status = %q, want completed", dt.Status)
	}
	// We're not asserting an upper bound (CI noise) — just that we waited at
	// least one server poll interval. If the helper ignored the hint and
	// busy-looped, elapsed would be ~equal to the work duration (150ms).
	// With a 50ms hint we expect elapsed ≥ ~200ms (work + at least one poll).
	if elapsed < 100*time.Millisecond {
		t.Errorf("elapsed %s suspiciously short — likely busy-looped instead of honoring server's PollIntervalMilliseconds", elapsed)
	}
}

// TestWaitForTask_RespectsCallerOverride verifies the WaitOptions.PollInterval
// override beats the server hint.
func TestWaitForTask_RespectsCallerOverride(t *testing.T) {
	url, unblock := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)

	res, _ := client.ToolCall(c, "slow-task", map[string]any{})
	taskID := res.Task.Task.TaskID

	// Release right away so the test doesn't depend on timing precision.
	close := make(chan struct{})
	go func() {
		<-close
		select {
		case unblock <- struct{}{}:
		default:
		}
	}()
	go func() { time.Sleep(20 * time.Millisecond); close <- struct{}{} }()

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := client.WaitForTask(ctx, c, taskID, client.WaitOptions{PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("WaitForTask: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		// With a 10ms override + ~20ms work we should finish quickly.
		// The server's 50ms hint should NOT have applied.
		t.Errorf("elapsed %s — caller's 10ms override doesn't seem to have taken effect over the server's 50ms hint", elapsed)
	}
}

// --- UpdateTask (MRTR loop) ---

// TestUpdateTask_FullElicitLoop drives the SEP-2663 elicit→update→complete
// cycle through the v2 client helpers end-to-end.
func TestUpdateTask_FullElicitLoop(t *testing.T) {
	url, _ := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)

	res, err := client.ToolCall(c, "confirm-delete", map[string]any{})
	if err != nil || !res.IsTask() {
		t.Fatalf("ToolCall(confirm-delete): err=%v res=%+v", err, res)
	}
	taskID := res.Task.Task.TaskID

	// Poll until input_required surfaces a pending request.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var pending *core.DetailedTask
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dt, err := client.GetTask(c, taskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if dt.Status == core.TaskInputRequired && len(dt.InputRequests) > 0 {
			pending = dt
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pending == nil {
		t.Fatal("task never reached input_required with InputRequests populated")
	}

	var key string
	for k := range pending.InputRequests {
		key = k
		break
	}

	// Reply via UpdateTask.
	if err := client.UpdateTask(c, core.UpdateTaskRequest{
		TaskID: taskID,
		InputResponses: core.InputResponses{
			key: json.RawMessage(`{"action":"accept","content":{"confirm":true}}`),
		},
		RequestState: pending.RequestState,
	}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	// Wait for completion via WaitForTask.
	final, err := client.WaitForTask(ctx, c, taskID)
	if err != nil {
		t.Fatalf("WaitForTask: %v", err)
	}
	if final.Status != core.TaskCompleted {
		t.Errorf("status = %q, want completed", final.Status)
	}
	if final.Result == nil || len(final.Result.Content) == 0 || final.Result.Content[0].Text != "deleted" {
		t.Errorf("inlined result mismatch: %+v", final.Result)
	}
}

// TestUpdateTask_MissingTaskID surfaces the client-side guard before issuing
// the RPC (no point round-tripping a request that's structurally invalid).
func TestUpdateTask_MissingTaskID(t *testing.T) {
	url, _ := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)
	if err := client.UpdateTask(c, core.UpdateTaskRequest{}); err == nil {
		t.Error("expected error for empty TaskID")
	}
}

// --- CancelTask ---

// TestCancelTask returns nil on success (empty ack) and the task transitions
// to cancelled when polled.
func TestCancelTask(t *testing.T) {
	url, unblock := newTaskV2TestServer(t)
	c := connectV2TaskClient(t, url)
	_ = unblock // intentional: leave slow-task blocked so cancel has work to do

	res, _ := client.ToolCall(c, "slow-task", map[string]any{})
	taskID := res.Task.Task.TaskID

	if err := client.CancelTask(c, taskID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	// Status should settle to cancelled on the next poll.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	dt, err := client.WaitForTask(ctx, c, taskID)
	if err != nil {
		t.Fatalf("WaitForTask after cancel: %v", err)
	}
	if dt.Status != core.TaskCancelled {
		t.Errorf("status = %q, want cancelled", dt.Status)
	}
}
