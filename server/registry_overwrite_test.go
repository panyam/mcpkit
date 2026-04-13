package server

// Registry overwrite tests (issue #80).
//
// These tests verify that re-registering a tool/resource/template/prompt with
// the same identity key overwrites the entry WITHOUT creating duplicates in
// the ordering slice. Before the fix, Add* methods unconditionally appended
// to the order slice, causing duplicate entries in list responses.

import (
	"context"
	"encoding/json"
	"testing"

	core "github.com/panyam/mcpkit/core"
)

// TestAddTool_OverwriteDoesNotDuplicateOrder registers a tool twice with the
// same name and verifies that tools/list returns it exactly once. Before the
// fix, the second AddTool call would append a duplicate to toolOrder, causing
// the tool to appear twice in list responses.
func TestAddTool_OverwriteDoesNotDuplicateOrder(t *testing.T) {
	d := testDynamicDispatcher()

	handler := func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResult, error) {
		return core.TextResult("v1"), nil
	}
	d.Reg.AddTool(core.ToolDef{Name: "dup", Description: "first"}, handler)
	d.Reg.AddTool(core.ToolDef{Name: "dup", Description: "second"}, handler)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/list",
	})
	assertToolCount(t, resp, 1)
}

// TestAddResource_OverwriteDoesNotDuplicateOrder registers a resource twice
// with the same URI and verifies that resources/list returns it exactly once.
func TestAddResource_OverwriteDoesNotDuplicateOrder(t *testing.T) {
	d := testDynamicDispatcher()

	handler := func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
		return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: req.URI, Text: "v1"}}}, nil
	}
	d.Reg.AddResource(core.ResourceDef{URI: "test://dup", Name: "dup"}, handler)
	d.Reg.AddResource(core.ResourceDef{URI: "test://dup", Name: "dup-updated"}, handler)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/list",
	})
	if resp.Error != nil {
		t.Fatalf("resources/list failed: %s", resp.Error.Message)
	}
	raw, _ := json.Marshal(resp.Result)
	var result struct {
		Resources []core.ResourceDef `json:"resources"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(result.Resources))
	}
}

// TestAddResourceTemplate_OverwriteDoesNotDuplicateOrder registers a resource
// template twice with the same URI template and verifies that
// resources/templates/list returns it exactly once. The URI template string
// serves as the unique identifier per issue #80.
func TestAddResourceTemplate_OverwriteDoesNotDuplicateOrder(t *testing.T) {
	d := testDynamicDispatcher()

	handler := func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
		return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: uri, Text: "v1"}}}, nil
	}
	d.Reg.AddResourceTemplate(core.ResourceTemplate{URITemplate: "test://items/{id}", Name: "first"}, handler)
	d.Reg.AddResourceTemplate(core.ResourceTemplate{URITemplate: "test://items/{id}", Name: "second"}, handler)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/templates/list",
	})
	if resp.Error != nil {
		t.Fatalf("resources/templates/list failed: %s", resp.Error.Message)
	}
	raw, _ := json.Marshal(resp.Result)
	var result struct {
		ResourceTemplates []core.ResourceTemplate `json:"resourceTemplates"`
	}
	json.Unmarshal(raw, &result)
	if len(result.ResourceTemplates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(result.ResourceTemplates))
	}
}

// TestAddPrompt_OverwriteDoesNotDuplicateOrder registers a prompt twice with
// the same name and verifies that prompts/list returns it exactly once.
func TestAddPrompt_OverwriteDoesNotDuplicateOrder(t *testing.T) {
	d := testDynamicDispatcher()

	handler := func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResult, error) {
		return core.PromptResult{}, nil
	}
	d.Reg.AddPrompt(core.PromptDef{Name: "dup", Description: "first"}, handler)
	d.Reg.AddPrompt(core.PromptDef{Name: "dup", Description: "second"}, handler)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "prompts/list",
	})
	if resp.Error != nil {
		t.Fatalf("prompts/list failed: %s", resp.Error.Message)
	}
	raw, _ := json.Marshal(resp.Result)
	var result struct {
		Prompts []core.PromptDef `json:"prompts"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(result.Prompts))
	}
}

// TestAddResourceTemplate_OverwriteUpdatesHandler registers a resource template,
// overwrites it with a new handler, and verifies that resources/read dispatches
// to the updated handler. This confirms that overwrite replaces the handler,
// not just the definition metadata.
func TestAddResourceTemplate_OverwriteUpdatesHandler(t *testing.T) {
	d := testDynamicDispatcher()

	d.Reg.AddResourceTemplate(
		core.ResourceTemplate{URITemplate: "test://items/{id}", Name: "items"},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: uri, Text: "v1"}}}, nil
		},
	)
	// Overwrite with new handler
	d.Reg.AddResourceTemplate(
		core.ResourceTemplate{URITemplate: "test://items/{id}", Name: "items"},
		func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
			return core.ResourceResult{Contents: []core.ResourceReadContent{{URI: uri, Text: "v2"}}}, nil
		},
	)

	resp := d.Dispatch(context.Background(), &core.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "resources/read",
		Params: json.RawMessage(`{"uri":"test://items/42"}`),
	})
	if resp.Error != nil {
		t.Fatalf("resources/read failed: %s", resp.Error.Message)
	}
	raw, _ := json.Marshal(resp.Result)
	var result struct {
		Contents []core.ResourceReadContent `json:"contents"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Contents) == 0 || result.Contents[0].Text != "v2" {
		t.Fatalf("expected v2 from overwritten handler, got: %v", result.Contents)
	}
}
