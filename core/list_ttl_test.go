package core

import (
	"encoding/json"
	"testing"
)

// TestListResultTTL_WireFormat verifies the SEP-2549 three-state TTL
// encoding for all four paginated list result types. The pointer
// semantics matter — Go's omitempty omits nil but keeps a pointer to
// the zero value, so `nil → absent`, `&0 → "ttl": 0`, `&N → "ttl": N`
// all round-trip distinctly through JSON.
//
// Without this, "do not cache" (`&0`) would be indistinguishable from
// "no guidance" (`nil`) on the wire — a silent regression that breaks
// the spec's three-state contract.
func TestListResultTTL_WireFormat(t *testing.T) {
	cases := []struct {
		name        string
		ttl         *int
		wantPresent bool
		wantValue   float64 // json.Number unmarshals to float64 in map[string]any
	}{
		{"nil omits the field", nil, false, 0},
		{"zero stays as explicit 0", IntPtr(0), true, 0},
		{"positive value round-trips", IntPtr(300), true, 300},
	}

	type variant struct {
		name string
		// build returns a populated list result with the given TTL.
		build func(ttl *int) any
	}
	variants := []variant{
		{"ToolsListResult", func(ttl *int) any { return ToolsListResult{TTL: ttl} }},
		{"PromptsListResult", func(ttl *int) any { return PromptsListResult{TTL: ttl} }},
		{"ResourcesListResult", func(ttl *int) any { return ResourcesListResult{TTL: ttl} }},
		{"ResourceTemplatesListResult", func(ttl *int) any { return ResourceTemplatesListResult{TTL: ttl} }},
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
				got, present := m["ttl"]
				if present != tc.wantPresent {
					t.Errorf("ttl present = %v, want %v; wire=%s", present, tc.wantPresent, data)
				}
				if tc.wantPresent {
					if gotF, ok := got.(float64); !ok || gotF != tc.wantValue {
						t.Errorf("ttl = %v (%T), want %v; wire=%s", got, got, tc.wantValue, data)
					}
				}
			})
		}
	}
}

// TestListResultTTL_RoundTrip verifies that decoding a wire payload back
// into the typed result preserves the *int distinction between nil (field
// absent) and &0 (field present with zero value). A naive `int` field
// would conflate the two.
func TestListResultTTL_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want *int
	}{
		{"absent → nil", `{"tools":[]}`, nil},
		{"explicit 0 → &0", `{"tools":[],"ttl":0}`, IntPtr(0)},
		{"explicit positive → &N", `{"tools":[],"ttl":42}`, IntPtr(42)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got ToolsListResult
			if err := json.Unmarshal([]byte(tc.wire), &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			switch {
			case tc.want == nil && got.TTL != nil:
				t.Errorf("TTL = &%d, want nil", *got.TTL)
			case tc.want != nil && got.TTL == nil:
				t.Errorf("TTL = nil, want &%d", *tc.want)
			case tc.want != nil && got.TTL != nil && *got.TTL != *tc.want:
				t.Errorf("TTL = %d, want %d", *got.TTL, *tc.want)
			}
		})
	}
}
