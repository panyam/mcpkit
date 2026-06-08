package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/oneauth/apiauth"
)

// fakeIntrospectionEndpoint stands in for the AS's introspection
// endpoint. The handler echoes the supplied response and tracks call
// counts so the cache test can assert single-flight behavior.
type fakeIntrospectionEndpoint struct {
	calls    atomic.Int64
	response map[string]any
	status   int
}

func (f *fakeIntrospectionEndpoint) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.response)
	})
}

func newFakeAS(t *testing.T, response map[string]any) (*fakeIntrospectionEndpoint, string) {
	t.Helper()
	fake := &fakeIntrospectionEndpoint{response: response}
	srv := httptest.NewServer(fake.Handler())
	t.Cleanup(srv.Close)
	return fake, srv.URL
}

func newAuthedRequest(token string) *http.Request {
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func TestIntrospectionValidator_ActiveToken_RealmFromIssuer(t *testing.T) {
	_, asURL := newFakeAS(t, map[string]any{
		"active": true,
		"sub":    "alice",
		"iss":    "https://kc.example.test/realms/tenant-a",
		"scope":  "read write",
		"aud":    "mcp-events",
	})

	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: asURL,
		ClientID:         "client",
		ClientSecret:     "secret",
	})

	r := newAuthedRequest("token-abc")
	if err := v.Validate(r); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claims := v.Claims(r)
	if claims == nil {
		t.Fatal("Claims returned nil after successful Validate")
	}
	if claims.Subject != "tenant-a/alice" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "tenant-a/alice")
	}
	if claims.Issuer != "https://kc.example.test/realms/tenant-a" {
		t.Errorf("Issuer = %q", claims.Issuer)
	}
	if got, want := strings.Join(claims.Scopes, " "), "read write"; got != want {
		t.Errorf("Scopes = %v, want %q", claims.Scopes, want)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != "mcp-events" {
		t.Errorf("Audience = %v", claims.Audience)
	}
	if claims.Extra["tenant"] != "tenant-a" {
		t.Errorf("Extra.tenant = %v", claims.Extra["tenant"])
	}
	if claims.Extra["raw_sub"] != "alice" {
		t.Errorf("Extra.raw_sub = %v", claims.Extra["raw_sub"])
	}
}

func TestIntrospectionValidator_InactiveToken(t *testing.T) {
	_, asURL := newFakeAS(t, map[string]any{"active": false})

	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: asURL,
		ClientID:         "c",
		ClientSecret:     "s",
	})

	err := v.Validate(newAuthedRequest("revoked-token"))
	if err == nil {
		t.Fatal("Validate accepted an inactive token")
	}
	authErr, ok := err.(*mcpcore.AuthError)
	if !ok {
		t.Fatalf("error type = %T, want *mcpcore.AuthError", err)
	}
	if authErr.Code != http.StatusUnauthorized {
		t.Errorf("Code = %d, want 401", authErr.Code)
	}
	if !strings.Contains(authErr.WWWAuthenticate, "Bearer") {
		t.Errorf("WWWAuthenticate = %q (no Bearer scheme)", authErr.WWWAuthenticate)
	}
}

func TestIntrospectionValidator_MissingAuthHeader(t *testing.T) {
	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: "http://unused",
		ClientID:         "c",
		ClientSecret:     "s",
	})
	r := httptest.NewRequest("POST", "/", nil)
	if err := v.Validate(r); err == nil {
		t.Fatal("Validate accepted a request with no Authorization header")
	}
}

func TestIntrospectionValidator_MissingSubject(t *testing.T) {
	_, asURL := newFakeAS(t, map[string]any{
		"active": true,
		"iss":    "https://kc.example.test/realms/x",
		// no "sub" — the AS shouldn't do this, but if it does we
		// must reject rather than stamp an empty principal.
	})
	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: asURL,
		ClientID:         "c",
		ClientSecret:     "s",
	})
	err := v.Validate(newAuthedRequest("subjectless-token"))
	if err == nil {
		t.Fatal("Validate accepted a response with no subject")
	}
}

func TestIntrospectionValidator_NetworkFailure(t *testing.T) {
	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: "http://127.0.0.1:1", // refused
		ClientID:         "c",
		ClientSecret:     "s",
	})
	err := v.Validate(newAuthedRequest("any-token"))
	if err == nil {
		t.Fatal("Validate succeeded against an unreachable AS")
	}
	authErr, ok := err.(*mcpcore.AuthError)
	if !ok || authErr.Code != http.StatusUnauthorized {
		t.Errorf("err = %v (want 401 AuthError)", err)
	}
}

