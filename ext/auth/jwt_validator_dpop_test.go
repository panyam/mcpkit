package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	mcpcore "github.com/panyam/mcpkit/core"
	"github.com/panyam/oneauth/apiauth"
	"github.com/panyam/oneauth/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The resource URL the proofs are minted for, and the BaseURL the validator
// is told clients call. httptest requests carry no TLS, so without BaseURL the
// validator would derive http://example.com and refuse every https proof.
const (
	dpopBaseURL  = "https://mcp.test"
	dpopResource = dpopBaseURL + "/mcp"
)

// dpopFixture is a JWKS-backed validator with DPoP wired, plus a client key
// and a token bound to it.
type dpopFixture struct {
	v       *JWTValidator
	key     *client.DPoPKey
	token   string // bound to key via cnf.jkt
	unbound string // no cnf
}

func newDPoPFixture(t *testing.T, cfg apiauth.DPoPConfig, require_ bool) *dpopFixture {
	t.Helper()
	key, err := client.NewDPoPKey()
	require.NoError(t, err)

	jwksURL, issuer, mint, cleanup := setupTestJWKSMinter(t)
	bound := mint(func(c jwt.MapClaims) {
		c["sub"] = "alice"
		c["scopes"] = []string{"read"}
		c["cnf"] = map[string]any{"jkt": key.Thumbprint()}
	})
	unbound := mint(func(c jwt.MapClaims) { c["sub"] = "bob" })

	cfg.BaseURL = dpopBaseURL
	v := NewJWTValidator(JWTConfig{
		JWKSURL:             jwksURL,
		Issuer:              issuer,
		Audience:            "https://mcp.test",
		ResourceMetadataURL: "https://mcp.test/.well-known/oauth-protected-resource/mcp",
		DPoP:                apiauth.NewDPoPProofValidator(cfg),
		RequireDPoP:         require_,
	})
	t.Cleanup(cleanup)
	return &dpopFixture{v: v, key: key, token: bound, unbound: unbound}
}

// request builds a POST to the MCP endpoint presenting token under scheme,
// with proof in the DPoP header when non-empty.
func dpopRequest(scheme, token, proof string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, dpopResource, nil)
	r.Header.Set("Authorization", scheme+" "+token)
	if proof != "" {
		r.Header.Set("DPoP", proof)
	}
	return r
}

func (f *dpopFixture) proof(t *testing.T, token string) string {
	t.Helper()
	p, err := f.key.Proof(http.MethodPost, dpopResource, token)
	require.NoError(t, err)
	return p
}

func authErr(t *testing.T, err error) *mcpcore.AuthError {
	t.Helper()
	var ae *mcpcore.AuthError
	require.True(t, errors.As(err, &ae), "want *core.AuthError, got %T: %v", err, err)
	return ae
}

func TestJWTValidator_DPoP_BoundTokenWithProofPasses(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	r := dpopRequest("DPoP", f.token, f.proof(t, f.token))

	require.NoError(t, f.v.Validate(r))
	claims := f.v.Claims(r)
	require.NotNil(t, claims, "Claims must read the token from the DPoP scheme too")
	assert.Equal(t, "alice", claims.Subject)
}

// §7.2: dropping the proof and presenting a bound token as Bearer is the
// downgrade a thief would try.
func TestJWTValidator_DPoP_BoundTokenAsBearerIsRefused(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	ae := authErr(t, f.v.Validate(dpopRequest("Bearer", f.token, "")))
	assert.Equal(t, http.StatusUnauthorized, ae.Code)
	assert.True(t, strings.HasPrefix(ae.WWWAuthenticate, `DPoP error="invalid_token"`), ae.WWWAuthenticate)
}

func TestJWTValidator_DPoP_MissingProofIsInvalidProof(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	ae := authErr(t, f.v.Validate(dpopRequest("DPoP", f.token, "")))
	assert.Contains(t, ae.WWWAuthenticate, `error="invalid_dpop_proof"`)
}

func TestJWTValidator_DPoP_ProofFromAnotherKeyIsRefused(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	other, err := client.NewDPoPKey()
	require.NoError(t, err)
	proof, err := other.Proof(http.MethodPost, dpopResource, f.token)
	require.NoError(t, err)

	ae := authErr(t, f.v.Validate(dpopRequest("DPoP", f.token, proof)))
	assert.Contains(t, ae.WWWAuthenticate, `error="invalid_token"`)
}

func TestJWTValidator_DPoP_UnboundTokenUnderDPoPIsRefused(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	ae := authErr(t, f.v.Validate(dpopRequest("DPoP", f.unbound, f.proof(t, f.unbound))))
	assert.Contains(t, ae.WWWAuthenticate, `error="invalid_token"`)
}

// A proof is single use. The second presentation of the same proof is a
// replay, caught by the jti store.
func TestJWTValidator_DPoP_ReplayedProofIsRefused(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	proof := f.proof(t, f.token)
	require.NoError(t, f.v.Validate(dpopRequest("DPoP", f.token, proof)))
	ae := authErr(t, f.v.Validate(dpopRequest("DPoP", f.token, proof)))
	assert.Contains(t, ae.WWWAuthenticate, `error="invalid_dpop_proof"`)
}

