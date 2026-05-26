package client

import (
	"reflect"
	"testing"
)

// extractMcpParamHeaders — schema walking.

func TestExtractMcpParamHeaders_NilSchema(t *testing.T) {
	got := extractMcpParamHeaders(nil)
	if len(got) != 0 {
		t.Errorf("nil schema should yield empty map, got %v", got)
	}
}

func TestExtractMcpParamHeaders_NoAnnotations(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
	}
	got := extractMcpParamHeaders(schema)
	if len(got) != 0 {
		t.Errorf("schema without annotations should yield empty map, got %v", got)
	}
}

func TestExtractMcpParamHeaders_PrimitiveTypesOnly(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"str_field":  map[string]any{"type": "string", "x-mcp-header": "Str"},
			"num_field":  map[string]any{"type": "number", "x-mcp-header": "Num"},
			"int_field":  map[string]any{"type": "integer", "x-mcp-header": "Int"},
			"bool_field": map[string]any{"type": "boolean", "x-mcp-header": "Bool"},
			// These should be IGNORED (non-primitive types).
			"obj_field":   map[string]any{"type": "object", "x-mcp-header": "Obj"},
			"arr_field":   map[string]any{"type": "array", "x-mcp-header": "Arr"},
			"null_field":  map[string]any{"type": "null", "x-mcp-header": "Null"},
			"empty_field": map[string]any{"type": "string", "x-mcp-header": ""},
		},
	}
	got := extractMcpParamHeaders(schema)
	want := map[string]string{
		"str_field":  "Str",
		"num_field":  "Num",
		"int_field":  "Int",
		"bool_field": "Bool",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// encodeMcpParamHeaderValue — value encoding.

func TestEncodeMcpParamHeaderValue_NilOmitsHeader(t *testing.T) {
	if _, ok := encodeMcpParamHeaderValue(nil); ok {
		t.Error("nil value should report ok=false (omit header)")
	}
}

func TestEncodeMcpParamHeaderValue_PlainASCII(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"simple", "us-west1", "us-west1"},
		{"with-internal-space", "us west 1", "us west 1"},
		{"with-printable-ascii", "test-method", "test-method"},
		{"empty-string", "", ""},
		{"sql-like", "SELECT * FROM users", "SELECT * FROM users"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := encodeMcpParamHeaderValue(tc.in)
			if !ok {
				t.Fatal("ok=false for non-nil input")
			}
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestEncodeMcpParamHeaderValue_RequiresBase64(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"non-ascii", "Hello, 世界", "=?base64?SGVsbG8sIOS4lueVjA==?="},
		{"leading-space", " us-west1", "=?base64?IHVzLXdlc3Qx?="},
		{"trailing-space", "us-west1 ", "=?base64?dXMtd2VzdDEg?="},
		{"both-edges", " padded ", "=?base64?IHBhZGRlZCA=?="},
		{"control-char-lf", "line1\nline2", "=?base64?bGluZTEKbGluZTI=?="},
		{"crlf", "line1\r\nline2", "=?base64?bGluZTENCmxpbmUy?="},
		{"leading-tab", "\tindented", "=?base64?CWluZGVudGVk?="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := encodeMcpParamHeaderValue(tc.in)
			if !ok {
				t.Fatal("ok=false for non-nil input")
			}
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestEncodeMcpParamHeaderValue_Numbers(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{"int", 42, "42"},
		{"int64", int64(42), "42"},
		{"float-integer-value", float64(42), "42"},
		{"float-with-decimal", 3.14159, "3.14159"},
		{"float-small", 0.001, "0.001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := encodeMcpParamHeaderValue(tc.in)
			if !ok {
				t.Fatal("ok=false for number")
			}
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestEncodeMcpParamHeaderValue_Booleans(t *testing.T) {
	if got, _ := encodeMcpParamHeaderValue(true); got != "true" {
		t.Errorf("true → %q, want %q", got, "true")
	}
	if got, _ := encodeMcpParamHeaderValue(false); got != "false" {
		t.Errorf("false → %q, want %q", got, "false")
	}
}

// needsBase64Encoding — boundary tests.

func TestNeedsBase64Encoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain-ascii", "us-west1", false},
		{"sql", "SELECT * FROM users", false},
		{"internal-spaces-only", "us west 1", false},
		{"non-ascii", "Hello, 世界", true},
		{"leading-space", " padded", true},
		{"trailing-space", "padded ", true},
		{"both-edges", " padded ", true},
		{"tab", "\tindented", true},
		{"newline", "line1\nline2", true},
		{"crlf", "line1\r\nline2", true},
		{"high-ascii", "café", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsBase64Encoding(tc.in); got != tc.want {
				t.Errorf("needsBase64Encoding(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestMcpParamHeaderName(t *testing.T) {
	if got := mcpParamHeaderName("Region"); got != "Mcp-Param-Region" {
		t.Errorf("got %q, want Mcp-Param-Region", got)
	}
	if got := mcpParamHeaderName("Method"); got != "Mcp-Param-Method" {
		t.Errorf("got %q, want Mcp-Param-Method", got)
	}
}
