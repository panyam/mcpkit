package core

import (
	"encoding/json"
	"testing"
)

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
			TTLMs:         IntPtr(60),
		},
		InputRequests: InputRequests{
			"elicit-1": InputRequest{
				Method: "elicitation/create",
				Params: json.RawMessage(`{"message":"Proceed?"}`),
			},
		},
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

// TestDetailedTaskNoRequestState verifies that DetailedTask never emits a
// `requestState` JSON field — the merged SEP-2663 removed the field from
// the tasks-v2 wire entirely. (MRTR's InputRequiredResult.RequestState
// remains on a different surface; see TestInputRequiredResult in mrtr_test.go.)
func TestDetailedTaskNoRequestState(t *testing.T) {
	res := DetailedTask{
		TaskInfoV2: TaskInfoV2{
			TaskID: "t1", Status: TaskWorking,
			CreatedAt: "2025-01-15T10:00:00Z", LastUpdatedAt: "2025-01-15T10:00:00Z",
			TTLMs: IntPtr(30),
		},
	}
	data, _ := json.Marshal(res)
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["requestState"]; ok {
		t.Errorf("DetailedTask: requestState MUST NOT appear on the tasks-v2 wire; got %s", data)
	}
}

// --- SEP-2663 wire-shape tests ---

// TestTaskInfoV2WireFields verifies the renamed wire fields (ttlMs,
// pollIntervalMs) and the absence of a parentTaskId field.
func TestTaskInfoV2WireFields(t *testing.T) {
	info := TaskInfoV2{
		TaskID:         "task-123",
		Status:         TaskWorking,
		CreatedAt:      "2025-01-15T10:00:00Z",
		LastUpdatedAt:  "2025-01-15T10:00:01Z",
		TTLMs:          IntPtr(300),
		PollIntervalMs: IntPtr(1000),
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	for _, key := range []string{"taskId", "status", "createdAt", "lastUpdatedAt", "ttlMs", "pollIntervalMs"} {
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
	if m["ttlMs"] != float64(300) {
		t.Errorf("ttlMs = %v, want 300", m["ttlMs"])
	}
	if m["pollIntervalMs"] != float64(1000) {
		t.Errorf("pollIntervalMs = %v, want 1000", m["pollIntervalMs"])
	}
}

// TestTaskInfoV2TTLMsNullable verifies that ttlMs is required-but-
// nullable (present as null when nil). Mirrors v1 TaskInfo.TTL semantics.
func TestTaskInfoV2TTLMsNullable(t *testing.T) {
	info := TaskInfoV2{
		TaskID:        "t1",
		Status:        TaskWorking,
		CreatedAt:     "2025-01-15T10:00:00Z",
		LastUpdatedAt: "2025-01-15T10:00:00Z",
		TTLMs:         nil,
	}
	data, _ := json.Marshal(info)
	var m map[string]any
	json.Unmarshal(data, &m)
	val, ok := m["ttlMs"]
	if !ok {
		t.Fatalf("ttlMs must be present even when nil; got %s", data)
	}
	if val != nil {
		t.Errorf("ttlMs = %v, want null", val)
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
			TaskID:         "task-abc",
			Status:         TaskWorking,
			CreatedAt:      "2025-01-15T10:00:00Z",
			LastUpdatedAt:  "2025-01-15T10:00:00Z",
			TTLMs:          IntPtr(60),
			PollIntervalMs: IntPtr(1000),
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
	for _, key := range []string{"taskId", "status", "createdAt", "lastUpdatedAt", "ttlMs", "pollIntervalMs"} {
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
	if decoded.TTLMs == nil || *decoded.TTLMs != 60 {
		t.Errorf("decoded.TTLMs = %v, want *60", decoded.TTLMs)
	}
	if decoded.PollIntervalMs == nil || *decoded.PollIntervalMs != 1000 {
		t.Errorf("decoded.PollIntervalMs = %v, want *1000", decoded.PollIntervalMs)
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
			TTLMs:         IntPtr(60),
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
			TTLMs:         IntPtr(60),
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
// {taskId, inputResponses}. inputResponses preserves opaque per-key payloads
// (json.RawMessage). The merged spec removed requestState from this shape.
func TestUpdateTaskRequestWireShape(t *testing.T) {
	req := UpdateTaskRequest{
		TaskID: "task-xyz",
		InputResponses: InputResponses{
			"elicit-1": json.RawMessage(`{"action":"accept","content":{"ok":true}}`),
		},
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
	if _, ok := m["requestState"]; ok {
		t.Errorf("requestState MUST NOT appear on UpdateTaskRequest (removed by merged SEP-2663); got %s", data)
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

// TestUpdateTaskRequestOmitEmpty verifies that inputResponses is omitted
// when empty (only taskId is required).
func TestUpdateTaskRequestOmitEmpty(t *testing.T) {
	req := UpdateTaskRequest{TaskID: "t1"}
	data, _ := json.Marshal(req)
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["inputResponses"]; ok {
		t.Errorf("inputResponses should be omitted when empty; got %s", data)
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
