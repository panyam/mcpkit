package tasks_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	. "github.com/panyam/mcpkit/server"
	tasks "github.com/panyam/mcpkit/ext/tasks"
)

// TestMRTR_TaskComposition exercises the SEP-2322 + SEP-2663 composition that
// the GoAsync sentinel was introduced to enable: a single tool can run an
// MRTR round-trip (returning InputRequiredResult to gather input) and then
// escalate to async (returning CreateTaskResult). Pre-Option-2 this was
// impossible because taskV2Middleware created the task BEFORE the handler ran,
// so round 1 on a task-eligible tool could never surface as InputRequiredResult.
//
// Mirrors the upstream conformance scenario "mrtr-tasks-composition" / mrtr-08
// (panyam/mcpconformance, branch feat/tasks-mrtr-extension,
// src/scenarios/server/mrtr/ephemeral-flow.ts).
func TestMRTR_TaskComposition(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "mrtr-task-composition-test", Version: "0.0.1"})

	// Register the composition tool. Mirrors examples/mrtr's mrtrTaskCompositionTool:
	//   1. sync, no user_name → InputRequiredResult.
	//   2. sync, user_name present → GoAsync sentinel.
	//   3. goroutine (TaskContext present) → final greeting.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_task",
			Description: "MRTR → Tasks composition fixture",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if tasks.GetTaskContext(ctx) != nil {
				// Continuation phase: produce the final result.
				var er struct {
					Action  string `json:"action"`
					Content struct {
						Name string `json:"name"`
					} `json:"content"`
				}
				if raw := ctx.InputResponse("user_name"); raw != nil {
					_ = json.Unmarshal(raw, &er)
				}
				if er.Content.Name == "" {
					return core.ErrorResult("task continuation lost user_name"), nil
				}
				return core.TextResult("Hello, " + er.Content.Name + "! (computed in task)"), nil
			}
			if ctx.InputResponse("user_name") == nil {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
					},
				})
			}
			return core.ToolResult{GoAsync: true}, nil
		},
	)

	tasks.Register(tasks.Config{Server: srv})

	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "mrtr-task-comp-test", Version: "0.0.1"},
		client.WithTasksExtension(),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Round 1: no inputResponses. Expect InputRequiredResult, no task.
	r1, err := c.Call("tools/call", map[string]any{
		"name":      "test_tool_with_task",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	var m1 map[string]any
	if err := json.Unmarshal(r1.Raw, &m1); err != nil {
		t.Fatalf("round 1 unmarshal: %v (raw=%s)", err, r1.Raw)
	}
	if rt, _ := m1["resultType"].(string); rt != string(core.ResultTypeInputRequired) {
		t.Fatalf("round 1 resultType = %q, want %q; raw=%s",
			rt, core.ResultTypeInputRequired, r1.Raw)
	}
	if _, present := m1["taskId"]; present {
		t.Fatalf("round 1 MUST NOT carry taskId on InputRequiredResult; raw=%s", r1.Raw)
	}
	ir, ok := m1["inputRequests"].(map[string]any)
	if !ok || ir["user_name"] == nil {
		t.Fatalf("round 1 missing inputRequests.user_name; raw=%s", r1.Raw)
	}
	mrtrRequestState, _ := m1["requestState"].(string)

	// Round 2: echo back the elicit response (+ requestState). Expect
	// CreateTaskResult — the handler returned GoAsync after the MRTR loop.
	r2, err := c.Call("tools/call", map[string]any{
		"name":      "test_tool_with_task",
		"arguments": map[string]any{},
		"inputResponses": map[string]any{
			"user_name": map[string]any{
				"action":  "accept",
				"content": map[string]any{"name": "Alice"},
			},
		},
		"requestState": mrtrRequestState,
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	var ctr core.CreateTaskResult
	if err := json.Unmarshal(r2.Raw, &ctr); err != nil {
		t.Fatalf("round 2 unmarshal CreateTaskResult: %v (raw=%s)", err, r2.Raw)
	}
	if ctr.ResultType != core.ResultTypeTask {
		t.Fatalf("round 2 resultType = %q, want %q; raw=%s",
			ctr.ResultType, core.ResultTypeTask, r2.Raw)
	}
	if ctr.TaskID == "" {
		t.Fatalf("round 2 CreateTaskResult missing taskId; raw=%s", r2.Raw)
	}

	// Spec separation: CreateTaskResult MUST NOT carry MRTR requestState (the
	// merged SEP-2663 removed requestState from the v2 wire shape).
	var raw2 map[string]any
	json.Unmarshal(r2.Raw, &raw2)
	if _, present := raw2["requestState"]; present {
		t.Errorf("CreateTaskResult MUST NOT carry requestState (SEP-2663 spec separation); raw=%s", r2.Raw)
	}

	// Round 3: poll tasks/get until terminal, then assert the inlined result
	// reflects the answer gathered during the MRTR phase.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	for {
		gres, err := c.Call("tasks/get", map[string]any{"taskId": ctr.TaskID})
		if err != nil {
			t.Fatalf("tasks/get: %v", err)
		}
		var dt core.DetailedTask
		if err := json.Unmarshal(gres.Raw, &dt); err != nil {
			t.Fatalf("unmarshal DetailedTask: %v", err)
		}
		if dt.Status.IsTerminal() {
			if dt.Status != core.TaskCompleted {
				t.Fatalf("final status = %q, want %q; raw=%s",
					dt.Status, core.TaskCompleted, gres.Raw)
			}
			if dt.Result == nil || len(dt.Result.Content) == 0 {
				t.Fatalf("terminal task missing inlined result; raw=%s", gres.Raw)
			}
			text := dt.Result.Content[0].Text
			if !strings.Contains(text, "Alice") {
				t.Errorf("final task text = %q, want it to contain \"Alice\"", text)
			}
			if !strings.Contains(text, "computed in task") {
				t.Errorf("final task text = %q, want it to mention task continuation", text)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not reach terminal in time; last status = %q", dt.Status)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context expired waiting for terminal: %v", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}
