package ui

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestRegisterTypedAppToolInputSchemaOverride verifies the issue-542 escape
// hatch: when InputSchemaOverride is set on TypedAppToolConfig, the auto-
// derived schema from the In type parameter is bypassed and the override
// flows through to the final core.ToolDef.InputSchema. Necessary for fixtures
// whose default / description values contain commas (invopop's tag parser
// truncates at the first comma).
func TestRegisterTypedAppToolInputSchemaOverride(t *testing.T) {
	type fakeInput struct {
		ABCNotation string `json:"abcNotation,omitempty"`
	}

	// Override has a multi-comma default that struct tags would mangle.
	overrideSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"abcNotation": map[string]any{
				"type":        "string",
				"default":     "X:1\nT:Twinkle, Twinkle Little Star",
				"description": "ABC notation, free-form, comma-laden",
			},
		},
	}

	reg := &mockRegistrar{}
	RegisterTypedAppTool(reg, TypedAppToolConfig[fakeInput, string]{
		Name:                "play-sheet-music",
		Title:               "Play Sheet Music",
		Description:         "Plays ABC.",
		InputSchemaOverride: overrideSchema,
		Handler: func(ctx core.ToolContext, _ fakeInput) (string, error) {
			return "ok", nil
		},
		ResourceURI: "ui://sheet-music/mcp-app.html",
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
	})

	if len(reg.tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(reg.tools))
	}
	td := reg.tools[0]

	// Round-trip InputSchema through JSON so both sides come out as the same
	// map shape regardless of declaration order.
	gotBytes, err := json.Marshal(td.InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatalf("unmarshal InputSchema: %v", err)
	}

	wantBytes, _ := json.Marshal(overrideSchema)
	var want map[string]any
	_ = json.Unmarshal(wantBytes, &want)

	if string(gotBytes) != string(wantBytes) {
		t.Errorf("InputSchema = %s, want %s", gotBytes, wantBytes)
	}

	// The override must preserve the multi-comma default verbatim — that's
	// the whole reason this escape hatch exists.
	props := got["properties"].(map[string]any)
	abc := props["abcNotation"].(map[string]any)
	if abc["default"] != "X:1\nT:Twinkle, Twinkle Little Star" {
		t.Errorf("default = %q, want full comma-bearing string", abc["default"])
	}
	if abc["description"] != "ABC notation, free-form, comma-laden" {
		t.Errorf("description = %q, want full comma-bearing string", abc["description"])
	}
}

// TestRegisterTypedAppToolNoOverrideUsesReflection sanity-checks that when
// InputSchemaOverride is nil the path falls back to invopop reflection on
// the In type parameter (existing behavior, unchanged by issue 542).
func TestRegisterTypedAppToolNoOverrideUsesReflection(t *testing.T) {
	type simpleInput struct {
		Name string `json:"name" jsonschema:"required"`
	}

	reg := &mockRegistrar{}
	RegisterTypedAppTool(reg, TypedAppToolConfig[simpleInput, string]{
		Name:        "echo",
		Description: "Echoes the name.",
		Handler: func(ctx core.ToolContext, in simpleInput) (string, error) {
			return in.Name, nil
		},
		ResourceURI: "ui://echo/mcp-app.html",
		ResourceHandler: func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
			return core.ResourceResult{}, nil
		},
	})

	if len(reg.tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(reg.tools))
	}
	gotBytes, err := json.Marshal(reg.tools[0].InputSchema)
	if err != nil {
		t.Fatalf("marshal InputSchema: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(gotBytes, &got); err != nil {
		t.Fatalf("unmarshal InputSchema: %v", err)
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("InputSchema has no properties, raw: %s", gotBytes)
	}
	if _, ok := props["name"]; !ok {
		t.Errorf("expected 'name' property from reflection, got %v", props)
	}
}
