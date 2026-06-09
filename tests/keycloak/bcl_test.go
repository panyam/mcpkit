package keycloak_test

// Keycloak interop tests for OIDC Back-Channel Logout 1.0 (RFC-draft
// spec at https://openid.net/specs/openid-connect-backchannel-1_0.html).
//
// These tests probe Keycloak's BCL behavior in our test config to
// pin down which session-termination paths actually fire the
// backchannel_logout_uri POST. The whole-enchilada demo (#407) wires
// the receiver side end-to-end; what these tests verify is the
// AS-side trigger — independent of the demo stack, the event-server,
// and our BackChannelLogoutHandler. The mcpkit BCL receiver itself is
// unit-tested in ext/auth/back_channel_logout_test.go.
//
// Each test patches mcp-confidential's `backchannel.logout.url`
// attribute at runtime to point at a per-test httptest.Server, fires
// one revocation path, and asserts whether Keycloak POSTs to the
// captured URL. The matrix:
//
//   - TestKeycloak_BCL_FiresOnRealmLogoutAll          POST /admin/realms/{realm}/logout-all
//   - TestKeycloak_BCL_FiresOnUserLogout              POST /admin/realms/{realm}/users/{user}/logout
//   - TestKeycloak_BCL_FiresOnSessionDelete           DELETE /admin/realms/{realm}/sessions/{id}
//   - TestKeycloak_BCL_FiresOnOIDCLogoutWithRefresh   POST /realms/{realm}/.../logout (RFC 7009-ish)
//   - TestKeycloak_BCL_FiresOnTokenRevoke             POST /realms/{realm}/.../revoke (RFC 7009)
//
// Tests skip gracefully when Keycloak is not running (`make upkcl`
// to start). Each test cleans up its BCL URL change in a t.Cleanup
// so subsequent runs start fresh.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dockerHostAlias is the hostname containers can use to reach the
// docker host's loopback interface. On Docker Desktop (Mac/Windows)
// and recent Docker Engine releases (Linux) this resolves to the
// host's gateway address. Required because Keycloak runs inside a
// container and the test's httptest.Server binds to localhost on the
// host.
const dockerHostAlias = "host.docker.internal"

// bclCapture wraps an httptest.Server that records every POST
// Keycloak sends to the BCL URL. The test assertions inspect the
// captured payload to verify the logout_token shape (when one
// arrives) and to distinguish "Keycloak fired but the body was
// malformed" from "Keycloak never fired at all".
type bclCapture struct {
	srv *httptest.Server

	mu      sync.Mutex
	hits    int
	bodies  [][]byte
	headers []http.Header
}

