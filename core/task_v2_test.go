package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestResultTypeConstants verifies the wire values of the ResultType
// discriminator. "task" is SEP-2557; "complete"/"incomplete" are SEP-2322 (MRTR).
func TestResultTypeConstants(t *testing.T) {
	cases := []struct {
		rt   ResultType
		want string
	}{
		{ResultTypeTask, "task"},
		{ResultTypeComplete, "complete"},
		{ResultTypeIncomplete, "incomplete"},
	}
	for _, tc := range cases {
		if string(tc.rt) != tc.want {
			t.Errorf("ResultType(%v) = %q, want %q", tc.rt, string(tc.rt), tc.want)
		}
		// JSON round-trip as a bare string.
		data, err := json.Marshal(tc.rt)
		if err != nil {
			t.Fatalf("Marshal(%q): %v", tc.rt, err)
		}
		var got ResultType
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", data, err)
		}
		if got != tc.rt {
			t.Errorf("round-trip: got %q, want %q", got, tc.rt)
		}
	}
}

// TestInputRequestJSON verifies wire shape: {"method": "...", "params": {...}}
// with params omitted when nil. Round-trip must preserve raw params bytes.
func TestInputRequestJSON(t *testing.T) {
	params := json.RawMessage(`{"message":"Confirm?","schema":{"type":"object"}}`)
	req := InputRequest{
		Method: "elicitation/create",
		Params: params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["method"] != "elicitation/create" {
		t.Errorf("method = %v, want elicitation/create", m["method"])
	}
	if _, ok := m["params"]; !ok {
		t.Error("params missing from JSON output")
	}

	var decoded InputRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Method != req.Method {
		t.Errorf("Method = %q, want %q", decoded.Method, req.Method)
	}
	// json.RawMessage round-trips as raw bytes; compact equivalence is enough
	// because the encoder may re-format whitespace.
	var orig, got any
	_ = json.Unmarshal(params, &orig)
	_ = json.Unmarshal(decoded.Params, &got)
	origBytes, _ := json.Marshal(orig)
	gotBytes, _ := json.Marshal(got)
	if string(origBytes) != string(gotBytes) {
		t.Errorf("Params mismatch: got %s, want %s", gotBytes, origBytes)
	}
}

// TestInputRequestOmitEmptyParams verifies that an InputRequest with nil
// params omits the field entirely (not "params":null).
func TestInputRequestOmitEmptyParams(t *testing.T) {
	req := InputRequest{Method: "ping"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["params"]; ok {
		t.Errorf("params should be omitted when nil; got %s", data)
	}
}

// TestInputRequestsMapJSON verifies that InputRequests (alias for
// map[string]InputRequest) marshals as a JSON object keyed by request id.
func TestInputRequestsMapJSON(t *testing.T) {
	reqs := InputRequests{
		"elicit-1": InputRequest{Method: "elicitation/create", Params: json.RawMessage(`{"message":"ok?"}`)},
		"sample-1": InputRequest{Method: "sampling/createMessage", Params: json.RawMessage(`{"maxTokens":50}`)},
	}
	data, err := json.Marshal(reqs)
	if err != nil {
		t.Fatal(err)
	}
	var decoded InputRequests
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("len = %d, want 2", len(decoded))
	}
	if decoded["elicit-1"].Method != "elicitation/create" {
		t.Errorf("elicit-1.method = %q, want elicitation/create", decoded["elicit-1"].Method)
	}
	if decoded["sample-1"].Method != "sampling/createMessage" {
		t.Errorf("sample-1.method = %q, want sampling/createMessage", decoded["sample-1"].Method)
	}
}

// TestInputResponsesMapJSON verifies that InputResponses (alias for
// map[string]json.RawMessage) preserves opaque payload bytes per key.
func TestInputResponsesMapJSON(t *testing.T) {
	resps := InputResponses{
		"elicit-1": json.RawMessage(`{"action":"accept","content":{"confirmed":true}}`),
		"sample-1": json.RawMessage(`{"role":"assistant","content":{"type":"text","text":"hello"}}`),
	}
	data, err := json.Marshal(resps)
	if err != nil {
		t.Fatal(err)
	}
	var decoded InputResponses
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("len = %d, want 2", len(decoded))
	}
	var v map[string]any
	if err := json.Unmarshal(decoded["elicit-1"], &v); err != nil {
		t.Fatalf("elicit-1 not valid JSON: %v", err)
	}
	if v["action"] != "accept" {
		t.Errorf("elicit-1.action = %v, want accept", v["action"])
	}
}

