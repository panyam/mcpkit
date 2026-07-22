package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockMCPAuthServer creates an httptest.Server that simulates:
// - An MCP server that returns 401 with WWW-Authenticate on POST /mcp
// - A PRM endpoint at /.well-known/oauth-protected-resource/mcp
// - An AS metadata endpoint at /.well-known/oauth-authorization-server
//
// This allows testing DiscoverMCPAuth end-to-end without real servers.
func mockMCPAuthServer(t *testing.T, opts ...mockOption) *httptest.Server {
	t.Helper()
	cfg := &mockConfig{
		scopes:      []string{"tools:read", "tools:call"},
		prmScopes:   []string{"tools:read", "tools:call"},
		servePRM:    true,
		serveASMeta: true,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	mux := http.NewServeMux()
	var srv *httptest.Server

	// MCP endpoint — returns 401 with WWW-Authenticate
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			wwwAuth := `Bearer resource_metadata="` + srv.URL + `/.well-known/oauth-protected-resource/mcp"`
			if cfg.headerScope != "" {
				wwwAuth += `, scope="` + cfg.headerScope + `"`
			}
			w.Header().Set("WWW-Authenticate", wwwAuth)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// resourceValue resolves the PRM `resource` field for the handlers
	// below. Default matches the URL the test invokes DiscoverMCPAuth
	// against (${srv.URL}/mcp) so PRM-resource validation passes; tests
	// that want a mismatched / empty / oddly-formatted value pass
	// withPRMResourceFn to override.
	resourceValue := func() string {
		if cfg.prmResourceFn != nil {
			return cfg.prmResourceFn(srv.URL)
		}
		return srv.URL + "/mcp"
	}

	// PRM endpoint (path-based)
	if cfg.servePRM {
		mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
			prm := ProtectedResourceMetadata{
				Resource:             resourceValue(),
				AuthorizationServers: []string{srv.URL},
				ScopesSupported:      cfg.prmScopes,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(prm)
		})
	}

	// PRM endpoint (root fallback)
	if cfg.serveRootPRM {
		mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
			prm := ProtectedResourceMetadata{
				Resource:             resourceValue(),
				AuthorizationServers: []string{srv.URL},
				ScopesSupported:      cfg.prmScopes,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(prm)
		})
	}

	// AS metadata endpoint (RFC 8414)
	if cfg.serveASMeta {
		mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
			meta := map[string]any{
				"issuer":                              srv.URL,
				"authorization_endpoint":              srv.URL + "/authorize",
				"token_endpoint":                      srv.URL + "/token",
				"code_challenge_methods_supported":     cfg.codeChallenges,
				"scopes_supported":                    cfg.scopes,
				"response_types_supported":            []string{"code"},
				"grant_types_supported":               []string{"authorization_code"},
				"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"},
			}
			if cfg.registrationEndpoint {
				meta["registration_endpoint"] = srv.URL + "/register"
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(meta)
		})
	}

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Default to S256 support unless overridden
	if cfg.codeChallenges == nil {
		cfg.codeChallenges = []string{"S256"}
	}

	return srv
}

type mockConfig struct {
	scopes               []string
	prmScopes            []string
	headerScope          string
	codeChallenges       []string
	servePRM             bool
	serveRootPRM         bool
	serveASMeta          bool
	registrationEndpoint bool

	// prmResourceFn lets a test override the PRM `resource` field. It
	// receives the mock server's base URL (since httptest assigns the
	// port at construction time and the handler closes over `srv`) and
	// returns the value to emit. nil means "use the default", which is
	// `${srv.URL}/mcp` — matching the URL the test invokes
	// DiscoverMCPAuth against, so resource-validation passes.
	prmResourceFn func(srvURL string) string
}

type mockOption func(*mockConfig)

func withHeaderScope(s string) mockOption {
	return func(c *mockConfig) { c.headerScope = s }
}

func withPRMScopes(s []string) mockOption {
	return func(c *mockConfig) { c.prmScopes = s }
}

func withNoPRM() mockOption {
	return func(c *mockConfig) { c.servePRM = false }
}

func withRootPRM() mockOption {
	return func(c *mockConfig) { c.serveRootPRM = true }
}

func withCodeChallenges(methods []string) mockOption {
	return func(c *mockConfig) { c.codeChallenges = methods }
}

func withRegistrationEndpoint() mockOption {
	return func(c *mockConfig) { c.registrationEndpoint = true }
}

// withPRMResourceFn overrides the PRM `resource` field. The closure
// receives the mock server's base URL (post-listen) so callers can
// compose values that depend on the assigned port.
func withPRMResourceFn(fn func(srvURL string) string) mockOption {
	return func(c *mockConfig) { c.prmResourceFn = fn }
}

