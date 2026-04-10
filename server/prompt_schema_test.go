package server

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// TestPromptsListAdvertisesSchema verifies that a PromptArgument with a Schema
// field is advertised verbatim through prompts/list. Clients rely on this to
// render typed inputs — dropping the schema silently would defeat the feature.
// Issue #87.
func TestPromptsListAdvertisesSchema(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterPrompt(
		core.PromptDef{
			Name: "filter",
			Arguments: []core.PromptArgument{
				{
					Name:     "limit",
					Required: true,
					Schema: map[string]any{
						"type":    "integer",
						"minimum": float64(1),
					},
				},
			},
		},
		func(ctx context.Context, req core.PromptRequest) (core.PromptResult, error) {
			return core.PromptResult{}, nil
		},
	)
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
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Prompts) != 1 || len(result.Prompts[0].Arguments) != 1 {
		t.Fatalf("unexpected prompts shape: %+v", result.Prompts)
	}
	arg := result.Prompts[0].Arguments[0]
	schema, ok := arg.Schema.(map[string]any)
	if !ok {
		t.Fatalf("schema = %T, want map[string]any — not advertised", arg.Schema)
	}
	if schema["type"] != "integer" {
		t.Errorf("schema.type = %v, want integer", schema["type"])
	}
}

// TestPromptsGetPassesTypedArguments verifies that prompts/get forwards
// non-string JSON values (numbers, booleans, nested objects) to the handler
// via PromptRequest.Arguments. Issue #87: widening from map[string]string to
// map[string]any is the only way schema'd arguments (integers, enums) reach
// handlers meaningfully.
func TestPromptsGetPassesTypedArguments(t *testing.T) {
	var captured map[string]any
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterPrompt(
		core.PromptDef{
			Name: "q",
			Arguments: []core.PromptArgument{
				{Name: "limit", Schema: map[string]any{"type": "integer"}},
				{Name: "verbose", Schema: map[string]any{"type": "boolean"}},
			},
		},
		func(ctx context.Context, req core.PromptRequest) (core.PromptResult, error) {
			captured = req.Arguments
			return core.PromptResult{
				Messages: []core.PromptMessage{{
					Role:    "user",
					Content: core.Content{Type: "text", Text: "ok"},
				}},
			}, nil
		},
	)
	initDispatcher(d)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/get",
		Params: json.RawMessage(`{"name":"q","arguments":{"limit":42,"verbose":true}}`),
	})
	if resp.Error != nil {
		t.Fatalf("error: %s", resp.Error.Message)
	}
	if captured == nil {
		t.Fatal("handler not invoked")
	}
	// JSON numbers decode to float64 by default when the destination is any.
	if captured["limit"] != float64(42) {
		t.Errorf("limit = %v (%T), want float64(42)", captured["limit"], captured["limit"])
	}
	if captured["verbose"] != true {
		t.Errorf("verbose = %v, want true", captured["verbose"])
	}
}