// TestDetailedTaskInputRequestsTyped verifies the InputRequests field on
// DetailedTask (the SEP-2663 input_required variant) marshals to the
// SEP-2322 wire shape and round-trips through the typed alias.
func TestDetailedTaskInputRequestsTyped(t *testing.T) {
	res := DetailedTask{
		TaskInfoV2: TaskInfoV2{
			TaskID:        "task-mrtr",
			Status:        TaskInputRequired,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:01Z",
			TTLSeconds:    IntPtr(60),
		},
		InputRequests: InputRequests{
			"elicit-1": InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"Proceed?"}`),
			},
		},
		RequestState: "opaque-state-token",
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}

	// Wire shape: inputRequests is a JSON object keyed by request id; each
	// entry has method + params.
	var m map[string]any
	json.Unmarshal(data, &m)
	irs, ok := m["inputRequests"].(map[string]any)
	if !ok {
		t.Fatalf("inputRequests missing or wrong type; got %s", data)
	}
	entry, ok := irs["elicit-1"].(map[string]any)
	if !ok {
		t.Fatal("inputRequests[elicit-1] missing")
	}
	if entry["method"] != "elicitation/create" {
		t.Errorf("inputRequests[elicit-1].method = %v, want elicitation/create", entry["method"])
	}
	if m["requestState"] != "opaque-state-token" {
		t.Errorf("requestState = %v, want opaque-state-token", m["requestState"])
	}

	// Typed round-trip preserves InputRequest.Method through GetTaskResult,
	// which is an alias for DetailedTask.
	var decoded GetTaskResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded.InputRequests["elicit-1"].Method; got != "elicitation/create" {
		t.Errorf("decoded method = %q, want elicitation/create", got)
	}
}

// TestDetailedTaskRequestStateOmitEmpty verifies that RequestState is omitted
// when empty on DetailedTask (it's optional per SEP-2322).
func TestDetailedTaskRequestStateOmitEmpty(t *testing.T) {
	res := DetailedTask{
		TaskInfoV2: TaskInfoV2{
			TaskID: "t1", Status: TaskWorking,
			CreatedAt: "2025-01-15T10:00:00Z", LastUpdatedAt: "2025-01-15T10:00:00Z",
			TTLSeconds: IntPtr(30),
		},
	}
	data, _ := json.Marshal(res)
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["requestState"]; ok {
		t.Errorf("DetailedTask: requestState should be omitted when empty; got %s", data)
	}
}

// --- SEP-2663 wire-shape tests ---

// TestTaskInfoV2WireFields verifies the renamed wire fields (ttlSeconds,
// pollIntervalMilliseconds) and the absence of a parentTaskId field.
func TestTaskInfoV2WireFields(t *testing.T) {
	info := TaskInfoV2{
		TaskID:                   "task-123",
		Status:                   TaskWorking,
		CreatedAt:                "2025-01-15T10:00:00Z",
		LastUpdatedAt:            "2025-01-15T10:00:01Z",
		TTLSeconds:               IntPtr(300),
		PollIntervalMilliseconds: IntPtr(1000),
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	for _, key := range []string{"taskId", "status", "createdAt", "lastUpdatedAt", "ttlSeconds", "pollIntervalMilliseconds"} {
		if _, ok := m[key]; !ok {
			t.Errorf("TaskInfoV2 missing %q; got %s", key, data)
		}
	}
	// Old v1 wire keys must be absent.
	for _, key := range []string{"ttl", "pollInterval", "parentTaskId"} {
		if _, ok := m[key]; ok {
			t.Errorf("TaskInfoV2 should not emit v1 key %q; got %s", key, data)
		}
	}
	if m["ttlSeconds"] != float64(300) {
		t.Errorf("ttlSeconds = %v, want 300", m["ttlSeconds"])
	}
	if m["pollIntervalMilliseconds"] != float64(1000) {
		t.Errorf("pollIntervalMilliseconds = %v, want 1000", m["pollIntervalMilliseconds"])
	}
}

// TestTaskInfoV2TTLSecondsNullable verifies that ttlSeconds is required-but-
// nullable (present as null when nil). Mirrors v1 TaskInfo.TTL semantics.
func TestTaskInfoV2TTLSecondsNullable(t *testing.T) {
	info := TaskInfoV2{
		TaskID:        "t1",
		Status:        TaskWorking,
		CreatedAt:     "2025-01-15T10:00:00Z",
		LastUpdatedAt: "2025-01-15T10:00:00Z",
		TTLSeconds:    nil,
	}
	data, _ := json.Marshal(info)
	var m map[string]any
	json.Unmarshal(data, &m)
	val, ok := m["ttlSeconds"]
	if !ok {
		t.Fatalf("ttlSeconds must be present even when nil; got %s", data)
	}
	if val != nil {
		t.Errorf("ttlSeconds = %v, want null", val)
	}
}

// TestCreateTaskResultWireShape verifies the SEP-2663 wire shape:
// {"resultType": "task", "taskId": "...", "status": "...", ...} — a flat
// intersection of Result and Task. No nested "task" wrapper key (the v0/v1
// shape that SEP-2663 dropped). No result / error / inputRequests /
// requestState — those belong on tasks/get's DetailedTask.
func TestCreateTaskResultWireShape(t *testing.T) {
	res := CreateTaskResult{
		ResultType: ResultTypeTask,
		TaskInfoV2: TaskInfoV2{
			TaskID:                   "task-abc",
			Status:                   TaskWorking,
			CreatedAt:                "2025-01-15T10:00:00Z",
			LastUpdatedAt:            "2025-01-15T10:00:00Z",
			TTLSeconds:               IntPtr(60),
			PollIntervalMilliseconds: IntPtr(1000),
		},
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	if m["resultType"] != "task" {
		t.Errorf("resultType = %v, want task", m["resultType"])
	}
	// SEP-2663: the task fields are at the top level alongside resultType;
	// no "task" wrapper key.
	if _, ok := m["task"]; ok {
		t.Errorf("SEP-2663 CreateTaskResult must not nest under a \"task\" wrapper; got %s", data)
	}
	for _, key := range []string{"taskId", "status", "createdAt", "lastUpdatedAt", "ttlSeconds", "pollIntervalMilliseconds"} {
		if _, ok := m[key]; !ok {
			t.Errorf("CreateTaskResult missing top-level %q; got %s", key, data)
		}
	}
	if m["taskId"] != "task-abc" {
		t.Errorf("taskId = %v, want task-abc", m["taskId"])
	}
	for _, forbidden := range []string{"result", "error", "inputRequests", "requestState"} {
		if _, ok := m[forbidden]; ok {
			t.Errorf("CreateTaskResult must not carry %q (SEP-2663); got %s", forbidden, data)
		}
	}

	// Round-trip: flat JSON must decode back into the embedded TaskInfoV2.
	var decoded CreateTaskResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if decoded.ResultType != ResultTypeTask {
		t.Errorf("decoded.ResultType = %q, want %q", decoded.ResultType, ResultTypeTask)
	}
	if decoded.TaskID != "task-abc" {
		t.Errorf("decoded.TaskID = %q, want task-abc", decoded.TaskID)
	}
	if decoded.TTLSeconds == nil || *decoded.TTLSeconds != 60 {
		t.Errorf("decoded.TTLSeconds = %v, want *60", decoded.TTLSeconds)
	}
	if decoded.PollIntervalMilliseconds == nil || *decoded.PollIntervalMilliseconds != 1000 {
		t.Errorf("decoded.PollIntervalMilliseconds = %v, want *1000", decoded.PollIntervalMilliseconds)
	}
}

// TestDetailedTaskCompletedShape verifies the wire shape for the completed
// variant: status + result inlined, no error/inputRequests.
func TestDetailedTaskCompletedShape(t *testing.T) {
	res := CompletedTask{
		TaskInfoV2: TaskInfoV2{
			TaskID:        "t1",
			Status:        TaskCompleted,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:05Z",
			TTLSeconds:    IntPtr(60),
		},
		Result: &ToolResult{Content: []Content{{Type: "text", Text: "done"}}},
	}
	data, _ := json.Marshal(res)
	var m map[string]any
	json.Unmarshal(data, &m)

	if m["status"] != "completed" {
		t.Errorf("status = %v, want completed", m["status"])
	}
	if _, ok := m["result"]; !ok {
		t.Errorf("result must be inlined when status=completed; got %s", data)
	}
	for _, absent := range []string{"error", "inputRequests"} {
		if _, ok := m[absent]; ok {
			t.Errorf("completed task should omit %q; got %s", absent, data)
		}
	}
}

// TestDetailedTaskFailedShape verifies the wire shape for the failed variant:
// status + error inlined (JSON-RPC error shape), no result/inputRequests.
func TestDetailedTaskFailedShape(t *testing.T) {
	res := FailedTask{
		TaskInfoV2: TaskInfoV2{
			TaskID:        "t1",
			Status:        TaskFailed,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:05Z",
			TTLSeconds:    IntPtr(60),
		},
		Error: &TaskError{Code: -32603, Message: "internal error"},
	}
	data, _ := json.Marshal(res)
	var m map[string]any
	json.Unmarshal(data, &m)

	if m["status"] != "failed" {
		t.Errorf("status = %v, want failed", m["status"])
	}
	errMap, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("error must be inlined when status=failed; got %s", data)
	}
	if errMap["code"] != float64(-32603) || errMap["message"] != "internal error" {
		t.Errorf("error shape mismatch: got %v", errMap)
	}
	for _, absent := range []string{"result", "inputRequests"} {
		if _, ok := m[absent]; ok {
			t.Errorf("failed task should omit %q; got %s", absent, data)
		}
	}
}

// TestUpdateTaskRequestWireShape verifies the SEP-2663 tasks/update params:
// {taskId, inputResponses, requestState}. inputResponses preserves opaque
// per-key payloads (json.RawMessage).
func TestUpdateTaskRequestWireShape(t *testing.T) {
	req := UpdateTaskRequest{
		TaskID: "task-xyz",
		InputResponses: InputResponses{
			"elicit-1": json.RawMessage(`{"action":"accept","content":{"ok":true}}`),
		},
		RequestState: "state-1",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	if m["taskId"] != "task-xyz" {
		t.Errorf("taskId = %v, want task-xyz", m["taskId"])
	}
	if m["requestState"] != "state-1" {
		t.Errorf("requestState = %v, want state-1", m["requestState"])
	}
	resps, ok := m["inputResponses"].(map[string]any)
	if !ok {
		t.Fatalf("inputResponses missing or wrong shape; got %s", data)
	}
	entry, ok := resps["elicit-1"].(map[string]any)
	if !ok {
		t.Fatalf("inputResponses[elicit-1] missing; got %s", data)
	}
	if entry["action"] != "accept" {
		t.Errorf("inputResponses[elicit-1].action = %v, want accept", entry["action"])
	}

	// Round-trip preserves raw bytes per key.
	var decoded UpdateTaskRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TaskID != req.TaskID {
		t.Errorf("TaskID = %q, want %q", decoded.TaskID, req.TaskID)
	}
	if string(decoded.InputResponses["elicit-1"]) == "" {
		t.Error("decoded InputResponses[elicit-1] is empty")
	}
}

// TestUpdateTaskRequestOmitEmpty verifies that inputResponses and requestState
// are omitted when empty/zero (only taskId is required).
func TestUpdateTaskRequestOmitEmpty(t *testing.T) {
	req := UpdateTaskRequest{TaskID: "t1"}
	data, _ := json.Marshal(req)
	var m map[string]any
	json.Unmarshal(data, &m)
	for _, absent := range []string{"inputResponses", "requestState"} {
		if _, ok := m[absent]; ok {
			t.Errorf("%q should be omitted when empty; got %s", absent, data)
		}
	}
}

// TestIncompleteResultWireShape verifies the SEP-2322 ephemeral wire shape:
// {"resultType": "incomplete", "inputRequests": {...}, "requestState": "..."}.
// resultType is camelCase like every other MCP wire field — Luca confirmed
// camelCase is the SEP-2322 spec standard.
func TestIncompleteResultWireShape(t *testing.T) {
	res := IncompleteResult{
		InputRequests: InputRequests{
			"user_name": InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"What is your name?"}`),
			},
		},
		RequestState: "opaque-state",
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if m["resultType"] != "incomplete" {
		t.Errorf("resultType = %v, want \"incomplete\"; got %s", m["resultType"], data)
	}
	if _, ok := m["result_type"]; ok {
		t.Errorf("snake_case result_type must NOT appear (camelCase is the wire field); got %s", data)
	}
	if m["requestState"] != "opaque-state" {
		t.Errorf("requestState = %v, want opaque-state", m["requestState"])
	}
	reqs, ok := m["inputRequests"].(map[string]any)
	if !ok {
		t.Fatalf("inputRequests missing or wrong shape; got %s", data)
	}
	entry, ok := reqs["user_name"].(map[string]any)
	if !ok {
		t.Fatalf("inputRequests[user_name] missing; got %s", data)
	}
	if entry["method"] != "elicitation/create" {
		t.Errorf("inputRequests[user_name].method = %v, want elicitation/create", entry["method"])
	}
}

