package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// TestPromptsListOmitsArgumentSchema checks that prompts/list never carries
// PromptArgument.Schema. It is server-side validation config, and no MCP
// schema defines it on the wire (#1562).
func TestPromptsListOmitsArgumentSchema(t *testing.T) {
	d := NewDispatcher(core.ServerInfo{Name: "test", Version: "1.0"})
	d.RegisterPrompt(
		core.PromptDef{
			Name: "filter",
			Arguments: []core.PromptArgument{{
				Name:     "limit",
				Required: true,
				Schema:   map[string]any{"type": "integer", "minimum": float64(1)},
			}},
		},
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
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
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"name":"limit"`) {
		t.Fatalf("argument missing from prompts/list: %s", raw)
	}
	if strings.Contains(string(raw), `"schema"`) {
		t.Errorf("prompts/list advertises schema: %s", raw)
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
		func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
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
		Params: core.NewRawJSON(json.RawMessage(`{"name":"q","arguments":{"limit":42,"verbose":true}}`)),
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
