package server

import (
	core "github.com/panyam/mcpkit/core"
	"context"
	"encoding/json"
	"testing"
)

// TestCompletionComplete verifies that completion/complete dispatches to a registered
// core.CompletionHandler and returns the handler's suggestions wrapped in a "completion" object.
// This is the happy path for argument autocompletion.
func TestCompletionComplete(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterCompletion("ref/prompt", "my-prompt", func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
		return core.CompletionResult{
			Values:  []string{"val1", "val2"},
			Total:   2,
			HasMore: false,
		}, nil
	})
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "completion/complete",
		Params:  json.RawMessage(`{"ref":{"type":"ref/prompt","name":"my-prompt"},"argument":{"name":"arg1","value":"v"}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result struct {
		Completion core.CompletionResult `json:"completion"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Completion.Values) != 2 {
		t.Fatalf("got %d values, want 2", len(result.Completion.Values))
	}
	if result.Completion.Values[0] != "val1" {
		t.Errorf("values[0] = %q, want val1", result.Completion.Values[0])
	}
}

// TestCompletionCompleteNoHandler verifies that completion/complete returns an empty
// result (not an error) when no core.CompletionHandler is registered for the requested ref.
// This allows clients to always try completion without needing to check capabilities first.
func TestCompletionCompleteNoHandler(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "completion/complete",
		Params:  json.RawMessage(`{"ref":{"type":"ref/prompt","name":"unregistered"},"argument":{"name":"arg","value":""}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result struct {
		Completion core.CompletionResult `json:"completion"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Completion.Values) != 0 {
		t.Errorf("got %d values, want 0 for unregistered handler", len(result.Completion.Values))
	}
	if result.Completion.HasMore {
		t.Error("hasMore should be false for empty result")
	}
}

// TestCompletionCompleteBeforeInit verifies that completion/complete is rejected
// before the initialization handshake completes, consistent with MCP init gating.
func TestCompletionCompleteBeforeInit(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "completion/complete",
		Params:  json.RawMessage(`{"ref":{"type":"ref/prompt","name":"x"},"argument":{"name":"a","value":""}}`),
	})

	if resp == nil {
		t.Fatal("response is nil")
	}
	if resp.Error == nil {
		t.Fatal("expected error before init")
	}
	if resp.Error.Code != core.ErrCodeInvalidRequest {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidRequest)
	}
}

// TestCompletionCapability verifies that the server advertises the completions
// capability in the initialize response. Clients check this to know whether
// completion/complete is supported.
func TestCompletionCapability(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})

	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	caps := result["capabilities"].(map[string]any)
	if _, ok := caps["completions"]; !ok {
		t.Error("capabilities missing completions")
	}
}

// TestCompletionCompleteResourceRef verifies that completion/complete works with
// "ref/resource" references (URI-based) in addition to "ref/prompt" references.
func TestCompletionCompleteResourceRef(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterCompletion("ref/resource", "file:///{path}", func(ctx core.PromptContext, ref core.CompletionRef, arg core.CompletionArgument) (core.CompletionResult, error) {
		return core.CompletionResult{Values: []string{"/etc", "/usr"}, HasMore: false}, nil
	})
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "completion/complete",
		Params:  json.RawMessage(`{"ref":{"type":"ref/resource","uri":"file:///{path}"},"argument":{"name":"path","value":"/"}}`),
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", resp.Error.Message)
	}

	var result struct {
		Completion core.CompletionResult `json:"completion"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Completion.Values) != 2 {
		t.Errorf("got %d values, want 2", len(result.Completion.Values))
	}
}

// TestCompletionCompleteBadParams verifies that completion/complete returns an
// invalid params error when the request body cannot be parsed.
func TestCompletionCompleteBadParams(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "completion/complete",
		Params:  json.RawMessage(`not json`),
	})

	if resp.Error == nil {
		t.Fatal("expected error for bad params")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
}
