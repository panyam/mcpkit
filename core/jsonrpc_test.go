package core

import (
	"encoding/json"
	"testing"
)

func TestNewResponse(t *testing.T) {
	id := json.RawMessage(`1`)
	resp := NewResponse(id, map[string]string{"key": "value"})

	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want 2.0", resp.JSONRPC)
	}
	if string(resp.ID) != "1" {
		t.Errorf("ID = %s, want 1", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("Error = %v, want nil", resp.Error)
	}

	var result map[string]string
	if err := resp.ResultAs(&result); err != nil {
		t.Fatal(err)
	}
	if result["key"] != "value" {
		t.Errorf("result[key] = %q, want value", result["key"])
	}
}

func TestNewErrorResponse(t *testing.T) {
	id := json.RawMessage(`"abc"`)
	resp := NewErrorResponse(id, ErrCodeMethodNotFound, "not found")

	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want 2.0", resp.JSONRPC)
	}
	if resp.Result != nil {
		t.Errorf("Result = %s, want nil", resp.Result)
	}
	if resp.Error == nil {
		t.Fatal("Error is nil")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, ErrCodeMethodNotFound)
	}
	if resp.Error.Message != "not found" {
		t.Errorf("Error.Message = %q, want not found", resp.Error.Message)
	}
}

// TestNewErrorResponseWithData verifies that the error response constructor correctly
// sets the Data field, which is used to carry machine-readable context like the list
// of supported protocol versions in a version negotiation failure.
func TestNewErrorResponseWithData(t *testing.T) {
	id := json.RawMessage(`1`)
	data := map[string]any{"supported": []string{"2025-11-25", "2024-11-05"}}
	resp := NewErrorResponseWithData(id, ErrCodeInvalidParams, "unsupported version", data)

	if resp.Error == nil {
		t.Fatal("Error is nil")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("Error.Code = %d, want %d", resp.Error.Code, ErrCodeInvalidParams)
	}
	if resp.Error.Data == nil {
		t.Fatal("Error.Data is nil")
	}

	// Verify data round-trips through JSON
	raw, err := json.Marshal(resp.Error)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Data struct {
			Supported []string `json:"supported"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Data.Supported) != 2 {
		t.Errorf("got %d supported versions, want 2", len(parsed.Data.Supported))
	}
}

// BenchmarkNewResponseAndMarshal measures the full create+serialize path
// that every JSON-RPC response goes through.
func BenchmarkNewResponseAndMarshal(b *testing.B) {
	id := json.RawMessage(`1`)
	result := ToolsListResult{
		Tools: []ToolDef{
			{Name: "tool1", Description: "A test tool", InputSchema: map[string]any{"type": "object"}},
			{Name: "tool2", Description: "Another tool", InputSchema: map[string]any{"type": "object"}},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := NewResponse(id, result)
		MarshalJSON(resp)
	}
}

// BenchmarkNewResponseHTML measures with HTML content (the MCP Apps case).
func BenchmarkNewResponseHTML(b *testing.B) {
	id := json.RawMessage(`1`)
	html := "<html><head><script type=\"module\">console.log('hello')</script></head><body><p>Content</p></body></html>"
	result := ResourceResult{
		Contents: []ResourceReadContent{{
			URI: "ui://test/view", MimeType: "text/html;profile=mcp-app", Text: html,
		}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := NewResponse(id, result)
		MarshalJSON(resp)
	}
}

func TestRequestIsNotification(t *testing.T) {
	tests := []struct {
		name string
		id   json.RawMessage
		want bool
	}{
		{"nil ID", nil, true},
		{"null ID", json.RawMessage("null"), true},
		{"numeric ID", json.RawMessage("1"), false},
		{"string ID", json.RawMessage(`"abc"`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Request{ID: tt.id}
			if got := r.IsNotification(); got != tt.want {
				t.Errorf("IsNotification() = %v, want %v", got, tt.want)
			}
		})
	}
}

// JSON-RPC 2.0 defines a notification as a request without an id member, so a
// nil ID is left out rather than written as null (#1520). Any ID the caller
// set, null included, is written as given.
func TestRequestMarshalOmitsNilID(t *testing.T) {
	for _, tc := range []struct {
		id   json.RawMessage
		want string
	}{
		{nil, `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{json.RawMessage("0"), `{"jsonrpc":"2.0","id":0,"method":"notifications/initialized"}`},
		{json.RawMessage(`"a"`), `{"jsonrpc":"2.0","id":"a","method":"notifications/initialized"}`},
		{json.RawMessage("null"), `{"jsonrpc":"2.0","id":null,"method":"notifications/initialized"}`},
	} {
		got, err := json.Marshal(Request{JSONRPC: "2.0", ID: tc.id, Method: "notifications/initialized"})
		if err != nil {
			t.Fatalf("marshal id %s: %v", tc.id, err)
		}
		if string(got) != tc.want {
			t.Errorf("id %s: got %s, want %s", tc.id, got, tc.want)
		}
	}
}
