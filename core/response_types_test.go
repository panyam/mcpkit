package core

import (
	"encoding/json"
	"testing"
)

// TestInitializeResultJSON verifies that InitializeResult serializes to the
// exact JSON wire format expected by MCP clients: {"protocolVersion":...,
// "capabilities":{...}, "serverInfo":{...}}. This ensures the typed struct
// produces identical output to the map[string]any it replaces.
func TestInitializeResultJSON(t *testing.T) {
	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCapabilities{
			Tools:     &ToolsCap{ListChanged: true},
			Resources: &ResourcesCap{Subscribe: true, ListChanged: true},
			Prompts:   &PromptsCap{ListChanged: true},
			Logging:   &struct{}{},
			Extensions: map[string]ExtensionCapability{
				"io.example/ext": {SpecVersion: "2025-01-01", Stability: "stable"},
			},
		},
		ServerInfo: ServerInfo{Name: "test-server", Version: "1.0"},
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Round-trip: unmarshal into a generic map to verify key names
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}

	if m["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", m["protocolVersion"])
	}
	si := m["serverInfo"].(map[string]any)
	if si["name"] != "test-server" {
		t.Errorf("serverInfo.name = %v, want test-server", si["name"])
	}

	caps := m["capabilities"].(map[string]any)
	tools := caps["tools"].(map[string]any)
	if tools["listChanged"] != true {
		t.Error("capabilities.tools.listChanged should be true")
	}

	exts := caps["extensions"].(map[string]any)
	ext := exts["io.example/ext"].(map[string]any)
	if ext["specVersion"] != "2025-01-01" {
		t.Errorf("extension specVersion = %v", ext["specVersion"])
	}

	// Round-trip back into typed struct
	var parsed InitializeResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("round-trip unmarshal failed: %v", err)
	}
	if parsed.ProtocolVersion != result.ProtocolVersion {
		t.Errorf("round-trip protocolVersion = %q, want %q", parsed.ProtocolVersion, result.ProtocolVersion)
	}
	if parsed.ServerInfo.Name != result.ServerInfo.Name {
		t.Errorf("round-trip serverInfo.name = %q, want %q", parsed.ServerInfo.Name, result.ServerInfo.Name)
	}
	if parsed.Capabilities.Extensions["io.example/ext"].SpecVersion != "2025-01-01" {
		t.Error("round-trip lost extension data")
	}
}

// TestToolsListResultJSON verifies that ToolsListResult serializes to
// {"tools":[...]} matching the MCP tools/list wire format.
func TestToolsListResultJSON(t *testing.T) {
	result := ToolsListResult{
		Tools: []ToolDef{
			{Name: "echo", Description: "Echoes input"},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	if _, ok := m["tools"]; !ok {
		t.Fatal("missing 'tools' key")
	}
	var parsed ToolsListResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if len(parsed.Tools) != 1 || parsed.Tools[0].Name != "echo" {
		t.Errorf("round-trip tools = %+v", parsed.Tools)
	}
}

// TestResourcesListResultJSON verifies that ResourcesListResult serializes to
// {"resources":[...]} matching the MCP resources/list wire format.
func TestResourcesListResultJSON(t *testing.T) {
	result := ResourcesListResult{
		Resources: []ResourceDef{
			{URI: "test://info", Name: "Info"},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	if _, ok := m["resources"]; !ok {
		t.Fatal("missing 'resources' key")
	}
	var parsed ResourcesListResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if len(parsed.Resources) != 1 || parsed.Resources[0].URI != "test://info" {
		t.Errorf("round-trip resources = %+v", parsed.Resources)
	}
}

// TestResourceTemplatesListResultJSON verifies that ResourceTemplatesListResult
// serializes with the key "resourceTemplates" (camelCase per MCP spec).
func TestResourceTemplatesListResultJSON(t *testing.T) {
	result := ResourceTemplatesListResult{
		ResourceTemplates: []ResourceTemplate{
			{URITemplate: "test://items/{id}", Name: "Item"},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	if _, ok := m["resourceTemplates"]; !ok {
		t.Fatal("missing 'resourceTemplates' key")
	}
}

// TestPromptsListResultJSON verifies that PromptsListResult serializes to
// {"prompts":[...]} matching the MCP prompts/list wire format.
func TestPromptsListResultJSON(t *testing.T) {
	result := PromptsListResult{
		Prompts: []PromptDef{
			{Name: "greet", Description: "Greeting prompt"},
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var parsed PromptsListResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if len(parsed.Prompts) != 1 || parsed.Prompts[0].Name != "greet" {
		t.Errorf("round-trip prompts = %+v", parsed.Prompts)
	}
}

// TestCompletionCompleteResultJSON verifies that CompletionCompleteResult
// serializes to {"completion":{...}} matching the MCP completion/complete format.
func TestCompletionCompleteResultJSON(t *testing.T) {
	result := CompletionCompleteResult{
		Completion: CompletionResult{
			Values:  []string{"foo", "bar"},
			HasMore: true,
		},
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m)
	if _, ok := m["completion"]; !ok {
		t.Fatal("missing 'completion' key")
	}
	var parsed CompletionCompleteResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("round-trip failed: %v", err)
	}
	if len(parsed.Completion.Values) != 2 || !parsed.Completion.HasMore {
		t.Errorf("round-trip completion = %+v", parsed.Completion)
	}
}

// TestPingResultJSON verifies that PingResult serializes to {} (empty object),
// identical to the map[string]any{} it replaces.
func TestPingResultJSON(t *testing.T) {
	raw, err := json.Marshal(PingResult{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("PingResult JSON = %q, want {}", string(raw))
	}
}

// TestEmptyResultJSON verifies that struct{}{} serializes to {} (empty object),
// matching the map[string]any{} used for subscribe/unsubscribe/setLevel responses.
func TestEmptyResultJSON(t *testing.T) {
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("struct{}{} JSON = %q, want {}", string(raw))
	}
}
