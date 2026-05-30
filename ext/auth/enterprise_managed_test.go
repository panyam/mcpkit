package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type idpRequest struct {
	grantType          string
	subjectToken       string
	subjectTokenType   string
	requestedTokenType string
	audience           []string
	resource           []string
	clientID           string
}

type asRequest struct {
	grantType     string
	assertion     string
	authorization string
}

// enterpriseMockServers stands up two httptest.Servers — one for the IdP
// (RFC 8693 token-exchange) and one combining the MCP server + AS
// (RFC 7523 jwt-bearer grant) — and exposes the most recent request to
// each token endpoint for assertion.
type enterpriseMockServers struct {
	mcpAndAS *httptest.Server
	idp      *httptest.Server

	idpCalls    atomic.Int32
	asCalls     atomic.Int32
	lastIdp     idpRequest
	lastAS      asRequest
	asResponder func(asRequest) (statusCode int, body any)
}

func newEnterpriseMockServers(t *testing.T) *enterpriseMockServers {
	t.Helper()
	m := &enterpriseMockServers{}

	idpMux := http.NewServeMux()
	idpMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		m.idpCalls.Add(1)
		_ = r.ParseForm()
		m.lastIdp = idpRequest{
			grantType:          r.PostForm.Get("grant_type"),
			subjectToken:       r.PostForm.Get("subject_token"),
			subjectTokenType:   r.PostForm.Get("subject_token_type"),
			requestedTokenType: r.PostForm.Get("requested_token_type"),
			audience:           r.PostForm["audience"],
			resource:           r.PostForm["resource"],
			clientID:           r.PostForm.Get("client_id"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":      "idjag-" + m.lastIdp.subjectToken[:8],
			"issued_token_type": "urn:ietf:params:oauth:token-type:id-jag",
			"token_type":        "N_A",
		})
	})
	m.idp = httptest.NewServer(idpMux)
	t.Cleanup(m.idp.Close)

	mcpMux := http.NewServeMux()
	mcpMux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate",
			`Bearer resource_metadata="`+m.mcpAndAS.URL+`/.well-known/oauth-protected-resource/mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	mcpMux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		// PRM `resource` MUST match the URL the client invokes
		// DiscoverMCPAuth against (ServerURL: mock.mcpAndAS.URL + "/mcp").
		// validatePRMResource in discovery.go rejects any mismatch.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ProtectedResourceMetadata{
			Resource:             m.mcpAndAS.URL + "/mcp",
			AuthorizationServers: []string{m.mcpAndAS.URL},
		})
	})
	mcpMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                m.mcpAndAS.URL,
			"token_endpoint":                        m.mcpAndAS.URL + "/token",
			"grant_types_supported":                 []string{"urn:ietf:params:oauth:grant-type:jwt-bearer"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			"code_challenge_methods_supported":      []string{"S256"},
			"response_types_supported":              []string{"code"},
			"scopes_supported":                      []string{"mcp.tools"},
		})
	})
	mcpMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		m.asCalls.Add(1)
		_ = r.ParseForm()
		m.lastAS = asRequest{
			grantType:     r.PostForm.Get("grant_type"),
			assertion:     r.PostForm.Get("assertion"),
			authorization: r.Header.Get("Authorization"),
		}
		if m.asResponder != nil {
			status, body := m.asResponder(m.lastAS)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "mcp-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	m.mcpAndAS = httptest.NewServer(mcpMux)
	t.Cleanup(m.mcpAndAS.Close)

	return m
}

func TestEnterpriseManagedTokenSource_TwoStageRoundtrip(t *testing.T) {
	mock := newEnterpriseMockServers(t)
	ts := &EnterpriseManagedTokenSource{
		ServerURL:        mock.mcpAndAS.URL + "/mcp",
		ClientID:         "mcp-client",
		ClientSecret:     "mcp-secret",
		IdpClientID:      "idp-client",
		IdpIDToken:       "idtoken12345abcdef",
		IdpTokenEndpoint: mock.idp.URL + "/token",
		HTTPClient:       mock.mcpAndAS.Client(),
		AllowInsecure:    true,
	}

	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "mcp-access-token" {
		t.Errorf("Token() = %q, want mcp-access-token", tok)
	}

	if got, want := mock.lastIdp.grantType, "urn:ietf:params:oauth:grant-type:token-exchange"; got != want {
		t.Errorf("idp grant_type = %q, want %q", got, want)
	}
	if got, want := mock.lastIdp.subjectToken, "idtoken12345abcdef"; got != want {
		t.Errorf("idp subject_token = %q, want %q", got, want)
	}
	if got, want := mock.lastIdp.subjectTokenType, "urn:ietf:params:oauth:token-type:id_token"; got != want {
		t.Errorf("idp subject_token_type = %q, want %q", got, want)
	}
	if got, want := mock.lastIdp.requestedTokenType, "urn:ietf:params:oauth:token-type:id-jag"; got != want {
		t.Errorf("idp requested_token_type = %q, want %q", got, want)
	}
	if len(mock.lastIdp.audience) != 1 || mock.lastIdp.audience[0] != mock.mcpAndAS.URL {
		t.Errorf("idp audience = %v, want [%q]", mock.lastIdp.audience, mock.mcpAndAS.URL)
	}
	wantResource := mock.mcpAndAS.URL + "/mcp"
	if len(mock.lastIdp.resource) != 1 || mock.lastIdp.resource[0] != wantResource {
		t.Errorf("idp resource = %v, want [%q]", mock.lastIdp.resource, wantResource)
	}

	if got, want := mock.lastAS.grantType, "urn:ietf:params:oauth:grant-type:jwt-bearer"; got != want {
		t.Errorf("as grant_type = %q, want %q", got, want)
	}
	if !strings.HasPrefix(mock.lastAS.assertion, "idjag-") {
		t.Errorf("as assertion = %q, want id-jag from stage 1", mock.lastAS.assertion)
	}
	if !strings.HasPrefix(mock.lastAS.authorization, "Basic ") {
		t.Fatalf("as Authorization = %q, want Basic prefix", mock.lastAS.authorization)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(mock.lastAS.authorization, "Basic "))
	if err != nil {
		t.Fatalf("decode basic auth: %v", err)
	}
	if got, want := string(decoded), "mcp-client:mcp-secret"; got != want {
		t.Errorf("as basic creds = %q, want %q", got, want)
	}

	tok2, err := ts.Token()
	if err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if tok2 != tok {
		t.Errorf("cached Token() = %q, want %q", tok2, tok)
	}
	if mock.idpCalls.Load() != 1 {
		t.Errorf("idp call count = %d after cache hit, want 1", mock.idpCalls.Load())
	}
	if mock.asCalls.Load() != 1 {
		t.Errorf("as call count = %d after cache hit, want 1", mock.asCalls.Load())
	}
}

func TestEnterpriseManagedTokenSource_IdpClientIDFlowsThrough(t *testing.T) {
	mock := newEnterpriseMockServers(t)
	ts := &EnterpriseManagedTokenSource{
		ServerURL:        mock.mcpAndAS.URL + "/mcp",
		ClientID:         "mcp-client",
		ClientSecret:     "mcp-secret",
		IdpClientID:      "enterprise-idp-client",
		IdpIDToken:       "idtokenABCDEFGHIJ",
		IdpTokenEndpoint: mock.idp.URL + "/token",
		HTTPClient:       mock.mcpAndAS.Client(),
		AllowInsecure:    true,
	}
	if _, err := ts.Token(); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got, want := mock.lastIdp.clientID, "enterprise-idp-client"; got != want {
		t.Errorf("idp client_id = %q, want %q", got, want)
	}
}

func TestEnterpriseManagedTokenSource_ValidationErrors(t *testing.T) {
	mock := newEnterpriseMockServers(t)
	base := EnterpriseManagedTokenSource{
		ServerURL:        mock.mcpAndAS.URL + "/mcp",
		ClientID:         "mcp-client",
		ClientSecret:     "mcp-secret",
		IdpIDToken:       "idtoken12345abcdef",
		IdpTokenEndpoint: mock.idp.URL + "/token",
		HTTPClient:       mock.mcpAndAS.Client(),
		AllowInsecure:    true,
	}
	cases := []struct {
		name    string
		mut     func(*EnterpriseManagedTokenSource)
		wantMsg string
	}{
		{"missing ClientID", func(s *EnterpriseManagedTokenSource) { s.ClientID = "" }, "ClientID is required"},
		{"missing ClientSecret", func(s *EnterpriseManagedTokenSource) { s.ClientSecret = "" }, "ClientSecret is required"},
		{"missing IdpIDToken", func(s *EnterpriseManagedTokenSource) { s.IdpIDToken = "" }, "IdpIDToken is required"},
		{"missing IdpTokenEndpoint", func(s *EnterpriseManagedTokenSource) { s.IdpTokenEndpoint = "" }, "IdpTokenEndpoint is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := base
			tc.mut(&ts)
			_, err := ts.Token()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want substring %q", err, tc.wantMsg)
			}
		})
	}
}

func TestEnterpriseManagedTokenSource_IdpRejects(t *testing.T) {
	mock := newEnterpriseMockServers(t)
	// Override IdP /token to reject with invalid_grant
	mock.idp.Close()
	idpMux := http.NewServeMux()
	idpMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "bad subject_token"})
	})
	mock.idp = httptest.NewServer(idpMux)
	t.Cleanup(mock.idp.Close)

	ts := &EnterpriseManagedTokenSource{
		ServerURL:        mock.mcpAndAS.URL + "/mcp",
		ClientID:         "mcp-client",
		ClientSecret:     "mcp-secret",
		IdpIDToken:       "idtoken12345abcdef",
		IdpTokenEndpoint: mock.idp.URL + "/token",
		HTTPClient:       mock.mcpAndAS.Client(),
		AllowInsecure:    true,
	}
	_, err := ts.Token()
	if err == nil {
		t.Fatal("expected idp rejection error")
	}
	if !strings.Contains(err.Error(), "idp token exchange") {
		t.Errorf("error = %q, want substring 'idp token exchange'", err)
	}
}
