package server

import (
	core "github.com/panyam/mcpkit/core"
	"context"
	"encoding/json"
	"testing"
)

// testPromptDispatcher creates an initialized dispatcher with test prompts.
func testPromptDispatcher() *Dispatcher {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterPrompt(
		core.PromptDef{
			Name:        "greet",
			Description: "Greeting prompt",
			Arguments: []core.PromptArgument{
				{Name: "name", Description: "Name to greet", Required: true},
			},
		},
		func(ctx context.Context, req core.PromptRequest) (core.PromptResult, error) {
			name, _ := req.Arguments["name"].(string)
			return core.PromptResult{
				Description: "Greeting",
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "Hello, " + name + "!"},
				}},
			}, nil
		},
	)
	d.RegisterPrompt(
		core.PromptDef{Name: "simple", Description: "No-args prompt"},
		func(ctx context.Context, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "Simple prompt text"},
				}},
			}, nil
		},
	)
	initDispatcher(d)
	return d
}

// TestPromptsList verifies that prompts/list returns all registered prompts
// in registration order.
func TestPromptsList(t *testing.T) {
	d := testPromptDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/list",
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result struct {
		Prompts []core.PromptDef `json:"prompts"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Prompts) != 2 {
		t.Fatalf("got %d prompts, want 2", len(result.Prompts))
	}
	if result.Prompts[0].Name != "greet" {
		t.Errorf("first prompt = %q, want greet", result.Prompts[0].Name)
	}
}

// TestPromptsListEmpty verifies that prompts/list returns an empty list
// when no prompts are registered.
func TestPromptsListEmpty(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	initDispatcher(d)
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/list",
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result struct {
		Prompts []core.PromptDef `json:"prompts"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Prompts) != 0 {
		t.Errorf("got %d prompts, want 0", len(result.Prompts))
	}
}

// TestPromptsGetSimple verifies that prompts/get returns messages for a
// prompt without arguments.
func TestPromptsGetSimple(t *testing.T) {
	d := testPromptDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/get",
		Params: json.RawMessage(`{"name":"simple"}`),
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result core.PromptResult
	json.Unmarshal(resp.Result, &result)
	if len(result.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(result.Messages))
	}
	if result.Messages[0].Content.Text != "Simple prompt text" {
		t.Errorf("text = %q", result.Messages[0].Content.Text)
	}
}

// TestPromptsGetWithArgs verifies that prompts/get passes arguments to the
// handler and returns the interpolated result.
func TestPromptsGetWithArgs(t *testing.T) {
	d := testPromptDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/get",
		Params: json.RawMessage(`{"name":"greet","arguments":{"name":"World"}}`),
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	var result core.PromptResult
	json.Unmarshal(resp.Result, &result)
	if result.Messages[0].Content.Text != "Hello, World!" {
		t.Errorf("text = %q, want Hello, World!", result.Messages[0].Content.Text)
	}
}

// TestPromptsGetUnknown verifies that prompts/get returns an error for
// an unknown prompt name.
func TestPromptsGetUnknown(t *testing.T) {
	d := testPromptDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/get",
		Params: json.RawMessage(`{"name":"nonexistent"}`),
	})
	if resp.Error == nil {
		t.Fatal("expected error for unknown prompt")
	}
	if resp.Error.Code != core.ErrCodeInvalidParams {
		t.Errorf("code = %d, want %d", resp.Error.Code, core.ErrCodeInvalidParams)
	}
}

// TestPromptsCapabilities verifies that the initialize response includes
// prompts capability when prompts are registered.
func TestPromptsCapabilities(t *testing.T) {
	d := testPromptDispatcher()
	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize",
		Params: json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
	})
	var result map[string]any
	json.Unmarshal(resp.Result, &result)
	caps := result["capabilities"].(map[string]any)
	if _, ok := caps["prompts"]; !ok {
		t.Error("capabilities missing 'prompts'")
	}
}
