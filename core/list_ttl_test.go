package core

import (
	"encoding/json"
	"testing"
)

// TestCacheableResultTTLMs_WireFormat verifies the SEP-2549 ttlMs encoding
// for every cacheable result type — the four paginated list results plus
// ResourceResult (resources/read). The pointer keeps the encoding precise:
// omitempty omits nil but keeps a pointer to the zero value, so an absent
// field, an explicit `"ttlMs": 0`, and a positive value all round-trip
// distinctly. Per the merged spec absent and 0 are client-equivalent, but
// the pointer still lets a server state the 0 deliberately on the wire.
func TestCacheableResultTTLMs_WireFormat(t *testing.T) {
	cases := []struct {
		name        string
		ttl         *int
		wantPresent bool
		wantValue   float64 // JSON numbers unmarshal to float64 in map[string]any
	}{
		{"nil omits the field", nil, false, 0},
		{"zero stays as explicit 0", IntPtr(0), true, 0},
		{"positive value round-trips", IntPtr(300000), true, 300000},
	}

	variants := []struct {
		name  string
		build func(ttl *int) any
	}{
		{"ToolsListResult", func(ttl *int) any { return ToolsListResult{TTLMs: ttl} }},
		{"PromptsListResult", func(ttl *int) any { return PromptsListResult{TTLMs: ttl} }},
		{"ResourcesListResult", func(ttl *int) any { return ResourcesListResult{TTLMs: ttl} }},
		{"ResourceTemplatesListResult", func(ttl *int) any { return ResourceTemplatesListResult{TTLMs: ttl} }},
		{"ResourceResult", func(ttl *int) any { return ResourceResult{TTLMs: ttl} }},
	}

	for _, v := range variants {
		for _, tc := range cases {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				data, err := json.Marshal(v.build(tc.ttl))
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				got, present := m["ttlMs"]
				if present != tc.wantPresent {
					t.Errorf("ttlMs present = %v, want %v; wire=%s", present, tc.wantPresent, data)
				}
				if tc.wantPresent {
					if gotF, ok := got.(float64); !ok || gotF != tc.wantValue {
						t.Errorf("ttlMs = %v (%T), want %v; wire=%s", got, got, tc.wantValue, data)
					}
				}
				// The pre-merge `ttl` (seconds) field must never reappear.
				if _, stale := m["ttl"]; stale {
					t.Errorf("stale `ttl` field present; wire=%s", data)
				}
			})
		}
	}
}

// TestCacheableResultCacheScope_WireFormat verifies the SEP-2549 cacheScope
// field encoding. An empty string omits the field (clients default to
// "public"); a set value surfaces verbatim.
func TestCacheableResultCacheScope_WireFormat(t *testing.T) {
	cases := []struct {
		name        string
		scope       string
		wantPresent bool
	}{
		{"empty omits the field", "", false},
		{"public surfaces", CacheScopePublic, true},
		{"private surfaces", CacheScopePrivate, true},
	}

	variants := []struct {
		name  string
		build func(scope string) any
	}{
		{"ToolsListResult", func(s string) any { return ToolsListResult{CacheScope: s} }},
		{"PromptsListResult", func(s string) any { return PromptsListResult{CacheScope: s} }},
		{"ResourcesListResult", func(s string) any { return ResourcesListResult{CacheScope: s} }},
		{"ResourceTemplatesListResult", func(s string) any { return ResourceTemplatesListResult{CacheScope: s} }},
		{"ResourceResult", func(s string) any { return ResourceResult{CacheScope: s} }},
	}

	for _, v := range variants {
		for _, tc := range cases {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				data, err := json.Marshal(v.build(tc.scope))
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				got, present := m["cacheScope"]
				if present != tc.wantPresent {
					t.Errorf("cacheScope present = %v, want %v; wire=%s", present, tc.wantPresent, data)
				}
				if tc.wantPresent && got != tc.scope {
					t.Errorf("cacheScope = %v, want %q; wire=%s", got, tc.scope, data)
				}
			})
		}
	}
}

// TestToolsListResultTTLMs_RoundTrip verifies decoding a wire payload back
// into the typed result preserves the *int distinction between nil (field
// absent) and &0 (field present, zero value). A naive `int` field would
// conflate the two.
func TestToolsListResultTTLMs_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want *int
	}{
		{"absent -> nil", `{"tools":[]}`, nil},
		{"explicit 0 -> &0", `{"tools":[],"ttlMs":0}`, IntPtr(0)},
		{"explicit positive -> &N", `{"tools":[],"ttlMs":42000}`, IntPtr(42000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got ToolsListResult
			if err := json.Unmarshal([]byte(tc.wire), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			switch {
			case tc.want == nil && got.TTLMs != nil:
				t.Errorf("TTLMs = &%d, want nil", *got.TTLMs)
			case tc.want != nil && got.TTLMs == nil:
				t.Errorf("TTLMs = nil, want &%d", *tc.want)
			case tc.want != nil && got.TTLMs != nil && *got.TTLMs != *tc.want:
				t.Errorf("TTLMs = %d, want %d", *got.TTLMs, *tc.want)
			}
		})
	}
}

// TestResourceResultCacheHints_RoundTrip guards the custom ResourceResult
// UnmarshalJSON: it decodes `contents` specially (single-object tolerance,
// see #81), so the SEP-2549 ttlMs / cacheScope hints must be decoded
// explicitly there or they would be silently dropped on the client side.
func TestResourceResultCacheHints_RoundTrip(t *testing.T) {
	const wire = `{"contents":[{"uri":"file:///x","text":"hi"}],"ttlMs":60000,"cacheScope":"private"}`
	var got ResourceResult
	if err := json.Unmarshal([]byte(wire), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TTLMs == nil || *got.TTLMs != 60000 {
		t.Errorf("TTLMs = %v, want &60000", got.TTLMs)
	}
	if got.CacheScope != CacheScopePrivate {
		t.Errorf("CacheScope = %q, want %q", got.CacheScope, CacheScopePrivate)
	}
	if len(got.Contents) != 1 || got.Contents[0].Text != "hi" {
		t.Errorf("Contents = %+v, want one item with text \"hi\"", got.Contents)
	}
}
