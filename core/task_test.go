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
		TTL:           IntPtr(300000),
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
	if decoded.TTL == nil || *decoded.TTL != 300000 {
		t.Errorf("TTL = %v, want 300000", decoded.TTL)
	}
}

// TestTaskInfoOmitEmpty verifies that optional fields are omitted when zero-valued,
// but TTL is always present (required+nullable per spec).
func TestTaskInfoOmitEmpty(t *testing.T) {
	info := TaskInfo{
		TaskID:        "task-1",
		Status:        TaskCompleted,
		CreatedAt:     "2025-01-15T10:00:00Z",
		LastUpdatedAt: "2025-01-15T10:00:05Z",
		TTL:           nil, // null = unlimited
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	// TTL must be present (as null), statusMessage and pollInterval omitted.
	for _, absent := range []string{"statusMessage", "pollInterval"} {
		if _, ok := m[absent]; ok {
			t.Errorf("expected field %q to be omitted, but it was present", absent)
		}
	}
	if _, ok := m["ttl"]; !ok {
		t.Error("TTL must be present even when nil (spec requires it)")
	}
}

// TestCreateTaskResultJSON verifies the v1 CreateTaskResultV1 envelope
// matches the spec wire format: {"task": {...}}.
func TestCreateTaskResultJSON(t *testing.T) {
	result := CreateTaskResultV1{
		Task: TaskInfo{
			TaskID:        "task-abc",
			Status:        TaskWorking,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:00Z",
			TTL:           IntPtr(60000),
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
			List:   &TasksCapMethod{},
			Cancel: &TasksCapMethod{},
			Requests: &TasksCapRequests{
				Tools: &TasksCapToolsMethods{Call: &TasksCapMethod{}},
			},
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
	if _, ok := tasksMap["list"]; !ok {
		t.Error("tasks missing 'list' field")
	}
	reqs, ok := tasksMap["requests"]
	if !ok {
		t.Fatal("tasks missing 'requests' field")
	}
	reqsMap := reqs.(map[string]any)
	toolsMap := reqsMap["tools"].(map[string]any)
	if _, ok := toolsMap["call"]; !ok {
		t.Error("tasks.requests.tools missing 'call'")
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
		Tasks: &ClientTasksCap{
			Requests: &TasksCapRequests{
				Tools: &TasksCapToolsMethods{Call: &TasksCapMethod{}},
			},
		},
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

// TestListTasksResultJSON verifies the v1 pagination envelope.
func TestListTasksResultJSON(t *testing.T) {
	result := ListTasksResultV1{
		Tasks: []TaskInfo{
			{TaskID: "t1", Status: TaskWorking, CreatedAt: "2025-01-15T10:00:00Z", LastUpdatedAt: "2025-01-15T10:00:00Z", TTL: IntPtr(30000)},
		},
		NextCursor: "cursor-2",
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ListTasksResultV1
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

// --- Spec wire-format compliance tests (red-first) ---

// TestGetTaskResultFlatJSON verifies that the v1 GetTaskResultV1 serializes
// task fields at the root level (not nested under a "task" key). Per MCP
// spec 2025-11-25: tasks/get returns Result & Task intersection.
func TestGetTaskResultFlatJSON(t *testing.T) {
	result := GetTaskResultV1{
		TaskInfo: TaskInfo{
			TaskID:        "task-flat",
			Status:        TaskWorking,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:01Z",
			TTL:           IntPtr(30000),
			PollInterval:  5000,
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	// Must NOT have a "task" wrapper key.
	if _, ok := m["task"]; ok {
		t.Error("GetTaskResultV1 should serialize flat, not under a 'task' key")
	}
	// Must have taskId at root.
	if m["taskId"] != "task-flat" {
		t.Errorf("taskId = %v, want task-flat", m["taskId"])
	}
	if m["status"] != "working" {
		t.Errorf("status = %v, want working", m["status"])
	}
}

// TestCancelTaskResultFlatJSON verifies that the v1 CancelTaskResultV1
// serializes task fields at the root level. Per MCP spec 2025-11-25:
// tasks/cancel returns Result & Task.
func TestCancelTaskResultFlatJSON(t *testing.T) {
	result := CancelTaskResultV1{
		TaskInfo: TaskInfo{
			TaskID:        "task-cancel",
			Status:        TaskCancelled,
			CreatedAt:     "2025-01-15T10:00:00Z",
			LastUpdatedAt: "2025-01-15T10:00:05Z",
			TTL:           IntPtr(30000),
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	if _, ok := m["task"]; ok {
		t.Error("CancelTaskResultV1 should serialize flat, not under a 'task' key")
	}
	if m["taskId"] != "task-cancel" {
		t.Errorf("taskId = %v, want task-cancel", m["taskId"])
	}
	if m["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", m["status"])
	}
}

// TestTasksCapNestedJSON verifies that TasksCap serializes to the nested
// structure required by the MCP spec: {list:{}, cancel:{}, requests:{tools:{call:{}}}}.
func TestTasksCapNestedJSON(t *testing.T) {
	cap := TasksCap{
		List:   &TasksCapMethod{},
		Cancel: &TasksCapMethod{},
		Requests: &TasksCapRequests{
			Tools: &TasksCapToolsMethods{
				Call: &TasksCapMethod{},
			},
		},
	}
	data, err := json.Marshal(cap)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	// Must have "list", "cancel", "requests" at top level.
	if _, ok := m["list"]; !ok {
		t.Error("TasksCap missing 'list' field")
	}
	if _, ok := m["cancel"]; !ok {
		t.Error("TasksCap missing 'cancel' field")
	}
	reqs, ok := m["requests"]
	if !ok {
		t.Fatal("TasksCap missing 'requests' field")
	}
	reqsMap := reqs.(map[string]any)
	tools, ok := reqsMap["tools"]
	if !ok {
		t.Fatal("requests missing 'tools' field")
	}
	toolsMap := tools.(map[string]any)
	if _, ok := toolsMap["call"]; !ok {
		t.Error("requests.tools missing 'call' field")
	}
	// Must NOT have old flat "requests" map with "tools/call" key.
	if _, ok := reqsMap["tools/call"]; ok {
		t.Error("requests should not have flat 'tools/call' key")
	}
}

// TestTTLNullJSON verifies that TaskInfo with nil TTL serializes as "ttl": null
// (required field, nullable). Per MCP spec: ttl is required but can be null.
func TestTTLNullJSON(t *testing.T) {
	info := TaskInfo{
		TaskID:        "task-null-ttl",
		Status:        TaskWorking,
		CreatedAt:     "2025-01-15T10:00:00Z",
		LastUpdatedAt: "2025-01-15T10:00:00Z",
		TTL:           nil,
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}

	// Must contain "ttl": null, not omit the field.
	var m map[string]any
	json.Unmarshal(data, &m)
	val, ok := m["ttl"]
	if !ok {
		t.Fatal("TTL field must be present even when nil (spec requires it)")
	}
	if val != nil {
		t.Errorf("TTL = %v, want null", val)
	}
}

// TestTTLValueJSON verifies that TaskInfo with a set TTL serializes correctly.
func TestTTLValueJSON(t *testing.T) {
	info := TaskInfo{
		TaskID:        "task-ttl",
		Status:        TaskWorking,
		CreatedAt:     "2025-01-15T10:00:00Z",
		LastUpdatedAt: "2025-01-15T10:00:00Z",
		TTL:           IntPtr(60000),
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	ttl, ok := m["ttl"]
	if !ok {
		t.Fatal("TTL field must be present")
	}
	if ttl != float64(60000) {
		t.Errorf("TTL = %v, want 60000", ttl)
	}
}

// TestToolResultRelatedTaskMeta verifies that ToolResult with RelatedTask
// metadata serializes to _meta["io.modelcontextprotocol/related-task"].
func TestToolResultRelatedTaskMeta(t *testing.T) {
	result := ToolResult{
		Content: []Content{{Type: "text", Text: "hello"}},
		Meta: &ToolResultMeta{
			RelatedTask: &RelatedTaskMeta{TaskID: "task-abc"},
		},
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(data, &m)

	meta, ok := m["_meta"]
	if !ok {
		t.Fatal("ToolResult missing '_meta' field")
	}
	metaMap := meta.(map[string]any)
	related, ok := metaMap["io.modelcontextprotocol/related-task"]
	if !ok {
		t.Fatal("_meta missing 'io.modelcontextprotocol/related-task'")
	}
	relatedMap := related.(map[string]any)
	if relatedMap["taskId"] != "task-abc" {
		t.Errorf("related-task.taskId = %v, want task-abc", relatedMap["taskId"])
	}
}

// TestParentTaskIDJSON verifies parentTaskId is included when set and
// omitted when empty (backward compatible extension).
func TestParentTaskIDJSON(t *testing.T) {
	// With parentTaskId set.
	info := TaskInfo{
		TaskID:        "child-1",
		ParentTaskID:  "parent-1",
		Status:        TaskWorking,
		CreatedAt:     "2025-01-01T00:00:00Z",
		LastUpdatedAt: "2025-01-01T00:00:00Z",
		TTL:           IntPtr(60000),
	}
	raw, _ := MarshalJSON(info)
	var m map[string]any
	json.Unmarshal(raw, &m)

	if m["parentTaskId"] != "parent-1" {
		t.Errorf("parentTaskId = %v, want parent-1", m["parentTaskId"])
	}

	// Without parentTaskId (root task) — field should be omitted.
	root := TaskInfo{
		TaskID:        "root-1",
		Status:        TaskWorking,
		CreatedAt:     "2025-01-01T00:00:00Z",
		LastUpdatedAt: "2025-01-01T00:00:00Z",
		TTL:           IntPtr(60000),
	}
	raw2, _ := MarshalJSON(root)
	var m2 map[string]any
	json.Unmarshal(raw2, &m2)

	if _, ok := m2["parentTaskId"]; ok {
		t.Error("parentTaskId should be omitted for root tasks")
	}
}

// TestRelatedTaskOnElicitationMeta verifies the related-task metadata
// field on ElicitationMeta serializes correctly.
func TestRelatedTaskOnElicitationMeta(t *testing.T) {
	req := ElicitationRequest{
		Message: "confirm?",
		Meta: &ElicitationMeta{
			RelatedTask: &RelatedTaskMeta{TaskID: "task-abc"},
		},
	}
	raw, _ := MarshalJSON(req)
	var m map[string]any
	json.Unmarshal(raw, &m)

	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		t.Fatal("missing _meta")
	}
	related, ok := meta["io.modelcontextprotocol/related-task"].(map[string]any)
	if !ok {
		t.Fatal("missing related-task in _meta")
	}
	if related["taskId"] != "task-abc" {
		t.Errorf("taskId = %v, want task-abc", related["taskId"])
	}
}

// TestRelatedTaskOnSamplingMeta verifies the related-task metadata
// field on SamplingMeta serializes correctly.
func TestRelatedTaskOnSamplingMeta(t *testing.T) {
	req := CreateMessageRequest{
		Messages:  []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "hi"}}},
		MaxTokens: 10,
		Meta: &SamplingMeta{
			RelatedTask: &RelatedTaskMeta{TaskID: "task-xyz"},
		},
	}
	raw, _ := MarshalJSON(req)
	var m map[string]any
	json.Unmarshal(raw, &m)

	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		t.Fatal("missing _meta")
	}
	related, ok := meta["io.modelcontextprotocol/related-task"].(map[string]any)
	if !ok {
		t.Fatal("missing related-task in _meta")
	}
	if related["taskId"] != "task-xyz" {
		t.Errorf("taskId = %v, want task-xyz", related["taskId"])
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
