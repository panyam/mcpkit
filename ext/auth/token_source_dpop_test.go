package auth

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/oneauth/apiauth"
	"github.com/panyam/oneauth/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func proofPayload(t *testing.T, proof string) map[string]any {
	t.Helper()
	parts := strings.Split(proof, ".")
	require.Len(t, parts, 3)
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func TestOAuthTokenSource_AuthorizeRequest_BearerWithoutKey(t *testing.T) {
	s := &OAuthTokenSource{}
	r := httptest.NewRequest(http.MethodPost, "https://mcp.test/mcp", nil)
	require.NoError(t, s.AuthorizeRequest(r, "tok"))
	assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
	assert.Empty(t, r.Header.Get("DPoP"))

	resp := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}
	resp.Header.Set("DPoP-Nonce", "n1")
	resp.Header.Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
	assert.False(t, s.ObserveResponse(r, resp), "a bearer source never retries for a nonce")
}

func TestOAuthTokenSource_AuthorizeRequest_DPoP(t *testing.T) {
	key, err := client.NewDPoPKey()
	require.NoError(t, err)
	s := &OAuthTokenSource{DPoPKey: key}

	r := httptest.NewRequest(http.MethodPost, "https://mcp.test/mcp?x=1", nil)
	require.NoError(t, s.AuthorizeRequest(r, "tok"))
	assert.Equal(t, "DPoP tok", r.Header.Get("Authorization"))
	p := proofPayload(t, r.Header.Get("DPoP"))
	assert.Equal(t, "POST", p["htm"])
	assert.Equal(t, "https://mcp.test/mcp", p["htu"], "query is not part of htu")
	assert.NotEmpty(t, p["ath"])
	assert.NotContains(t, p, "nonce")
}

// A nonce from one origin is used on that origin's next proof and nowhere
// else, and only a 401 use_dpop_nonce asks for a retry.
func TestOAuthTokenSource_ObserveResponse_NoncePerOrigin(t *testing.T) {
	key, err := client.NewDPoPKey()
	require.NoError(t, err)
	s := &OAuthTokenSource{DPoPKey: key}
	a := httptest.NewRequest(http.MethodPost, "https://a.test/mcp", nil)

	ok := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	ok.Header.Set("DPoP-Nonce", "from-200")
	assert.False(t, s.ObserveResponse(a, ok), "a nonce on a 200 is kept, not retried for")

	challenge := &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}
	challenge.Header.Set("DPoP-Nonce", "n1")
	challenge.Header.Set("WWW-Authenticate", `DPoP error="use_dpop_nonce"`)
	assert.True(t, s.ObserveResponse(a, challenge))

	next := httptest.NewRequest(http.MethodPost, "https://a.test/other", nil)
	require.NoError(t, s.AuthorizeRequest(next, "tok"))
	assert.Equal(t, "n1", proofPayload(t, next.Header.Get("DPoP"))["nonce"])

	b := httptest.NewRequest(http.MethodPost, "https://b.test/mcp", nil)
	require.NoError(t, s.AuthorizeRequest(b, "tok"))
	assert.NotContains(t, proofPayload(t, b.Header.Get("DPoP")), "nonce", "a nonce never crosses origins")
}

func TestMountAuth_AdvertisesDPoP(t *testing.T) {
	for _, required := range []bool{false, true} {
		v := NewJWTValidator(JWTConfig{
			JWKSURL:     "https://auth.example.com/jwks",
			DPoP:        apiauth.NewDPoPProofValidator(apiauth.DPoPConfig{AllowedAlgs: []string{"ES256"}}),
			RequireDPoP: required,
		})
		mux := http.NewServeMux()
		MountAuth(mux, AuthConfig{
			ResourceURI:          "https://mcp.example.com",
			AuthorizationServers: []string{"https://auth.example.com"},
			MCPPath:              "/mcp",
			Validator:            v,
		})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
		require.Equal(t, http.StatusOK, rec.Code)

		var prm map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prm))
		assert.Equal(t, []any{"ES256"}, prm["dpop_signing_alg_values_supported"])
		if required {
			assert.Equal(t, true, prm["dpop_bound_access_tokens_required"])
		} else {
			assert.NotContains(t, prm, "dpop_bound_access_tokens_required")
		}
	}
}

func TestMountAuth_NoDPoPFieldsWithoutDPoP(t *testing.T) {
	mux := http.NewServeMux()
	MountAuth(mux, AuthConfig{
		ResourceURI:          "https://mcp.example.com",
		AuthorizationServers: []string{"https://auth.example.com"},
		MCPPath:              "/mcp",
		Validator:            NewJWTValidator(JWTConfig{JWKSURL: "https://auth.example.com/jwks"}),
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil))
	assert.NotContains(t, rec.Body.String(), "dpop")
}
