package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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

	RegisterTasks(TasksConfig{Server: srv})
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

// --- Phase 4: tasks/update handler ---

// newTaskV2ServerWithSlow extends the standard v2 fixture with a "slow-task"
// tool that blocks until the returned channel is closed. Phase 4 update tests
// use it to land an update while the task is still in the working state.
// Phase 5 will add a real input_required tool; this is the pre-Phase-5 stand-in.
func newTaskV2ServerWithSlow(t *testing.T) (*Server, chan struct{}) {
	t.Helper()
	srv := newTaskV2Server(t)
	unblock := make(chan struct{})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "slow-task",
			Description: "Blocks until unblocked — used to update during working",
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
	return srv, unblock
}

func createTaskForUpdateTest(t *testing.T, c *client.Client, tool string) string {
	t.Helper()
	res, err := c.Call("tools/call", map[string]any{
		"name":      tool,
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call %q: %v", tool, err)
	}
	var ctr core.CreateTaskResult
	if err := json.Unmarshal(res.Raw, &ctr); err != nil {
		t.Fatalf("unmarshal CreateTaskResult: %v", err)
	}
	if ctr.Task.TaskID == "" {
		t.Fatal("missing taskId in CreateTaskResult")
	}
	return ctr.Task.TaskID
}

// TestV2_UpdateAck verifies tasks/update returns an empty {} ack when the
// supplied taskId matches a non-terminal task. Phase 4 only validates the
// shell — Phase 5 adds the actual input-response delivery.
func TestV2_UpdateAck(t *testing.T) {
	srv, unblock := newTaskV2ServerWithSlow(t)
	defer close(unblock)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	taskID := createTaskForUpdateTest(t, c, "slow-task")

	res, err := c.Call("tasks/update", core.UpdateTaskRequest{
		TaskID: taskID,
		InputResponses: core.InputResponses{
			"elicit-1": json.RawMessage(`{"action":"accept","content":{"ok":true}}`),
		},
		RequestState: "echoed-state",
	})
	if err != nil {
		t.Fatalf("tasks/update: %v", err)
	}

	// Empty ack — JSON should be {} (or absent fields), no task envelope.
	var m map[string]any
	if err := json.Unmarshal(res.Raw, &m); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("UpdateTaskResult should be empty {}, got %d keys: %v", len(m), m)
	}
}

