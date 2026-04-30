package core

import (
	"encoding/json"
	"testing"
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
// {"resultType": "task", "task": {...}} with no extra fields (no result,
// no error, no inputRequests, no requestState — all forbidden on
// CreateTaskResult per SEP-2663).
func TestCreateTaskResultWireShape(t *testing.T) {
	res := CreateTaskResult{
		ResultType: ResultTypeTask,
		Task: TaskInfoV2{
			TaskID:        "task-abc",
			Status:        TaskWorking,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:00Z",
			TTLSeconds:    IntPtr(60),
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
	if _, ok := m["task"]; !ok {
		t.Fatalf("task missing; got %s", data)
	}
	for _, forbidden := range []string{"result", "error", "inputRequests", "requestState"} {
		if _, ok := m[forbidden]; ok {
			t.Errorf("CreateTaskResult must not carry %q (SEP-2663); got %s", forbidden, data)
		}
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

// TestEmptyAckShapes verifies that UpdateTaskResult and CancelTaskResult
// serialize as empty JSON objects (no task state, per SEP-2663).
func TestEmptyAckShapes(t *testing.T) {
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
		if string(data) != "{}" {
			t.Errorf("%s = %s, want {}", tc.name, data)
		}
	}
}