// TestIncompleteResultDefaultsResultType verifies that a zero-value
// IncompleteResult marshals with resultType = "incomplete" so handlers
// can build IncompleteResult{InputRequests: ...} without setting the
// discriminator manually.
func TestIncompleteResultDefaultsResultType(t *testing.T) {
	data, err := json.Marshal(IncompleteResult{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if m["resultType"] != "incomplete" {
		t.Errorf("zero-value IncompleteResult.resultType = %v, want \"incomplete\"; got %s",
			m["resultType"], data)
	}
}

// TestAckShapes verifies that UpdateTaskResult and CancelTaskResult serialize
// to the SEP-2322 minimum: a single resultType:"complete" discriminator and
// no other fields. SEP-2663 says the acks carry no task state — but SEP-2322
// requires the resultType discriminator on every non-task response so
// clients can dispatch sync/task/multi-round uniformly.
func TestAckShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
	}{
		{"UpdateTaskResult", UpdateTaskResult{}},
		{"CancelTaskResult", CancelTaskResult{}},
	} {
		data, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		if got := m["resultType"]; got != "complete" {
			t.Errorf("%s.resultType = %v, want \"complete\"", tc.name, got)
		}
		if len(m) != 1 {
			t.Errorf("%s should carry only resultType (got %d keys: %v)", tc.name, len(m), m)
		}
	}
}

