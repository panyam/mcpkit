package auth_test

import (
	"strings"
	"testing"

	"github.com/panyam/mcpkit/ext/auth"
)

func TestDefaultScopeExtractor(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]any
		want   []string
	}{
		{"oneauth scopes array", map[string]any{"scopes": []any{"read", "write"}}, []string{"read", "write"}},
		{"okta scp array", map[string]any{"scp": []any{"read", "write"}}, []string{"read", "write"}},
		{"keycloak scope string", map[string]any{"scope": "read write"}, []string{"read", "write"}},
		{"scp as string", map[string]any{"scp": "read write"}, []string{"read", "write"}},
		{"no scope claim", map[string]any{"sub": "alice"}, nil},
		{"empty scope string", map[string]any{"scope": ""}, nil},

		// Precedence: scopes beats scp beats scope. Locked in because a token
		// carrying two of them must not depend on map iteration order.
		{
			"scopes wins over scp",
			map[string]any{"scopes": []any{"a"}, "scp": []any{"b"}},
			[]string{"a"},
		},
		{
			"scp array wins over scope string",
			map[string]any{"scp": []any{"a"}, "scope": "b"},
			[]string{"a"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := auth.DefaultScopeExtractor(tc.claims)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A provider whose scope values are structured rather than free-form cannot be
// served by DefaultScopeExtractor at all, which is the whole reason the hook
// exists. FusionAuth's client_credentials grant is the case that surfaced it:
// Entity Management encodes scopes as target-entity:<uuid>:<permission>, so a
// server gating on "admin-write" never matches without a mapping.
func TestScopeExtractorHandlesStructuredScopeValues(t *testing.T) {
	claims := map[string]any{
		"scope": "target-entity:b1b2c3d4-0002-0002-0002-000000000001:admin-write " +
			"target-entity:b1b2c3d4-0002-0002-0002-000000000001:tools-read",
	}

	if got := auth.DefaultScopeExtractor(claims); got[0] == "admin-write" {
		t.Fatal("default extractor should not decode structured scope values; " +
			"if it does, the hook is not needed and this test is wrong")
	}

	fusionAuth := func(c map[string]any) []string {
		out := auth.DefaultScopeExtractor(c)
		for i, s := range out {
			if idx := strings.LastIndex(s, ":"); idx >= 0 {
				out[i] = s[idx+1:]
			}
		}
		return out
	}

	got := fusionAuth(claims)
	want := []string{"admin-write", "tools-read"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
