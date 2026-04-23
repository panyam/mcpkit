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

// TestElicitURLModeSerialization verifies that Mode, URL, and ElicitationID
// fields round-trip correctly in URL-mode elicitation requests (SEP-1036).
func TestElicitURLModeSerialization(t *testing.T) {
	req := ElicitationRequest{
		Message:       "Please approve access",
		Mode:          ElicitModeURL,
		URL:           "https://example.com/approve",
		ElicitationID: "el_12345",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify JSON contains the expected fields.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mode", "url", "elicitationId"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("expected %q key in JSON output", key)
		}
	}
	// requestedSchema must NOT be present for URL mode.
	if _, ok := raw["requestedSchema"]; ok {
		t.Error("requestedSchema should be absent for URL-mode request")
	}

	// Round-trip.
	var got ElicitationRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != ElicitModeURL {
		t.Errorf("Mode = %q, want %q", got.Mode, ElicitModeURL)
	}
	if got.URL != "https://example.com/approve" {
		t.Errorf("URL = %q, want %q", got.URL, "https://example.com/approve")
	}
	if got.ElicitationID != "el_12345" {
		t.Errorf("ElicitationID = %q, want %q", got.ElicitationID, "el_12345")
	}
}

// TestElicitFormModeDefault verifies that omitted Mode field is treated
// as form mode (backwards compatibility with pre-SEP-1036 requests).
func TestElicitFormModeDefault(t *testing.T) {
	req := ElicitationRequest{
		Message:         "Pick a color",
		RequestedSchema: json.RawMessage(`{"type":"object"}`),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// mode field should be absent (omitempty).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["mode"]; ok {
		t.Error("mode should be absent when empty (form is default)")
	}

	// Round-trip: Mode should be empty string (treated as form).
	var got ElicitationRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "" {
		t.Errorf("Mode = %q, want empty (implicit form)", got.Mode)
	}
}

// TestElicitationCompleteParamsSerialization verifies that the completion
// notification params serialize correctly (SEP-1036).
func TestElicitationCompleteParamsSerialization(t *testing.T) {
	p := ElicitationCompleteParams{ElicitationID: "el_abc123"}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var got ElicitationCompleteParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ElicitationID != "el_abc123" {
		t.Errorf("ElicitationID = %q, want %q", got.ElicitationID, "el_abc123")
	}
}

// TestElicitationCapSerialization verifies that ElicitationCap marshals
// to the expected wire format for form-only and form+url modes.
func TestElicitationCapSerialization(t *testing.T) {
	t.Run("form only", func(t *testing.T) {
		cap := ElicitationCap{Form: &ElicitationFormCap{}}
		data, err := json.Marshal(cap)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		json.Unmarshal(data, &raw)
		if _, ok := raw["form"]; !ok {
			t.Error("expected 'form' key")
		}
		if _, ok := raw["url"]; ok {
			t.Error("url should be absent for form-only cap")
		}
	})

	t.Run("form and url", func(t *testing.T) {
		cap := ElicitationCap{Form: &ElicitationFormCap{}, URL: &ElicitationURLCap{}}
		data, err := json.Marshal(cap)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]json.RawMessage
		json.Unmarshal(data, &raw)
		if _, ok := raw["form"]; !ok {
			t.Error("expected 'form' key")
		}
		if _, ok := raw["url"]; !ok {
			t.Error("expected 'url' key")
		}
	})
}

// TestURLElicitationRequiredErrorData verifies the -32042 error data
// serialization, including the Extra map for FineGrainedAuth composability.
func TestURLElicitationRequiredErrorData(t *testing.T) {
	data := URLElicitationRequiredErrorData{
		Elicitations: []ElicitationRequest{
			{
				Mode:          ElicitModeURL,
				Message:       "Approve access",
				URL:           "https://example.com/approve",
				ElicitationID: "el_001",
			},
		},
		Extra: map[string]any{
			"authorization": map[string]any{
				"reason":                 "insufficient_authorization",
				"authorizationContextId": "authzctx_abc",
			},
		},
	}

	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}

	// elicitations array must be present.
	if _, ok := m["elicitations"]; !ok {
		t.Fatal("expected 'elicitations' key")
	}

	// Extra keys must be flattened to top level.
	if _, ok := m["authorization"]; !ok {
		t.Fatal("expected 'authorization' key (flattened from Extra)")
	}

	// Verify the error response constructor.
	resp := NewURLElicitationRequiredError(json.RawMessage(`1`), "Approval needed", data)
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != ErrCodeURLElicitationRequired {
		t.Errorf("code = %d, want %d", resp.Error.Code, ErrCodeURLElicitationRequired)
	}
}