// --- SEP-2322 requestState signing (gap-3) ---

// TestRequestState_RoundTrip verifies a freshly signed token verifies cleanly
// and exposes the original taskID. The base case for the HMAC flow.
func TestRequestState_RoundTrip(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	state := SignRequestState(key, "task-abc", time.Hour)
	if state == "" {
		t.Fatal("SignRequestState returned empty string")
	}
	got, err := VerifyRequestState(key, state)
	if err != nil {
		t.Fatalf("VerifyRequestState: %v", err)
	}
	if got != "task-abc" {
		t.Errorf("decoded taskID = %q, want %q", got, "task-abc")
	}
}

// TestRequestState_TamperedPayload verifies that any modification to the
// payload bytes (even a single character flip) invalidates the signature.
// This is the core attacker scenario the HMAC defends against.
func TestRequestState_TamperedPayload(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	state := SignRequestState(key, "task-abc", time.Hour)
	dot := strings.IndexByte(state, '.')
	if dot < 0 {
		t.Fatalf("expected '.' separator in signed state %q", state)
	}
	// Re-encode a different payload (taskId swap) but keep the original sig.
	tampered := state[:dot+1] + base64.RawURLEncoding.EncodeToString([]byte(`{"taskId":"task-evil","exp":9999999999}`))
	if _, err := VerifyRequestState(key, tampered); err != ErrRequestStateInvalidSignature {
		t.Errorf("tampered payload: err = %v, want ErrRequestStateInvalidSignature", err)
	}
}

