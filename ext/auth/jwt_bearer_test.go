package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type jwtBearerMock struct {
	srv           *httptest.Server
	tokenRequests []url.Values
	tokenStatus   int
	tokenError    string
}

func newJWTBearerMock(t *testing.T) *jwtBearerMock {
	t.Helper()
	m := &jwtBearerMock{tokenStatus: http.StatusOK}
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srvURL+`/.well-known/oauth-protected-resource/mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"resource":              srvURL + "/mcp",
			"authorization_servers": []string{srvURL},
			"scopes_supported":      []string{"wif.read"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srvURL,
			"token_endpoint":                        srvURL + "/token",
			"grant_types_supported":                 []string{"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"token_endpoint_auth_methods_supported": []string{"none"},
			"code_challenge_methods_supported":      []string{"S256"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		m.tokenRequests = append(m.tokenRequests, r.PostForm)
		if m.tokenStatus != http.StatusOK {
			w.WriteHeader(m.tokenStatus)
			json.NewEncoder(w).Encode(map[string]any{"error": m.tokenError})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "wif-token-" + string(rune('0'+len(m.tokenRequests))),
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	m.srv = httptest.NewServer(mux)
	srvURL = m.srv.URL
	t.Cleanup(m.srv.Close)
	return m
}

func TestJWTBearerTokenSource_GrantShape(t *testing.T) {
	m := newJWTBearerMock(t)
	s := &JWTBearerTokenSource{
		ServerURL:     m.srv.URL + "/mcp",
		ClientID:      "wif-client",
		Assertion:     "signed.workload.jwt",
		HTTPClient:    m.srv.Client(),
		AllowInsecure: true,
	}

	tok, err := s.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	if got := len(m.tokenRequests); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
	form := m.tokenRequests[0]
	if got, want := form.Get("grant_type"), "urn:ietf:params:oauth:grant-type:jwt-bearer"; got != want {
		t.Errorf("grant_type = %q, want %q", got, want)
	}
	if got, want := form.Get("assertion"), "signed.workload.jwt"; got != want {
		t.Errorf("assertion = %q, want %q", got, want)
	}
	if got := form.Get("client_id"); got != "wif-client" {
		t.Errorf("client_id = %q, want wif-client", got)
	}
	if got := form.Get("scope"); !strings.Contains(got, "wif.read") {
		t.Errorf("scope = %q, want PRM scopes_supported inherited", got)
	}

	// Second call must serve from cache, not re-exchange.
	if _, err := s.Token(); err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if got := len(m.tokenRequests); got != 1 {
		t.Errorf("token requests after cached call = %d, want 1", got)
	}
}

func TestJWTBearerTokenSource_TokenForScopes(t *testing.T) {
	m := newJWTBearerMock(t)
	s := &JWTBearerTokenSource{
		ServerURL:     m.srv.URL + "/mcp",
		ClientID:      "wif-client",
		Assertion:     "signed.workload.jwt",
		HTTPClient:    m.srv.Client(),
		AllowInsecure: true,
	}
	if _, err := s.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if _, err := s.TokenForScopes([]string{"wif.write"}); err != nil {
		t.Fatalf("TokenForScopes: %v", err)
	}
	if got := len(m.tokenRequests); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
	scope := m.tokenRequests[1].Get("scope")
	if !strings.Contains(scope, "wif.read") || !strings.Contains(scope, "wif.write") {
		t.Errorf("step-up scope = %q, want merged wif.read + wif.write", scope)
	}
}

func TestJWTBearerTokenSource_GrantErrorSurfacedNotRetried(t *testing.T) {
	m := newJWTBearerMock(t)
	m.tokenStatus = http.StatusBadRequest
	m.tokenError = "invalid_grant"
	s := &JWTBearerTokenSource{
		ServerURL:     m.srv.URL + "/mcp",
		ClientID:      "wif-client",
		Assertion:     "expired.workload.jwt",
		HTTPClient:    m.srv.Client(),
		AllowInsecure: true,
	}
	_, err := s.Token()
	if err == nil {
		t.Fatal("expected error from invalid_grant")
	}
	if got := len(m.tokenRequests); got != 1 {
		t.Errorf("token requests = %d, want exactly 1 (no self-retry)", got)
	}
}

func TestJWTBearerTokenSource_ConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		src  *JWTBearerTokenSource
	}{
		{"missing client id", &JWTBearerTokenSource{ServerURL: "http://x/mcp", Assertion: "a"}},
		{"missing assertion", &JWTBearerTokenSource{ServerURL: "http://x/mcp", ClientID: "c"}},
		{"both assertion forms", &JWTBearerTokenSource{
			ServerURL: "http://x/mcp", ClientID: "c", Assertion: "a",
			AssertionProvider: func() (string, error) { return "b", nil },
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.src.Token(); err == nil {
				t.Fatal("expected config error")
			}
		})
	}
}
