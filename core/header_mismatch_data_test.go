package core

import (
	"encoding/json"
	"testing"
)

// HeaderMismatchData is the typed decoder for -32001 error responses.
// Both producer surfaces (SEP-2243 routing-header validation in
// server/header_validation.go and SEP-2575 protocol-version cross-check)
// emit the same wire shape; these tests pin the round trip both ways.

func TestHeaderMismatchData_MarshalFlattensExtra(t *testing.T) {
	d := HeaderMismatchData{
		Reason:   "Mcp-Method header value does not match request body",
		Header:   "Mcp-Method",
		Expected: "tools/call",
		Received: "tools/list",
		Extra: map[string]any{
			"tool": "create_cart",
		},
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out["reason"] != d.Reason || out["header"] != d.Header {
		t.Errorf("fixed fields not on top-level; got %s", raw)
	}
	if out["tool"] != "create_cart" {
		t.Errorf("Extra not flattened; got %s", raw)
	}
}

func TestHeaderMismatchData_FixedFieldsWinOverExtra(t *testing.T) {
	// A caller that accidentally shadows a fixed field in Extra should
	// not corrupt the wire shape.
	d := HeaderMismatchData{
		Reason: "official reason",
		Extra:  map[string]any{"reason": "shadow"},
	}
	raw, _ := json.Marshal(d)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["reason"] != "official reason" {
		t.Errorf("Extra clobbered fixed field; got %s", raw)
	}
}

// TestHeaderMismatchData_DecodeFromServerWire feeds the exact wire shape
// server/header_validation.go (PR #477) emits and confirms the typed
// decoder pulls every field cleanly — including the variadic extras
// (passed as Mcp-Method's `header`/`expected`/`received` plus a tool
// name).
func TestHeaderMismatchData_DecodeFromServerWire(t *testing.T) {
	// What server/header_validation.go writes for a tool-name mismatch:
	wire := []byte(`{
		"reason":   "Mcp-Name header value does not match request body",
		"header":   "Mcp-Name",
		"expected": "create_cart",
		"received": "add_item",
		"tool":     "add_item"
	}`)
	var d HeaderMismatchData
	if err := json.Unmarshal(wire, &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Header != "Mcp-Name" {
		t.Errorf("Header = %q, want Mcp-Name", d.Header)
	}
	if d.Expected != "create_cart" || d.Received != "add_item" {
		t.Errorf("Expected/Received decode wrong: %+v", d)
	}
	if d.Extra == nil || d.Extra["tool"] != "add_item" {
		t.Errorf("Extra missing tool key: %+v", d.Extra)
	}
}

// TestHeaderMismatchData_DecodeWithoutExtras handles the SEP-2575
// version-mismatch emit shape (no extras attached).
func TestHeaderMismatchData_DecodeWithoutExtras(t *testing.T) {
	wire := []byte(`{
		"reason":   "MCP-Protocol-Version header value does not match request body",
		"header":   "MCP-Protocol-Version",
		"expected": "DRAFT-2026-v1",
		"received": "v999.0.0"
	}`)
	var d HeaderMismatchData
	if err := json.Unmarshal(wire, &d); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if d.Expected != "DRAFT-2026-v1" {
		t.Errorf("Expected = %q, want DRAFT-2026-v1", d.Expected)
	}
	if d.Extra != nil {
		t.Errorf("Extra populated when wire carried no extras: %+v", d.Extra)
	}
}
