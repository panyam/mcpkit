package auth

import (
	"testing"

	"github.com/panyam/oneauth/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEP-2352 AS-migration behavior on OAuthTokenSource.
//
// These tests exercise the *core* migration logic directly — cached-DCR
// invalidation when the AS changes, and the Invalidate() seam from the
// new core.InvalidatingTokenSource interface — without standing up the
// full OAuth dance. The end-to-end interop is verified by the upstream
// `auth/authorization-server-migration` conformance scenario, which
// drives a real two-AS flow against testclient.

// TestOAuthTokenSource_Invalidate_ClearsCache verifies Invalidate()
// drops authInfo, the cached access token, the AuthClient handle, and
// the in-memory credential store entry — so the next Token() call
// re-runs discovery. Cached DCR credentials are intentionally kept
// (resolveClientID clears them lazily on AS mismatch).
func TestOAuthTokenSource_Invalidate_ClearsCache(t *testing.T) {
	s := &OAuthTokenSource{ServerURL: "http://localhost:1/mcp"}
	s.authInfo = &MCPAuthInfo{
		AuthorizationServers: []string{"http://as1.example/"},
		ASMetadata:           &client.ASMetadata{Issuer: "http://as1.example/"},
	}
	s.token = "cached-token"
	s.oaClient = &client.AuthClient{}
	s.dcrClientID = "as1-client"
	s.dcrClientSecret = "as1-secret"
	s.dcrAS = "http://as1.example/"

	s.Invalidate()

	assert.Nil(t, s.authInfo, "authInfo should be cleared")
	assert.Empty(t, s.token, "cached access token should be cleared")
	assert.Nil(t, s.oaClient, "AuthClient handle should be cleared so next Token rebuilds it")
	// DCR cache is intentionally NOT cleared by Invalidate — resolveClientID
	// clears it when it observes a new AS, which avoids re-DCRing when the
	// AS hasn't actually changed.
	assert.Equal(t, "as1-client", s.dcrClientID, "DCR cache survives Invalidate")
}

// TestOAuthTokenSource_resolveClientID_ASChange_ClearsDCRCache verifies
// the SEP-2352 cross-AS credential-reuse prevention: when the current
// AS (from re-discovered authInfo) differs from the AS the cached DCR
// credentials were issued against, resolveClientID drops the cache so
// the next call re-registers at the new AS. Without this, a server
// swapping its PRM authorization_servers would silently reuse the old
// AS's client_id at the new AS — a security violation the upstream
// `sep-2352-no-reuse-on-as-change` check catches.
//
// Test shape: directly install AS₁-DCR cache + AS₂ in authInfo. Because
// the AS₂ mock has no registration endpoint, resolveClientID returns
// the "no client_id" error AFTER clearing the cache — proving the
// clear happened. (Driving an actual second DCR requires a live AS₂
// mock, which the upstream conformance scenario covers end-to-end.)
func TestOAuthTokenSource_resolveClientID_ASChange_ClearsDCRCache(t *testing.T) {
	s := &OAuthTokenSource{
		ServerURL: "http://localhost:1/mcp",
		EnableDCR: true,
	}
	// AS₁ DCR creds, cached from a prior round.
	s.dcrClientID = "as1-client-LEAKED-IF-SEEN-AT-AS2"
	s.dcrClientSecret = "as1-secret"
	s.dcrAS = "http://as1.example/"
	// Current authInfo points at AS₂ — no registration endpoint, so
	// resolveClientID can't actually re-register here.
	s.authInfo = &MCPAuthInfo{
		AuthorizationServers: []string{"http://as2.example/"},
		ASMetadata:           &client.ASMetadata{Issuer: "http://as2.example/"},
	}

	_, _, err := s.resolveClientID()
	require.Error(t, err, "expected error because AS₂ has no registration endpoint")
	assert.Contains(t, err.Error(), "no client_id",
		"expected fall-through error; got %v", err)

	assert.Empty(t, s.dcrClientID, "DCR client_id MUST be cleared after AS change")
	assert.Empty(t, s.dcrClientSecret, "DCR client_secret MUST be cleared after AS change")
	assert.Empty(t, s.dcrAS, "cached DCR AS marker MUST be cleared after AS change")
}

// TestOAuthTokenSource_resolveClientID_ASUnchanged_KeepsCache verifies
// the no-op path: when the cached DCR AS matches the current AS,
// resolveClientID returns the cached credentials without re-registering.
// Guards against an over-aggressive clear that would force needless
// DCRs on every Token() call.
func TestOAuthTokenSource_resolveClientID_ASUnchanged_KeepsCache(t *testing.T) {
	s := &OAuthTokenSource{
		ServerURL: "http://localhost:1/mcp",
		EnableDCR: true,
	}
	s.dcrClientID = "as1-client"
	s.dcrClientSecret = "as1-secret"
	s.dcrAS = "http://as1.example/"
	s.authInfo = &MCPAuthInfo{
		AuthorizationServers: []string{"http://as1.example/"},
		ASMetadata:           &client.ASMetadata{Issuer: "http://as1.example/"},
	}

	cid, csec, err := s.resolveClientID()
	require.NoError(t, err)
	assert.Equal(t, "as1-client", cid)
	assert.Equal(t, "as1-secret", csec)
	assert.Equal(t, "as1-client", s.dcrClientID, "DCR cache should survive same-AS resolve")
}

// TestOAuthTokenSource_SatisfiesInvalidatingTokenSource is a
// compile-time check that *OAuthTokenSource implements the new
// core.InvalidatingTokenSource interface. Drops the marker if the
// method ever gets renamed.
func TestOAuthTokenSource_SatisfiesInvalidatingTokenSource(t *testing.T) {
	// Indirect through a package-level var so the assertion can't be
	// optimized away.
	var _ interface{ Invalidate() } = (*OAuthTokenSource)(nil)
}