func TestIntrospectionValidator_CacheHitAvoidsRoundTrip(t *testing.T) {
	fake, asURL := newFakeAS(t, map[string]any{
		"active": true,
		"sub":    "alice",
		"iss":    "https://kc.example.test/realms/t",
	})

	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: asURL,
		ClientID:         "c",
		ClientSecret:     "s",
		CacheTTL:         time.Second,
	})

	// Three consecutive Validate calls on the same token should hit
	// the AS exactly once (the first one); the next two are cache
	// hits.
	for i := 0; i < 3; i++ {
		if err := v.Validate(newAuthedRequest("cached-token")); err != nil {
			t.Fatalf("Validate %d: %v", i, err)
		}
	}
	if got := fake.calls.Load(); got != 1 {
		t.Errorf("introspection calls = %d, want 1 (cache miss only on first)", got)
	}
}

func TestIntrospectionValidator_CustomMapperOverride(t *testing.T) {
	_, asURL := newFakeAS(t, map[string]any{
		"active":   true,
		"sub":      "alice",
		"iss":      "https://generic-as.example.test/", // no /realms/ — default mapper would yield tenant=""
		"username": "alice@org-x",
	})

	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: asURL,
		ClientID:         "c",
		ClientSecret:     "s",
		// Custom mapper: derive tenant from a hardcoded value
		// regardless of the response. Mirrors how a deployment with a
		// custom "organization" claim would slot in once oneauth
		// surfaces arbitrary claims.
		TenantMapper: func(*apiauth.IntrospectionResult) (string, string) {
			return "org-x", "alice"
		},
	})

	if err := v.Validate(newAuthedRequest("any-token")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	claims := v.Claims(newAuthedRequest("any-token"))
	// Claims may be nil because LoadAndDelete already popped — fetch via the same request.
	// Re-validate to repopulate the recentClaims slot for the assertion request.
	r := newAuthedRequest("any-token")
	if err := v.Validate(r); err != nil {
		t.Fatalf("re-validate: %v", err)
	}
	claims = v.Claims(r)
	if claims == nil {
		t.Fatal("Claims returned nil")
	}
	if claims.Subject != "org-x/alice" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "org-x/alice")
	}
}

func TestIntrospectionValidator_InsufficientScope(t *testing.T) {
	_, asURL := newFakeAS(t, map[string]any{
		"active": true,
		"sub":    "alice",
		"iss":    "https://kc.example.test/realms/t",
		"scope":  "read",
	})
	v := NewIntrospectionValidator(IntrospectionConfig{
		IntrospectionURL: asURL,
		ClientID:         "c",
		ClientSecret:     "s",
		RequiredScopes:   []string{"read", "write"},
	})

	err := v.Validate(newAuthedRequest("low-scope-token"))
	if err == nil {
		t.Fatal("Validate accepted a token missing required scope")
	}
	authErr, ok := err.(*mcpcore.AuthError)
	if !ok {
		t.Fatalf("err type = %T", err)
	}
	if authErr.Code != http.StatusForbidden {
		t.Errorf("Code = %d, want 403", authErr.Code)
	}
}

func TestRealmFromIssuer(t *testing.T) {
	cases := []struct {
		iss  string
		want string
	}{
		{"https://kc.example.test/realms/tenant-a", "tenant-a"},
		{"http://localhost:8081/realms/master", "master"},
		{"https://kc.example.test/realms/t/extra", "t"},
		{"https://generic-as.example.test/", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := realmFromIssuer(tc.iss); got != tc.want {
			t.Errorf("realmFromIssuer(%q) = %q, want %q", tc.iss, got, tc.want)
		}
	}
}

func TestAudienceSlice(t *testing.T) {
	cases := []struct {
		in   any
		want []string
	}{
		{"a", []string{"a"}},
		{"", nil},
		{[]any{"a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b"}, []string{"a", "b"}},
		{nil, nil},
		{42, nil},
	}
	for _, tc := range cases {
		got := audienceSlice(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("audienceSlice(%v) len = %d, want %d", tc.in, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("audienceSlice(%v)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