// TestRequestState_TamperedSignature verifies that a flipped signature byte
// (with the original payload intact) is also rejected. Symmetric with the
// payload-tamper case.
func TestRequestState_TamperedSignature(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	state := SignRequestState(key, "task-abc", time.Hour)
	dot := strings.IndexByte(state, '.')
	if dot < 0 {
		t.Fatalf("expected '.' separator")
	}
	// Flip a byte inside the signature (replace first char with one that
	// decodes to different bytes).
	sigChar := byte('A')
	if state[0] == 'A' {
		sigChar = 'B'
	}
	tampered := string(sigChar) + state[1:]
	if _, err := VerifyRequestState(key, tampered); err != ErrRequestStateInvalidSignature {
		t.Errorf("tampered signature: err = %v, want ErrRequestStateInvalidSignature", err)
	}
}

// TestRequestState_Expired verifies that a token whose exp is in the past
// is rejected even when the signature is valid.
func TestRequestState_Expired(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	// Negative TTL = exp in the past.
	state := SignRequestState(key, "task-abc", -time.Second)
	if _, err := VerifyRequestState(key, state); err != ErrRequestStateExpired {
		t.Errorf("expired state: err = %v, want ErrRequestStateExpired", err)
	}
}

// TestRequestState_Malformed batches the structural-failure cases so a
// single test guards every parse path that should map to ErrRequestStateMalformed.
func TestRequestState_Malformed(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	cases := []struct {
		name  string
		state string
	}{
		{"missing separator", "no-dot-here-just-text"},
		{"bad signature base64", "!!!!.eyJ0YXNrSWQiOiJ0YXNrLWFiYyIsImV4cCI6MX0"},
		{"bad payload base64", "abc.!!!!"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyRequestState(key, tc.state); err != ErrRequestStateMalformed {
				t.Errorf("err = %v, want ErrRequestStateMalformed", err)
			}
		})
	}
}

