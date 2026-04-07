package mcpkit

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

// testClaimsValidator is an AuthValidator + ClaimsProvider for testing.
type testClaimsValidator struct {
	validToken string
	claims     *Claims
}

func (v *testClaimsValidator) Validate(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if auth != "Bearer "+v.validToken {
		return &AuthError{Code: http.StatusUnauthorized, Message: "unauthorized"}
	}
	return nil
}

func (v *testClaimsValidator) Claims(r *http.Request) *Claims {
	return v.claims
}

func TestAuthClaimsFromContext(t *testing.T) {
	claims := &Claims{
		Subject:  "user-123",
		Issuer:   "https://auth.example.com",
		Audience: []string{"https://mcp.example.com"},
		Scopes:   []string{"tools:read", "admin:write"},
		Extra:    map[string]any{"org": "acme"},
	}

	var logLevel atomic.Pointer[LogLevel]
	ctx := contextWithSession(context.Background(), nil, nil, &logLevel, nil, claims)

	got := AuthClaims(ctx)
	if got == nil {
		t.Fatal("AuthClaims returned nil")
	}
	if got.Subject != "user-123" {
		t.Errorf("Subject = %q, want %q", got.Subject, "user-123")
	}
	if got.Issuer != "https://auth.example.com" {
		t.Errorf("Issuer = %q, want %q", got.Issuer, "https://auth.example.com")
	}
	if len(got.Audience) != 1 || got.Audience[0] != "https://mcp.example.com" {
		t.Errorf("Audience = %v, want [https://mcp.example.com]", got.Audience)
	}
	if len(got.Scopes) != 2 {
		t.Errorf("Scopes = %v, want 2 entries", got.Scopes)
	}
	if got.Extra["org"] != "acme" {
		t.Errorf("Extra[org] = %v, want acme", got.Extra["org"])
	}
}

func TestAuthClaimsNilWithoutSession(t *testing.T) {
	got := AuthClaims(context.Background())
	if got != nil {
		t.Errorf("AuthClaims without session = %v, want nil", got)
	}
}

func TestAuthClaimsNilWithoutAuth(t *testing.T) {
	var logLevel atomic.Pointer[LogLevel]
	ctx := contextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)

	got := AuthClaims(ctx)
	if got != nil {
		t.Errorf("AuthClaims without auth = %v, want nil", got)
	}
}

func TestHasScope(t *testing.T) {
	claims := &Claims{Scopes: []string{"tools:read", "admin:write"}}
	var logLevel atomic.Pointer[LogLevel]
	ctx := contextWithSession(context.Background(), nil, nil, &logLevel, nil, claims)

	if !HasScope(ctx, "tools:read") {
		t.Error("HasScope(tools:read) = false, want true")
	}
	if !HasScope(ctx, "admin:write") {
		t.Error("HasScope(admin:write) = false, want true")
	}
	if HasScope(ctx, "admin:read") {
		t.Error("HasScope(admin:read) = true, want false")
	}
}

func TestHasScopeWithoutClaims(t *testing.T) {
	if HasScope(context.Background(), "anything") {
		t.Error("HasScope without session = true, want false")
	}

	var logLevel atomic.Pointer[LogLevel]
	ctx := contextWithSession(context.Background(), nil, nil, &logLevel, nil, nil)
	if HasScope(ctx, "anything") {
		t.Error("HasScope without claims = true, want false")
	}
}

func TestCheckAuthWithClaimsProvider(t *testing.T) {
	expectedClaims := &Claims{
		Subject: "user-456",
		Scopes:  []string{"read"},
	}
	validator := &testClaimsValidator{
		validToken: "good-token",
		claims:     expectedClaims,
	}
	srv := NewServer(
		ServerInfo{Name: "test", Version: "0.1.0"},
		WithAuth(validator),
	)

	// Valid token → returns claims
	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer good-token")
	claims, err := srv.CheckAuth(r)
	if err != nil {
		t.Fatalf("CheckAuth error: %v", err)
	}
	if claims == nil {
		t.Fatal("CheckAuth returned nil claims")
	}
	if claims.Subject != "user-456" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "user-456")
	}

	// Invalid token → returns error, no claims
	r2, _ := http.NewRequest("GET", "/", nil)
	r2.Header.Set("Authorization", "Bearer bad-token")
	claims2, err2 := srv.CheckAuth(r2)
	if err2 == nil {
		t.Error("CheckAuth should fail for bad token")
	}
	if claims2 != nil {
		t.Error("CheckAuth should return nil claims on failure")
	}
}