// TestDiscoverMCPAuth_FullChain verifies the complete discovery chain:
// probe → 401 → parse WWW-Authenticate → fetch PRM → discover AS metadata.
// Uses an httptest server that simulates all three endpoints (MCP, PRM, AS metadata).
func TestDiscoverMCPAuth_FullChain(t *testing.T) {
	srv := mockMCPAuthServer(t, withCodeChallenges([]string{"S256"}))

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth failed: %v", err)
	}

	// Verify PRM was fetched
	if info.PRM == nil {
		t.Fatal("PRM is nil")
	}
	if want := srv.URL + "/mcp"; info.PRM.Resource != want {
		t.Errorf("PRM resource = %q, want %q", info.PRM.Resource, want)
	}

	// Verify authorization servers
	if len(info.AuthorizationServers) == 0 {
		t.Fatal("no authorization servers")
	}
	if info.AuthorizationServers[0] != srv.URL {
		t.Errorf("AS[0] = %q, want %q", info.AuthorizationServers[0], srv.URL)
	}

	// Verify AS metadata
	if info.ASMetadata == nil {
		t.Fatal("ASMetadata is nil")
	}
	if info.ASMetadata.TokenEndpoint != srv.URL+"/token" {
		t.Errorf("token_endpoint = %q, want %q", info.ASMetadata.TokenEndpoint, srv.URL+"/token")
	}
	if info.ASMetadata.AuthorizationEndpoint != srv.URL+"/authorize" {
		t.Errorf("authorization_endpoint = %q, want %q", info.ASMetadata.AuthorizationEndpoint, srv.URL+"/authorize")
	}
}

// TestDiscoverMCPAuth_DoesNotPinScopeFromProbe verifies that discovery no
// longer captures the probe's WWW-Authenticate scope= for token acquisition
// (issue 818). Discovery's job is endpoint resolution; the only scope it
// exposes is the PRM scopes_supported catalog (info.PRM.ScopesSupported).
// Per-operation scope is selected later from the real request's 401
// challenge (RFC 6750 §3.1), not pinned here from a fixed probe method.
func TestDiscoverMCPAuth_DoesNotPinScopeFromProbe(t *testing.T) {
	srv := mockMCPAuthServer(t,
		withHeaderScope("tools:read admin:write"),
		withPRMScopes([]string{"tools:read", "tools:call", "admin:write"}),
		withCodeChallenges([]string{"S256"}),
	)

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth failed: %v", err)
	}

	// The probe's scope= ("tools:read admin:write", 2 scopes) must NOT win;
	// the catalog discovery exposes is the full PRM scopes_supported (3).
	if got := info.PRM.ScopesSupported; len(got) != 3 {
		t.Fatalf("expected 3 PRM scopes_supported, got %d: %v", len(got), got)
	}
}

// TestDiscoverMCPAuth_PRMScopesExposed verifies that PRM scopes_supported is
// surfaced on info.PRM for token sources to use as the catalog fallback when
// a 401 challenge carries no scope=.
func TestDiscoverMCPAuth_PRMScopesExposed(t *testing.T) {
	srv := mockMCPAuthServer(t,
		// No headerScope — WWW-Authenticate has no scope= param
		withPRMScopes([]string{"tools:read", "tools:call"}),
		withCodeChallenges([]string{"S256"}),
	)

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth failed: %v", err)
	}

	if len(info.PRM.ScopesSupported) != 2 {
		t.Fatalf("expected 2 PRM scopes_supported, got %d: %v",
			len(info.PRM.ScopesSupported), info.PRM.ScopesSupported)
	}
}

// TestDiscoverMCPAuth_WellKnownPathBased verifies that the well-known URL
// is correctly constructed as scheme://host/.well-known/oauth-protected-resource/path
// (not serverURL + "/.well-known/...").
func TestDiscoverMCPAuth_WellKnownPathBased(t *testing.T) {
	srv := mockMCPAuthServer(t, withCodeChallenges([]string{"S256"}))

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth failed: %v", err)
	}

	// The resource_metadata URL from WWW-Authenticate should be used
	expectedPRM := srv.URL + "/.well-known/oauth-protected-resource/mcp"
	if info.ResourceMetadataURL != expectedPRM {
		t.Errorf("ResourceMetadataURL = %q, want %q", info.ResourceMetadataURL, expectedPRM)
	}
}

