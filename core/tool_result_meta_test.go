package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolResultMetaExtras_RoundTrip verifies the Extras map's wire shape:
// extension-namespaced keys spread at the top level of the _meta object
// alongside typed fields, survive marshal+unmarshal, and don't collide
// with the closed typed fields.
//
// This is the wire contract pdf-server (and any future extension fixture)
// relies on to emit per-tool metadata like `interactEnabled` or
// `vendor.com/feature: ...` without library changes per fixture.
func TestToolResultMetaExtras_RoundTrip(t *testing.T) {
	m := ToolResultMeta{
		NextCursor: "next-page-token",
		Extras: map[string]any{
			"interactEnabled":      true,
			"viewUUID":             "abc-123",
			"vendor.com/foo":       "bar",
			"vendor.com/structured": map[string]any{"k": "v"},
		},
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// All keys must appear at the top level — extras spread, they don't nest.
	got := string(data)
	for _, want := range []string{
		`"interactEnabled":true`,
		`"viewUUID":"abc-123"`,
		`"vendor.com/foo":"bar"`,
		`"nextCursor":"next-page-token"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("marshal output missing %q\ngot: %s", want, got)
		}
	}

	// Round-trip: unmarshal the bytes, verify typed fields land on the typed
	// fields and unknown keys land in Extras.
	var back ToolResultMeta
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.NextCursor != "next-page-token" {
		t.Errorf("NextCursor lost: %q", back.NextCursor)
	}
	if back.Extras["interactEnabled"] != true {
		t.Errorf("Extras[interactEnabled] = %v, want true", back.Extras["interactEnabled"])
	}
	if back.Extras["viewUUID"] != "abc-123" {
		t.Errorf("Extras[viewUUID] = %v, want abc-123", back.Extras["viewUUID"])
	}
}

// TestToolResultMetaExtras_TypedFieldsWin verifies that if the caller sets
// both a typed field (NextCursor) and an Extras key with the same JSON name,
// the typed field wins on the wire. This prevents accidental shadowing of
// spec fields by extension data.
func TestToolResultMetaExtras_TypedFieldsWin(t *testing.T) {
	m := ToolResultMeta{
		NextCursor: "typed-wins",
		Extras: map[string]any{
			"nextCursor": "shadow-attempt",
		},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["nextCursor"] != "typed-wins" {
		t.Errorf("typed NextCursor did not override extras: got %v", back["nextCursor"])
	}
}

// TestToolResultMeta_EmitsOnToolResult verifies the Extras flow end-to-end
// through ToolResult marshal — the wire shape pdf-server emits.
func TestToolResultMeta_EmitsOnToolResult(t *testing.T) {
	r := ToolResult{
		Content: []Content{{Type: "text", Text: "ok"}},
		Meta: &ToolResultMeta{
			Extras: map[string]any{"interactEnabled": true},
		},
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"_meta":{"interactEnabled":true}`) {
		t.Errorf("expected _meta.interactEnabled in wire output, got: %s", data)
	}
}

// TestToolResultMeta_OmittedWhenEmpty verifies an empty meta object doesn't
// pollute the wire — the existing omitempty contract on Meta still holds.
func TestToolResultMeta_OmittedWhenEmpty(t *testing.T) {
	r := ToolResult{Content: []Content{{Type: "text", Text: "ok"}}}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), `"_meta"`) {
		t.Errorf("expected no _meta field for empty meta, got: %s", data)
	}
}
