package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPromptArgumentSchemaIsNotSerialized pins Schema as server-side config.
// No MCP schema defines a schema on PromptArgument, so it stays off the wire
// while the dispatcher still validates against it (#1562, #184).
func TestPromptArgumentSchemaIsNotSerialized(t *testing.T) {
	arg := PromptArgument{
		Name:        "age",
		Title:       "Age",
		Description: "user age",
		Required:    true,
		Schema:      map[string]any{"type": "integer", "minimum": float64(1)},
	}
	data, err := json.Marshal(arg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "schema") {
		t.Errorf("marshal emitted %s, schema must stay server-side", data)
	}
	var parsed PromptArgument
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.Name != arg.Name || parsed.Title != arg.Title || parsed.Required != arg.Required {
		t.Errorf("spec fields lost: %+v", parsed)
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

// TestPromptArgumentIgnoresPeerSchema checks that a schema sent by a peer
// built against the old shape is dropped on decode rather than becoming
// server-side validation config.
func TestPromptArgumentIgnoresPeerSchema(t *testing.T) {
	raw := `{"name":"filter","schema":{"$ref":"#/$defs/Filter","$defs":{"Filter":{"type":"object"}}}}`
	var arg PromptArgument
	if err := json.Unmarshal([]byte(raw), &arg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if arg.Name != "filter" {
		t.Errorf("name = %q, want filter", arg.Name)
	}
	if arg.Schema != nil {
		t.Errorf("schema = %v, want nil (not read from the wire)", arg.Schema)
	}
}
