package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	core "github.com/panyam/mcpkit/core"
)

// TestMRTR_BasicElicitationRoundTrip exercises the SEP-2322 ephemeral flow
// end-to-end through the live HTTP transport: round 1 returns
// IncompleteResult{inputRequests, requestState}; round 2 (same tool, same
// arguments, but with the echoed inputResponses + requestState) returns a
// complete ToolResult. Mirrors the upstream conformance scenario A1.
func TestMRTR_BasicElicitationRoundTrip(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "mrtr-test", Version: "0.0.1"})

	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_elicitation",
			Description: "Asks for user name then greets them",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"What is your name?","requestedSchema":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}`),
					},
				})
			}
			raw := ctx.InputResponse("user_name")
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Name string `json:"name"`
				} `json:"content"`
			}
			if err := json.Unmarshal(raw, &er); err != nil {
				return core.ErrorResult("malformed elicitation response"), nil
			}
			return core.TextResult("Hello, " + er.Content.Name + "!"), nil
		},
	)

	c := connectMRTRClient(t, srv)

	// Round 1: no inputResponses → expect IncompleteResult
	r1, err := c.Call("tools/call", map[string]any{
		"name":      "test_tool_with_elicitation",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	var m1 map[string]any
	if err := json.Unmarshal(r1.Raw, &m1); err != nil {
		t.Fatalf("unmarshal r1: %v", err)
	}
	if m1["resultType"] != "incomplete" {
		t.Fatalf("round 1 resultType = %v, want \"incomplete\"; raw=%s", m1["resultType"], r1.Raw)
	}
	reqs, ok := m1["inputRequests"].(map[string]any)
	if !ok {
		t.Fatalf("round 1 inputRequests missing; raw=%s", r1.Raw)
	}
	entry, ok := reqs["user_name"].(map[string]any)
	if !ok {
		t.Fatalf("round 1 inputRequests[user_name] missing; raw=%s", r1.Raw)
	}
	if entry["method"] != "elicitation/create" {
		t.Errorf("round 1 method = %v, want elicitation/create", entry["method"])
	}
	state1, _ := m1["requestState"].(string)
	if state1 == "" {
		t.Fatalf("round 1 requestState missing; raw=%s", r1.Raw)
	}

	// Round 2: echo back inputResponses + requestState → expect complete
	r2, err := c.Call("tools/call", map[string]any{
		"name":      "test_tool_with_elicitation",
		"arguments": map[string]any{},
		"inputResponses": map[string]any{
			"user_name": map[string]any{
				"action":  "accept",
				"content": map[string]any{"name": "Alice"},
			},
		},
		"requestState": state1,
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	var m2 map[string]any
	if err := json.Unmarshal(r2.Raw, &m2); err != nil {
		t.Fatalf("unmarshal r2: %v", err)
	}
	if rt := m2["resultType"]; rt != "complete" && rt != nil {
		t.Errorf("round 2 resultType = %v, want \"complete\" (or absent)", rt)
	}
	content, ok := m2["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("round 2 missing content array; raw=%s", r2.Raw)
	}
	first := content[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "Hello, Alice") {
		t.Errorf("round 2 text = %v, want greeting with Alice", first["text"])
	}
}

// TestMRTR_RequestStateSigned verifies that with WithRequestStateSigning, the
// requestState minted by the server is HMAC-signed (decodes via the same
// VerifyRequestState helper used internally) and that the signed token
// round-trips intact through the dispatch verifier on retry.
func TestMRTR_RequestStateSigned(t *testing.T) {
	signingKey := []byte("test-signing-key-do-not-use-in-prod")

	srv := NewServer(
		core.ServerInfo{Name: "mrtr-signed-test", Version: "0.0.1"},
		WithRequestStateSigning(signingKey, time.Hour),
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "stateful",
			Description: "Confirms requestState validation",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if ctx.RequestState() == "" {
				return ctx.RequestInput(core.InputRequests{
					"confirm": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"Confirm?"}`),
					},
				})
			}
			// Dispatch already verified the token by the time we get here.
			return core.TextResult("state-ok"), nil
		},
	)

	c := connectMRTRClient(t, srv)

	r1, err := c.Call("tools/call", map[string]any{
		"name":      "stateful",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	var m1 map[string]any
	json.Unmarshal(r1.Raw, &m1)
	state1 := m1["requestState"].(string)
	if state1 == "" {
		t.Fatalf("expected non-empty requestState; raw=%s", r1.Raw)
	}
	// Signed tokens are "<sig>.<payload>" base64url; non-signed nonces have no '.'.
	if !strings.Contains(state1, ".") {
		t.Errorf("expected signed requestState (sig.payload form); got %q", state1)
	}

	// Verify the embedded payload with the same key/helper the server uses.
	embedded, err := core.VerifyMRTRState(signingKey, state1)
	if err != nil {
		t.Fatalf("VerifyMRTRState: %v (token=%q)", err, state1)
	}
	if embedded.Tool != "stateful" {
		t.Errorf("embedded.Tool = %q, want stateful", embedded.Tool)
	}

	// Round 2: echo signed state back — should be accepted.
	r2, err := c.Call("tools/call", map[string]any{
		"name":      "stateful",
		"arguments": map[string]any{},
		"inputResponses": map[string]any{
			"confirm": map[string]any{"action": "accept", "content": map[string]any{"ok": true}},
		},
		"requestState": state1,
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	var m2 map[string]any
	json.Unmarshal(r2.Raw, &m2)
	content, _ := m2["content"].([]any)
	if len(content) == 0 || !strings.Contains(content[0].(map[string]any)["text"].(string), "state-ok") {
		t.Errorf("round 2 should confirm state-ok; raw=%s", r2.Raw)
	}
}

// TestMRTR_TamperedRequestStateRejected verifies that a signed-mode server
// rejects a forged requestState with -32602. Without this, an attacker could
// drive the MRTR loop with arbitrary state.
func TestMRTR_TamperedRequestStateRejected(t *testing.T) {
	srv := NewServer(
		core.ServerInfo{Name: "mrtr-tamper-test", Version: "0.0.1"},
		WithRequestStateSigning([]byte("real-key"), time.Hour),
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "stateful",
			Description: "",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("ok"), nil
		},
	)

	c := connectMRTRClient(t, srv)

	// Forged token: well-formed MRTR shape but signed with a different key.
	forged, err := core.SignMRTRState([]byte("attacker-key"), core.MRTRRoundState{Tool: "stateful"}, time.Hour)
	if err != nil {
		t.Fatalf("SignMRTRState: %v", err)
	}

	_, callErr := c.Call("tools/call", map[string]any{
		"name":         "stateful",
		"arguments":    map[string]any{},
		"requestState": forged,
	})
	if callErr == nil {
		t.Fatal("expected tampered requestState to be rejected")
	}
	rpcErr, ok := callErr.(*client.RPCError)
	if !ok {
		t.Fatalf("expected *client.RPCError, got %T: %v", callErr, callErr)
	}
	if rpcErr.Code != core.ErrCodeInvalidParams {
		t.Errorf("code = %d, want %d (invalid params)", rpcErr.Code, core.ErrCodeInvalidParams)
	}
}

// TestMRTR_MultiRoundAccumulatesAnswers verifies the SEP-2322 multi-round
// flow: round 1 asks for step1, round 2 asks for step2 (with step1 carried
// over via requestState), round 3 returns complete with both answers
// available to the handler. The wire payload only ships the CURRENT round's
// inputResponses; dispatch carries the rest forward inside requestState.
// Mirrors the upstream conformance scenario A6.
func TestMRTR_MultiRoundAccumulatesAnswers(t *testing.T) {
	srv := NewServer(
		core.ServerInfo{Name: "mrtr-multi-round", Version: "0.0.1"},
		WithRequestStateSigning([]byte("multi-round-key"), time.Hour),
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_incomplete_result_multi_round",
			Description: "Two-step elicitation",
			InputSchema: map[string]any{"type": "object"},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if ctx.InputResponse("step1") == nil {
				return ctx.RequestInput(core.InputRequests{
					"step1": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"Step 1: name?"}`),
					},
				})
			}
			if ctx.InputResponse("step2") == nil {
				return ctx.RequestInput(core.InputRequests{
					"step2": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"Step 2: color?"}`),
					},
				})
			}
			// Both answered — handler can see step1 even though the
			// client only sent step2 in the latest round.
			var s1, s2 struct {
				Action  string         `json:"action"`
				Content map[string]any `json:"content"`
			}
			json.Unmarshal(ctx.InputResponse("step1"), &s1)
			json.Unmarshal(ctx.InputResponse("step2"), &s2)
			return core.TextResult(s1.Content["name"].(string) + " likes " + s2.Content["color"].(string)), nil
		},
	)

	c := connectMRTRClient(t, srv)

	// Round 1
	r1, err := c.Call("tools/call", map[string]any{
		"name":      "test_incomplete_result_multi_round",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	var m1 map[string]any
	json.Unmarshal(r1.Raw, &m1)
	if m1["resultType"] != "incomplete" {
		t.Fatalf("round 1 not incomplete; raw=%s", r1.Raw)
	}
	state1 := m1["requestState"].(string)

	// Round 2: send step1 only — server must forward it into next requestState
	r2, err := c.Call("tools/call", map[string]any{
		"name":      "test_incomplete_result_multi_round",
		"arguments": map[string]any{},
		"inputResponses": map[string]any{
			"step1": map[string]any{
				"action":  "accept",
				"content": map[string]any{"name": "Alice"},
			},
		},
		"requestState": state1,
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	var m2 map[string]any
	json.Unmarshal(r2.Raw, &m2)
	if m2["resultType"] != "incomplete" {
		t.Fatalf("round 2 not incomplete; raw=%s", r2.Raw)
	}
	state2 := m2["requestState"].(string)
	if state2 == state1 {
		t.Errorf("round 2 requestState should differ from round 1 (carries step1 answer)")
	}

	// Round 3: send step2 only — server must combine with carried-over step1
	r3, err := c.Call("tools/call", map[string]any{
		"name":      "test_incomplete_result_multi_round",
		"arguments": map[string]any{},
		"inputResponses": map[string]any{
			"step2": map[string]any{
				"action":  "accept",
				"content": map[string]any{"color": "blue"},
			},
		},
		"requestState": state2,
	})
	if err != nil {
		t.Fatalf("round 3: %v", err)
	}
	var m3 map[string]any
	json.Unmarshal(r3.Raw, &m3)
	if rt := m3["resultType"]; rt != "complete" && rt != nil {
		t.Errorf("round 3 resultType = %v, want \"complete\" (or absent); raw=%s", rt, r3.Raw)
	}
	content, ok := m3["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("round 3 missing content; raw=%s", r3.Raw)
	}
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Alice") || !strings.Contains(text, "blue") {
		t.Errorf("round 3 text = %q, want both \"Alice\" and \"blue\"", text)
	}
}

// TestMRTR_TaskComposition_Skipped is a tracking placeholder for MRTR↔Tasks
// composition (gather input via MRTR rounds, then return CreateTaskResult on
// the final round). SEP-2663 commit 451f5e1 (Apr 30) made this flow normative,
// but our taskV2Middleware (server/tasks_v2.go) creates the task BEFORE the
// handler runs, so the handler never gets to return IncompleteResult on
// round 1 — the middleware has already sent CreateTaskResult to the client.
//
// Resolving this is a real design choice (always-sync handler vs. handler-
// signalled async) tracked as mcpkit issue 347. Re-enable by deleting the
// t.Skip once that lands. A matching scenario lives skipped in the
// conformance suite (panyam/mcpconformance, branch feat/tasks-mrtr-extension,
// src/scenarios/server/mrtr/ephemeral-flow.ts:mrtr-tasks-composition).
func TestMRTR_TaskComposition_Skipped(t *testing.T) {
	t.Skip("MRTR→Tasks composition deferred — tracking: mcpkit issue 347")

	srv := NewServer(
		core.ServerInfo{Name: "mrtr-task-compose", Version: "0.0.1"},
		WithRequestStateSigning([]byte("compose-key"), time.Hour),
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "test_tool_with_task",
			Description: "Gather input then spin off a task",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			if ctx.InputResponse("input") == nil {
				return ctx.RequestInput(core.InputRequests{
					"input": core.InputRequest{
						Method: "elicitation/create",
						Params: json.RawMessage(`{"message":"Provide input"}`),
					},
				})
			}
			return core.TextResult("processing"), nil
		},
	)
	RegisterTasks(TasksConfig{Server: srv})

	c := connectMRTRClient(t, srv, client.WithTasksExtension())

	r1, err := c.Call("tools/call", map[string]any{
		"name":      "test_tool_with_task",
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("round 1: %v", err)
	}
	var m1 map[string]any
	json.Unmarshal(r1.Raw, &m1)
	if m1["resultType"] != "incomplete" {
		t.Fatalf("round 1 should be incomplete; raw=%s", r1.Raw)
	}

	r2, err := c.Call("tools/call", map[string]any{
		"name":      "test_tool_with_task",
		"arguments": map[string]any{},
		"inputResponses": map[string]any{
			"input": map[string]any{"action": "accept", "content": map[string]any{"value": "x"}},
		},
		"requestState": m1["requestState"],
	})
	if err != nil {
		t.Fatalf("round 2: %v", err)
	}
	var m2 map[string]any
	json.Unmarshal(r2.Raw, &m2)
	if m2["resultType"] != "task" {
		t.Errorf("round 2 resultType = %v, want \"task\"; raw=%s", m2["resultType"], r2.Raw)
	}
	if _, ok := m2["task"].(map[string]any); !ok {
		t.Errorf("round 2 missing task envelope; raw=%s", r2.Raw)
	}
}

// TestRequestStateSigning_SharedByMRTRAndTasks verifies that a server-wide
// WithRequestStateSigning is inherited by RegisterTasks when
// TasksConfig.RequestStateKey is left unset — production deployments
// should configure the HMAC key once and have BOTH ephemeral MRTR and
// SEP-2663 task requestStates signed with the same secret.
func TestRequestStateSigning_SharedByMRTRAndTasks(t *testing.T) {
	signingKey := []byte("shared-signing-key")

	srv := NewServer(
		core.ServerInfo{Name: "mrtr-shared-key", Version: "0.0.1"},
		WithRequestStateSigning(signingKey, time.Hour),
	)
	srv.RegisterTool(
		core.ToolDef{
			Name:        "fast-task",
			Description: "Async-eligible, completes immediately",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
			return core.TextResult("done"), nil
		},
	)
	// No RequestStateKey on the TasksConfig — should inherit from server-wide.
	RegisterTasks(TasksConfig{Server: srv})

	c := connectMRTRClient(t, srv, client.WithTasksExtension())

	// Drive task creation and pull tasks/get to inspect the requestState
	// the server minted; if inheritance worked, it's HMAC-signed (sig.payload),
	// otherwise it's the legacy plaintext taskID.
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
	getRes, err := c.Call("tasks/get", map[string]any{"taskId": ctr.TaskID})
	if err != nil {
		t.Fatalf("tasks/get: %v", err)
	}
	var dt core.DetailedTask
	json.Unmarshal(getRes.Raw, &dt)
	if dt.RequestState == "" {
		t.Fatal("DetailedTask.requestState empty; expected signed token")
	}
	if !strings.Contains(dt.RequestState, ".") {
		t.Errorf("DetailedTask.requestState = %q, want sig.payload form (server-wide signing didn't reach tasks)", dt.RequestState)
	}

	// And the same key must verify the token end-to-end via the public helper.
	if _, err := core.VerifyRequestState(signingKey, dt.RequestState); err != nil {
		t.Errorf("VerifyRequestState: %v (token=%q)", err, dt.RequestState)
	}
}

// connectMRTRClient is a one-off helper for MRTR tests — most existing v2
// fixtures register a full tasks setup we don't need here.
func connectMRTRClient(t *testing.T, srv *Server, opts ...client.ClientOption) *client.Client {
	t.Helper()
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "mrtr-test", Version: "0.0.1"}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
