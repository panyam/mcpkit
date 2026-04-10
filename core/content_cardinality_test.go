package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestPromptMessageUnmarshalSingleContent verifies that PromptMessage accepts
// the spec-canonical form where content is a single object. This is the baseline
// form; regressing it would break every existing MCP peer.
func TestPromptMessageUnmarshalSingleContent(t *testing.T) {
	data := []byte(`{"role":"user","content":{"type":"text","text":"hello"}}`)
	var m PromptMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal single-content form: %v", err)
	}
	if m.Role != "user" {
		t.Errorf("role = %q, want %q", m.Role, "user")
	}
	if m.Content.Type != "text" || m.Content.Text != "hello" {
		t.Errorf("content = %+v, want text/hello", m.Content)
	}
}

// TestPromptMessageUnmarshalArrayContent verifies that PromptMessage defensively
// accepts array-form content (taking the first element) even though the MCP
// spec specifies single form. Issue #81: when senders misinterpret the spec,
// we parse instead of failing hard. The first element wins because PromptMessage
// semantically carries one content block.
func TestPromptMessageUnmarshalArrayContent(t *testing.T) {
	data := []byte(`{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`)
	var m PromptMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal array-content form: %v", err)
	}
	if m.Content.Type != "text" || m.Content.Text != "first" {
		t.Errorf("content = %+v, want first element (text/first)", m.Content)
	}
}

// TestPromptMessageUnmarshalEmptyArrayContent verifies that an empty-array
// content field parses to a zero-value Content (not an error). Defensive
// parsing should never hard-fail on upstream bugs when a sensible default exists.
func TestPromptMessageUnmarshalEmptyArrayContent(t *testing.T) {
	data := []byte(`{"role":"user","content":[]}`)
	var m PromptMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal empty array: %v", err)
	}
	if m.Content.Type != "" {
		t.Errorf("content.type = %q, want empty", m.Content.Type)
	}
}