func TestCheckAuthWithoutClaimsProvider(t *testing.T) {
	// bearerTokenValidator does NOT implement ClaimsProvider
	srv := NewServer(
		ServerInfo{Name: "test", Version: "0.1.0"},
		WithBearerToken("secret"),
	)

	r, _ := http.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer secret")
	claims, err := srv.CheckAuth(r)
	if err != nil {
		t.Fatalf("CheckAuth error: %v", err)
	}
	if claims != nil {
		t.Error("bearerTokenValidator should not return claims")
	}
}

func TestStaticTokenSource(t *testing.T) {
	ts := &staticTokenSource{token: "my-token"}
	token, err := ts.Token()
	if err != nil {
		t.Fatalf("Token error: %v", err)
	}
	if token != "my-token" {
		t.Errorf("Token = %q, want %q", token, "my-token")
	}
}

func TestExtensionRegistration(t *testing.T) {
	ext := testExtension{
		ext: Extension{
			ID:          "io.test/foo",
			SpecVersion: "2025-01-01",
			Stability:   Experimental,
		},
	}

	srv := NewServer(
		ServerInfo{Name: "test", Version: "0.1.0"},
		WithExtension(ext),
	)

	// The extension should be registered on the dispatcher
	if _, ok := srv.dispatcher.extensions["io.test/foo"]; !ok {
		t.Error("extension not registered")
	}

	// Verify initialize response includes extensions
	resp := srv.Dispatch(context.Background(), &Request{
		JSONRPC: "2.0",
		ID:      []byte(`1`),
		Method:  "initialize",
		Params:  []byte(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}`),
	})

	if resp.Error != nil {
		t.Fatalf("initialize error: %s", resp.Error.Message)
	}

	// Unmarshal the result to inspect extensions
	var result map[string]any
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("capabilities is not a map")
	}
	exts, ok := caps["extensions"].(map[string]any)
	if !ok {
		t.Fatal("extensions not in capabilities")
	}
	fooExt, ok := exts["io.test/foo"].(map[string]any)
	if !ok {
		t.Fatal("io.test/foo not in extensions")
	}
	if fooExt["stability"] != "experimental" {
		t.Errorf("stability = %v, want experimental", fooExt["stability"])
	}
	if fooExt["specVersion"] != "2025-01-01" {
		t.Errorf("specVersion = %v, want 2025-01-01", fooExt["specVersion"])
	}
}

type testExtension struct {
	ext Extension
}

func (e testExtension) Extension() Extension { return e.ext }

func TestAnnotationsOnToolDef(t *testing.T) {
	srv := NewServer(ServerInfo{Name: "test", Version: "0.1.0"})
	srv.RegisterExperimentalTool(
		ToolDef{Name: "beta-tool", Description: "experimental"},
		func(ctx context.Context, req ToolRequest) (ToolResult, error) {
			return TextResult("ok"), nil
		},
	)

	entry, ok := srv.dispatcher.tools["beta-tool"]
	if !ok {
		t.Fatal("tool not registered")
	}
	if entry.def.Annotations == nil {
		t.Fatal("annotations is nil")
	}
	if entry.def.Annotations["experimental"] != true {
		t.Errorf("experimental = %v, want true", entry.def.Annotations["experimental"])
	}
}

func TestAuthErrorWithWWWAuthenticate(t *testing.T) {
	err := &AuthError{
		Code:            401,
		Message:         "unauthorized",
		WWWAuthenticate: `Bearer resource_metadata="https://example.com/.well-known/oauth-protected-resource"`,
	}

	if err.Error() != "unauthorized" {
		t.Errorf("Error() = %q, want %q", err.Error(), "unauthorized")
	}
	if err.WWWAuthenticate == "" {
		t.Error("WWWAuthenticate should not be empty")
	}
}
