package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// ccMockAS simulates an MCP server + AS pair for client_credentials testing.
// /mcp returns 401, /.well-known/* serves PRM and AS metadata, /token
// handles the client_credentials grant. The test installs an inspector
// function to assert on each /token request.
type ccMockAS struct {
	*httptest.Server
	tokenInspector func(r *http.Request, body map[string]string) tokenResponse
	tokenCallCount atomic.Int32
}

type tokenResponse struct {
	statusCode  int
	accessToken string
	expiresIn   int
	errorCode   string
	errorDesc   string
}

func newCCMockAS(t *testing.T, authMethods []string) *ccMockAS {
	t.Helper()
	mock := &ccMockAS{}
	mux := http.NewServeMux()

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate",
			`Bearer resource_metadata="`+mock.URL+`/.well-known/oauth-protected-resource/mcp"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		prm := ProtectedResourceMetadata{
			Resource:             mock.URL,
			AuthorizationServers: []string{mock.URL},
			ScopesSupported:      []string{"mcp.tools"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(prm)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer":                                mock.URL,
			"token_endpoint":                        mock.URL + "/token",
			"grant_types_supported":                 []string{"client_credentials"},
			"token_endpoint_auth_methods_supported": authMethods,
			"scopes_supported":                      []string{"mcp.tools"},
			"code_challenge_methods_supported":      []string{"S256"},
			"response_types_supported":              []string{"code"},
		}
		if slices.Contains(authMethods, "private_key_jwt") {
			meta["token_endpoint_auth_signing_alg_values_supported"] = []string{"ES256", "RS256"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(meta)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		mock.tokenCallCount.Add(1)
		_ = r.ParseForm()
		body := map[string]string{}
		for k := range r.PostForm {
			body[k] = r.PostForm.Get(k)
		}
		resp := mock.tokenInspector(r, body)
		w.Header().Set("Content-Type", "application/json")
		if resp.statusCode > 0 && resp.statusCode != http.StatusOK {
			w.WriteHeader(resp.statusCode)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":             resp.errorCode,
				"error_description": resp.errorDesc,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": resp.accessToken,
			"token_type":   "Bearer",
			"expires_in":   resp.expiresIn,
		})
	})

	mock.Server = httptest.NewServer(mux)
	t.Cleanup(mock.Close)
	return mock
}

// TestClientCredentialsTokenSource_BasicAuth_TokenRoundtrip exercises the
// client_secret_basic variant end-to-end: PRM discovery, AS metadata,
// grant_type=client_credentials + Authorization: Basic header at /token,
// then a second Token() call returns the cached value without re-hitting
// the AS.
func TestClientCredentialsTokenSource_BasicAuth_TokenRoundtrip(t *testing.T) {
	mock := newCCMockAS(t, []string{"client_secret_basic"})
	mock.tokenInspector = func(r *http.Request, body map[string]string) tokenResponse {
		if got, want := body["grant_type"], "client_credentials"; got != want {
			t.Errorf("grant_type = %q, want %q", got, want)
		}
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Basic ") {
			t.Errorf("Authorization header = %q, want Basic prefix", authHeader)
			return tokenResponse{statusCode: 401, errorCode: "invalid_client"}
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
		if err != nil {
			t.Errorf("decode basic auth: %v", err)
			return tokenResponse{statusCode: 401}
		}
		if got, want := string(decoded), "my-client:my-secret"; got != want {
			t.Errorf("basic creds = %q, want %q", got, want)
		}
		return tokenResponse{accessToken: "cc-token-basic", expiresIn: 3600}
	}

	ts := &ClientCredentialsTokenSource{
		ServerURL:     mock.URL + "/mcp",
		ClientID:      "my-client",
		ClientSecret:  "my-secret",
		HTTPClient:    mock.Client(),
		AllowInsecure: true,
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "cc-token-basic" {
		t.Errorf("Token() = %q, want cc-token-basic", tok)
	}
	if mock.tokenCallCount.Load() != 1 {
		t.Errorf("/token call count = %d, want 1", mock.tokenCallCount.Load())
	}

	tok2, err := ts.Token()
	if err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if tok2 != tok {
		t.Errorf("cached Token() = %q, want %q", tok2, tok)
	}
	if mock.tokenCallCount.Load() != 1 {
		t.Errorf("/token call count after cache hit = %d, want 1", mock.tokenCallCount.Load())
	}
}

// TestClientCredentialsTokenSource_JWT_TokenRoundtrip exercises the
// private_key_jwt variant: the AS verifies client_assertion_type,
// the JWT signature, iss/sub=client_id, and that aud equals the AS
// issuer URL (not the token endpoint) per RFC 7523bis / SEP-1046.
func TestClientCredentialsTokenSource_JWT_TokenRoundtrip(t *testing.T) {
	privKey, privPEM := mustGenerateES256Keypair(t)

	mock := newCCMockAS(t, []string{"private_key_jwt"})
	mock.tokenInspector = func(r *http.Request, body map[string]string) tokenResponse {
		if got, want := body["grant_type"], "client_credentials"; got != want {
			t.Errorf("grant_type = %q, want %q", got, want)
		}
		if got, want := body["client_assertion_type"], "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"; got != want {
			t.Errorf("client_assertion_type = %q, want %q", got, want)
		}
		assertion := body["client_assertion"]
		if assertion == "" {
			t.Error("client_assertion missing")
			return tokenResponse{statusCode: 401, errorCode: "invalid_client"}
		}
		tok, err := jwt.Parse(assertion, func(t *jwt.Token) (any, error) {
			return &privKey.PublicKey, nil
		}, jwt.WithValidMethods([]string{"ES256"}))
		if err != nil {
			t.Errorf("verify assertion: %v", err)
			return tokenResponse{statusCode: 401, errorCode: "invalid_client"}
		}
		claims := tok.Claims.(jwt.MapClaims)
		if got, want := claims["iss"], "my-client"; got != want {
			t.Errorf("iss = %v, want %v", got, want)
		}
		if got, want := claims["sub"], "my-client"; got != want {
			t.Errorf("sub = %v, want %v", got, want)
		}
		if got, want := claims["aud"], mock.URL; got != want {
			t.Errorf("aud = %v, want %v (issuer URL, not token endpoint)", got, want)
		}
		return tokenResponse{accessToken: "cc-token-jwt", expiresIn: 3600}
	}

	ts := &ClientCredentialsTokenSource{
		ServerURL:        mock.URL + "/mcp",
		ClientID:         "my-client",
		PrivateKeyPEM:    privPEM,
		SigningAlgorithm: "ES256",
		HTTPClient:       mock.Client(),
		AllowInsecure:    true,
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "cc-token-jwt" {
		t.Errorf("Token() = %q, want cc-token-jwt", tok)
	}
}

func TestClientCredentialsTokenSource_ValidationErrors(t *testing.T) {
	mock := newCCMockAS(t, []string{"client_secret_basic"})

	cases := []struct {
		name    string
		ts      ClientCredentialsTokenSource
		wantMsg string
	}{
		{
			name:    "missing ClientID",
			ts:      ClientCredentialsTokenSource{ServerURL: mock.URL + "/mcp", ClientSecret: "x"},
			wantMsg: "ClientID is required",
		},
		{
			name:    "missing credentials",
			ts:      ClientCredentialsTokenSource{ServerURL: mock.URL + "/mcp", ClientID: "c"},
			wantMsg: "ClientSecret or PrivateKeyPEM is required",
		},
		{
			name:    "both credentials set",
			ts:      ClientCredentialsTokenSource{ServerURL: mock.URL + "/mcp", ClientID: "c", ClientSecret: "x", PrivateKeyPEM: "y"},
			wantMsg: "mutually exclusive",
		},
		{
			name:    "JWT missing SigningAlgorithm",
			ts:      ClientCredentialsTokenSource{ServerURL: mock.URL + "/mcp", ClientID: "c", PrivateKeyPEM: "y"},
			wantMsg: "SigningAlgorithm is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.ts.HTTPClient = mock.Client()
			tc.ts.AllowInsecure = true
			_, err := tc.ts.Token()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want substring %q", err, tc.wantMsg)
			}
		})
	}
}

func TestClientCredentialsTokenSource_InvalidPEM(t *testing.T) {
	mock := newCCMockAS(t, []string{"private_key_jwt"})
	ts := &ClientCredentialsTokenSource{
		ServerURL:        mock.URL + "/mcp",
		ClientID:         "c",
		PrivateKeyPEM:    "not a pem",
		SigningAlgorithm: "ES256",
		HTTPClient:       mock.Client(),
		AllowInsecure:    true,
	}
	_, err := ts.Token()
	if err == nil {
		t.Fatal("expected PEM parse error")
	}
	if !strings.Contains(err.Error(), "parse private key") {
		t.Errorf("error = %q, want parse private key", err)
	}
}

func mustGenerateES256Keypair(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ES256 key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if pemBytes == nil {
		t.Fatal("nil PEM bytes")
	}
	return priv, string(pemBytes)
}