func newBCLCapture(t *testing.T) *bclCapture {
	t.Helper()
	c := &bclCapture{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.hits++
		c.bodies = append(c.bodies, body)
		c.headers = append(c.headers, r.Header.Clone())
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// urlForKeycloak returns the BCL URL Keycloak should be configured
// with. The httptest.Server binds to 127.0.0.1:<port> on the host;
// Keycloak in the container reaches it via the docker-host alias on
// the same port.
func (c *bclCapture) urlForKeycloak() string {
	u, _ := url.Parse(c.srv.URL)
	return "http://" + dockerHostAlias + ":" + u.Port()
}

func (c *bclCapture) waitFor(t *testing.T, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		hits := c.hits
		c.mu.Unlock()
		if hits > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (c *bclCapture) snapshot() (int, [][]byte, []http.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, append([][]byte(nil), c.bodies...), append([]http.Header(nil), c.headers...)
}

// kcAdminToken returns a master-realm admin access token (admin/admin
// credentials from the testkcl-auto recipe). The token is needed for
// every admin REST call this file makes — updating client attributes,
// listing sessions, deleting sessions, etc.
func kcAdminToken(t *testing.T) string {
	t.Helper()
	body := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {"admin"},
		"password":   {"admin"},
	}.Encode()
	req, err := http.NewRequest(http.MethodPost,
		keycloakURL()+"/realms/master/protocol/openid-connect/token",
		strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "master admin token grant failed")
	var out struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotEmpty(t, out.AccessToken)
	return out.AccessToken
}

// kcClientUUID looks up the internal UUID Keycloak assigns to a
// client (NOT the clientId). Used as the path segment in
// /admin/realms/<realm>/clients/<id>.
func kcClientUUID(t *testing.T, adminTok, realm, clientID string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet,
		keycloakURL()+"/admin/realms/"+realm+"/clients?clientId="+url.QueryEscape(clientID), nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	require.NotEmpty(t, list, "client %q not found in realm %q", clientID, realm)
	id, _ := list[0]["id"].(string)
	require.NotEmpty(t, id)
	return id
}

// kcSetClientBCLURL patches the `backchannel.logout.url` attribute on
// the named client and registers a cleanup that clears it. Cleanup
// runs before httptest.Server.Close so a stale Keycloak config
// doesn't leak into the next test.
func kcSetClientBCLURL(t *testing.T, adminTok, realm, clientID, bclURL string) {
	t.Helper()
	clientUUID := kcClientUUID(t, adminTok, realm, clientID)
	patch := func(value string) {
		attrs := map[string]string{
			"backchannel.logout.url":               value,
			"backchannel.logout.session.required":  "true",
			"backchannel.logout.revoke.offline.tokens": "false",
		}
		body, _ := json.Marshal(map[string]any{"attributes": attrs})
		req, _ := http.NewRequest(http.MethodPut,
			keycloakURL()+"/admin/realms/"+realm+"/clients/"+clientUUID,
			strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+adminTok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.True(t, resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK,
			"PUT clients/%s returned %d", clientUUID, resp.StatusCode)
	}
	patch(bclURL)
	t.Cleanup(func() { patch("") })
}

// kcUserID looks up a Keycloak user's internal UUID by username.
func kcUserID(t *testing.T, adminTok, realm, username string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet,
		keycloakURL()+"/admin/realms/"+realm+"/users?username="+url.QueryEscape(username), nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	require.NotEmpty(t, list, "user %q not found in realm %q", username, realm)
	id, _ := list[0]["id"].(string)
	require.NotEmpty(t, id)
	return id
}

// kcUserSessionID returns the first active session id for the given
// user, or empty if no session exists.
func kcUserSessionID(t *testing.T, adminTok, realm, userID string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet,
		keycloakURL()+"/admin/realms/"+realm+"/users/"+userID+"/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&list))
	if len(list) == 0 {
		return ""
	}
	id, _ := list[0]["id"].(string)
	return id
}

// kcAdminDo runs an admin-token authenticated HTTP request and
// returns the status code. Body responses are discarded — these
// helpers are used by tests that only care about the HTTP outcome.
func kcAdminDo(t *testing.T, adminTok, method, path string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, keycloakURL()+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+adminTok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// decodeLogoutToken pulls the `logout_token` form value out of the
// captured BCL POST body, decodes the JWT payload (no signature
// verification — this is a diagnostic test, the receiver-side
// verification lives in ext/auth/back_channel_logout_test.go), and
// returns the claim map.
func decodeLogoutToken(t *testing.T, body []byte) map[string]any {
	t.Helper()
	form, err := url.ParseQuery(string(body))
	require.NoError(t, err, "BCL body should be form-encoded")
	tok := form.Get("logout_token")
	require.NotEmpty(t, tok, "BCL POST should carry a logout_token form value (got %q)", string(body))
	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3, "logout_token must be a 3-part JWT")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	claims := map[string]any{}
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}

// assertLogoutTokenShape asserts the spec-mandated claim set per
// OIDC BCL 1.0 § 2.4 (events, iss, aud, iat, jti, sub-or-sid).
func assertLogoutTokenShape(t *testing.T, claims map[string]any, expectedIssuer, expectedAud string) {
	t.Helper()
	assert.Equal(t, expectedIssuer, claims["iss"], "iss claim")
	switch aud := claims["aud"].(type) {
	case string:
		assert.Equal(t, expectedAud, aud)
	case []any:
		assert.Contains(t, aud, expectedAud)
	default:
		t.Errorf("aud claim missing or unexpected type: %T %v", aud, aud)
	}
	assert.NotEmpty(t, claims["iat"], "iat claim required")
	assert.NotEmpty(t, claims["jti"], "jti claim required")
	events, _ := claims["events"].(map[string]any)
	require.NotNil(t, events, "events claim must be a JSON object")
	_, hasBCL := events["http://schemas.openid.net/event/backchannel-logout"]
	assert.True(t, hasBCL, "events claim must contain the BCL event URI")
	_, hasSub := claims["sub"].(string)
	_, hasSid := claims["sid"].(string)
	assert.True(t, hasSub || hasSid, "logout_token must carry at least one of sub or sid")
}

// setupBCLProbe is the common scaffolding for every test below:
// boots a capture server, points Keycloak at it, acquires a token
// for testUsername via password grant so a session exists, and
// returns the capture + the session id + the user id.
func setupBCLProbe(t *testing.T) (cap *bclCapture, adminTok, userID, sessionID string) {
	t.Helper()
	skipIfKeycloakNotRunning(t)

	adminTok = kcAdminToken(t)
	cap = newBCLCapture(t)
	kcSetClientBCLURL(t, adminTok, realmName, confidentialClientID, cap.urlForKeycloak())

	env := NewMCPTestEnv(t)
	tok := getPasswordTokenForUser(t, env.OIDC.TokenEndpoint, testUsername, testPassword)
	require.NotEmpty(t, tok.AccessToken, "password grant should produce an access_token")

	userID = kcUserID(t, adminTok, realmName, testUsername)
	sessionID = kcUserSessionID(t, adminTok, realmName, userID)
	require.NotEmpty(t, sessionID, "password grant should create a Keycloak session")

	return cap, adminTok, userID, sessionID
}

// reportBCLOutcome prints the captured POST count + (if any) the
// decoded logout_token claims. Diagnostic helper — the test
// assertions decide pass/fail, this just makes failed runs easier
// to read.
func reportBCLOutcome(t *testing.T, cap *bclCapture, expectedIssuer string) {
	t.Helper()
	hits, bodies, _ := cap.snapshot()
	t.Logf("BCL POSTs captured: %d", hits)
	for i, b := range bodies {
		t.Logf("  POST %d body=%s", i+1, string(b))
		claims := decodeLogoutToken(t, b)
		assertLogoutTokenShape(t, claims, expectedIssuer, confidentialClientID)
	}
}

// expectedRealmIssuer returns the iss claim Keycloak stamps on tokens
// from this realm — same value used as the iss in BCL logout_tokens.
func expectedRealmIssuer() string {
	return keycloakURL() + "/realms/" + realmName
}

// ---- the actual probe matrix ----

// TestKeycloak_BCL_DoesNotFireOnRealmLogoutAll pins the observed
// Keycloak behavior: POST /admin/realms/{realm}/logout-all does NOT
// fire BCL. The endpoint is a Keycloak admin convenience for "sign
// out everyone in this realm" and isn't standardized; the BCL
// adapter deliberately skips it (probably to avoid storming all
// registered backchannel_logout_uris in one shot). Treat this test
// as a regression pin — if a future Keycloak version flips it to
// fire BCL, we want to notice and adapt.
func TestKeycloak_BCL_DoesNotFireOnRealmLogoutAll(t *testing.T) {
	cap, adminTok, _, _ := setupBCLProbe(t)
	status := kcAdminDo(t, adminTok, http.MethodPost,
		fmt.Sprintf("/admin/realms/%s/logout-all", realmName))
	require.True(t, status >= 200 && status < 300, "logout-all returned %d", status)

	got := cap.waitFor(t, 2*time.Second)
	hits, _, _ := cap.snapshot()
	assert.False(t, got, "Keycloak (current behavior) does NOT fire BCL on /admin/realms/%s/logout-all; got %d POSTs", realmName, hits)
}

// TestKeycloak_BCL_FiresOnUserLogout fires
// POST /admin/realms/{realm}/users/{userId}/logout. This is the
// per-user "Sign out" admin action.
func TestKeycloak_BCL_FiresOnUserLogout(t *testing.T) {
	cap, adminTok, userID, _ := setupBCLProbe(t)
	status := kcAdminDo(t, adminTok, http.MethodPost,
		fmt.Sprintf("/admin/realms/%s/users/%s/logout", realmName, userID))
	require.True(t, status >= 200 && status < 300, "user logout returned %d", status)

	got := cap.waitFor(t, 5*time.Second)
	reportBCLOutcome(t, cap, expectedRealmIssuer())
	assert.True(t, got, "Keycloak should fire BCL POST after /admin/realms/%s/users/%s/logout", realmName, userID)
}

// TestKeycloak_BCL_FiresOnSessionDelete fires
// DELETE /admin/realms/{realm}/sessions/{id} — kills one specific
// session by id. This is what kcadm.sh delete sessions/<id> hits.
func TestKeycloak_BCL_FiresOnSessionDelete(t *testing.T) {
	cap, adminTok, _, sessionID := setupBCLProbe(t)
	status := kcAdminDo(t, adminTok, http.MethodDelete,
		fmt.Sprintf("/admin/realms/%s/sessions/%s", realmName, sessionID))
	require.True(t, status >= 200 && status < 300, "session delete returned %d", status)

	got := cap.waitFor(t, 5*time.Second)
	reportBCLOutcome(t, cap, expectedRealmIssuer())
	assert.True(t, got, "Keycloak should fire BCL POST after DELETE /admin/realms/%s/sessions/%s", realmName, sessionID)
}

// TestKeycloak_BCL_FiresOnOIDCLogoutWithRefresh fires the OIDC
// end-session endpoint via the refresh_token form (the same shape
// our whole-enchilada demo's poller would hit when the user
// explicitly logs out from the client).
func TestKeycloak_BCL_FiresOnOIDCLogoutWithRefresh(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	adminTok := kcAdminToken(t)
	cap := newBCLCapture(t)
	kcSetClientBCLURL(t, adminTok, realmName, confidentialClientID, cap.urlForKeycloak())

	env := NewMCPTestEnv(t)
	tok := getPasswordTokenForUser(t, env.OIDC.TokenEndpoint, testUsername, testPassword)
	require.NotEmpty(t, tok.RefreshToken, "password grant should include a refresh_token")

	body := url.Values{
		"client_id":     {confidentialClientID},
		"client_secret": {confidentialClientSecret},
		"refresh_token": {tok.RefreshToken},
	}.Encode()
	req, _ := http.NewRequest(http.MethodPost,
		keycloakURL()+"/realms/"+realmName+"/protocol/openid-connect/logout",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
		"OIDC logout returned %d", resp.StatusCode)

	got := cap.waitFor(t, 5*time.Second)
	reportBCLOutcome(t, cap, expectedRealmIssuer())
	assert.True(t, got, "Keycloak should fire BCL POST after OIDC end-session with refresh_token")
}

// TestKeycloak_BCL_DoesNotFireOnTokenRevoke pins the observed
// Keycloak behavior: RFC 7009 /revoke does NOT fire BCL. This is
// arguably correct per the spec — RFC 7009 revokes the TOKEN, not
// the SESSION; a revoked refresh_token still leaves the underlying
// SSO session alive (so backchannel-logout, which is session-scoped,
// shouldn't fire). The test is a regression pin: if Keycloak ever
// changes this, we want to notice.
//
// Practically: applications that need backchannel-logout on
// token-revoke should use the OIDC end-session endpoint
// (TestKeycloak_BCL_FiresOnOIDCLogoutWithRefresh) instead.
func TestKeycloak_BCL_DoesNotFireOnTokenRevoke(t *testing.T) {
	skipIfKeycloakNotRunning(t)
	adminTok := kcAdminToken(t)
	cap := newBCLCapture(t)
	kcSetClientBCLURL(t, adminTok, realmName, confidentialClientID, cap.urlForKeycloak())

	env := NewMCPTestEnv(t)
	tok := getPasswordTokenForUser(t, env.OIDC.TokenEndpoint, testUsername, testPassword)
	require.NotEmpty(t, tok.RefreshToken)

	body := url.Values{
		"client_id":       {confidentialClientID},
		"client_secret":   {confidentialClientSecret},
		"token":           {tok.RefreshToken},
		"token_type_hint": {"refresh_token"},
	}.Encode()
	req, _ := http.NewRequest(http.MethodPost,
		keycloakURL()+"/realms/"+realmName+"/protocol/openid-connect/revoke",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
		"RFC 7009 revoke returned %d", resp.StatusCode)

	got := cap.waitFor(t, 2*time.Second)
	hits, _, _ := cap.snapshot()
	assert.False(t, got, "Keycloak (current behavior) does NOT fire BCL on RFC 7009 /revoke; got %d POSTs", hits)
}
