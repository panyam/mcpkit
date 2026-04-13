package core

import (
	"encoding/json"
	"testing"
)

// TestElicitationRequestMetaSerialization verifies that ElicitationRequest
// serializes _meta.ui correctly: present when Meta is set, absent when nil.
func TestElicitationRequestMetaSerialization(t *testing.T) {
	t.Run("with meta", func(t *testing.T) {
		req := ElicitationRequest{
			Message:         "Which pizza?",
			RequestedSchema: json.RawMessage(`{"type":"object"}`),
			Meta: &ElicitationMeta{
				UI: &UIMetadata{ResourceUri: "ui://pizzas/picker"},
			},
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["_meta"]; !ok {
			t.Fatal("expected _meta key in JSON output")
		}

		// Round-trip
		var got ElicitationRequest
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Meta == nil || got.Meta.UI == nil {
			t.Fatal("Meta or Meta.UI is nil after round-trip")
		}
		if got.Meta.UI.ResourceUri != "ui://pizzas/picker" {
			t.Errorf("ResourceUri = %q, want %q", got.Meta.UI.ResourceUri, "ui://pizzas/picker")
		}
	})

	t.Run("nil meta omitted", func(t *testing.T) {
		req := ElicitationRequest{
			Message: "Pick one",
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		if _, ok := raw["_meta"]; ok {
			t.Errorf("_meta should be absent when Meta is nil, got %s", raw["_meta"])
		}
	})
}
