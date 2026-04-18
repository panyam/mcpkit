package core

import (
	"encoding/json"
	"testing"
)

// TestTaskStatusIsTerminal verifies the terminal/non-terminal classification
// of all five task lifecycle states.
func TestTaskStatusIsTerminal(t *testing.T) {
	cases := []struct {
		status   TaskStatus
		terminal bool
	}{
		{TaskWorking, false},
		{TaskInputRequired, false},
		{TaskCompleted, true},
		{TaskFailed, true},
		{TaskCancelled, true},
	}
	for _, tc := range cases {
		if got := tc.status.IsTerminal(); got != tc.terminal {
			t.Errorf("TaskStatus(%q).IsTerminal() = %v, want %v", tc.status, got, tc.terminal)
		}
	}
}

// TestTaskInfoJSON verifies that TaskInfo serializes to the camelCase wire
// format expected by the MCP spec and TypeScript SDK.
func TestTaskInfoJSON(t *testing.T) {
	info := TaskInfo{
		TaskID:        "task-123",
		Status:        TaskWorking,
		StatusMessage: "processing",
		CreatedAt:     "2025-01-15T10:00:00Z",
		LastUpdatedAt: "2025-01-15T10:00:01Z",
		TTL:           300000,
		PollInterval:  1000,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}

	// Verify camelCase field names (TS SDK compatibility).
	var m map[string]any
	json.Unmarshal(data, &m)

	mustHave := []string{"taskId", "status", "statusMessage", "createdAt", "lastUpdatedAt", "ttl", "pollInterval"}
	for _, key := range mustHave {
		if _, ok := m[key]; !ok {
			t.Errorf("TaskInfo JSON missing field %q; got keys: %v", key, keys(m))
		}
	}

	// Verify round-trip.
	var decoded TaskInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TaskID != info.TaskID {
		t.Errorf("TaskID = %q, want %q", decoded.TaskID, info.TaskID)
	}
	if decoded.Status != TaskWorking {
		t.Errorf("Status = %q, want %q", decoded.Status, TaskWorking)
	}
	if decoded.TTL != 300000 {
		t.Errorf("TTL = %d, want 300000", decoded.TTL)
	}
}

// TestTaskInfoOmitEmpty verifies that optional fields are omitted when zero-valued.
func TestTaskInfoOmitEmpty(t *testing.T) {
	info := TaskInfo{
		TaskID:        "task-1",
		Status:        TaskCompleted,
		CreatedAt:     "2025-01-15T10:00:00Z",
		LastUpdatedAt: "2025-01-15T10:00:05Z",
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	for _, absent := range []string{"statusMessage", "ttl", "pollInterval"} {
		if _, ok := m[absent]; ok {
			t.Errorf("expected field %q to be omitted, but it was present", absent)
		}
	}
}

// TestCreateTaskResultJSON verifies the CreateTaskResult envelope matches
// the spec wire format: {"task": {...}}.
func TestCreateTaskResultJSON(t *testing.T) {
	result := CreateTaskResult{
		Task: TaskInfo{
			TaskID:        "task-abc",
			Status:        TaskWorking,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:00Z",
			PollInterval:  1000,
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	task, ok := m["task"]
	if !ok {
		t.Fatal("CreateTaskResult JSON missing 'task' field")
	}
	taskMap := task.(map[string]any)
	if taskMap["taskId"] != "task-abc" {
		t.Errorf("task.taskId = %v, want task-abc", taskMap["taskId"])
	}
}

// TestToolDefWithExecution verifies that ToolDef serializes the Execution
// field as {"execution":{"taskSupport":"optional"}}.
func TestToolDefWithExecution(t *testing.T) {
	def := ToolDef{
		Name:        "my-tool",
		Description: "a tool",
		InputSchema: map[string]any{"type": "object"},
		Execution:   &ToolExecution{TaskSupport: TaskSupportOptional},
	}
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	exec, ok := m["execution"]
	if !ok {
		t.Fatal("ToolDef JSON missing 'execution' field")
	}
	execMap := exec.(map[string]any)
	if execMap["taskSupport"] != "optional" {
		t.Errorf("execution.taskSupport = %v, want 'optional'", execMap["taskSupport"])
	}
}

// TestToolDefWithoutExecution verifies that the execution field is omitted
// when nil (backward compatibility — existing tools unaffected).
func TestToolDefWithoutExecution(t *testing.T) {
	def := ToolDef{
		Name:        "plain-tool",
		Description: "no tasks",
		InputSchema: map[string]any{"type": "object"},
	}
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["execution"]; ok {
		t.Error("ToolDef without Execution should omit the field")
	}
}

// TestServerCapabilitiesWithTasks verifies that TasksCap is included in
// ServerCapabilities when set, and omitted when nil.
func TestServerCapabilitiesWithTasks(t *testing.T) {
	caps := ServerCapabilities{
		Tools: &ToolsCap{ListChanged: true},
		Tasks: &TasksCap{
			Requests: map[string]struct{}{"tools/call": {}},
		},
	}
	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	tasks, ok := m["tasks"]
	if !ok {
		t.Fatal("ServerCapabilities JSON missing 'tasks' field")
	}
	tasksMap := tasks.(map[string]any)
	reqs, ok := tasksMap["requests"]
	if !ok {
		t.Fatal("tasks missing 'requests' field")
	}
	reqsMap := reqs.(map[string]any)
	if _, ok := reqsMap["tools/call"]; !ok {
		t.Error("tasks.requests missing 'tools/call'")
	}

	// Without tasks — should be omitted.
	capsNoTasks := ServerCapabilities{Tools: &ToolsCap{}}
	data2, _ := json.Marshal(capsNoTasks)
	var m2 map[string]any
	json.Unmarshal(data2, &m2)
	if _, ok := m2["tasks"]; ok {
		t.Error("ServerCapabilities without Tasks should omit the field")
	}
}

// TestClientCapabilitiesWithTasks verifies ClientTasksCap serialization.
func TestClientCapabilitiesWithTasks(t *testing.T) {
	caps := ClientCapabilities{
		Sampling: &struct{}{},
		Tasks:    &ClientTasksCap{Requests: map[string]struct{}{"tools/call": {}}},
	}
	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	if _, ok := m["tasks"]; !ok {
		t.Fatal("ClientCapabilities JSON missing 'tasks' field")
	}
}

// TestListTasksResultJSON verifies pagination envelope.
func TestListTasksResultJSON(t *testing.T) {
	result := ListTasksResult{
		Tasks: []TaskInfo{
			{TaskID: "t1", Status: TaskWorking, CreatedAt: "2025-01-15T10:00:00Z", LastUpdatedAt: "2025-01-15T10:00:00Z"},
		},
		NextCursor: "cursor-2",
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ListTasksResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Tasks) != 1 {
		t.Fatalf("len(Tasks) = %d, want 1", len(decoded.Tasks))
	}
	if decoded.NextCursor != "cursor-2" {
		t.Errorf("NextCursor = %q, want cursor-2", decoded.NextCursor)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
