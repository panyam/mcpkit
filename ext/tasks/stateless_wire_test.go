package tasks_test

// SEP-2575 stateless-wire end-to-end tests for the SEP-2663 tasks extension.
// Each test POSTs a raw stateless-shaped request to a Dual-mode Streamable
// HTTP server with tasks.Register installed, and asserts the response shape
// matches what the v2-on-stateless conformance scenarios expect.
//
// Issue: panyam/mcpkit#480.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	tasks "github.com/panyam/mcpkit/ext/tasks"
	server "github.com/panyam/mcpkit/server"
)

const statelessDraftVersion = "DRAFT-2026-v1"

// newStatelessTaskServer registers a server that intentionally covers the
// scenarios the conformance fork's stateless-mode run exercises:
//
//   - slow_compute    — TaskSupport=optional
//   - failing_job     — TaskSupport=required (returns a tool error after a beat)
//   - confirm_delete  — TaskSupport=required, calls TaskElicit → input_required
//
// Wraps in an httptest server with the default ModeDual; stateless-shaped
// POSTs route through the SEP-2575 dispatcher.
func newStatelessTaskServer(t *testing.T) string {
	t.Helper()

	srv := server.NewServer(core.ServerInfo{Name: "stateless-tasks-test", Version: "0.0.1"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow_compute",
			Description: "optional task support",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("done"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "failing_job",
			Description: "required task support",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			return core.ToolResult{}, errors.New("planned failure")
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "confirm_delete",
			Description: "required task with elicitation",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, _ core.ToolRequest) (core.ToolResponse, error) {
			tc := tasks.GetTaskContext(ctx)
			if tc == nil {
				// SEP-2663 Option 2: TaskElicit needs the continuation
				// goroutine's TaskContext.
				return core.GoAsyncResult{}, nil
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

	tasks.Register(tasks.Config{Server: srv, DefaultPollMs: 50})

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// metaWithCaps builds an _meta envelope optionally declaring the tasks
// extension via per-request clientCapabilities (SEP-2575).
func metaWithCaps(withTasks bool) map[string]any {
	caps := map[string]any{}
	if withTasks {
		caps["extensions"] = map[string]any{
			core.TasksExtensionID: map[string]any{},
		}
	}
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    statelessDraftVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "stateless-task-test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": caps,
	}
}

// postStatelessRPC sends one JSON-RPC request over the stateless wire and
// returns the decoded *core.Response.
func postStatelessRPC(t *testing.T, url string, id int, method string, params map[string]any) *core.Response {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", core.StreamableHTTPAccept)
	req.Header.Set("MCP-Protocol-Version", statelessDraftVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http do: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	payload := bytes.TrimSpace(raw)
	if idx := bytes.Index(payload, []byte("data:")); idx >= 0 {
		payload = bytes.TrimSpace(payload[idx+len("data:"):])
	}

	var out core.Response
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("decode response (%d bytes, status %d): %v\n%s", len(raw), resp.StatusCode, err, string(raw))
	}
	return &out
}

// TestStateless_ToolsCall_OptionalTask_WithExtension covers
// `tasks-capability-negotiation::tasks-per-request-meta-opt-in` from the fork:
// a client that declares the tasks extension only via per-request _meta must
// receive `resultType: "task"` for a TaskSupport=optional tool.
func TestStateless_ToolsCall_OptionalTask_WithExtension(t *testing.T) {
	url := newStatelessTaskServer(t)

	resp := postStatelessRPC(t, url, 1, "tools/call", map[string]any{
		"name":      "slow_compute",
		"arguments": map[string]any{},
		"_meta":     metaWithCaps(true),
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	raw, _ := json.Marshal(resp.Result)
	var got core.CreateTaskResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode CreateTaskResult: %v\n%s", err, string(raw))
	}
	if got.ResultType != core.ResultTypeTask {
		t.Fatalf("resultType = %q, want %q (sync fall-through happened; per-request caps not honored)", got.ResultType, core.ResultTypeTask)
	}
	if got.TaskID == "" {
		t.Errorf("CreateTaskResult.taskId is empty")
	}
	if got.TTLMs == nil {
		t.Errorf("CreateTaskResult.ttlMs is nil; spec requires the renamed field")
	}
}

// TestStateless_ToolsCall_OptionalTask_NoExtension verifies the SEP-2663
// fall-through: a TaskSupport=optional tool called without the tasks
// extension declared returns a synchronous ToolResult, not -32003.
func TestStateless_ToolsCall_OptionalTask_NoExtension(t *testing.T) {
	url := newStatelessTaskServer(t)

	resp := postStatelessRPC(t, url, 1, "tools/call", map[string]any{
		"name":      "slow_compute",
		"arguments": map[string]any{},
		"_meta":     metaWithCaps(false),
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var got core.ToolResult
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode sync ToolResult: %v\n%s", err, string(raw))
	}
	if len(got.Content) == 0 || got.Content[0].Text != "done" {
		t.Errorf("sync content mismatch: %+v", got)
	}
}

// TestStateless_ToolsCall_RequiredTask_NoExtension covers
// `tasks-required-task-error`: TaskSupport=required without the extension
// declared MUST return -32003 with structured requiredCapabilities.
func TestStateless_ToolsCall_RequiredTask_NoExtension(t *testing.T) {
	url := newStatelessTaskServer(t)

	resp := postStatelessRPC(t, url, 1, "tools/call", map[string]any{
		"name":      "failing_job",
		"arguments": map[string]any{},
		"_meta":     metaWithCaps(false),
	})
	if resp.Error == nil {
		t.Fatalf("expected -32003, got success: %+v", resp.Result)
	}
	if resp.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Fatalf("error code = %d, want %d (-32003); body=%+v",
			resp.Error.Code, core.ErrCodeMissingRequiredClientCapability, resp.Error)
	}

	dataRaw, _ := json.Marshal(resp.Error.Data)
	var data struct {
		RequiredCapabilities struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"requiredCapabilities"`
	}
	if err := json.Unmarshal(dataRaw, &data); err != nil {
		t.Fatalf("decode error.data: %v", err)
	}
	if _, ok := data.RequiredCapabilities.Extensions[core.TasksExtensionID]; !ok {
		t.Errorf("requiredCapabilities.extensions missing %q; got %+v",
			core.TasksExtensionID, data.RequiredCapabilities.Extensions)
	}
}

// TestStateless_TasksGet_NoExtension covers
// `tasks-methods-gated-without-extension`: tasks/get on the stateless wire
// without the per-request extension declaration MUST return -32003 (not
// -32601 "method not found", which is what an unrouted method emits).
func TestStateless_TasksGet_NoExtension(t *testing.T) {
	url := newStatelessTaskServer(t)

	resp := postStatelessRPC(t, url, 1, "tasks/get", map[string]any{
		"taskId": "nonexistent",
		"_meta":  metaWithCaps(false),
	})
	if resp.Error == nil {
		t.Fatalf("expected -32003, got success: %+v", resp.Result)
	}
	if resp.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Fatalf("error code = %d, want %d (-32003); body=%+v",
			resp.Error.Code, core.ErrCodeMissingRequiredClientCapability, resp.Error)
	}
}

// TestStateless_TasksUpdate_NoExtension is the tasks/update sibling of the
// gating check.
func TestStateless_TasksUpdate_NoExtension(t *testing.T) {
	url := newStatelessTaskServer(t)

	resp := postStatelessRPC(t, url, 1, "tasks/update", map[string]any{
		"taskId":         "nonexistent",
		"inputResponses": map[string]any{},
		"_meta":          metaWithCaps(false),
	})
	if resp.Error == nil {
		t.Fatalf("expected -32003, got success: %+v", resp.Result)
	}
	if resp.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Fatalf("error code = %d, want %d (-32003); body=%+v",
			resp.Error.Code, core.ErrCodeMissingRequiredClientCapability, resp.Error)
	}
}

// TestStateless_TasksCancel_NoExtension is the tasks/cancel sibling.
func TestStateless_TasksCancel_NoExtension(t *testing.T) {
	url := newStatelessTaskServer(t)

	resp := postStatelessRPC(t, url, 1, "tasks/cancel", map[string]any{
		"taskId": "nonexistent",
		"_meta":  metaWithCaps(false),
	})
	if resp.Error == nil {
		t.Fatalf("expected -32003, got success: %+v", resp.Result)
	}
	if resp.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Fatalf("error code = %d, want %d (-32003); body=%+v",
			resp.Error.Code, core.ErrCodeMissingRequiredClientCapability, resp.Error)
	}
}

// TestStateless_TasksLifecycle covers `tasks-lifecycle`,
// `tasks-dispatch-and-envelope`, and `tasks-wire-field-renames` together:
// create a task, poll tasks/get with the extension declared until terminal,
// confirm the inlined result + ttlMs wire field.
func TestStateless_TasksLifecycle(t *testing.T) {
	url := newStatelessTaskServer(t)

	created := postStatelessRPC(t, url, 1, "tools/call", map[string]any{
		"name":      "slow_compute",
		"arguments": map[string]any{},
		"_meta":     metaWithCaps(true),
	})
	if created.Error != nil {
		t.Fatalf("create task: %+v", created.Error)
	}
	raw, _ := json.Marshal(created.Result)
	var ct core.CreateTaskResult
	if err := json.Unmarshal(raw, &ct); err != nil {
		t.Fatalf("decode CreateTaskResult: %v", err)
	}
	if ct.TaskID == "" {
		t.Fatalf("no taskId on CreateTaskResult: %+v", ct)
	}

	deadline := time.Now().Add(3 * time.Second)
	var final core.DetailedTask
	for time.Now().Before(deadline) {
		resp := postStatelessRPC(t, url, 2, "tasks/get", map[string]any{
			"taskId": ct.TaskID,
			"_meta":  metaWithCaps(true),
		})
		if resp.Error != nil {
			t.Fatalf("tasks/get: %+v", resp.Error)
		}
		raw, _ := json.Marshal(resp.Result)
		var dt core.DetailedTask
		if err := json.Unmarshal(raw, &dt); err != nil {
			t.Fatalf("decode DetailedTask: %v\n%s", err, string(raw))
		}
		if dt.Status == core.TaskCompleted || dt.Status == core.TaskFailed || dt.Status == core.TaskCancelled {
			final = dt
			break
		}
		time.Sleep(40 * time.Millisecond)
	}

	if final.Status != core.TaskCompleted {
		t.Fatalf("status = %q, want completed (lifecycle never terminated)", final.Status)
	}
	if final.Result == nil || len(final.Result.Content) == 0 || final.Result.Content[0].Text != "done" {
		t.Errorf("inlined result mismatch: %+v", final.Result)
	}
}

// TestStateless_TasksMRTR covers `tasks-mrtr-input`: a TaskElicit call parks
// the task in input_required, tasks/update delivers the response, and the
// task transitions to completed. All over the stateless wire.
func TestStateless_TasksMRTR(t *testing.T) {
	url := newStatelessTaskServer(t)

	created := postStatelessRPC(t, url, 1, "tools/call", map[string]any{
		"name":      "confirm_delete",
		"arguments": map[string]any{},
		"_meta":     metaWithCaps(true),
	})
	if created.Error != nil {
		t.Fatalf("create task: %+v", created.Error)
	}
	raw, _ := json.Marshal(created.Result)
	var ct core.CreateTaskResult
	if err := json.Unmarshal(raw, &ct); err != nil {
		t.Fatalf("decode CreateTaskResult: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var inputKey string
	for time.Now().Before(deadline) {
		resp := postStatelessRPC(t, url, 2, "tasks/get", map[string]any{
			"taskId": ct.TaskID,
			"_meta":  metaWithCaps(true),
		})
		if resp.Error != nil {
			t.Fatalf("tasks/get: %+v", resp.Error)
		}
		raw, _ := json.Marshal(resp.Result)
		var dt core.DetailedTask
		if err := json.Unmarshal(raw, &dt); err != nil {
			t.Fatalf("decode DetailedTask: %v", err)
		}
		if dt.Status == core.TaskInputRequired && len(dt.InputRequests) > 0 {
			for k := range dt.InputRequests {
				inputKey = k
				break
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if inputKey == "" {
		t.Fatal("task never surfaced input_required with an inputRequest key")
	}

	updateResp := postStatelessRPC(t, url, 3, "tasks/update", map[string]any{
		"taskId": ct.TaskID,
		"inputResponses": map[string]any{
			inputKey: map[string]any{
				"action":  "accept",
				"content": map[string]any{"confirm": true},
			},
		},
		"_meta": metaWithCaps(true),
	})
	if updateResp.Error != nil {
		t.Fatalf("tasks/update: %+v", updateResp.Error)
	}

	deadline = time.Now().Add(2 * time.Second)
	var final core.DetailedTask
	for time.Now().Before(deadline) {
		resp := postStatelessRPC(t, url, 4, "tasks/get", map[string]any{
			"taskId": ct.TaskID,
			"_meta":  metaWithCaps(true),
		})
		raw, _ := json.Marshal(resp.Result)
		var dt core.DetailedTask
		_ = json.Unmarshal(raw, &dt)
		if dt.Status == core.TaskCompleted || dt.Status == core.TaskFailed {
			final = dt
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != core.TaskCompleted {
		t.Fatalf("after tasks/update, status = %q, want completed", final.Status)
	}
	if final.Result == nil || len(final.Result.Content) == 0 || final.Result.Content[0].Text != "deleted" {
		t.Errorf("expected deleted result, got %+v", final.Result)
	}
}