// TestV2_UpdateUnknownTaskRejected verifies tasks/update on an unknown taskId
// surfaces -32602 (invalid params). The PLAN open question allows this to
// flip to a silent ack later if the spec settles that way.
func TestV2_UpdateUnknownTaskRejected(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	_, err := c.Call("tasks/update", core.UpdateTaskRequest{TaskID: "no-such-task"})
	if err == nil {
		t.Fatal("tasks/update with unknown taskId should fail")
	}
	rpcErr, ok := err.(*client.RPCError)
	if !ok {
		t.Fatalf("expected *client.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != core.ErrCodeInvalidParams {
		t.Errorf("code = %d, want %d (invalid params)", rpcErr.Code, core.ErrCodeInvalidParams)
	}
}

// TestV2_UpdateTerminalRejected verifies tasks/update on a terminal task
// surfaces -32602 — once a task is completed/failed/cancelled there's no
// goroutine to deliver responses to.
func TestV2_UpdateTerminalRejected(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	taskID := createTaskForUpdateTest(t, c, "fast-task")

	// Wait for fast-task to complete (it returns immediately on goroutine tick).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gres, err := c.Call("tasks/get", map[string]any{"taskId": taskID})
		if err != nil {
			t.Fatalf("tasks/get: %v", err)
		}
		var dt core.DetailedTask
		json.Unmarshal(gres.Raw, &dt)
		if dt.Status.IsTerminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	_, err := c.Call("tasks/update", core.UpdateTaskRequest{TaskID: taskID})
	if err == nil {
		t.Fatal("tasks/update on terminal task should fail")
	}
	rpcErr, ok := err.(*client.RPCError)
	if !ok {
		t.Fatalf("expected *client.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != core.ErrCodeInvalidParams {
		t.Errorf("code = %d, want %d (invalid params)", rpcErr.Code, core.ErrCodeInvalidParams)
	}
}

// TestV2_UpdateRejectedWithoutExtension verifies tasks/update returns -32601
// when the client did not negotiate the tasks extension at session level.
func TestV2_UpdateRejectedWithoutExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv) // no WithTasksExtension

	_, err := c.Call("tasks/update", core.UpdateTaskRequest{TaskID: "any-id"})
	if err == nil {
		t.Fatal("tasks/update without extension should fail")
	}
	rpcErr, ok := err.(*client.RPCError)
	if !ok {
		t.Fatalf("expected *client.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != core.ErrCodeMethodNotFound {
		t.Errorf("code = %d, want %d (method not found)", rpcErr.Code, core.ErrCodeMethodNotFound)
	}
}

// --- Phase 4: SEP-2243 Mcp-Name response header ---

// rawHTTPCall is a helper that POSTs a JSON-RPC request and returns the raw
// http.Response so tests can inspect transport-level headers (e.g., Mcp-Name).
// Hits the streamable-HTTP endpoint directly, bypassing the mcpkit client.
//
// jsonOnly forces the Accept header to "application/json" so the server picks
// the synchronous JSON path (vs the SSE path that wraps the response in an
// "id:/event:/data:" frame). Set true for tests that need to parse the JSON
// body directly; leave false to exercise the default streamable-HTTP path.
func rawHTTPCall(t *testing.T, baseURL, sessionID string, jsonOnly bool, method, name string) *http.Response {
	t.Helper()
	params := map[string]any{
		"name":      name,
		"arguments": map[string]any{},
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if jsonOnly {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", core.StreamableHTTPAccept)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP POST: %v", err)
	}
	return resp
}

// initializeSession runs initialize against the test server and returns the
// session id from the Mcp-Session-Id response header. Needed because the v2
// gating tests run header assertions against post-initialize requests.
func initializeSession(t *testing.T, baseURL string, declareTasksExt bool) string {
	t.Helper()
	caps := map[string]any{}
	if declareTasksExt {
		caps["extensions"] = map[string]any{core.TasksExtensionID: map[string]any{}}
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    caps,
			"clientInfo":      map[string]any{"name": "raw-test", "version": "0.0.1"},
		},
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", core.StreamableHTTPAccept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("initialize response missing Mcp-Session-Id")
	}

	// Send notifications/initialized so subsequent calls are accepted.
	notifBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	notif, _ := http.NewRequest(http.MethodPost, baseURL+"/mcp", bytes.NewReader(notifBody))
	notif.Header.Set("Content-Type", "application/json")
	notif.Header.Set("Accept", core.StreamableHTTPAccept)
	notif.Header.Set("Mcp-Session-Id", sid)
	if r, err := http.DefaultClient.Do(notif); err == nil {
		r.Body.Close()
	}
	return sid
}

// TestV2_McpNameHeaderOnTaskCreation verifies the streamable HTTP transport
// emits the SEP-2243 Mcp-Name header carrying the new taskId on the response
// to a task-creating tools/call.
func TestV2_McpNameHeaderOnTaskCreation(t *testing.T) {
	srv := newTaskV2Server(t)
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	sid := initializeSession(t, ts.URL, true /* declare tasks ext */)

	resp := rawHTTPCall(t, ts.URL, sid, true /* jsonOnly */, "tools/call", "fast-task")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	mcpName := resp.Header.Get("Mcp-Name")
	if mcpName == "" {
		t.Fatalf("missing Mcp-Name header on task-creating tools/call; got body: %s", body)
	}

	// Sanity: the header value should match the taskId in the response body.
	var rpcResp struct {
		Result core.CreateTaskResult `json:"result"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if mcpName != rpcResp.Result.Task.TaskID {
		t.Errorf("Mcp-Name = %q, want match for taskId %q", mcpName, rpcResp.Result.Task.TaskID)
	}
}

// TestV2_McpNameHeaderAbsentOnSyncCall verifies the Mcp-Name header is NOT
// set when the tools/call is handled synchronously (no task created).
func TestV2_McpNameHeaderAbsentOnSyncCall(t *testing.T) {
	srv := newTaskV2Server(t)
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	sid := initializeSession(t, ts.URL, true)

	// "echo" has no Execution field → forbidden → never becomes a task.
	resp := rawHTTPCall(t, ts.URL, sid, true /* jsonOnly */, "tools/call", "echo")
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	if got := resp.Header.Get("Mcp-Name"); got != "" {
		t.Errorf("sync tools/call should not emit Mcp-Name; got %q", got)
	}
}

// TestV2_McpNameHeaderAbsentWithoutExtension verifies the header is NOT set
// when the client did not negotiate the tasks extension — middleware never
// reaches the task-creation branch and so never stages the header.
func TestV2_McpNameHeaderAbsentWithoutExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	sid := initializeSession(t, ts.URL, false /* no extension */)

	resp := rawHTTPCall(t, ts.URL, sid, true /* jsonOnly */, "tools/call", "fast-task")
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	if got := resp.Header.Get("Mcp-Name"); got != "" {
		t.Errorf("tools/call without extension should not emit Mcp-Name; got %q", got)
	}
}

// --- Phase 5: SEP-2663 inputRequests / inputResponses flow ---

// newTaskV2ServerWithElicit registers a "confirm-delete" tool that calls
// TaskElicit and returns a result derived from the user's response. The
// Phase 5 integration test drives the full SEP-2663 elicit → update →
// complete cycle against this fixture.
func newTaskV2ServerWithElicit(t *testing.T) *Server {
	t.Helper()
	srv := newTaskV2Server(t)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "confirm-delete",
			Description: "Asks the client to confirm before deleting",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filename": map[string]any{"type": "string"},
				},
			},
			Execution: &core.ToolExecution{TaskSupport: core.TaskSupportRequired},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			tc := GetTaskContext(ctx)
			if tc == nil {
				return core.ToolResult{}, errorString("confirm-delete requires task context")
			}

			var args struct {
				Filename string `json:"filename"`
			}
			json.Unmarshal(req.Arguments, &args)
			if args.Filename == "" {
				args.Filename = "untitled"
			}

			res, err := tc.TaskElicit(core.ElicitationRequest{
				Message:         "Delete " + args.Filename + "?",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean"}}}`),
			})
			if err != nil {
				return core.ToolResult{}, err
			}
			if res.Action == "accept" {
				if confirmed, _ := res.Content["confirm"].(bool); confirmed {
					return core.TextResult("deleted " + args.Filename), nil
				}
			}
			return core.TextResult("kept " + args.Filename), nil
		},
	)
	return srv
}

