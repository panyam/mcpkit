package agent

import (
	"encoding/json"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// TestModuleWiring guards the sub-module seam itself: it fails if the replace
// directive or go.sum drifts out of sync with the root module (the documented
// failure mode for mcpkit sub-modules). Constraint A2 is carried by the event
// and delta round-trip tests in runner_test.go and provider_test.go.
func TestModuleWiring(t *testing.T) {
	res := core.ToolResult{Content: []core.Content{{Type: "text", Text: "ok"}}}

	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal core.ToolResult through the agent module: %v", err)
	}

	var back core.ToolResult
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal round-trip: %v", err)
	}
	if len(back.Content) != 1 || back.Content[0].Text != "ok" {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
}
