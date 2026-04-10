package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPromptArgumentSchemaRoundTrip verifies that a PromptArgument.Schema
// (arbitrary JSON Schema object) round-trips through marshal/unmarshal without
// loss. Issue #87: prompts gain optional schemas that mirror ToolDef.InputSchema
// so clients can render typed inputs (numbers, enums, etc.). Arbitrary JSON
// Schema fields ($ref, $defs, additionalProperties, etc.) must be preserved.
func TestPromptArgumentSchemaRoundTrip(t *testing.T) {
	arg := PromptArgument{
		Name:        "age",
		Description: "user age",
		Required:    true,
		Schema: map[string]any{
			"type":    "integer",
			"minimum": float64(1),
			"maximum": float64(150),
		},
	}
	data, err := json.Marshal(arg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed PromptArgument
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Name != arg.Name || parsed.Required != arg.Required {
		t.Errorf("basic fields lost: %+v", parsed)
	}
	schema, ok := parsed.Schema.(map[string]any)
	if !ok {
		t.Fatalf("schema = %T, want map[string]any", parsed.Schema)
	}
	if schema["type"] != "integer" {
		t.Errorf("schema.type = %v, want integer", schema["type"])
	}
	if schema["minimum"] != float64(1) || schema["maximum"] != float64(150) {
		t.Errorf("schema bounds lost: %+v", schema)
	}
}

// TestPromptArgumentWithoutSchemaOmitsField verifies that a PromptArgument with
// no schema does not emit a null/empty schema field. Backward-compatible with
// peers that reject unknown fields set to null.
func TestPromptArgumentWithoutSchemaOmitsField(t *testing.T) {
	arg := PromptArgument{Name: "name", Description: "user name"}
	data, err := json.Marshal(arg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "schema") {
		t.Errorf("marshal emitted %s, schema field should be omitted", s)
	}
}

// TestPromptArgumentSchemaWithNestedRef verifies that arbitrary JSON Schema
// keywords — including $ref and $defs — survive round-trip. This matches the
// treatment of ToolDef.InputSchema and is the foundation for future validation
// work in #184.
func TestPromptArgumentSchemaWithNestedRef(t *testing.T) {
	raw := `{"name":"filter","schema":{"$ref":"#/$defs/Filter","$defs":{"Filter":{"type":"object","properties":{"q":{"type":"string"}}}}}}`
	var arg PromptArgument
	if err := json.Unmarshal([]byte(raw), &arg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, err := json.Marshal(arg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"$ref":"#/$defs/Filter"`) {
		t.Errorf("marshal lost $ref: %s", s)
	}
	if !strings.Contains(s, `"$defs"`) {
		t.Errorf("marshal lost $defs: %s", s)
	}
}
