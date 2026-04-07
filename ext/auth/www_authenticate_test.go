package auth

import (
	"testing"
)

func TestWWWAuth401(t *testing.T) {
	tests := []struct {
		name       string
		prmURL     string
		scopes     []string
		wantPrefix string
		wantContains []string
	}{
		{
			name:       "with scopes",
			prmURL:     "https://mcp.example.com/.well-known/oauth-protected-resource/mcp",
			scopes:     []string{"tools:read", "admin:write"},
			wantPrefix: "Bearer ",
			wantContains: []string{
				`resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp"`,
				`scope="tools:read admin:write"`,
			},
		},
		{
			name:       "without scopes",
			prmURL:     "https://mcp.example.com/.well-known/oauth-protected-resource",
			scopes:     nil,
			wantPrefix: "Bearer ",
			wantContains: []string{
				`resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WWWAuth401(tt.prmURL, tt.scopes...)
			if got[:len("Bearer ")] != "Bearer " {
				t.Errorf("missing Bearer prefix: %s", got)
			}
			for _, want := range tt.wantContains {
				if !contains(got, want) {
					t.Errorf("missing %q in %q", want, got)
				}
			}
		})
	}
}

func TestWWWAuth403(t *testing.T) {
	got := WWWAuth403("admin:write", "files:read")
	want := `Bearer error="insufficient_scope", scope="admin:write files:read"`
	if got != want {
		t.Errorf("WWWAuth403 = %q, want %q", got, want)
	}

	// Without scopes
	got2 := WWWAuth403()
	want2 := `Bearer error="insufficient_scope"`
	if got2 != want2 {
		t.Errorf("WWWAuth403() = %q, want %q", got2, want2)
	}
}

func TestParseWWWAuthenticate(t *testing.T) {
	tests := []struct {
		name           string
		header         string
		wantRM         string
		wantScopes     []string
	}{
		{
			name:       "full header",
			header:     `Bearer resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource/mcp", scope="tools:read admin:write"`,
			wantRM:     "https://mcp.example.com/.well-known/oauth-protected-resource/mcp",
			wantScopes: []string{"tools:read", "admin:write"},
		},
		{
			name:       "no scope",
			header:     `Bearer resource_metadata="https://example.com/.well-known/oauth-protected-resource"`,
			wantRM:     "https://example.com/.well-known/oauth-protected-resource",
			wantScopes: nil,
		},
		{
			name:       "insufficient_scope error",
			header:     `Bearer error="insufficient_scope", scope="admin:write"`,
			wantRM:     "",
			wantScopes: []string{"admin:write"},
		},
		{
			name:       "empty",
			header:     "",
			wantRM:     "",
			wantScopes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rm, scopes, err := ParseWWWAuthenticate(tt.header)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rm != tt.wantRM {
				t.Errorf("resource_metadata = %q, want %q", rm, tt.wantRM)
			}
			if len(scopes) != len(tt.wantScopes) {
				t.Errorf("scopes = %v, want %v", scopes, tt.wantScopes)
			} else {
				for i, s := range scopes {
					if s != tt.wantScopes[i] {
						t.Errorf("scope[%d] = %q, want %q", i, s, tt.wantScopes[i])
					}
				}
			}
		})
	}
}

func TestExtension(t *testing.T) {
	ext := AuthExtension{}.Extension()
	if ext.ID != "io.mcpkit/auth" {
		t.Errorf("ID = %q, want %q", ext.ID, "io.mcpkit/auth")
	}
	if ext.SpecVersion != "2025-11-25" {
		t.Errorf("SpecVersion = %q, want %q", ext.SpecVersion, "2025-11-25")
	}
	if ext.Stability != "experimental" {
		t.Errorf("Stability = %q, want %q", ext.Stability, "experimental")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