// TestRequestState_WrongKey verifies that a token signed with one key can't
// be verified with a different key — covers the multi-tenant case where
// each tenant has its own signing key.
func TestRequestState_WrongKey(t *testing.T) {
	keyA := []byte("tenant-a-key-32-bytes-padding-here")
	keyB := []byte("tenant-b-key-32-bytes-padding-here")
	state := SignRequestState(keyA, "task-abc", time.Hour)
	if _, err := VerifyRequestState(keyB, state); err != ErrRequestStateInvalidSignature {
		t.Errorf("wrong key: err = %v, want ErrRequestStateInvalidSignature", err)
	}
}

// TestRequestState_BadJSONPayload verifies that a payload that survives
// base64 + HMAC but doesn't parse as the expected JSON shape is reported
// as malformed (not invalid-signature).
func TestRequestState_BadJSONPayload(t *testing.T) {
	key := []byte("test-key-32-bytes-min-for-hmac-shake")
	rawPayload := []byte("not-json-at-all")
	mac := hmacFromKey(t, key, rawPayload)
	state := base64.RawURLEncoding.EncodeToString(mac) + "." +
		base64.RawURLEncoding.EncodeToString(rawPayload)
	if _, err := VerifyRequestState(key, state); err != ErrRequestStateMalformed {
		t.Errorf("bad JSON payload: err = %v, want ErrRequestStateMalformed", err)
	}
}

// hmacFromKey is a tiny test helper to compute an HMAC-SHA256 sig over an
// arbitrary payload — used by TestRequestState_BadJSONPayload to forge a
// signature-valid-but-payload-broken token.
func hmacFromKey(t *testing.T, key, payload []byte) []byte {
	t.Helper()
	h := hmac.New(sha256.New, key)
	h.Write(payload)
	return h.Sum(nil)
}
