package client

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtractElicitationDefaults_EmptyOrInvalid(t *testing.T) {
	if got := extractElicitationDefaults(nil); len(got) != 0 {
		t.Errorf("nil schema should yield empty map, got %v", got)
	}
	if got := extractElicitationDefaults(json.RawMessage("not json")); len(got) != 0 {
		t.Errorf("invalid JSON should yield empty map, got %v", got)
	}
	if got := extractElicitationDefaults(json.RawMessage(`{"type":"object"}`)); len(got) != 0 {
		t.Errorf("schema without properties should yield empty map, got %v", got)
	}
}

func TestExtractElicitationDefaults_NoDefaults(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age":  {"type": "integer"}
		}
	}`)
	if got := extractElicitationDefaults(schema); len(got) != 0 {
		t.Errorf("schema without default keywords should yield empty map, got %v", got)
	}
}

// TestExtractElicitationDefaults_AllPrimitiveTypes — mirrors the upstream
// test_elicitation_sep1034_defaults fixture: one default per primitive type
// SEP-1034 covers.
func TestExtractElicitationDefaults_AllPrimitiveTypes(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name":     {"type": "string",  "default": "John Doe"},
			"age":      {"type": "integer", "default": 30},
			"score":    {"type": "number",  "default": 95.5},
			"status":   {"type": "string",  "enum": ["active", "inactive", "pending"], "default": "active"},
			"verified": {"type": "boolean", "default": true}
		}
	}`)
	got := extractElicitationDefaults(schema)
	want := map[string]any{
		"name":     "John Doe",
		"age":      float64(30), // JSON numbers decode to float64
		"score":    95.5,
		"status":   "active",
		"verified": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}

func TestExtractElicitationDefaults_TypeMismatchSkipped(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"age": {"type": "integer", "default": "thirty"},
			"name": {"type": "string", "default": 42}
		}
	}`)
	got := extractElicitationDefaults(schema)
	if len(got) != 0 {
		t.Errorf("type-mismatched defaults should be skipped, got %v", got)
	}
}

func TestExtractElicitationDefaults_IntegerAcceptsWholeFloat(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"age": {"type": "integer", "default": 30.0}
		}
	}`)
	got := extractElicitationDefaults(schema)
	if got["age"] != float64(30) {
		t.Errorf("integer schema should accept whole-number float, got %v", got)
	}
}

func TestExtractElicitationDefaults_IntegerRejectsNonWholeFloat(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"age": {"type": "integer", "default": 30.5}
		}
	}`)
	got := extractElicitationDefaults(schema)
	if _, present := got["age"]; present {
		t.Errorf("integer schema should reject non-whole-number float default, got %v", got)
	}
}

func TestMergeElicitationDefaults_NoDefaults(t *testing.T) {
	content := map[string]any{"x": 1}
	got := mergeElicitationDefaults(content, nil)
	if !reflect.DeepEqual(got, map[string]any{"x": 1}) {
		t.Errorf("empty defaults should not change content, got %v", got)
	}
}

func TestMergeElicitationDefaults_FillsOmittedKeys(t *testing.T) {
	content := map[string]any{"name": "Jane"} // user set name only
	defaults := map[string]any{
		"name":     "John Doe",
		"age":      float64(30),
		"verified": true,
	}
	got := mergeElicitationDefaults(content, defaults)
	want := map[string]any{
		"name":     "Jane", // user-provided wins over default
		"age":      float64(30),
		"verified": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestMergeElicitationDefaults_NilContentAllocates(t *testing.T) {
	defaults := map[string]any{"name": "John Doe"}
	got := mergeElicitationDefaults(nil, defaults)
	if !reflect.DeepEqual(got, map[string]any{"name": "John Doe"}) {
		t.Errorf("nil content should be initialized with defaults, got %v", got)
	}
}