// TestPromptMessageMarshalAlwaysEmitsSingle verifies that when we produce a
// PromptMessage on the wire we always emit spec-canonical single form.
// Accepting both forms on read must not leak array emission on write — the
// server would be silently non-conformant.
func TestPromptMessageMarshalAlwaysEmitsSingle(t *testing.T) {
	m := PromptMessage{
		Role:    "user",
		Content: Content{Type: "text", Text: "hi"},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"content":{"type":"text"`) {
		t.Errorf("marshal emitted %s, want content as single object", s)
	}
}

// TestSamplingMessageUnmarshalSingleContent verifies that SamplingMessage
// accepts the spec-canonical single-content form. Baseline regression guard.
func TestSamplingMessageUnmarshalSingleContent(t *testing.T) {
	data := []byte(`{"role":"user","content":{"type":"text","text":"hello"}}`)
	var m SamplingMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal single-content form: %v", err)
	}
	if m.Content.Type != "text" || m.Content.Text != "hello" {
		t.Errorf("content = %+v, want text/hello", m.Content)
	}
}

// TestSamplingMessageUnmarshalArrayContent verifies that SamplingMessage
// defensively accepts array-form content (first element wins) for #81
// cross-peer tolerance. SamplingMessage stays single-content per current spec;
// widening to multi-part is tracked by #141 and will reuse this hook by
// switching from "first element" to "all elements".
func TestSamplingMessageUnmarshalArrayContent(t *testing.T) {
	data := []byte(`{"role":"user","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}`)
	var m SamplingMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal array-content form: %v", err)
	}
	if m.Content.Type != "text" || m.Content.Text != "first" {
		t.Errorf("content = %+v, want first element (text/first)", m.Content)
	}
}

// TestSamplingMessageMarshalEmitsSingle verifies that SamplingMessage always
// marshals as spec-canonical single object. The read path tolerates arrays,
// but the write path must remain conformant.
func TestSamplingMessageMarshalEmitsSingle(t *testing.T) {
	m := SamplingMessage{
		Role:    "user",
		Content: Content{Type: "text", Text: "hi"},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"content":{"type":"text"`) {
		t.Errorf("marshal emitted %s, want content as single object", s)
	}
}

// TestCreateMessageResultUnmarshalArrayContent verifies that the server-side
// decode of sampling/createMessage responses tolerates array-form content from
// non-conformant clients. The server never sees this field directly — Sample()
// unmarshals it — so defensive tolerance prevents tool handlers from crashing
// on client quirks.
func TestCreateMessageResultUnmarshalArrayContent(t *testing.T) {
	data := []byte(`{"model":"m","role":"assistant","content":[{"type":"text","text":"reply"}]}`)
	var r CreateMessageResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Content.Type != "text" || r.Content.Text != "reply" {
		t.Errorf("content = %+v, want text/reply", r.Content)
	}
}

// TestContentUnmarshalResourceAsObject verifies the spec-canonical single
// embedded resource form. Baseline used by resources/read-backed tool content.
func TestContentUnmarshalResourceAsObject(t *testing.T) {
	data := []byte(`{"type":"resource","resource":{"uri":"file:///a","text":"hi"}}`)
	var c Content
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Resource == nil {
		t.Fatal("resource is nil")
	}
	if c.Resource.URI != "file:///a" || c.Resource.Text != "hi" {
		t.Errorf("resource = %+v", c.Resource)
	}
}

// TestContentUnmarshalResourceAsArray verifies that Content defensively accepts
// an array-form resource field and takes the first element. The spec says
// EmbeddedResource.resource is a single object, but some tooling emits an
// array by confusion with ReadResourceResult.contents. We parse instead of
// hard-failing. Cardinality alignment is #145 — this tolerance is the #81
// read-path prerequisite.
func TestContentUnmarshalResourceAsArray(t *testing.T) {
	data := []byte(`{"type":"resource","resource":[{"uri":"file:///a","text":"first"},{"uri":"file:///b","text":"second"}]}`)
	var c Content
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Resource == nil {
		t.Fatal("resource is nil")
	}
	if c.Resource.URI != "file:///a" {
		t.Errorf("resource.uri = %q, want file:///a (first element)", c.Resource.URI)
	}
}

// TestContentUnmarshalPreservesNonResourceFields verifies that the custom
// Content.UnmarshalJSON still populates type/text/mimeType/data for non-resource
// content types. Guards against a regression where the custom unmarshal forgets
// to copy scalar fields.
func TestContentUnmarshalPreservesNonResourceFields(t *testing.T) {
	data := []byte(`{"type":"image","mimeType":"image/png","data":"AAAA"}`)
	var c Content
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Type != "image" || c.MimeType != "image/png" || c.Data != "AAAA" {
		t.Errorf("content = %+v, want image/png/AAAA", c)
	}
	if c.Resource != nil {
		t.Errorf("resource = %+v, want nil", c.Resource)
	}
}

// TestContentMarshalRoundTrip verifies that a Content value round-trips
// unchanged through marshal → unmarshal → marshal. Catches asymmetry bugs in
// the custom unmarshal hook.
func TestContentMarshalRoundTrip(t *testing.T) {
	cases := []Content{
		{Type: "text", Text: "hi"},
		{Type: "image", MimeType: "image/png", Data: "AAAA"},
		{Type: "resource", Resource: &ResourceContent{URI: "file:///a", Text: "hi"}},
	}
	for _, orig := range cases {
		data, err := json.Marshal(orig)
		if err != nil {
			t.Fatalf("marshal %+v: %v", orig, err)
		}
		var parsed Content
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		data2, err := json.Marshal(parsed)
		if err != nil {
			t.Fatalf("remarshal: %v", err)
		}
		if string(data) != string(data2) {
			t.Errorf("round-trip mismatch:\n  first:  %s\n  second: %s", data, data2)
		}
	}
}

// TestToolResultUnmarshalSingleContent verifies that ToolResult tolerates a
// single-object form in the `content` field (spec says array). Some older
// peers or hand-written JSON emit a bare object; we wrap it into a 1-element
// slice instead of failing. Issue #81.
func TestToolResultUnmarshalSingleContent(t *testing.T) {
	data := []byte(`{"content":{"type":"text","text":"hi"}}`)
	var r ToolResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal single-content: %v", err)
	}
	if len(r.Content) != 1 {
		t.Fatalf("content len = %d, want 1", len(r.Content))
	}
	if r.Content[0].Type != "text" || r.Content[0].Text != "hi" {
		t.Errorf("content[0] = %+v, want text/hi", r.Content[0])
	}
}

// TestToolResultUnmarshalArrayContent verifies that the spec-canonical array
// form still works after we add single-object tolerance. Baseline regression.
func TestToolResultUnmarshalArrayContent(t *testing.T) {
	data := []byte(`{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}`)
	var r ToolResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal array-content: %v", err)
	}
	if len(r.Content) != 2 {
		t.Fatalf("content len = %d, want 2", len(r.Content))
	}
	if r.Content[0].Text != "a" || r.Content[1].Text != "b" {
		t.Errorf("content = %+v, want a/b", r.Content)
	}
}

// TestToolResultMarshalAlwaysEmitsArray verifies that ToolResult serializes the
// content field as a JSON array even when it holds a single element. The spec
// form is an array; read tolerance must not leak into write behavior.
func TestToolResultMarshalAlwaysEmitsArray(t *testing.T) {
	r := ToolResult{Content: []Content{{Type: "text", Text: "hi"}}}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"content":[`) {
		t.Errorf("marshal emitted %s, want content as array", s)
	}
}

// TestResourceResultUnmarshalSingleContent verifies that ResourceResult tolerates
// a single-object form in the `contents` field (spec says array). Mirror of
// the ToolResult tolerance for resources/read results. Issue #81.
func TestResourceResultUnmarshalSingleContent(t *testing.T) {
	data := []byte(`{"contents":{"uri":"file:///a","text":"hi"}}`)
	var r ResourceResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal single-contents: %v", err)
	}
	if len(r.Contents) != 1 {
		t.Fatalf("contents len = %d, want 1", len(r.Contents))
	}
	if r.Contents[0].URI != "file:///a" || r.Contents[0].Text != "hi" {
		t.Errorf("contents[0] = %+v, want file:///a/hi", r.Contents[0])
	}
}

// TestResourceResultUnmarshalArrayContent verifies the spec-canonical array
// form still parses after adding single-object tolerance. Baseline regression.
func TestResourceResultUnmarshalArrayContent(t *testing.T) {
	data := []byte(`{"contents":[{"uri":"file:///a","text":"a"},{"uri":"file:///b","text":"b"}]}`)
	var r ResourceResult
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal array-contents: %v", err)
	}
	if len(r.Contents) != 2 {
		t.Fatalf("contents len = %d, want 2", len(r.Contents))
	}
	if r.Contents[0].URI != "file:///a" || r.Contents[1].URI != "file:///b" {
		t.Errorf("contents = %+v, want a/b", r.Contents)
	}
}

// TestResourceResultMarshalAlwaysEmitsArray verifies that ResourceResult
// serializes `contents` as a JSON array even for a single element.
func TestResourceResultMarshalAlwaysEmitsArray(t *testing.T) {
	r := ResourceResult{Contents: []ResourceReadContent{{URI: "file:///a", Text: "hi"}}}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"contents":[`) {
		t.Errorf("marshal emitted %s, want contents as array", s)
	}
}
