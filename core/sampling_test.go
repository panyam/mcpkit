package core

import (
	"encoding/json"
	"testing"
)

// TestCreateMessageRequestMetaSerialization verifies that CreateMessageRequest
// serializes _meta.ui correctly: present when Meta is set, absent when nil.
func TestCreateMessageRequestMetaSerialization(t *testing.T) {
	t.Run("with meta", func(t *testing.T) {
		req := CreateMessageRequest{
			Messages:  []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "hello"}}},
			MaxTokens: 100,
			Meta: &SamplingMeta{
				UI: &UIMetadata{ResourceUri: "ui://dashboard/view"},
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
		var got CreateMessageRequest
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Meta == nil || got.Meta.UI == nil {
			t.Fatal("Meta or Meta.UI is nil after round-trip")
		}
		if got.Meta.UI.ResourceUri != "ui://dashboard/view" {
			t.Errorf("ResourceUri = %q, want %q", got.Meta.UI.ResourceUri, "ui://dashboard/view")
		}
	})

	t.Run("nil meta omitted", func(t *testing.T) {
		req := CreateMessageRequest{
			Messages:  []SamplingMessage{{Role: "user", Content: Content{Type: "text", Text: "hi"}}},
			MaxTokens: 50,
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
