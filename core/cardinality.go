package core

import (
	"bytes"
	"encoding/json"
)

// Content-field cardinality helpers (#81).
//
// Several MCP message types have a "content" (or similarly-named) field whose
// JSON representation varies in the wild even though the 2025-11-25 spec picks
// one cardinality per message. We defensively parse both forms on read and
// always emit the spec-canonical form on write.
//
// The helpers below are the single place cardinality tolerance is defined.
// Future widening work (e.g., #141 — SamplingMessage carrying multiple content
// blocks) replaces a call to decodeContentSingle with decodeContentSlice at
// the call site; the logic here does not need to change.
//
// Why package-level functions rather than methods on Content / []Content: the
// Content type itself needs cardinality tolerance on its embedded `resource`
// field, so a method-on-Content implementation would need to call its own
// helper — awkward layering. Flat helpers keep the dependency graph simple.

// isJSONArray reports whether raw is a JSON array (first non-whitespace byte
// is '['). Used to branch between single-object and array decode paths.
func isJSONArray(raw json.RawMessage) bool {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	return len(raw) > 0 && raw[0] == '['
}

// decodeContentSingle reads a JSON value that is either a Content object or a
// Content array, and stores the first element (or zero value for empty array)
// into dst. Used by PromptMessage, SamplingMessage, and CreateMessageResult
// where the spec expects a single ContentBlock but tolerant parsing is still
// required. len(raw)==0 is treated as "field absent" and dst is left zero.
func decodeContentSingle(raw json.RawMessage, dst *Content) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if !isJSONArray(raw) {
		return json.Unmarshal(raw, dst)
	}
	var arr []Content
	if err := json.Unmarshal(raw, &arr); err != nil {
		return err
	}
	if len(arr) > 0 {
		*dst = arr[0]
	}
	return nil
}

// decodeContentSlice reads a JSON value that is either a single Content object
// or a Content array, and stores the result as a slice. Used by ToolResult
// where the spec says the field is always an array, but we accept a single
// object for cross-peer tolerance. Object form wraps into a 1-element slice.
func decodeContentSlice(raw json.RawMessage, dst *[]Content) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if isJSONArray(raw) {
		return json.Unmarshal(raw, dst)
	}
	var single Content
	if err := json.Unmarshal(raw, &single); err != nil {
		return err
	}
	*dst = []Content{single}
	return nil
}

// decodeResourceReadSlice is the ResourceReadContent analogue of
// decodeContentSlice, for ResourceResult.Contents.
func decodeResourceReadSlice(raw json.RawMessage, dst *[]ResourceReadContent) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if isJSONArray(raw) {
		return json.Unmarshal(raw, dst)
	}
	var single ResourceReadContent
	if err := json.Unmarshal(raw, &single); err != nil {
		return err
	}
	*dst = []ResourceReadContent{single}
	return nil
}

// decodeResourceContentSingle reads a JSON value that is either a single
// ResourceContent object or an array, and stores the first element into dst.
// Used by Content.UnmarshalJSON to tolerate array-form `resource` fields
// emitted by peers that confuse EmbeddedResource with ReadResourceResult.
func decodeResourceContentSingle(raw json.RawMessage, dst **ResourceContent) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if !isJSONArray(raw) {
		var rc ResourceContent
		if err := json.Unmarshal(raw, &rc); err != nil {
			return err
		}
		*dst = &rc
		return nil
	}
	var arr []ResourceContent
	if err := json.Unmarshal(raw, &arr); err != nil {
		return err
	}
	if len(arr) > 0 {
		rc := arr[0]
		*dst = &rc
	}
	return nil
}
