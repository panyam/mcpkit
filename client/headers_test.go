package client

import (
	"net/http"
	"net/http/httptest"
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

// validateMcpParamHeaders — SEP-2243 schema-validation rules.

func TestValidateMcpParamHeaders_Valid(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"region":   map[string]any{"type": "string", "x-mcp-header": "Region"},
			"priority": map[string]any{"type": "integer", "x-mcp-header": "Priority"},
			"verbose":  map[string]any{"type": "boolean", "x-mcp-header": "Verbose"},
			// Property with no annotation — OK.
			"query": map[string]any{"type": "string"},
		},
	}
	if err := validateMcpParamHeaders(schema); err != nil {
		t.Errorf("valid schema rejected: %v", err)
	}
}

func TestValidateMcpParamHeaders_NilSchemaPasses(t *testing.T) {
	if err := validateMcpParamHeaders(nil); err != nil {
		t.Errorf("nil schema should pass, got %v", err)
	}
}

// The invalid-case table mirrors the upstream HttpInvalidToolHeadersScenario
// in modelcontextprotocol/conformance: each row is a tool that must be
// rejected, one rule per row.
func TestValidateMcpParamHeaders_InvalidCases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema map[string]any
	}{
		{
			"empty-header-value",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string", "x-mcp-header": ""}},
			},
		},
		{
			"object-typed-property",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"data": map[string]any{"type": "object", "x-mcp-header": "Data"}},
			},
		},
		{
			"array-typed-property",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"items": map[string]any{"type": "array", "x-mcp-header": "Items"}},
			},
		},
		{
			"null-typed-property",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"nil": map[string]any{"type": "null", "x-mcp-header": "Nil"}},
			},
		},
		{
			"duplicate-same-case",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"field1": map[string]any{"type": "string", "x-mcp-header": "Region"},
					"field2": map[string]any{"type": "string", "x-mcp-header": "Region"},
				},
			},
		},
		{
			"duplicate-case-insensitive",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"field1": map[string]any{"type": "string", "x-mcp-header": "MyField"},
					"field2": map[string]any{"type": "string", "x-mcp-header": "myfield"},
				},
			},
		},
		{
			"space-in-name",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string", "x-mcp-header": "My Region"}},
			},
		},
		{
			"colon-in-name",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string", "x-mcp-header": "Region:Primary"}},
			},
		},
		{
			"non-ascii-name",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string", "x-mcp-header": "Région"}},
			},
		},
		{
			"control-char-name",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{"value": map[string]any{"type": "string", "x-mcp-header": "Region\t1"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMcpParamHeaders(tc.schema); err == nil {
				t.Errorf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

// SEP-2243 standard routing headers — Mcp-Method / Mcp-Name.

func TestDeriveMcpName(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		params any
		want   string
	}{
		{"tools-call-map-any", "tools/call", map[string]any{"name": "echo", "arguments": map[string]any{}}, "echo"},
		{"prompts-get-map-any", "prompts/get", map[string]any{"name": "summary"}, "summary"},
		{"resources-read-map-string", "resources/read", map[string]string{"uri": "file:///a.txt"}, "file:///a.txt"},
		{"resources-read-map-any", "resources/read", map[string]any{"uri": "file:///b.txt"}, "file:///b.txt"},
		{"tools-list-no-name", "tools/list", map[string]any{}, ""},
		{"unknown-method", "ping", map[string]any{"name": "ignored"}, ""},
		{"nil-params", "tools/call", nil, ""},
		{"name-not-string", "tools/call", map[string]any{"name": 42}, ""},
		{"empty-name", "tools/call", map[string]any{"name": ""}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveMcpName(tc.method, tc.params)
			if got != tc.want {
				t.Errorf("deriveMcpName(%q, %v) = %q, want %q", tc.method, tc.params, got, tc.want)
			}
		})
	}
}

func TestSetSEP2243RoutingHeaders(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     string
		cc         *CallContext
		wantMethod string
		wantName   string
	}{
		{"method-only-nil-cc", "tools/list", nil, "tools/list", ""},
		{"method-and-name", "tools/call", &CallContext{mcpName: "echo"}, "tools/call", "echo"},
		{"empty-method-skipped", "", &CallContext{mcpName: "echo"}, "", "echo"},
		{"cc-without-name", "ping", &CallContext{}, "ping", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", nil)
			setSEP2243RoutingHeaders(req, tc.method, tc.cc)
			if got := req.Header.Get("Mcp-Method"); got != tc.wantMethod {
				t.Errorf("Mcp-Method = %q, want %q", got, tc.wantMethod)
			}
			if got := req.Header.Get("Mcp-Name"); got != tc.wantName {
				t.Errorf("Mcp-Name = %q, want %q", got, tc.wantName)
			}
		})
	}
}

// End-to-end: a streamable POST through the client should land at the server
// with Mcp-Method (always) and Mcp-Name (when applicable) set. This guards
// the wire-level contract that SEP-2243 routing middleware depends on.
func TestStreamableTransportEmitsSEP2243Headers(t *testing.T) {
	type captured struct {
		method, mcpMethod, mcpName string
	}
	var got captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.mcpMethod = r.Header.Get("Mcp-Method")
		got.mcpName = r.Header.Get("Mcp-Name")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	t.Run("notify-without-name", func(t *testing.T) {
		got = captured{}
		tr := newStreamableClientTransport(srv.URL, nil)
		data := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
		if err := tr.notify("notifications/initialized", data); err != nil {
			t.Fatalf("notify: %v", err)
		}
		if got.mcpMethod != "notifications/initialized" {
			t.Errorf("Mcp-Method = %q, want %q", got.mcpMethod, "notifications/initialized")
		}
		if got.mcpName != "" {
			t.Errorf("Mcp-Name = %q, want empty (notify path never sets Mcp-Name)", got.mcpName)
		}
	})

	t.Run("call-with-name-via-cc", func(t *testing.T) {
		got = captured{}
		tr := newStreamableClientTransport(srv.URL, nil)
		data := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`)
		cc := &CallContext{mcpName: "echo"}
		if _, err := tr.callWithContext("tools/call", data, cc); err != nil {
			t.Fatalf("callWithContext: %v", err)
		}
		if got.mcpMethod != "tools/call" {
			t.Errorf("Mcp-Method = %q, want %q", got.mcpMethod, "tools/call")
		}
		if got.mcpName != "echo" {
			t.Errorf("Mcp-Name = %q, want %q", got.mcpName, "echo")
		}
	})
}