// The token cache must not let a cached token skip the per-request proof.
func TestJWTValidator_DPoP_CacheHitStillChecksTheProof(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	f.v.CacheTTL = time.Minute
	require.NoError(t, f.v.Validate(dpopRequest("DPoP", f.token, f.proof(t, f.token))))

	ae := authErr(t, f.v.Validate(dpopRequest("DPoP", f.token, "")))
	assert.Contains(t, ae.WWWAuthenticate, `error="invalid_dpop_proof"`)
	ae = authErr(t, f.v.Validate(dpopRequest("Bearer", f.token, "")))
	assert.Contains(t, ae.WWWAuthenticate, `error="invalid_token"`)
}

func TestJWTValidator_DPoP_UnboundBearerStillPassesUnlessRequired(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	require.NoError(t, f.v.Validate(dpopRequest("Bearer", f.unbound, "")))

	req := newDPoPFixture(t, apiauth.DPoPConfig{}, true)
	ae := authErr(t, req.v.Validate(dpopRequest("Bearer", req.unbound, "")))
	assert.Contains(t, ae.WWWAuthenticate, "requires a DPoP-bound access token")
}

// §9: a nonce-requiring server answers with use_dpop_nonce and the value to
// use. The DPoP-Nonce header rides on the error through
// core.AuthResponseHeaders, and a proof carrying it then passes.
func TestJWTValidator_DPoP_NonceChallengeCarriesTheNonce(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{
		NonceSource: apiauth.NewNonceSource(apiauth.NonceConfig{}),
		NoncePolicy: func(*http.Request) bool { return true },
	}, false)

	err := f.v.Validate(dpopRequest("DPoP", f.token, f.proof(t, f.token)))
	ae := authErr(t, err)
	assert.Contains(t, ae.WWWAuthenticate, `DPoP error="use_dpop_nonce"`)

	// The core this module builds against may predate
	// core.AuthResponseHeaders, so match it by method set, as the server does.
	var hdrs interface{ ResponseHeaders() http.Header }
	require.True(t, errors.As(err, &hdrs), "nonce challenge must carry response headers")
	nonce := hdrs.ResponseHeaders().Get("DPoP-Nonce")
	require.NotEmpty(t, nonce)

	proof, perr := f.key.ProofWithNonce(http.MethodPost, dpopResource, f.token, nonce)
	require.NoError(t, perr)
	require.NoError(t, f.v.Validate(dpopRequest("DPoP", f.token, proof)))
}

func TestJWTValidator_DPoP_Challenges(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{AllowedAlgs: []string{"ES256"}}, false)
	ae := authErr(t, f.v.Validate(httptest.NewRequest(http.MethodPost, dpopResource, nil)))
	assert.True(t, strings.HasPrefix(ae.WWWAuthenticate, "Bearer "), "bearer challenge stays first: %s", ae.WWWAuthenticate)
	assert.Contains(t, ae.WWWAuthenticate, `, DPoP algs="ES256", resource_metadata=`)

	req := newDPoPFixture(t, apiauth.DPoPConfig{AllowedAlgs: []string{"ES256"}}, true)
	ae = authErr(t, req.v.Validate(httptest.NewRequest(http.MethodPost, dpopResource, nil)))
	assert.True(t, strings.HasPrefix(ae.WWWAuthenticate, `DPoP algs="ES256"`), ae.WWWAuthenticate)
	assert.NotContains(t, ae.WWWAuthenticate, "Bearer")

	rm, _, perr := ParseWWWAuthenticate(ae.WWWAuthenticate)
	require.NoError(t, perr)
	assert.Equal(t, "https://mcp.test/.well-known/oauth-protected-resource/mcp", rm,
		"the client's parser must still find resource_metadata in a DPoP challenge")
}

// A DPoP-bound token refused for scope is told so under the DPoP scheme.
func TestJWTValidator_DPoP_InsufficientScopeUsesThePresentedScheme(t *testing.T) {
	f := newDPoPFixture(t, apiauth.DPoPConfig{}, false)
	f.v.RequiredScopes = []string{"admin"}
	ae := authErr(t, f.v.Validate(dpopRequest("DPoP", f.token, f.proof(t, f.token))))
	assert.Equal(t, http.StatusForbidden, ae.Code)
	assert.True(t, strings.HasPrefix(ae.WWWAuthenticate, `DPoP error="insufficient_scope"`), ae.WWWAuthenticate)
}

// Without DPoP wired nothing changes: the DPoP scheme is simply not a
// credential the validator recognises.
func TestJWTValidator_NoDPoP_DPoPSchemeIsNotACredential(t *testing.T) {
	jwksURL, issuer, token, cleanup := setupTestJWKS(t, "alice", nil)
	defer cleanup()
	v := NewJWTValidator(JWTConfig{JWKSURL: jwksURL, Issuer: issuer, Audience: "https://mcp.test", ResourceMetadataURL: "https://rm"})

	ae := authErr(t, v.Validate(dpopRequest("DPoP", token, "")))
	assert.Equal(t, `Bearer resource_metadata="https://rm"`, ae.WWWAuthenticate)
	require.NoError(t, v.Validate(dpopRequest("Bearer", token, "")))
}
