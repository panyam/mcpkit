package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// taskFixture is a hand-rolled SEP-2663 server: one task-returning tool plus
// tasks/get and tasks/update over custom method handlers. Deliberately not
// ext/tasks, so the agent module's dependency set stays core+client+server.
type taskFixture struct {
	mu        sync.Mutex
	polls     int
	responses core.InputResponses
	failMode  bool
}

func (f *taskFixture) server(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "task-fixture", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "long_job", Description: "task-backed job", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.CreateTaskResult{
				ResultType: core.ResultTypeTask,
				TaskInfoV2: core.TaskInfoV2{
					TaskID: "t-1", Status: core.TaskWorking,
					CreatedAt: "2026-07-16T00:00:00Z", LastUpdatedAt: "2026-07-16T00:00:00Z",
					TTLMs: core.IntPtr(60000), PollIntervalMs: core.IntPtr(1),
				},
			}, nil
		},
	)
	srv.HandleMethod("tasks/get", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		dt := core.DetailedTask{TaskInfoV2: core.TaskInfoV2{
			TaskID: "t-1", CreatedAt: "2026-07-16T00:00:00Z", LastUpdatedAt: "2026-07-16T00:00:00Z",
			TTLMs: core.IntPtr(60000), PollIntervalMs: core.IntPtr(1),
		}}
		switch {
		case f.failMode && f.polls >= 2:
			dt.Status = core.TaskFailed
			dt.Error = &core.TaskError{Code: -32000, Message: "disk full"}
		case f.responses != nil:
			dt.Status = core.TaskCompleted
			name, _ := decodeName(f.responses["confirm"])
			dt.Result = &core.ToolResult{Content: []core.Content{{Type: "text", Text: "job done for " + name}}}
		case f.polls >= 2:
			dt.Status = core.TaskInputRequired
			dt.InputRequests = core.InputRequests{
				"confirm": core.NewElicitationInputRequest(core.ElicitationRequest{
					Message:         "Who is this job for?",
					RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
				}),
			}
		default:
			dt.Status = core.TaskWorking
		}
		return methodResult(id, dt)
	})
	srv.HandleMethod("tasks/update", func(ctx core.MethodContext, id json.RawMessage, params json.RawMessage) *core.Response {
		var req core.UpdateTaskRequest
		json.Unmarshal(params, &req)
		f.mu.Lock()
		f.responses = req.InputResponses
		f.mu.Unlock()
		return methodResult(id, core.UpdateTaskResult{})
	})
	return srv
}

func decodeName(raw json.RawMessage) (string, error) {
	var er struct {
		Content struct {
			Name string `json:"name"`
		} `json:"content"`
	}
	err := json.Unmarshal(raw, &er)
	return er.Content.Name, err
}

func methodResult(id json.RawMessage, v any) *core.Response {
	return &core.Response{JSONRPC: "2.0", ID: id, Result: v}
}

func connectTaskSource(t *testing.T, f *taskFixture, ui ElicitationUI) (*ClientSource, *[]core.TaskStatus) {
	t.Helper()
	ts := httptest.NewServer(f.server(t).Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	coord := NewElicitationCoordinator(ui)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "task-test", Version: "1.0"},
		client.WithElicitationHandler(coord.Handler()))
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	var seen []core.TaskStatus
	src := NewClientSource(c,
		WithInputHandler(client.DefaultInputHandler(c)),
		WithTaskStatusHook(func(dt *core.DetailedTask) { seen = append(seen, dt.Status) }),
	)
	return src, &seen
}

func TestTaskDispatchWithInputPause(t *testing.T) {
	f := &taskFixture{}
	src, seen := connectTaskSource(t, f, func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		if req.Message != "Who is this job for?" {
			t.Errorf("unexpected elicitation: %+v", req)
		}
		return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": "Ada"}}, nil
	})

	res, err := src.Call(context.Background(), "long_job", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content[0].Text != "job done for Ada" {
		t.Fatalf("result = %+v", res)
	}
	var sawWorking, sawInput, sawDone bool
	for _, s := range *seen {
		switch s {
		case core.TaskWorking:
			sawWorking = true
		case core.TaskInputRequired:
			sawInput = true
		case core.TaskCompleted:
			sawDone = true
		}
	}
	if !sawWorking || !sawInput || !sawDone {
		t.Fatalf("status hook must observe the lifecycle, saw %v", *seen)
	}
}

func TestTaskDispatchFailureMapsToError(t *testing.T) {
	f := &taskFixture{failMode: true}
	src, _ := connectTaskSource(t, f, func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		return core.ElicitationResult{Action: "accept"}, nil
	})
	_, err := src.Call(context.Background(), "long_job", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("failed task must surface its error: %v", err)
	}
}

func TestTaskDispatchCancelledContextAborts(t *testing.T) {
	f := &taskFixture{}
	src, _ := connectTaskSource(t, f, func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": "x"}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.Call(ctx, "long_job", map[string]any{}); err == nil {
		t.Fatal("cancelled ctx must abort the task wait")
	}
}
