package server_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	. "github.com/panyam/mcpkit/server"
)

// newTaskV2Server registers a v2 tasks server with three tools:
//   - "echo"      — sync (no Execution → forbidden)
//   - "fast-task" — async-eligible (TaskSupportOptional, completes immediately)
//   - "must-task" — async-required (TaskSupportRequired, completes immediately)
//
// fast-task / must-task complete on the first goroutine tick so polling tests
// don't need to thread an unblock channel through every assertion.
func newTaskV2Server(t *testing.T) *Server {
	t.Helper()

	srv := NewServer(core.ServerInfo{Name: "tasks-v2-test", Version: "0.0.1"})

	type echoInput struct {
		Message string `json:"message"`
	}
	srv.Register(core.TextTool[echoInput]("echo", "Echoes input",
		func(ctx core.ToolContext, input echoInput) (string, error) {
			return "echo: " + input.Message, nil
		},
	))

	srv.RegisterTool(
		core.ToolDef{
			Name:        "fast-task",
			Description: "Async-eligible, completes immediately",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("fast-done"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "must-task",
			Description: "Async-required, completes immediately",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("must-done"), nil
		},
	)

	RegisterTasksV2(TasksV2Config{Server: srv})
	return srv
}

func connectV2Client(t *testing.T, srv *Server, opts ...client.ClientOption) *client.Client {
	t.Helper()
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "v2-test", Version: "0.0.1"}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// --- Phase 3 gating tests ---

// TestV2_ExtensionAdvertised verifies the SEP-2663 Tasks extension is
// advertised in the initialize response under capabilities.extensions.
func TestV2_ExtensionAdvertised(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	if !c.ServerSupportsExtension(core.TasksExtensionID) {
		t.Errorf("server should advertise %q in capabilities.extensions", core.TasksExtensionID)
	}
}

// TestV2_NoTaskCreationWithoutExtension verifies that an async-eligible tool
// call returns a synchronous ToolResult (no resultType discriminator) when
// the client has not negotiated the tasks extension. SEP-2663: server MUST
// NOT return CreateTaskResult without negotiation.
func TestV2_NoTaskCreationWithoutExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv) // no WithTasksExtension

	res, err := c.Call("tools/call", map[string]any{
		"name":      "fast-task",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(res.Raw, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if rt, ok := m["resultType"]; ok {
		t.Errorf("response must NOT carry resultType when extension not negotiated; got resultType=%v", rt)
	}
	if _, ok := m["task"]; ok {
		t.Errorf("response must NOT carry task envelope when extension not negotiated; got %s", res.Raw)
	}
	// Sync ToolResult shape: should have content[].
	if _, ok := m["content"]; !ok {
		t.Errorf("expected sync ToolResult shape with content[]; got %s", res.Raw)
	}
}

// TestV2_TaskCreationWithExtension verifies that the same async-eligible tool
// returns a CreateTaskResult once the client has negotiated the extension.
func TestV2_TaskCreationWithExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	res, err := c.Call("tools/call", map[string]any{
		"name":      "fast-task",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}

	var ctr core.CreateTaskResult
	if err := json.Unmarshal(res.Raw, &ctr); err != nil {
		t.Fatalf("unmarshal CreateTaskResult: %v", err)
	}
	if ctr.ResultType != core.ResultTypeTask {
		t.Errorf("resultType = %q, want %q", ctr.ResultType, core.ResultTypeTask)
	}
	if ctr.Task.TaskID == "" {
		t.Error("CreateTaskResult.task.taskId should not be empty")
	}
}

// TestV2_TasksGetRejectedWithoutExtension verifies tasks/get returns
// -32601 (method not found) when the client has not negotiated the extension.
func TestV2_TasksGetRejectedWithoutExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv) // no WithTasksExtension

	_, err := c.Call("tasks/get", map[string]any{"taskId": "any-id"})
	if err == nil {
		t.Fatal("tasks/get should fail without extension negotiation")
	}
	rpcErr, ok := err.(*client.RPCError)
	if !ok {
		t.Fatalf("expected *client.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != core.ErrCodeMethodNotFound {
		t.Errorf("code = %d, want %d (method not found)", rpcErr.Code, core.ErrCodeMethodNotFound)
	}
}

// TestV2_TasksCancelRejectedWithoutExtension verifies tasks/cancel returns
// -32601 when the client has not negotiated the extension.
func TestV2_TasksCancelRejectedWithoutExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv) // no WithTasksExtension

	_, err := c.Call("tasks/cancel", map[string]any{"taskId": "any-id"})
	if err == nil {
		t.Fatal("tasks/cancel should fail without extension negotiation")
	}
	rpcErr, ok := err.(*client.RPCError)
	if !ok {
		t.Fatalf("expected *client.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != core.ErrCodeMethodNotFound {
		t.Errorf("code = %d, want %d (method not found)", rpcErr.Code, core.ErrCodeMethodNotFound)
	}
}

// TestV2_PerRequestExtensionOptIn verifies SEP-2575: a client that did NOT
// negotiate the extension in initialize can still opt into task creation on
// a single tools/call by including io.modelcontextprotocol/clientCapabilities
// under _meta.
func TestV2_PerRequestExtensionOptIn(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv) // session-level: no extension

	res, err := c.Call("tools/call", map[string]any{
		"name":      "fast-task",
		"arguments": map[string]any{},
		"_meta": map[string]any{
			core.PerRequestClientCapsKey: map[string]any{
				"extensions": map[string]any{
					core.TasksExtensionID: map[string]any{},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("tools/call with per-request opt-in: %v", err)
	}

	var ctr core.CreateTaskResult
	if err := json.Unmarshal(res.Raw, &ctr); err != nil {
		t.Fatalf("unmarshal CreateTaskResult: %v", err)
	}
	if ctr.ResultType != core.ResultTypeTask {
		t.Errorf("per-request opt-in should produce CreateTaskResult; got resultType=%q, raw=%s", ctr.ResultType, res.Raw)
	}
}

// TestV2_TasksGetWorksWithExtension is a smoke test that with the extension
// negotiated, the full create → get cycle works end-to-end.
func TestV2_TasksGetWorksWithExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	res, err := c.Call("tools/call", map[string]any{
		"name":      "fast-task",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	var ctr core.CreateTaskResult
	if err := json.Unmarshal(res.Raw, &ctr); err != nil {
		t.Fatalf("unmarshal CreateTaskResult: %v", err)
	}

	// Poll tasks/get until terminal — fast-task completes on first goroutine tick.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		gres, err := c.Call("tasks/get", map[string]any{"taskId": ctr.Task.TaskID})
		if err != nil {
			t.Fatalf("tasks/get: %v", err)
		}
		var dt core.DetailedTask
		if err := json.Unmarshal(gres.Raw, &dt); err != nil {
			t.Fatalf("unmarshal DetailedTask: %v", err)
		}
		if dt.Status.IsTerminal() {
			if dt.Status != core.TaskCompleted {
				t.Errorf("status = %q, want %q", dt.Status, core.TaskCompleted)
			}
			if dt.Result == nil || len(dt.Result.Content) == 0 || dt.Result.Content[0].Text != "fast-done" {
				t.Errorf("inlined result mismatch; got %+v", dt.Result)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("task did not reach terminal: %v", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