// errorString is a tiny adapter so the inline tool handler doesn't have to
// import "errors" / "fmt" just for one literal.
type errorString string

func (e errorString) Error() string { return string(e) }

// pollV2Detailed polls tasks/get every interval until pred returns true or
// the context expires. Returns the last DetailedTask seen.
func pollV2Detailed(t *testing.T, ctx context.Context, c *client.Client, taskID string, interval time.Duration, pred func(core.DetailedTask) bool) core.DetailedTask {
	t.Helper()
	var last core.DetailedTask
	for {
		gres, err := c.Call("tasks/get", map[string]any{"taskId": taskID})
		if err != nil {
			t.Fatalf("tasks/get: %v", err)
		}
		if err := json.Unmarshal(gres.Raw, &last); err != nil {
			t.Fatalf("unmarshal DetailedTask: %v", err)
		}
		if pred(last) {
			return last
		}
		select {
		case <-ctx.Done():
			t.Fatalf("tasks/get poll timed out; last status %q, raw %s", last.Status, gres.Raw)
		case <-time.After(interval):
		}
	}
}

// TestV2_ElicitUpdateCompleteFlow drives the full SEP-2663 input-request
// loop end-to-end:
//
//	tools/call confirm-delete  →  CreateTaskResult
//	poll tasks/get             →  status: input_required, inputRequests populated
//	tasks/update               →  empty ack, server-side goroutine unblocks
//	poll tasks/get             →  status: completed, result inlined
func TestV2_ElicitUpdateCompleteFlow(t *testing.T) {
	srv := newTaskV2ServerWithElicit(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	res, err := c.Call("tools/call", map[string]any{
		"name":      "confirm-delete",
		"arguments": map[string]any{"filename": "important.txt"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	var ctr core.CreateTaskResult
	if err := json.Unmarshal(res.Raw, &ctr); err != nil {
		t.Fatalf("unmarshal CreateTaskResult: %v", err)
	}
	taskID := ctr.Task.TaskID

	// 1. Wait for input_required + a populated inputRequests map.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pending := pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status == core.TaskInputRequired && len(d.InputRequests) > 0
	})
	if got := len(pending.InputRequests); got != 1 {
		t.Fatalf("expected 1 pending input request, got %d (raw: %+v)", got, pending.InputRequests)
	}

	// Pick whatever key the server minted — clients MUST treat it as opaque.
	var key string
	var inputReq core.InputRequest
	for k, v := range pending.InputRequests {
		key = k
		inputReq = v
		break
	}
	if inputReq.Method != "elicitation/create" {
		t.Errorf("inputRequests[%q].method = %q, want elicitation/create", key, inputReq.Method)
	}

	// 2. Reply via tasks/update — empty ack confirms the server accepted it.
	ackRes, err := c.Call("tasks/update", core.UpdateTaskRequest{
		TaskID: taskID,
		InputResponses: core.InputResponses{
			key: json.RawMessage(`{"action":"accept","content":{"confirm":true}}`),
		},
	})
	if err != nil {
		t.Fatalf("tasks/update: %v", err)
	}
	var ackMap map[string]any
	json.Unmarshal(ackRes.Raw, &ackMap)
	if len(ackMap) != 0 {
		t.Errorf("tasks/update ack should be empty {}, got %v", ackMap)
	}

	// 3. Poll until the goroutine resumes and the task completes.
	final := pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status.IsTerminal()
	})
	if final.Status != core.TaskCompleted {
		t.Fatalf("status = %q, want completed", final.Status)
	}
	if final.Result == nil || len(final.Result.Content) == 0 {
		t.Fatalf("expected inlined result with content, got %+v", final.Result)
	}
	if got := final.Result.Content[0].Text; got != "deleted important.txt" {
		t.Errorf("result text = %q, want %q", got, "deleted important.txt")
	}
}