// TestDiscoverMCPAuth_WellKnownRootFallback verifies that when the path-based
// well-known URL returns 404, the discovery falls back to the root well-known URL.
func TestDiscoverMCPAuth_WellKnownRootFallback(t *testing.T) {
	// Server with root PRM only (no path-based), and no WWW-Authenticate resource_metadata
	mux := http.NewServeMux()
	var srv *httptest.Server

	// MCP endpoint — 401 without resource_metadata in header
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})

	// Only root PRM (no path-based). Resource = the actual MCP server
	// URL the client invokes DiscoverMCPAuth against — same canonical
	// form the new PRM-resource-validation check expects.
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		prm := ProtectedResourceMetadata{
			Resource:             srv.URL + "/mcp",
			AuthorizationServers: []string{srv.URL},
			ScopesSupported:      []string{"tools:read"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(prm)
	})

	// AS metadata
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer":                           srv.URL,
			"authorization_endpoint":           srv.URL + "/authorize",
			"token_endpoint":                   srv.URL + "/token",
			"code_challenge_methods_supported":  []string{"S256"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(meta)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth failed: %v", err)
	}

	if info.PRM == nil {
		t.Fatal("PRM is nil — root fallback did not work")
	}
	if want := srv.URL + "/mcp"; info.PRM.Resource != want {
		t.Errorf("PRM resource = %q, want %q", info.PRM.Resource, want)
	}
}

// TestDiscoverMCPAuth_PRMResourceMismatch_Rejected verifies that a PRM
// emitting a `resource` field pointing somewhere other than the MCP
// server URL is rejected with ErrPRMResourceMismatch — the
// token-recipient-confusion vector tested by the upstream
// `auth/resource-mismatch` scenario (RFC 8707 §2 / SEP-2352).
func TestDiscoverMCPAuth_PRMResourceMismatch_Rejected(t *testing.T) {
	srv := mockMCPAuthServer(t,
		withCodeChallenges([]string{"S256"}),
		withPRMResourceFn(func(_ string) string { return "https://evil.example.com/mcp" }),
	)

	_, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err == nil {
		t.Fatal("expected error for mismatched PRM resource, got nil")
	}
	if !errors.Is(err, ErrPRMResourceMismatch) {
		t.Errorf("error = %v, want errors.Is(err, ErrPRMResourceMismatch)", err)
	}
}

// TestDiscoverMCPAuth_PRMResourceMatch_TrailingSlash verifies that a PRM
// `resource` differing only in trailing slash from the MCP server URL is
// accepted. RFC 3986 §6.2.3 says trailing slash is significant; mcpkit
// normalizes it for resource comparison because real-world PRM emitters
// frequently get this wrong and the path semantics are otherwise
// identical.
func TestDiscoverMCPAuth_PRMResourceMatch_TrailingSlash(t *testing.T) {
	srv := mockMCPAuthServer(t,
		withCodeChallenges([]string{"S256"}),
		withPRMResourceFn(func(srvURL string) string { return srvURL + "/mcp/" }),
	)

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth: %v", err)
	}
	if info.PRM == nil {
		t.Fatal("PRM is nil")
	}
}

// TestDiscoverMCPAuth_PRMResourceOriginOnly_Accepts is the carve-out
// for AS-coupled deployments where the PRM document covers the whole
// origin and emits `resource: "http://host:port"` (no path) against a
// server mounted at a path like `/mcp`. The upstream
// `auth/metadata-var*` fixtures use this shape; rejecting them would
// break those scenarios. ORIGIN mismatch is still rejected — that's
// the load-bearing security check the upstream
// `auth/resource-mismatch` scenario grades.
func TestDiscoverMCPAuth_PRMResourceOriginOnly_Accepts(t *testing.T) {
	srv := mockMCPAuthServer(t,
		withCodeChallenges([]string{"S256"}),
		withPRMResourceFn(func(srvURL string) string { return srvURL }), // origin only
	)

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth: %v", err)
	}
	if info.PRM == nil {
		t.Fatal("PRM is nil")
	}
}

// TestDiscoverMCPAuth_PRMResourceEmpty_Accepts documents the
// backwards-compat carve-out: a PRM that omits the `resource` field
// entirely is accepted (treated as "no resource binding asserted").
// Some early PRM emitters omit the field; rejecting outright would
// break working integrations and isn't what the upstream
// `resource-mismatch` scenario tests.
func TestDiscoverMCPAuth_PRMResourceEmpty_Accepts(t *testing.T) {
	srv := mockMCPAuthServer(t,
		withCodeChallenges([]string{"S256"}),
		withPRMResourceFn(func(_ string) string { return "" }),
	)

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth: %v", err)
	}
	if info.PRM == nil {
		t.Fatal("PRM is nil")
	}
	if info.PRM.Resource != "" {
		t.Errorf("PRM resource = %q, want empty", info.PRM.Resource)
	}
}

