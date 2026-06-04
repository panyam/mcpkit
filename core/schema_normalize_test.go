package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSchemaGen_AnyBecomesEmptySchema verifies that an `any` field
// reflects to `{}` (empty object schema) on the wire instead of the
// literal `true` invopop emits by default. Issue 548 Gap 2 — the MCP
// TypeScript SDK's zod validator rejects bare-`true` property schemas.
func TestSchemaGen_AnyBecomesEmptySchema(t *testing.T) {
	type In struct {
		Name    string `json:"name"`
		Payload any    `json:"payload"`
	}
	raw := GenerateSchema[In]()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %s", raw)
	}
	payload, ok := props["payload"]
	if !ok {
		t.Fatalf("payload property missing: %s", raw)
	}
	// Must be `{}` (an object schema), NOT bare `true`.
	pm, ok := payload.(map[string]any)
	if !ok {
		t.Errorf("payload should be an object schema, got %T: %v", payload, payload)
	}
	if len(pm) != 0 {
		t.Errorf("payload should be the empty object {}, got %v", pm)
	}
	if strings.Contains(string(raw), `"payload":true`) {
		t.Errorf("emitted schema still contains bare-true payload: %s", raw)
	}
}

// TestSchemaGen_NestedAnyBecomesEmptySchema covers `any` nested inside
// an array's items + inside another struct field.
func TestSchemaGen_NestedAnyBecomesEmptySchema(t *testing.T) {
	type Inner struct {
		Value any `json:"value"`
	}
	type In struct {
		Items []any `json:"items"`
		Inner Inner `json:"inner"`
	}
	raw := GenerateSchema[In]()
	if strings.Contains(string(raw), `:true`) {
		// Loose check: no bare-true survived anywhere a schema value
		// would be expected. additionalProperties:true would survive
		// since we deliberately don't normalize there — but the test
		// struct doesn't trigger that.
		t.Errorf("emitted schema still contains bare-true: %s", raw)
	}
}

// TestSchemaGen_OtherFieldsUnchanged verifies the normalization
// doesn't touch fields it shouldn't (regular typed fields land
// unchanged).
func TestSchemaGen_OtherFieldsUnchanged(t *testing.T) {
	type In struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	raw := GenerateSchema[In]()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := got["properties"].(map[string]any)
	if props["name"].(map[string]any)["type"] != "string" {
		t.Errorf("name should be string type: %v", props["name"])
	}
	if props["age"].(map[string]any)["type"] != "integer" {
		t.Errorf("age should be integer type: %v", props["age"])
	}
}