// TestV2_ElicitCancelUnblocks verifies that a tasks/cancel issued while a
// task is parked in input_required cancels the background goroutine — the
// pending TaskElicit returns via ctx.Done() instead of waiting forever for
// a tasks/update that will never come. The task transitions to cancelled.
func TestV2_ElicitCancelUnblocks(t *testing.T) {
	srv := newTaskV2ServerWithElicit(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	res, err := c.Call("tools/call", map[string]any{
		"name":      "confirm-delete",
		"arguments": map[string]any{"filename": "doc.txt"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	var ctr core.CreateTaskResult
	json.Unmarshal(res.Raw, &ctr)
	taskID := ctr.Task.TaskID

	// Wait for input_required, then cancel.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status == core.TaskInputRequired
	})

	if _, err := c.Call("tasks/cancel", map[string]any{"taskId": taskID}); err != nil {
		t.Fatalf("tasks/cancel: %v", err)
	}

	final := pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status.IsTerminal()
	})
	if final.Status != core.TaskCancelled {
		t.Errorf("status = %q, want cancelled", final.Status)
	}
}

// TestV2_UpdateUnknownKeyIgnored verifies that delivering a tasks/update
// payload for a key that doesn't match any pending request is silently
// dropped — the still-blocked TaskElicit keeps waiting. Important because
// the wait-then-deliver dance can race; an in-flight tasks/update with a
// stale or made-up key MUST NOT crash the server.
func TestV2_UpdateUnknownKeyIgnored(t *testing.T) {
	srv := newTaskV2ServerWithElicit(t)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	res, err := c.Call("tools/call", map[string]any{
		"name":      "confirm-delete",
		"arguments": map[string]any{"filename": "x.txt"},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	var ctr core.CreateTaskResult
	json.Unmarshal(res.Raw, &ctr)
	taskID := ctr.Task.TaskID

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pending := pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status == core.TaskInputRequired && len(d.InputRequests) > 0
	})

	// Send an update for a bogus key — should ack but NOT unblock.
	if _, err := c.Call("tasks/update", core.UpdateTaskRequest{
		TaskID: taskID,
		InputResponses: core.InputResponses{
			"bogus-key": json.RawMessage(`{"action":"reject"}`),
		},
	}); err != nil {
		t.Fatalf("tasks/update with unknown key: %v", err)
	}

	// Confirm the task is still parked — the real key is still pending.
	gres, _ := c.Call("tasks/get", map[string]any{"taskId": taskID})
	var still core.DetailedTask
	json.Unmarshal(gres.Raw, &still)
	if still.Status != core.TaskInputRequired {
		t.Errorf("status = %q, want still input_required", still.Status)
	}

	// Now satisfy the real key so the test cleanly completes the goroutine.
	var realKey string
	for k := range pending.InputRequests {
		realKey = k
		break
	}
	c.Call("tasks/update", core.UpdateTaskRequest{
		TaskID: taskID,
		InputResponses: core.InputResponses{
			realKey: json.RawMessage(`{"action":"accept","content":{"confirm":false}}`),
		},
	})
	pollV2Detailed(t, ctx, c, taskID, 20*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status.IsTerminal()
	})
}