// TestDiscoverMCPAuth_NoPRM verifies that DiscoverMCPAuth returns an error
// when both path-based and root well-known URIs return 404.
func TestDiscoverMCPAuth_NoPRM_LegacyDefaultEndpoints(t *testing.T) {
	// A server with no PRM and no AS metadata is the 2025-03-26
	// endpoint-fallback shape: discovery synthesizes the legacy default
	// endpoints at the origin instead of erroring.
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth: %v", err)
	}
	if !info.LegacyDiscovery {
		t.Error("expected LegacyDiscovery to be set")
	}
	if info.PRM != nil {
		t.Error("expected nil PRM on the legacy path")
	}
	if got, want := info.ASMetadata.AuthorizationEndpoint, srv.URL+"/authorize"; got != want {
		t.Errorf("AuthorizationEndpoint = %q, want %q", got, want)
	}
	if got, want := info.ASMetadata.TokenEndpoint, srv.URL+"/token"; got != want {
		t.Errorf("TokenEndpoint = %q, want %q", got, want)
	}
	if got, want := info.ASMetadata.RegistrationEndpoint, srv.URL+"/register"; got != want {
		t.Errorf("RegistrationEndpoint = %q, want %q", got, want)
	}
	if got, want := len(info.AuthorizationServers), 1; got != want || info.AuthorizationServers[0] != srv.URL {
		t.Errorf("AuthorizationServers = %v, want [%s]", info.AuthorizationServers, srv.URL)
	}
	if err := ValidatePKCES256(info.ASMetadata); err != nil {
		t.Errorf("synthesized metadata must pass the PKCE gate: %v", err)
	}
}

func TestDiscoverMCPAuth_Legacy_ASMetadataAtRoot(t *testing.T) {
	// The 2025-03-26 metadata-backcompat shape: no PRM, but AS metadata
	// at the origin's oauth-authorization-server well-known path. The
	// advertised endpoints must win over the synthesized defaults.
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srvURL,
			"authorization_endpoint": srvURL + "/oauth/authorize",
			"token_endpoint":         srvURL + "/oauth/token",
			"registration_endpoint":  srvURL + "/oauth/register",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	srvURL = srv.URL

	info, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("DiscoverMCPAuth: %v", err)
	}
	if !info.LegacyDiscovery {
		t.Error("expected LegacyDiscovery to be set")
	}
	if got, want := info.ASMetadata.AuthorizationEndpoint, srv.URL+"/oauth/authorize"; got != want {
		t.Errorf("AuthorizationEndpoint = %q, want %q", got, want)
	}
	if got, want := info.ASMetadata.TokenEndpoint, srv.URL+"/oauth/token"; got != want {
		t.Errorf("TokenEndpoint = %q, want %q", got, want)
	}
	if got, want := info.ASMetadata.RegistrationEndpoint, srv.URL+"/oauth/register"; got != want {
		t.Errorf("RegistrationEndpoint = %q, want %q", got, want)
	}
}

func TestDiscoverMCPAuth_NoLegacyFallbackOnPRMServerError(t *testing.T) {
	// The downgrade guard: only a definitive 404 at both PRM locations
	// enters the legacy path. A 5xx (transient failure, misbehaving
	// proxy, attacker-induced error) must abort discovery, and no legacy
	// well-known or default-endpoint request may be made.
	for _, tc := range []struct {
		name          string
		pathBasedCode int
		rootCode      int
	}{
		{"path-based 500", http.StatusInternalServerError, http.StatusNotFound},
		{"root 500 after path 404", http.StatusNotFound, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacyProbed := false
			mux := http.NewServeMux()
			mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			})
			mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", tc.pathBasedCode)
			})
			mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", tc.rootCode)
			})
			mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
				legacyProbed = true
				http.Error(w, "should not be reached", http.StatusNotFound)
			})

			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			_, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
			if err == nil {
				t.Fatal("expected error on PRM server error, got legacy fallback")
			}
			if legacyProbed {
				t.Error("legacy AS metadata endpoint was probed despite PRM server error")
			}
		})
	}
}

func TestDiscoverMCPAuth_NoLegacyFallbackWhenHeaderAdvertisesPRM(t *testing.T) {
	// A WWW-Authenticate resource_metadata link marks the server as
	// modern: a failing fetch of that URL must error, never fall back
	// to legacy discovery.
	mux := http.NewServeMux()
	var srvURL string
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srvURL+`/.well-known/oauth-protected-resource/mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	// The advertised PRM URL 404s; nothing else is served.

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	srvURL = srv.URL

	_, err := DiscoverMCPAuth(srv.URL+"/mcp", WithHTTPClient(srv.Client()))
	if err == nil {
		t.Fatal("expected error when the header-advertised PRM URL fails")
	}
}
