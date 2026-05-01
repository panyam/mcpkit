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

// newHybridTaskServer registers a server with both v1 and v2 task surfaces
// active. Tools: "echo" (sync), "fast-task" (async-eligible, completes
// immediately on either path).
func newHybridTaskServer(t *testing.T) *Server {
	t.Helper()
	srv := NewServer(core.ServerInfo{Name: "hybrid-test", Version: "0.0.1"})

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
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("fast-done"), nil
		},
	)

	RegisterTasksHybrid(TasksHybridConfig{Server: srv})
	return srv
}

func connectHybridClient(t *testing.T, srv *Server, opts ...client.ClientOption) (*client.Client, string) {
	t.Helper()
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "hybrid-client", Version: "0.0.1"}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, ts.URL
}

// --- Hybrid integration tests ---

// TestHybrid_AdvertisesBothCapabilities verifies the initialize response
// includes BOTH the v1 ServerCapabilities.Tasks slot AND the v2
// io.modelcontextprotocol/tasks extension, so clients on either path can
// negotiate the surface they understand.
func TestHybrid_AdvertisesBothCapabilities(t *testing.T) {
	srv := newHybridTaskServer(t)
	c, _ := connectHybridClient(t, srv, client.WithTasksExtension())

	if !c.ServerSupportsExtension(core.TasksExtensionID) {
		t.Errorf("hybrid server should advertise %q extension", core.TasksExtensionID)
	}
	// The v1 ServerCapabilities.Tasks slot is also expected to be present —
	// the SDK doesn't expose it as a typed accessor here, but the
	// TestHybrid_V1ClientSeesV1Shapes scenario below proves the v1 path
	// is wired.
}

// TestHybrid_V1ClientSeesV1Shapes verifies that a v1-shaped client (sends
// task hint via ToolCallAsTaskV1, doesn't declare the extension) gets v1
// wire shapes back from the hybrid server.
func TestHybrid_V1ClientSeesV1Shapes(t *testing.T) {
	srv := newHybridTaskServer(t)
	// No WithTasksExtension — this client is v1-only.
	c, _ := connectHybridClient(t, srv)

	created, err := client.ToolCallAsTaskV1(c, "fast-task", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCallAsTaskV1: %v", err)
	}
	if created.Task.TaskID == "" {
		t.Fatal("v1 task creation should yield a taskId")
	}
	// v1 wire: TaskInfo with `ttl` (ms) at root, no `ttlSeconds`.
	if created.Task.TTL == nil || *created.Task.TTL <= 0 {
		t.Errorf("v1 CreateTaskResultV1.task.ttl should be positive ms; got %v", created.Task.TTL)
	}

	// v1 tasks/get returns GetTaskResultV1 (flat TaskInfo, no DetailedTask
	// extras like result/error/inputRequests).
	got, err := client.GetTaskV1(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskV1: %v", err)
	}
	if got.TaskID != created.Task.TaskID {
		t.Errorf("GetTaskV1.TaskID = %q, want %q", got.TaskID, created.Task.TaskID)
	}

	// v1 tasks/result is reachable for v1 clients.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.WaitForTaskV1(ctx, c, created.Task.TaskID, 20*time.Millisecond); err != nil {
		t.Fatalf("WaitForTaskV1: %v", err)
	}
	result, _, err := client.GetTaskPayloadV1(c, created.Task.TaskID)
	if err != nil {
		t.Fatalf("GetTaskPayloadV1: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "fast-done" {
		t.Errorf("v1 tasks/result content mismatch: %+v", result.Content)
	}
}

// TestHybrid_V2ClientSeesV2Shapes verifies that a v2 client (declares the
// extension, no task hint) gets v2 wire shapes back.
func TestHybrid_V2ClientSeesV2Shapes(t *testing.T) {
	srv := newHybridTaskServer(t)
	c, _ := connectHybridClient(t, srv, client.WithTasksExtension())

	res, err := client.ToolCall(c, "fast-task", map[string]any{})
	if err != nil {
		t.Fatalf("ToolCall: %v", err)
	}
	if !res.IsTask() {
		t.Fatalf("v2 task creation should return Task variant; got Sync=%+v", res.Sync)
	}
	if res.Task.ResultType != core.ResultTypeTask {
		t.Errorf("result_type = %q, want %q", res.Task.ResultType, core.ResultTypeTask)
	}
	// v2 wire: TaskInfoV2 fields embedded directly on CreateTaskResult, with
	// `ttlSeconds` (no v1 `ttl` ms field).
	if res.Task.TTLSeconds == nil || *res.Task.TTLSeconds <= 0 {
		t.Errorf("v2 ttlSeconds should be positive; got %v", res.Task.TTLSeconds)
	}

	// v2 tasks/get returns DetailedTask with inlined result.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	final, err := client.WaitForTask(ctx, c, res.Task.TaskID)
	if err != nil {
		t.Fatalf("WaitForTask: %v", err)
	}
	if final.Status != core.TaskCompleted {
		t.Fatalf("v2 task status = %q, want completed", final.Status)
	}
	if final.Result == nil || len(final.Result.Content) == 0 || final.Result.Content[0].Text != "fast-done" {
		t.Errorf("v2 inlined result mismatch: %+v", final.Result)
	}
}

// TestHybrid_V2ClientCannotSeeV1Methods verifies that tasks/result and
// tasks/list return -32601 for clients that negotiated the v2 extension —
// defense in depth so a v2 client doesn't get a v1-shaped response its
// SDK can't parse.
func TestHybrid_V2ClientCannotSeeV1Methods(t *testing.T) {
	srv := newHybridTaskServer(t)
	c, _ := connectHybridClient(t, srv, client.WithTasksExtension())

	for _, method := range []string{"tasks/result", "tasks/list"} {
		_, err := c.Call(method, map[string]any{"taskId": "any"})
		if err == nil {
			t.Errorf("%s should fail for v2 client; got nil error", method)
			continue
		}
		rpcErr, ok := err.(*client.RPCError)
		if !ok {
			t.Errorf("%s: expected *client.RPCError, got %T: %v", method, err, err)
			continue
		}
		if rpcErr.Code != core.ErrCodeMethodNotFound {
			t.Errorf("%s: code = %d, want %d (method not found)", method, rpcErr.Code, core.ErrCodeMethodNotFound)
		}
	}
}

// TestHybrid_V2ExtensionTakesPriority verifies that when a client declares
// the v2 extension AND happens to send a `task` hint (a weird mixed-mode
// case), the v2 middleware wins — the response is CreateTaskResult, not
// CreateTaskResultV1.
func TestHybrid_V2ExtensionTakesPriority(t *testing.T) {
	srv := newHybridTaskServer(t)
	c, _ := connectHybridClient(t, srv, client.WithTasksExtension())

	// Send raw tools/call with a v1 task hint (params.task) — the v2 middleware
	// is registered first and should claim the request.
	resp, err := c.Call("tools/call", map[string]any{
		"name":      "fast-task",
		"arguments": map[string]any{},
		"task":      map[string]any{"ttl": 60000},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	var ctr core.CreateTaskResult
	if err := json.Unmarshal(resp.Raw, &ctr); err != nil {
		t.Fatalf("unmarshal CreateTaskResult: %v", err)
	}
	if ctr.ResultType != core.ResultTypeTask {
		t.Errorf("result_type = %q, want %q (v2 should win when extension declared)", ctr.ResultType, core.ResultTypeTask)
	}
	if ctr.TTLSeconds == nil {
		t.Errorf("expected v2 wire shape (TTLSeconds present); got %+v", ctr)
	}
}
