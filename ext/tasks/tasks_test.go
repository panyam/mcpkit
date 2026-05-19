package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	. "github.com/panyam/mcpkit/server"
	tasks "github.com/panyam/mcpkit/ext/tasks"
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

	tasks.Register(tasks.Config{Server: srv})
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
// call returns a synchronous ToolResult — resultType:"complete" per
// SEP-2322, no task envelope — when the client has not negotiated the
// tasks extension. SEP-2663: server MUST NOT return CreateTaskResult
// (resultType:"task") without negotiation.
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
	// SEP-2322: sync ToolResult carries resultType:"complete" (not "task").
	if rt := m["resultType"]; rt != "complete" {
		t.Errorf("sync ToolResult.resultType = %v, want \"complete\"", rt)
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
	if ctr.TaskID == "" {
		t.Error("CreateTaskResult.taskId should not be empty (SEP-2663 flat shape)")
	}
}

// TestV2_RequiredTaskRejectsClientWithoutExtension verifies the merged
// SEP-2663 required-tasks error spec. When a tool declared with
// TaskSupport=required is invoked by a client that has not negotiated the
// io.modelcontextprotocol/tasks extension, the server MUST return -32003
// (Missing Required Client Capability) with a structured `requiredCapabilities`
// payload — NOT silently fall through to synchronous execution.
// TaskSupport=optional retains the sync-fallback behaviour because the
// server can still service those requests without a task.
func TestV2_RequiredTaskRejectsClientWithoutExtension(t *testing.T) {
	srv := newTaskV2Server(t)
	c := connectV2Client(t, srv) // no WithTasksExtension

	_, err := c.Call("tools/call", map[string]any{
		"name":      "must-task",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("must-task without extension should reject with -32003, got nil error")
	}
	rpcErr, ok := err.(*client.RPCError)
	if !ok {
		t.Fatalf("expected *client.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Errorf("code = %d, want %d (-32003 Missing Required Client Capability)",
			rpcErr.Code, core.ErrCodeMissingRequiredClientCapability)
	}

	// Validate the data shape: requiredCapabilities.extensions.<tasksExtensionID>.
	// Use round-trip JSON to normalize map types regardless of decoder path.
	raw, err := json.Marshal(rpcErr.Data)
	if err != nil {
		t.Fatalf("marshal error data: %v", err)
	}
	var data struct {
		RequiredCapabilities struct {
			Extensions map[string]json.RawMessage `json:"extensions"`
		} `json:"requiredCapabilities"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal error data: %v (raw=%s)", err, raw)
	}
	if _, present := data.RequiredCapabilities.Extensions[core.TasksExtensionID]; !present {
		t.Errorf("required extension %q missing from error data; got %s",
			core.TasksExtensionID, raw)
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
	if ctr.TaskID == "" {
		t.Fatal("missing taskId in CreateTaskResult")
	}
	return ctr.TaskID
}

// TestV2_UpdateAck verifies tasks/update returns an empty {} ack when the
// supplied taskId matches a non-terminal task. Phase 4 only validates the
// shell — Phase 5 adds the actual input-response delivery.
func TestV2_UpdateAck(t *testing.T) {
	srv, unblock := newTaskV2ServerWithSlow(t)
	defer close(unblock)
	c := connectV2Client(t, srv, client.WithTasksExtension())

	taskID := createTaskForUpdateTest(t, c, "slow-task")

	// Send no RequestState — empty is allowed (legacy/first-call path).
	// HMAC-state coverage is in TestV2_RequestState_* below.
	res, err := c.Call("tasks/update", core.UpdateTaskRequest{
		TaskID: taskID,
		InputResponses: core.InputResponses{
			"elicit-1": json.RawMessage(`{"action":"accept","content":{"ok":true}}`),
		},
	})
	if err != nil {
		t.Fatalf("tasks/update: %v", err)
	}

	// SEP-2663 ack: no task state. SEP-2322: must carry the resultType
	// discriminator. So the wire payload is {"resultType":"complete"} —
	// nothing else.
	var m map[string]any
	if err := json.Unmarshal(res.Raw, &m); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if rt := m["resultType"]; rt != "complete" {
		t.Errorf("UpdateTaskResult.resultType = %v, want \"complete\"", rt)
	}
	if len(m) != 1 {
		t.Errorf("UpdateTaskResult should carry only resultType (got %d keys: %v)", len(m), m)
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

	// SEP-2243 also requires Mcp-Method — the JSON-RPC method that produced
	// this response. Pairs with Mcp-Name so HTTP infrastructure can route /
	// log against (method, taskId) without parsing the JSON body.
	if got := resp.Header.Get("Mcp-Method"); got != "tools/call" {
		t.Errorf("Mcp-Method = %q, want \"tools/call\"", got)
	}

	// Sanity: the header value should match the taskId in the response body.
	var rpcResp struct {
		Result core.CreateTaskResult `json:"result"`
	}
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if mcpName != rpcResp.Result.TaskID {
		t.Errorf("Mcp-Name = %q, want match for taskId %q", mcpName, rpcResp.Result.TaskID)
	}
}

// TestV2_McpMethodHeaderOnTasksMethods verifies the SEP-2243 Mcp-Method
// header carries the JSON-RPC method name on every tasks/* response, not
// just task-creating tools/call. Mcp-Name is task-creation-specific and
// stays empty for these reads.
func TestV2_McpMethodHeaderOnTasksMethods(t *testing.T) {
	srv := newTaskV2Server(t)
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	sid := initializeSession(t, ts.URL, true /* declare tasks ext */)

	// Create a task so we have an id to query.
	createResp := rawHTTPCall(t, ts.URL, sid, true /* jsonOnly */, "tools/call", "fast-task")
	createBody, _ := io.ReadAll(createResp.Body)
	createResp.Body.Close()
	var createWrap struct {
		Result core.CreateTaskResult `json:"result"`
	}
	if err := json.Unmarshal(createBody, &createWrap); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	taskID := createWrap.Result.TaskID
	if taskID == "" {
		t.Fatal("missing taskId in CreateTaskResult")
	}

	cases := []struct {
		method string
		body   string
	}{
		{"tasks/get", `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"` + taskID + `"}}`},
		{"tasks/update", `{"jsonrpc":"2.0","id":2,"method":"tasks/update","params":{"taskId":"` + taskID + `"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			req, _ := http.NewRequest("POST", ts.URL+"/mcp", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Mcp-Session-Id", sid)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tc.method, err)
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
			if got := resp.Header.Get("Mcp-Method"); got != tc.method {
				t.Errorf("%s: Mcp-Method = %q, want %q", tc.method, got, tc.method)
			}
			if got := resp.Header.Get("Mcp-Name"); got != "" {
				t.Errorf("%s: Mcp-Name should be empty for non-task-creating responses; got %q", tc.method, got)
			}
		})
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
			tc := tasks.GetTaskContext(ctx)
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
	taskID := ctr.TaskID

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
	// SEP-2322: ack carries only the resultType discriminator.
	if rt := ackMap["resultType"]; rt != "complete" {
		t.Errorf("tasks/update ack.resultType = %v, want \"complete\"", rt)
	}
	if len(ackMap) != 1 {
		t.Errorf("tasks/update ack should carry only resultType (got %d keys: %v)", len(ackMap), ackMap)
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
	taskID := ctr.TaskID

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
	taskID := ctr.TaskID

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

// TestV2_StatusNotificationOmitsRequestState verifies that notifications/tasks
// payloads MUST NOT carry `requestState`. The merged SEP-2663 removed the
// field from the Task base interface, so the DetailedTask shape on every
// task-bearing message — CreateTaskResult, tasks/get, and these
// notifications — omits it. The test inspects the raw JSON payload (rather
// than the typed DetailedTask, which would lose the field at the struct
// boundary) so a server that re-introduces the field would be caught.
func TestV2_StatusNotificationOmitsRequestState(t *testing.T) {
	srv := newTaskV2Server(t)

	// Capture incoming notifications/tasks payloads as raw JSON so we can
	// assert on the actual wire field set.
	notifs := make(chan map[string]any, 8)
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "v2-notif-test", Version: "0.0.1"},
		client.WithGetSSEStream(),
		client.WithTasksExtension(),
		client.WithNotificationCallback(func(method string, params any) {
			if method != "notifications/tasks" {
				return
			}
			raw, err := json.Marshal(params)
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return
			}
			select {
			case notifs <- m:
			default:
			}
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	// Drive a fast task to terminal and watch for the status notification.
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
	taskID := ctr.TaskID

	// Wait up to 2s for any notification matching our task and assert the
	// raw payload omits `requestState`.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m := <-notifs:
			if m["taskId"] != taskID {
				continue
			}
			if _, present := m["requestState"]; present {
				t.Fatalf("notifications/tasks for %s carries requestState; merged SEP-2663 removed it from the v2 wire (payload: %+v)", taskID, m)
			}
			return
		case <-deadline:
			t.Fatalf("timed out waiting for notifications/tasks for %s", taskID)
		}
	}
}

// TestV2_NoProgressOrMessageOnTaskGoroutine verifies that a tool which calls
// EmitProgress or EmitLog while running as a task does not leak
// notifications/progress or notifications/message onto the session stream.
// The merged SEP-2663 made this a MUST NOT; mcpkit enforces it at the
// session-notify boundary by wrapping the bgCtx with
// core.ApplySessionNotifyFilter so neither emission path reaches the wire
// — regardless of whether the tool author remembers the rule.
func TestV2_NoProgressOrMessageOnTaskGoroutine(t *testing.T) {
	srv := newTaskV2Server(t)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "progress-emit-task",
			Description: "Emits progress + log notifications while running as a task; tests SEP-2663 G6 filter",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			// Both of these would normally fan out on the session stream.
			// The v2 task filter is expected to drop both silently.
			ctx.EmitProgress("ignored-token", 1, 2, "halfway")
			ctx.EmitLog(core.LogInfo, "test-logger", map[string]any{"msg": "from-task"})
			return core.TextResult("progress-done"), nil
		},
	)

	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Capture every notification the client sees so we can assert the filter
	// is comprehensive (no leakage of either method).
	type seen struct {
		method string
	}
	notifs := make(chan seen, 16)
	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "v2-g6-test", Version: "0.0.1"},
		client.WithGetSSEStream(),
		client.WithTasksExtension(),
		client.WithNotificationCallback(func(method string, _ any) {
			select {
			case notifs <- seen{method}:
			default:
			}
		}),
	)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	res, err := c.Call("tools/call", map[string]any{
		"name":      "progress-emit-task",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	var ctr core.CreateTaskResult
	if err := json.Unmarshal(res.Raw, &ctr); err != nil {
		t.Fatalf("unmarshal CreateTaskResult: %v", err)
	}

	// Poll until terminal so the goroutine has run + emitted (and we know the
	// filter has had its chance to drop).
	pollV2Detailed(t, context.Background(), c, ctr.TaskID, 10*time.Millisecond, func(d core.DetailedTask) bool {
		return d.Status.IsTerminal()
	})

	// Drain notifications with a short tail wait so any leaked emission has
	// time to land on the SSE stream before we judge.
	deadline := time.After(200 * time.Millisecond)
drain:
	for {
		select {
		case n := <-notifs:
			switch n.method {
			case "notifications/progress":
				t.Fatalf("SEP-2663 G6 violation: notifications/progress leaked onto task stream")
			case "notifications/message":
				t.Fatalf("SEP-2663 G6 violation: notifications/message leaked onto task stream")
			}
		case <-deadline:
			break drain
		}
	}
}
