package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEP-2663: CreateTaskResult and GetTaskResult extend the open Result type,
// so unknown top-level keys belong to extensions and must survive a decode.
// Task Event Sources adds `eventSource` to CreateTaskResult this way. A client
// that decodes into these structs loses the key before any extension sees it.
// The TS reference SDK fixed the same loss in ext-tasks (Nov 2026).

func TestCreateTaskResult_KeepsExtensionFields(t *testing.T) {
	wire := `{"resultType":"task","taskId":"t1","status":"working","createdAt":"2026-11-08T00:00:00Z",` +
		`"lastUpdatedAt":"2026-11-08T00:00:00Z","ttlMs":60000,"vendor/trace":{"hop":1}}`
	var got CreateTaskResult
	require.NoError(t, json.Unmarshal([]byte(wire), &got))
	out, err := json.Marshal(got)
	require.NoError(t, err)
	var round map[string]any
	require.NoError(t, json.Unmarshal(out, &round))
	assert.Equal(t, map[string]any{"hop": float64(1)}, round["vendor/trace"],
		"an extension field on CreateTaskResult must survive decode and re-encode")
}

func TestGetTaskResult_KeepsExtensionFields(t *testing.T) {
	wire := `{"resultType":"complete","taskId":"t1","status":"working","createdAt":"2026-11-08T00:00:00Z",` +
		`"lastUpdatedAt":"2026-11-08T00:00:00Z","ttlMs":60000,"vendor/trace":{"hop":1}}`
	var got GetTaskResult
	require.NoError(t, json.Unmarshal([]byte(wire), &got))
	out, err := json.Marshal(got)
	require.NoError(t, err)
	var round map[string]any
	require.NoError(t, json.Unmarshal(out, &round))
	assert.Equal(t, map[string]any{"hop": float64(1)}, round["vendor/trace"],
		"an extension field on tasks/get's result must survive decode and re-encode")
}

func TestCreateTaskResult_ExtrasEncodeAndTypedFieldsWin(t *testing.T) {
	ttl := 1000
	res := CreateTaskResult{
		ResultType: ResultTypeTask,
		TaskInfoV2: TaskInfoV2{TaskID: "t1", Status: TaskWorking, TTLMs: &ttl},
		Extras: map[string]any{
			"eventSource": map[string]any{"name": "task.progress"},
			"taskId":      "spoofed",
		},
	}
	out, err := json.Marshal(res)
	require.NoError(t, err)
	var round map[string]any
	require.NoError(t, json.Unmarshal(out, &round))
	assert.Equal(t, map[string]any{"name": "task.progress"}, round["eventSource"])
	assert.Equal(t, "t1", round["taskId"], "an extra cannot overwrite a typed field")
}

func TestDetailedTask_NoExtrasLeavesFieldNil(t *testing.T) {
	var got DetailedTask
	require.NoError(t, json.Unmarshal([]byte(`{"resultType":"complete","taskId":"t1","status":"working","createdAt":"x","lastUpdatedAt":"x","ttlMs":null}`), &got))
	assert.Nil(t, got.Extras, "known keys only must not allocate Extras")
}
