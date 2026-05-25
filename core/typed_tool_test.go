package core

import (
	"reflect"
	"testing"
)

type overrideTestInput struct {
	Name string `json:"name"`
}

// TestTypedTool_DefaultSchemaFromStructTags confirms the baseline behavior:
// without WithInputSchemaOverride, the input schema is derived from the In
// type via reflection. Anchor test so the override test below means something.
func TestTypedTool_DefaultSchemaFromStructTags(t *testing.T) {
	tt := TypedTool[overrideTestInput, string]("greet", "say hi",
		func(ctx ToolContext, in overrideTestInput) (string, error) {
			return "hi " + in.Name, nil
		},
	)
	got := tt.InputSchema
	if got == nil {
		t.Fatal("InputSchema is nil; expected reflection-derived schema")
	}
	// The reflection-derived schema should be a non-nil object referencing
	// the struct's `name` property. We don't pin exact shape (the generator
	// can evolve); we just confirm it isn't the override sentinel and isn't
	// empty.
	if asMap, ok := got.(map[string]any); ok {
		if len(asMap) == 0 {
			t.Errorf("reflection-derived schema is an empty map; expected populated schema")
		}
	}
}

// TestTypedTool_WithInputSchemaOverride confirms that the caller-supplied
// schema replaces the reflection-derived one verbatim. This is the load-
// bearing test for SEP-1613/2106 fixture parity: a real mcpkit user with a
// 2020-12 schema (conditional, composition, $anchor) must be able to keep
// using TypedTool and override only the schema.
func TestTypedTool_WithInputSchemaOverride(t *testing.T) {
	override := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"$defs": map[string]any{
			"addr": map[string]any{"$anchor": "addrDef", "type": "object"},
		},
		"if":   map[string]any{"properties": map[string]any{"name": map[string]any{"const": "admin"}}},
		"then": map[string]any{"required": []string{"name"}},
	}

	tt := TypedTool[overrideTestInput, string]("greet", "say hi",
		func(ctx ToolContext, in overrideTestInput) (string, error) {
			return "hi " + in.Name, nil
		},
		WithInputSchemaOverride(override),
	)

	if !reflect.DeepEqual(tt.InputSchema, override) {
		t.Errorf("InputSchema was not preserved verbatim;\n got %#v\nwant %#v", tt.InputSchema, override)
	}
}

// TestTypedTool_DefaultExecutionUnset confirms the baseline: without
// WithToolExecution, the generated ToolDef.Execution is nil (sync-only,
// equivalent to TaskSupportForbidden). Anchor test so the option test below
// means something.
func TestTypedTool_DefaultExecutionUnset(t *testing.T) {
	tt := TypedTool[overrideTestInput, string]("greet", "say hi",
		func(ctx ToolContext, in overrideTestInput) (string, error) { return "", nil },
	)
	if tt.Execution != nil {
		t.Errorf("Execution should be nil by default; got %#v", tt.Execution)
	}
}

// TestTypedTool_WithToolExecution confirms the option threads the
// ToolExecution through to the generated ToolDef. Load-bearing for the
// PROMOTE of v2-tasks tools from raw RegisterTool to the typed helper path
// (issue #457).
func TestTypedTool_WithToolExecution(t *testing.T) {
	exec := &ToolExecution{TaskSupport: TaskSupportRequired}

	tt := TypedTool[overrideTestInput, string]("greet", "say hi",
		func(ctx ToolContext, in overrideTestInput) (string, error) { return "", nil },
		WithToolExecution(exec),
	)

	if tt.Execution == nil {
		t.Fatal("Execution is nil after WithToolExecution; expected non-nil")
	}
	if tt.Execution.TaskSupport != TaskSupportRequired {
		t.Errorf("TaskSupport = %q, want %q", tt.Execution.TaskSupport, TaskSupportRequired)
	}
	// Pointer should be preserved exactly (we threaded it through, not copied).
	if tt.Execution != exec {
		t.Errorf("Execution pointer was copied; expected the same pointer threaded through")
	}
}
